// tailscale-drop desktop shell.
//
// Spawns the Go sidecar (tailscale-drop), which talks to the local
// tailscaled daemon and serves the UI on a random localhost port, then
// shows that UI in a native window with tray + notifications.
const { app, BrowserWindow, Tray, Menu, Notification, nativeImage } = require('electron');
const { spawn } = require('child_process');
const http = require('http');
const path = require('path');
const fs = require('fs');

const ICON_PATH = path.join(__dirname, 'icon.png');

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
  sidecar = spawn(sidecarPath(), ['--port', '0'], { stdio: ['ignore', 'pipe', 'pipe'] });
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
      console.error('sidecar exited with code', code);
      app.quit();
    }
  });
}

function onServerUp() {
  createWindow();
  setupTray();
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

// --- tray -----------------------------------------------------------------

function setupTray() {
  const img = nativeImage.createFromPath(ICON_PATH);
  tray = new Tray(img.isEmpty() ? undefined : img.resize({ width: 22, height: 22 }));
  tray.setToolTip('Taildrop');
  tray.setContextMenu(Menu.buildFromTemplate([
    { label: 'Show Taildrop', click: () => { win.show(); win.focus(); } },
    { type: 'separator' },
    {
      label: 'Quit',
      click: () => {
        quitting = true;
        app.quit();
      },
    },
  ]));
  tray.on('click', () => { win.show(); win.focus(); });
}

// --- notifications (native, driven by the sidecar's SSE stream) -----------

let knownNames = new Set();
let notifBuf = '';

function setupNotifications() {
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
  req.on('error', () => setTimeout(setupNotifications, 2000));
}

function handleEvent(ev) {
  if (ev.type !== 'inbox') return;
  const names = new Set(ev.files.map((f) => f.name));
  const fresh = ev.files.filter((f) => !knownNames.has(f.name));
  knownNames = names;
  if (fresh.length === 0 || Notification.isSupported() === false) return;
  for (const f of fresh) {
    new Notification({
      title: 'Taildrop: new file',
      body: `${f.name} (${fmtSize(f.size)})`,
      icon: ICON_PATH,
      silent: true,
    }).show();
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
  app.on('second-instance', () => {
    if (win) { win.show(); win.focus(); }
  });

  app.whenReady().then(() => {
    startSidecar();
  });

  app.on('before-quit', () => {
    quitting = true;
    if (sidecar) sidecar.kill();
  });

  // Never quit when the window closes — keep running in the tray.
  app.on('window-all-closed', () => {});
}
