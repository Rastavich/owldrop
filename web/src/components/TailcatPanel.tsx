import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import { createTailcatShare, getTailcat, openExternal, stopTailcatShare, tailcatLink } from '../api';
import { toast } from '../store';
import { copyText, fmtSize } from '../utils';
import type { TailcatShare } from '../types';

const INSTALL_URL = 'https://github.com/tailscale/tailcat#install';

export const TAILCAT_TTLS = [
  { label: '15 minutes', minutes: 15 },
  { label: '1 hour', minutes: 60 },
  { label: '24 hours', minutes: 24 * 60 },
];

// "Send to anyone": share files with someone who isn't on your tailnet. Owldrop
// runs a read-only tailcat file server per share; the recipient runs one
// command. The address is the credential, so shares expire and can be stopped.
export default function TailcatPanel({ ttl, setTtl }: { ttl: number; setTtl: (m: number) => void }) {
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: ['tailcat'], queryFn: getTailcat });
  const [busy, setBusy] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const [, tick] = useState(0);

  // Re-render the "expires in" labels.
  useEffect(() => {
    if (!data?.shares.length) return;
    const t = window.setInterval(() => tick((n) => n + 1), 30000);
    return () => window.clearInterval(t);
  }, [data?.shares.length]);

  if (!data) return null;

  const share = async (files: File[]) => {
    if (!files.length) return;
    setBusy(true);
    try {
      const sh = await createTailcatShare(files, ttl);
      qc.invalidateQueries({ queryKey: ['tailcat'] });
      const ok = await copyText(shareLink(sh), true);
      toast(ok ? 'tailcat share ready — link copied' : 'tailcat share ready');
    } catch (e) {
      toast("Couldn't start tailcat share: " + (e instanceof Error ? e.message : e), undefined, 'err');
    }
    setBusy(false);
  };

  const stop = async (id: string) => {
    try {
      await stopTailcatShare(id);
    } catch (e) {
      toast("Couldn't stop share: " + (e instanceof Error ? e.message : e), undefined, 'err');
    }
    qc.invalidateQueries({ queryKey: ['tailcat'] });
  };

  const install = () =>
    openExternal(INSTALL_URL).catch(() => {
      copyText(INSTALL_URL);
    });

  return (
    <div className="tc-panel">
      <div className="tc-head">
        <div>
          <p className="t">Send to anyone</p>
          <p className="sub">
            For people who aren't on your tailnet. Uses{' '}
            <button className="linkbtn" onClick={install}>
              tailcat
            </button>{' '}
            (Tailscale's encrypted peer-to-peer tunnel, no account needed). Send the recipient the link: it opens in
            their Owldrop, or shows them the one tailcat command to run.
          </p>
        </div>
        {data.installed && (
          <div className="tc-actions">
            <select value={ttl} onChange={(e) => setTtl(Number(e.target.value))} title="The share stops after this">
              {TAILCAT_TTLS.map((t) => (
                <option key={t.minutes} value={t.minutes}>
                  {t.label}
                </option>
              ))}
            </select>
            <button className="btn mini primary" disabled={busy} onClick={() => fileRef.current?.click()}>
              {busy ? 'Starting…' : 'Share files…'}
            </button>
            <input
              ref={fileRef}
              type="file"
              multiple
              hidden
              onChange={(e) => {
                const files = Array.from(e.target.files ?? []);
                e.target.value = '';
                share(files);
              }}
            />
          </div>
        )}
      </div>

      {!data.installed && (
        <p className="sub">
          tailcat isn't installed on this machine.{' '}
          <button className="linkbtn" onClick={install}>
            Install it
          </button>
          , then come back. No restart needed.
        </p>
      )}

      {data.shares.map((sh) => (
        <ShareRow key={sh.id} sh={sh} onStop={() => stop(sh.id)} />
      ))}

      {data.shares.length > 0 && (
        <p className="tc-note">
          Anyone with the link or address can download these files until the share stops. Send it privately. Recipients
          without Owldrop need tailcat:{' '}
          <button className="linkbtn" onClick={() => copyText(INSTALL_URL)}>
            copy install link
          </button>
        </p>
      )}
    </div>
  );
}

function ShareRow({ sh, onStop }: { sh: TailcatShare; onStop: () => void }) {
  const total = sh.files.reduce((n, f) => n + f.size, 0);
  const title = sh.files.length === 1 ? sh.files[0].name : `${sh.files.length} files (${sh.folder})`;
  return (
    <div className="tc-share">
      <div className="tc-share-head">
        <span className="name" title={sh.files.map((f) => f.name).join('\n')}>
          {title} · {fmtSize(total)}
        </span>
        <span className="hint">{expiresIn(sh.expiresAt)}</span>
        <button
          className="btn mini primary"
          onClick={() => copyText(shareLink(sh))}
          title="Opens in the recipient's Owldrop, or shows them the tailcat command. The address stays in the link's #fragment, which never reaches owldrop.app."
        >
          Copy link
        </button>
        <button className="btn mini" onClick={() => copyText(sh.command)} title="For recipients using the tailcat CLI">
          Copy command
        </button>
        <button className="btn mini" onClick={onStop}>
          Stop
        </button>
      </div>
      <code className="tc-cmd" title="Run this on the receiving machine">
        {sh.command}
      </code>
    </div>
  );
}

export function shareLink(sh: TailcatShare): string {
  return tailcatLink(sh.addr, sh.folder || sh.files[0].name);
}

function expiresIn(iso: string): string {
  const mins = Math.max(0, Math.round((new Date(iso).getTime() - Date.now()) / 60000));
  if (mins < 1) return 'expires now';
  if (mins < 90) return `expires in ${mins} min`;
  const h = Math.round(mins / 60);
  return `expires in ${h} h`;
}
