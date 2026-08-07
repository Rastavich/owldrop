import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import {
  checkUpdate,
  createDropLink,
  getConfig,
  getDropLinks,
  getFunnel,
  getUpdateState,
  installUpdate,
  patchConfig,
  revokeDropLink,
  setFunnel,
  testNtfy,
} from '../api';
import { toast } from '../store';
import { copyText, fmtAge } from '../utils';
import type { AppConfig, DropLink } from '../types';

type LinkState = 'active' | 'revoked' | 'expired' | 'used';

function linkState(l: DropLink): LinkState {
  if (l.revoked) return 'revoked';
  if (l.expired) return 'expired';
  if (l.maxUses > 0 && l.uses >= l.maxUses) return 'used';
  return 'active';
}

export default function Settings() {
  const qc = useQueryClient();
  const { data: config } = useQuery({ queryKey: ['config'], queryFn: getConfig });
  const { data: links = [] } = useQuery({ queryKey: ['droplinks'], queryFn: getDropLinks });
  const { data: funnel } = useQuery({ queryKey: ['funnel'], queryFn: getFunnel });
  const { data: update } = useQuery({ queryKey: ['update'], queryFn: getUpdateState });
  const [updateBusy, setUpdateBusy] = useState(false);
  const activeLinks = links.filter((l) => linkState(l) === 'active');
  const archivedCount = links.length - activeLinks.length;
  const [name, setName] = useState('');
  const [ttl, setTtl] = useState(60);
  const [single, setSingle] = useState(true);
  const [funnelBusy, setFunnelBusy] = useState(false);
  const [showArchived, setShowArchived] = useState(false);
  const [ntfyBusy, setNtfyBusy] = useState(false);
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

  const toggleFunnel = async (want: boolean) => {
    setFunnelBusy(true);
    try {
      const res = await setFunnel(want);
      qc.setQueryData(['funnel'], res);
      qc.invalidateQueries({ queryKey: ['droplinks'] });
      toast(want ? 'Public access enabled — drop links are now reachable over the internet' : 'Public access disabled');
    } catch (e) {
      qc.invalidateQueries({ queryKey: ['funnel'] });
      toast('Funnel: ' + (e instanceof Error ? e.message : e), undefined, 'err');
    }
    setFunnelBusy(false);
  };

  const create = async () => {
    try {
      const res = await createDropLink(name.trim(), ttl, single ? 1 : 0);
      setName('');
      await copyText(res.url, true);
      toast('Link created');
      qc.invalidateQueries({ queryKey: ['droplinks'] });
    } catch (e) {
      toast("Couldn't create link: " + (e instanceof Error ? e.message : e), undefined, 'err');
    }
  };

  const revoke = async (token: string) => {
    try {
      await revokeDropLink(token);
      toast('Link revoked');
      qc.invalidateQueries({ queryKey: ['droplinks'] });
    } catch (e) {
      toast("Couldn't revoke link: " + (e instanceof Error ? e.message : e), undefined, 'err');
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

      <h3>Save folder</h3>
      <p className="sub2">Incoming files are saved to <b>{config?.saveDir}</b></p>

      <h3>LAN access</h3>
      <label className="check">
        <input type="checkbox" checked={!!config?.lan} onChange={(e) => patch({ lan: e.target.checked }, 'Restarting to apply LAN mode…')} />
        Allow other devices on my tailnet to open this app
      </label>
      {config?.lan && config.lanUrl && <p className="sub2">{config.lanUrl}</p>}
      <p className="sub2">Opening this app from another device is as powerful as being at this machine — only enable it if you trust your tailnet.</p>

      <div className="settings-hrow">
        <h3>Drop links</h3>
        {archivedCount > 0 && (
          <button className="btn ghost mini" onClick={() => setShowArchived(!showArchived)}>
            {showArchived ? 'Hide archived' : `Show archived (${archivedCount})`}
          </button>
        )}
      </div>
      <p className="sub2">
        Create a short-lived link — anyone who opens it can drop a file into your inbox from a browser, no Tailscale needed. It
        expires or dies after its first use, and you can revoke it anytime.
      </p>
      <div className="funnelrow">
        <div className="funnel-info">
          <label className="check">
            <input type="checkbox" checked={!!funnel?.enabled} disabled={funnelBusy} onChange={(e) => toggleFunnel(e.target.checked)} />
            Public access (Funnel)
          </label>
          {funnel?.url && (
            <p className="sub2">
              {funnel.url}
              {funnel.enabled ? ' — public drop links live here' : ''}
            </p>
          )}
          <p className="sub2">Makes drop links reachable at your public <code>*.ts.net</code> URL. Only the drop pages are ever exposed. Free — runs on your own machine.</p>
        </div>
        {funnel?.url && (
          <button className="btn ghost" onClick={() => copyText(funnel.url ?? '')}>
            Copy public URL
          </button>
        )}
      </div>
      <div className="dropform">
        <input className="search" type="text" placeholder="Their name (optional)" value={name} onChange={(e) => setName(e.target.value)} spellCheck={false} autoComplete="off" />
        <select className="search" style={{ flex: '0 0 auto', width: 'auto' }} value={ttl} onChange={(e) => setTtl(Number(e.target.value))}>
          <option value="5">5 minutes</option>
          <option value="60">1 hour</option>
          <option value="1440">1 day</option>
        </select>
        <label className="check">
          <input type="checkbox" checked={single} onChange={(e) => setSingle(e.target.checked)} /> single file
        </label>
        <button className="btn" onClick={create}>
          Create link
        </button>
      </div>
      {(showArchived ? links : activeLinks).length > 0 && (
        <div className="drop-links">
          {(showArchived ? links : activeLinks).map((l) => {
            const state = linkState(l);
            const publicUrl = l.publicUrl;
            const meta: string[] = [state];
            if (state === 'active') {
              meta.push('expires ' + fmtAge(l.expires) + ' · ' + (l.maxUses > 0 ? l.uses + '/' + l.maxUses + ' used' : 'unlimited'));
            }
            return (
              <div key={l.token} className={'droplink' + (state === 'active' ? '' : ' dead')}>
                <span>{l.name || 'unnamed link'}</span>
                <span className="dl-meta">{meta.join(' · ')}</span>
                {state === 'active' && (
                  <>
                    <span className="dl-url">
                      {l.url}
                      {funnel?.enabled && publicUrl ? '  ·  ' + publicUrl : ''}
                    </span>
                    <button className="dl-copy" onClick={() => copyText(l.url)}>
                      Copy
                    </button>
                    {funnel?.enabled && publicUrl && (
                      <button className="dl-copy" onClick={() => copyText(publicUrl)}>
                        Copy public
                      </button>
                    )}
                    <button className="btn mini danger" onClick={() => revoke(l.token)}>
                      Revoke
                    </button>
                  </>
                )}
              </div>
            );
          })}
        </div>
      )}

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

      <h3>Shortcuts</h3>
      <p className="sub2">
        <code>/</code> filter · <code>j</code>/<code>k</code> select · <code>s</code> save · <code>d</code> delete ·{' '}
        <code>Ctrl+V</code> paste to send
      </p>
      <h3>About</h3>
      <p className="sub2">Talks directly to your local tailscaled daemon. Nothing leaves this machine except files you send.</p>

      <h3>Support</h3>
      <p className="sub2">
        Owldrop is free — public drop links included. If it saves you time,{' '}
        <a href="https://ko-fi.com/owldrop" target="_blank" rel="noopener">buy me a coffee</a>.
      </p>
    </section>
  );
}
