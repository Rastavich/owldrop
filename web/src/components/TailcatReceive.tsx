import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { dropTailcatReceive, getConfig, getTailcat, inspectTailcat, openExternal, receiveTailcat } from '../api';
import { closeTailcatReceive, tailcatReceiveStore, toast, useStore } from '../store';
import { copyText, fmtSize, pct } from '../utils';
import type { TailcatInspect, TailcatReceive } from '../types';
import { openPathWithWarning } from './ConfirmModal';

const INSTALL_URL = 'https://github.com/tailscale/tailcat#install';
const PREVIEW_ROWS = 8;

// "Receive via tailcat": paste what a sender gave you (address, the
// `tailcat cp …` command, or an owldrop.app/r link), see what's in the share,
// and download it into a folder. No terminal, no scp.
export function TailcatReceiveModal() {
  const st = useStore(tailcatReceiveStore, (s) => s);
  const qc = useQueryClient();
  const { data: tc } = useQuery({ queryKey: ['tailcat'], queryFn: getTailcat, enabled: st.open });
  const { data: config } = useQuery({ queryKey: ['config'], queryFn: getConfig, enabled: st.open });
  const [input, setInput] = useState('');
  const [dir, setDir] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [share, setShare] = useState<TailcatInspect | null>(null);

  const lookUp = async (text: string) => {
    if (!text.trim()) return;
    setBusy(true);
    setErr('');
    setShare(null);
    try {
      setShare(await inspectTailcat(text));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
    setBusy(false);
  };

  // Fresh state on every open; an owldrop:// link looks the share up at once.
  useEffect(() => {
    if (!st.open) return;
    setInput(st.input);
    setErr('');
    setShare(null);
    setBusy(false);
    if (st.auto && st.input) lookUp(st.input);
  }, [st.open, st.input, st.auto]);

  useEffect(() => {
    if (st.open && !dir && config?.saveDir) setDir(config.saveDir);
  }, [st.open, config?.saveDir, dir]);

  if (!st.open) return null;

  const download = async () => {
    if (!share) return;
    setBusy(true);
    try {
      await receiveTailcat(share.addr, dir.trim(), share.total);
      qc.invalidateQueries({ queryKey: ['tailcat'] });
      toast('Downloading via tailcat — progress is on the Inbox tab');
      closeTailcatReceive();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
    setBusy(false);
  };

  const notInstalled = tc && !tc.installed;

  return (
    <div className="modal-overlay">
      <div className="modal">
        <div className="modal-head">
          <span className="modal-title">Receive via tailcat</span>
          <button className="btn x" onClick={closeTailcatReceive}>
            ✕
          </button>
        </div>
        <div className="modal-body">
          {notInstalled ? (
            <p className="confirm-text">
              tailcat isn't installed on this machine.{' '}
              <button className="linkbtn" onClick={() => openExternal(INSTALL_URL).catch(() => copyText(INSTALL_URL))}>
                Install it
              </button>
              , then try again.
            </p>
          ) : (
            <>
              <p className="tc-sub">
                Paste the address, link or <code>tailcat cp</code> command you were sent. Owldrop downloads every file in
                the share.
              </p>
              <textarea
                className="tc-input"
                rows={3}
                spellCheck={false}
                autoFocus={!st.input}
                placeholder="tc… address, https://owldrop.app/r#… link, or tailcat cp command"
                value={input}
                onChange={(e) => {
                  setInput(e.target.value);
                  setShare(null);
                  setErr('');
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.shiftKey) {
                    e.preventDefault();
                    lookUp(input);
                  } else if (e.key === 'Escape') closeTailcatReceive();
                }}
              />
              {busy && !share && <p className="tc-sub">Connecting to the share… (this can take a few seconds)</p>}
              {err && <p className="tc-err">{err}</p>}
              {share && (
                <>
                  <div className="tc-files">
                    {share.files.slice(0, PREVIEW_ROWS).map((f) => (
                      <div key={f.path} className="tc-file">
                        <span className="name">{f.path}</span>
                        <span className="hint">{fmtSize(f.size)}</span>
                      </div>
                    ))}
                    {share.files.length > PREVIEW_ROWS && (
                      <div className="tc-file hint">…and {share.files.length - PREVIEW_ROWS} more</div>
                    )}
                  </div>
                  <p className="tc-sub">
                    {share.files.length} file{share.files.length === 1 ? '' : 's'} · {fmtSize(share.total)}. Only accept
                    files from people you trust.
                  </p>
                  <label className="dir-field">
                    <span>Save to</span>
                    <input type="text" value={dir} onChange={(e) => setDir(e.target.value)} spellCheck={false} autoComplete="off" />
                  </label>
                </>
              )}
            </>
          )}
        </div>
        <div className="modal-foot">
          <span className="spacer" />
          <button className="btn ghost" onClick={closeTailcatReceive}>
            Cancel
          </button>
          {share ? (
            <button className="btn" disabled={busy || !dir.trim()} onClick={download}>
              Download
            </button>
          ) : (
            <button className="btn" disabled={busy || !input.trim() || !!notInstalled} onClick={() => lookUp(input)}>
              {busy ? 'Connecting…' : 'Look up'}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

// Running and finished tailcat downloads, shown on the Inbox tab.
export function TailcatReceives() {
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: ['tailcat'], queryFn: getTailcat });
  const receives = data?.receives ?? [];
  if (!receives.length) return null;
  const drop = (r: TailcatReceive) =>
    dropTailcatReceive(r.id)
      .catch((e) => toast(e instanceof Error ? e.message : String(e), undefined, 'err'))
      .finally(() => qc.invalidateQueries({ queryKey: ['tailcat'] }));
  return (
    <div className="list tc-receives">
      {receives.map((r) => (
        <div key={r.id} className="row">
          <div className="chip c-arc">TC</div>
          <div className="meta">
            <div className="name">
              {r.saved?.length ? r.saved.map((p) => p.split(/[\\/]/).pop()).join(', ') : 'tailcat share'}
              {r.files > 0 && ` · ${r.files} file${r.files === 1 ? '' : 's'}`}
            </div>
            {r.done ? (
              r.err ? (
                <div className="sub" style={{ color: r.err === 'cancelled' ? 'var(--muted)' : 'var(--red)' }}>
                  {r.err === 'cancelled' ? 'Cancelled' : r.err}
                </div>
              ) : (
                <div className="sub" style={{ color: 'var(--green)' }}>
                  ✓ Saved to {r.dir} · {fmtSize(r.total)}
                </div>
              )
            ) : (
              <div className="bar">
                <div className="track">
                  <div className="fill" style={{ width: pct(r.received, r.total) + '%' }} />
                </div>
                <div className="lbl">
                  {r.total > 0
                    ? `receiving ${r.current ?? ''}… ${fmtSize(r.received)} / ${fmtSize(r.total)} (${pct(r.received, r.total)}%)`
                    : 'connecting to the share…'}
                </div>
              </div>
            )}
          </div>
          <div className="actions">
            {r.done && !r.err && r.saved?.length === 1 && (
              <button className="btn mini" onClick={() => openPathWithWarning(r.saved![0])}>
                Open
              </button>
            )}
            {r.done && !r.err && (
              <button className="btn mini" onClick={() => openPathWithWarning(r.dir)}>
                Folder
              </button>
            )}
            <button className="btn mini" onClick={() => drop(r)}>
              {r.done ? 'Dismiss' : 'Cancel'}
            </button>
          </div>
        </div>
      ))}
    </div>
  );
}
