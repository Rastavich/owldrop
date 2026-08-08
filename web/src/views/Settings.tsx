import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import {
  checkUpdate,
  getConfig,
  getServe,
  getUpdateState,
  installUpdate,
  openExternal,
  patchConfig,
  setServe,
  testNtfy,
} from '../api';
import { toast } from '../store';
import type { AppConfig } from '../types';

export default function Settings() {
  const qc = useQueryClient();
  const { data: config } = useQuery({ queryKey: ['config'], queryFn: getConfig });
  const { data: update } = useQuery({ queryKey: ['update'], queryFn: getUpdateState });
  const { data: serve } = useQuery({ queryKey: ['serve'], queryFn: getServe });
  const [updateBusy, setUpdateBusy] = useState(false);
  const [ntfyBusy, setNtfyBusy] = useState(false);
  const [serveBusy, setServeBusy] = useState(false);

  const toggleServe = async (want: boolean) => {
    setServeBusy(true);
    try {
      const res = await setServe(want);
      qc.setQueryData(['serve'], res);
      toast(want ? 'HTTPS enabled — https://' + (res.url ?? '') + ' is live on your tailnet' : 'HTTPS disabled');
    } catch (e) {
      qc.invalidateQueries({ queryKey: ['serve'] });
      toast('HTTPS: ' + (e instanceof Error ? e.message : e), undefined, 'err');
    }
    setServeBusy(false);
  };
  const [ntfyTopicEdit, setNtfyTopicEdit] = useState<string | null>(null);

  const patch = async (body: Partial<AppConfig>, okMsg?: string) => {
    try {
      const res = await patchConfig(body);
      qc.setQueryData(['config'], res);
      if (okMsg) toast(okMsg);
    } catch (e) {
      qc.invalidateQueries({ queryKey: ['config'] });
      toast(e instanceof Error ? e.message : String(e), undefined, 'err');
    }
  };

  const checkForUpdates = async () => {
    setUpdateBusy(true);
    try {
      const st = await checkUpdate();
      qc.setQueryData(['update'], st);
      if (st.available) toast('Version ' + st.latest + ' is available — install it below');
      else toast('You are on the latest version (' + st.current + ')');
    } catch (e) {
      toast('Update check failed: ' + (e instanceof Error ? e.message : e), undefined, 'err');
    }
    setUpdateBusy(false);
  };

  const doInstallUpdate = async () => {
    setUpdateBusy(true);
    try {
      await installUpdate();
      toast('Update downloaded — the app will restart to install it');
    } catch (e) {
      toast('Update failed: ' + (e instanceof Error ? e.message : e), undefined, 'err');
      setUpdateBusy(false);
    }
  };

  return (
    <section className="pane settings">
      <div className="settings-grid">
      <div className="set-card">
        <h3>Notifications</h3>
      <label className="check">
        <input type="checkbox" checked={config?.notifyArrival !== false} onChange={(e) => patch({ notifyArrival: e.target.checked })} />
        New file arrival
      </label>
      <label className="check">
        <input type="checkbox" checked={config?.notifySave !== false} onChange={(e) => patch({ notifySave: e.target.checked })} />
        Save finished
      </label>
      <label className="check">
        <input type="checkbox" checked={config?.notifySend !== false} onChange={(e) => patch({ notifySend: e.target.checked })} />
        Send finished
      </label>
      <label className="check">
        <input type="checkbox" checked={config?.notifyError !== false} onChange={(e) => patch({ notifyError: e.target.checked })} />
        Errors
      </label>

      </div>

      <div className="set-card">
      <h3>Phone notifications</h3>
      <p className="sub2">
        Get a push notification on your phone when a file is sent to it. Install the <b>ntfy</b> app (Android/iOS), tap{' '}
        <b>+</b>, and subscribe to this topic:
      </p>
      <div className="row" style={{ gap: 8, display: 'flex', alignItems: 'center' }}>
        <input
          className="search"
          type="text"
          placeholder="pick a topic name, e.g. owldrop-mine-x9k2"
          value={ntfyTopicEdit ?? config?.ntfyTopic ?? ''}
          onChange={(e) => setNtfyTopicEdit(e.target.value)}
          onBlur={() => {
            if (ntfyTopicEdit !== null && ntfyTopicEdit !== (config?.ntfyTopic ?? '')) patch({ ntfyTopic: ntfyTopicEdit });
            setNtfyTopicEdit(null);
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
          }}
          spellCheck={false}
          autoComplete="off"
          style={{ flex: 1 }}
        />
        <button
          className="btn ghost"
          disabled={ntfyBusy || !config?.ntfyTopic}
          onClick={async () => {
            setNtfyBusy(true);
            try {
              await testNtfy();
              toast('Test notification sent — check your phone');
            } catch (e) {
              toast(e instanceof Error ? e.message : String(e), undefined, 'err');
            } finally {
              setNtfyBusy(false);
            }
          }}
        >
          {ntfyBusy ? 'Sending…' : 'Send test'}
        </button>
      </div>
      <p className="sub2">
        Server: <code>{config?.ntfyServer || 'https://ntfy.sh'}</code> (public, no account). The Tailscale app can also
        notify on its own — on the phone: Settings → Apps → Tailscale → Notifications.
      </p>
      {config?.ntfyTopic && (
        <p className="sub2">
          <button className="linkbtn" onClick={() => patch({ ntfyTopic: '' })}>turn off</button>
        </p>
      )}

      </div>

      <div className="set-card">
      <h3>Save folder</h3>
      <p className="sub2">Incoming files are saved to <b>{config?.saveDir}</b></p>
      </div>

      <div className="set-card">
      <h3>LAN access</h3>
      <label className="check">
        <input type="checkbox" checked={!!config?.lan} onChange={(e) => patch({ lan: e.target.checked }, 'Applying LAN mode — reload the page if it disconnects')} />
        Allow other devices on my tailnet to open this app
      </label>
      {config?.lan && config.lanUrl && (
        <p className="sub2">
          {config.lanUrl}
          {config.lanUrls && config.lanUrls.length > 1 && (
            <span className="muted">
              <br />
              also via {config.lanUrls.slice(1).join(' · ')}
            </span>
          )}
        </p>
      )}
      <p className="sub2">Opening this app from another device is as powerful as being at this machine — only enable it if you trust your tailnet.</p>

      </div>

      <div className="set-card">
      <h3>HTTPS access</h3>
      <label className="check">
        <input
          type="checkbox"
          checked={!!serve?.enabled}
          disabled={serveBusy}
          onChange={(e) => toggleServe(e.target.checked)}
        />
        Secure https:// link on my tailnet (Tailscale Serve)
      </label>
      {serve?.enabled && serve.url && <p className="sub2">{serve.url.replace('https://', '')}</p>}
      <p className="sub2">
        Tailscale issues and renews the certificate automatically. Same URL shape as your Funnel drop-link
        hostname, but reachable only on your tailnet. Requires MagicDNS.
      </p>

      </div>

      <div className="set-card">
      <h3>Updates</h3>
      <div className="funnelrow">
        <div className="funnel-info">
          <p className="sub2">
            {update?.state === 'disabled'
              ? 'Updates are not available in this build.'
              : `Running version ${update?.current ?? '…'}`}
            {update?.latest && update.available ? ` — version ${update.latest} is available` : ''}
            {update?.state === 'downloading' ? ' — downloading…' : ''}
            {update?.state === 'error' ? ` — ${update.error ?? 'update failed'}` : ''}
          </p>
          {update?.state !== 'disabled' && (
            <p className="sub2">Checks the release feed and replaces this app in place (Windows/macOS).</p>
          )}
        </div>
        {update?.state !== 'disabled' && (
          <div className="updrow">
            <button className="btn ghost" onClick={checkForUpdates} disabled={updateBusy || update?.state === 'downloading'}>
              Check for updates
            </button>
            {update?.available && (
              <button className="btn" onClick={doInstallUpdate} disabled={updateBusy}>
                Download &amp; install
              </button>
            )}
          </div>
        )}
      </div>
      </div>

      <div className="set-card">
      <h3>Shortcuts</h3>
      <p className="sub2">
        <code>/</code> filter · <code>j</code>/<code>k</code> select · <code>s</code> save · <code>d</code> delete ·{' '}
        <code>Ctrl+V</code> paste to send
      </p>
      </div>

      <div className="set-card">
      <h3>Privacy</h3>
      <label className="check">
        <input type="checkbox" checked={config?.telemetry !== false} onChange={(e) => patch({ telemetry: e.target.checked })} />
        Share anonymous usage stats
      </label>
      </div>

      <div className="set-card">
      <h3>About &amp; support</h3>
      <p className="sub2">Talks directly to your local tailscaled daemon. Nothing leaves this machine except files you send and the anonymous stats above (if enabled).</p>
      <p className="sub2">
        Owldrop is free — public drop links included. If it saves you time,{' '}
        <a
          href="https://ko-fi.com/owldrop"
          onClick={(e) => {
            e.preventDefault(); // the webview can't navigate; the server opens the system browser
            openExternal('https://ko-fi.com/owldrop').catch((err) => toast('Could not open link: ' + (err instanceof Error ? err.message : err), undefined, 'err'));
          }}
        >
          buy me a coffee
        </a>.
      </p>
      </div>
      </div>
    </section>
  );
}
