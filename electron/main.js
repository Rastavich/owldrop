// tailscale-drop desktop shell.
//
// Spawns the Go sidecar (tailscale-drop), which talks to the local
// tailscaled daemon and serves the UI on a random localhost port, then
// shows that UI in a native window with tray + notifications.
const { app, BrowserWindow, Tray, Menu, Notification, nativeImage, dialog, globalShortcut, session } = require('electron');
const { spawn } = require('child_process');
const http = require('http');
const path = require('path');
const fs = require('fs');

const ICON_PATH = path.join(__dirname, 'icon.png');

// Forward renderer console output to stderr (lands in the systemd journal).
app.commandLine.appendSwitch('enable-logging');

let win = null;
let tray = null;
let sidecar = null;
let quitting = false;
let port = 0;

// --- sidecar --------------------------------------------------------------

function sidecarPath() {
  const candidates = [
    path.join(__dirname, '..', 'tailscale-drop'),   // repo layout
    path.join(process.resourcesPath || '', 'tailscale-drop'), // packaged
  ];
  return candidates.find((c) => fs.existsSync(c)) || candidates[0];
}

function startSidecar() {
  port = 0;
  // Fixed port so the LAN URL stays stable/bookmarkable; --port 0 would
  // pick a fresh ephemeral port on every restart.
  sidecar = spawn(sidecarPath(), ['--port', '8976'], { stdio: ['ignore', 'pipe', 'pipe'] });
  sidecar.stdout.setEncoding('utf8');
  sidecar.stderr.setEncoding('utf8');
  sidecar.stdout.on('data', (d) => {
    process.stdout.write(`[sidecar] ${d}`);
    const m = d.match(/http:\/\/127\.0\.0\.1:(\d+)\//);
    if (m && !port) {
      port = Number(m[1]);
      onServerUp();
    }
  });
  sidecar.stderr.on('data', (d) => process.stderr.write(`[sidecar] ${d}`));
  sidecar.on('exit', (code) => {
    sidecar = null;
    if (!quitting) {
      console.error(`sidecar exited with code ${code} — restarting in 2s`);
      setTimeout(startSidecar, 2000);
    }
  });
}

function onServerUp() {
  const url = `http://127.0.0.1:${port}/`;
  if (win) {
    // Sidecar (re)started. With a fixed port the URL is unchanged, so always
    // reload — this is also what picks up embedded-UI changes on hot reload.
    win.loadURL(url);
  } else {
    createWindow();
  }
  if (!tray) setupTray();
  refreshTrayDevices();
  refreshPrefs();
  // Reconnect notifications to the (possibly new) sidecar port.
  setupNotifications();
}

// --- window ---------------------------------------------------------------

function createWindow() {
  win = new BrowserWindow({
    width: 980,
    height: 720,
    minWidth: 720,
    minHeight: 480,
    title: 'Taildrop',
    autoHideMenuBar: true,
    backgroundColor: '#0b0e14',
    icon: ICON_PATH,
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });
  win.loadURL(`http://127.0.0.1:${port}/`);
  win.on('close', (e) => {
    // Closing the window hides to the tray; use the tray menu to quit.
    if (!quitting) {
      e.preventDefault();
      win.hide();
    }
  });
}

function showWindow() {
  if (!win) return;
  win.show();
  win.focus();
}

// --- tray: show + quick-send ----------------------------------------------

let trayDevices = []; // {id, name, label, usable}

function setupTray() {
  const img = nativeImage.createFromPath(ICON_PATH);
  tray = new Tray(img.isEmpty() ? undefined : img.resize({ width: 22, height: 22 }));
  tray.setToolTip('Taildrop');
  tray.on('click', showWindow);
  setupTrayMenu();
}

function setupTrayMenu() {
  if (!tray) return;
  tray.setContextMenu(Menu.buildFromTemplate([
    { label: 'Show Taildrop', click: showWindow },
    { type: 'separator' },
    { label: 'Send file to…', submenu: trayDeviceItems() },
    { type: 'separator' },
    {
      label: 'Quit',
      click: () => {
        quitting = true;
        app.quit();
      },
    },
  ]));
}

function trayDeviceItems() {
  const items = trayDevices
    .filter((d) => d.usable)
    .map((d) => ({
      label: d.label,
      click: () => pickFileAndSend(d),
    }));
  if (!items.length) {
    items.push({ label: '(no devices — is your tailnet up?)', enabled: false });
  }
  return items;
}

function refreshTrayDevices() {
  if (!port) return;
  http.get({ host: '127.0.0.1', port, path: '/api/devices' }, (res) => {
    let b = '';
    res.on('data', (d) => (b += d));
    res.on('end', () => {
      try {
        const data = JSON.parse(b);
        trayDevices = (data.devices || []).map((d) => ({
          id: d.id,
          name: d.name,
          usable: d.taildrop === 'available',
          label: d.name + (d.os ? ` (${d.os})` : '') + (d.online ? '' : ' · offline'),
        }));
        setupTrayMenu();
      } catch { /* sidecar not ready */ }
    });
  }).on('error', () => {});
}

// The mutating send API needs the session token that lives in the page.
function getToken() {
  return new Promise((resolve) => {
    http.get({ host: '127.0.0.1', port, path: '/' }, (res) => {
      let html = '';
      res.on('data', (d) => (html += d));
      res.on('end', () => {
        const m = html.match(/"token":"([0-9a-f]+)"/);
        resolve(m ? m[1] : '');
      });
    }).on('error', () => resolve(''));
  });
}

async function pickFileAndSend(device) {
  const { canceled, filePaths } = await dialog.showOpenDialog(win, {
    title: `Send to ${device.name}`,
    properties: ['openFile'],
  });
  if (canceled || !filePaths.length) return;
  const filePath = filePaths[0];
  const name = path.basename(filePath);
  const token = await getToken();
  if (!token) {
    showNotification('Taildrop', 'Could not authenticate with the local daemon');
    return;
  }
  let size;
  try {
    size = fs.statSync(filePath).size;
  } catch (e) {
    showNotification('Taildrop', e.message);
    return;
  }
  const req = http.request({
    host: '127.0.0.1',
    port,
    path: `/api/send?peer=${encodeURIComponent(device.id)}&name=${encodeURIComponent(name)}`,
    method: 'POST',
    headers: { 'X-Taildrop-Token': token, 'Content-Length': size },
  }, (res) => {
    res.resume();
    res.on('end', () => {
      if (res.statusCode === 200) {
        showNotification('Taildrop: sent', `${name} → ${device.name}`);
      } else {
        showNotification('Taildrop: send failed', `${name}: HTTP ${res.statusCode}`);
      }
    });
  });
  req.on('error', (e) => showNotification('Taildrop: send failed', e.message));
  fs.createReadStream(filePath).pipe(req);
}

// --- notifications (native, driven by the sidecar's SSE stream) -----------

let knownNames = new Set();
let notifBuf = '';
let notifReq = null;
let prefs = { arrival: true, save: true, send: true, errors: true };

function refreshPrefs() {
  if (!port) return;
  http.get({ host: '127.0.0.1', port, path: '/api/config' }, (res) => {
    let b = '';
    res.on('data', (d) => (b += d));
    res.on('end', () => {
      try {
        const c = JSON.parse(b);
        prefs = {
          arrival: c.notifyArrival !== false,
          save: c.notifySave !== false,
          send: c.notifySend !== false,
          errors: c.notifyError !== false,
        };
      } catch { /* keep defaults */ }
    });
  }).on('error', () => {});
}

function showNotification(title, body) {
  if (Notification.isSupported() === false) return;
  new Notification({ title, body, icon: ICON_PATH, silent: true }).show();
}

function stopNotifications() {
  if (notifReq) {
    notifReq.destroy();
    notifReq = null;
  }
}

function setupNotifications() {
  stopNotifications();
  const req = http.get({ host: '127.0.0.1', port, path: '/events' }, (res) => {
    res.setEncoding('utf8');
    res.on('data', (chunk) => {
      notifBuf += chunk;
      let i;
      while ((i = notifBuf.indexOf('\n\n')) >= 0) {
        const frame = notifBuf.slice(0, i);
        notifBuf = notifBuf.slice(i + 2);
        const dataLine = frame.split('\n').find((l) => l.startsWith('data: '));
        if (!dataLine) continue;
        let ev;
        try { ev = JSON.parse(dataLine.slice(6)); } catch { continue; }
        handleEvent(ev);
      }
    });
  });
  req.on('error', () => {
    // Sidecar down or restarting — keep trying; onServerUp reconnects on the
    // fresh port once it's back.
    if (!quitting) setTimeout(() => setupNotifications(), 2000);
  });
  notifReq = req;
}

function handleEvent(ev) {
  switch (ev.type) {
    case 'inbox': {
      const names = new Set(ev.files.map((f) => f.name));
      const fresh = ev.files.filter((f) => !knownNames.has(f.name));
      knownNames = names;
      if (fresh.length && prefs.arrival) {
        for (const f of fresh) {
          showNotification('Taildrop: new file', `${f.name} (${fmtSize(f.size)})`);
        }
      }
      break;
    }
    case 'save':
      if (ev.done && prefs.save) {
        if (ev.err) showNotification('Taildrop: save failed', `${ev.name}: ${ev.err}`);
        else showNotification('Taildrop: saved', `${ev.name} → ${ev.path || 'saved'}`);
      }
      break;
    case 'send':
      if (ev.done && prefs.send) {
        if (ev.err) showNotification('Taildrop: send failed', `${ev.name}: ${ev.err}`);
        else showNotification('Taildrop: sent', ev.name);
      }
      break;
    case 'status':
      if (ev.err && prefs.errors) {
        showNotification('Taildrop', 'tailscaled unreachable: ' + ev.err);
      }
      break;
  }
}

function fmtSize(n) {
  if (n < 1024) return n + ' B';
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let v = n;
  let i = -1;
  do { v /= 1024; i++; } while (v >= 1024 && i < units.length - 1);
  return (v >= 100 ? v.toFixed(0) : v.toFixed(1)) + ' ' + units[i];
}

// --- app lifecycle --------------------------------------------------------

const gotLock = app.requestSingleInstanceLock();
if (!gotLock) {
  app.quit();
} else {
  // Optional: --remote-debugging-port comes from the command line as usual;
  // TSD_DEBUG_PORT is honoured so a systemd service can enable it too.
  if (!process.argv.includes('--remote-debugging-port') && process.env.TSD_DEBUG_PORT) {
    app.commandLine.appendSwitch('remote-debugging-port', process.env.TSD_DEBUG_PORT);
  }
  app.on('second-instance', () => showWindow());

  app.whenReady().then(() => {
    // Paste-to-send reads the clipboard from the renderer; clipboard
    // read/write need an explicit grant in Electron.
    session.defaultSession.setPermissionRequestHandler((_wc, permission, callback) => {
      callback(permission === 'clipboard-read' || permission === 'clipboard-sanitized-write' || permission === 'notifications');
    });
    // Summon the window from anywhere.
    const ok = globalShortcut.register('CommandOrControl+Shift+T', showWindow);
    console.log(`global shortcut Ctrl+Shift+T: ${ok ? 'registered' : 'failed (Wayland compositor may not support it)'}`);
    // Keep tray devices + notification prefs fresh.
    setInterval(refreshTrayDevices, 15000);
    setInterval(refreshPrefs, 30000);
    startSidecar();
  });

  app.on('will-quit', () => globalShortcut.unregisterAll());

  app.on('before-quit', () => {
    quitting = true;
    if (sidecar) sidecar.kill();
    // Don't let a slow window teardown hang shutdown (seen as a 1-minute
    // stall + crash on stop); force-exit after a short grace period.
    setTimeout(() => process.exit(0), 1500);
  });

  // Never quit when the window closes — keep running in the tray.
  app.on('window-all-closed', () => {
    if (quitting) app.exit(0);
  });
}
