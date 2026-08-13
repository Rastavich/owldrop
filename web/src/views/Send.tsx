import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import {
  clearSharePending,
  createDropLink,
  getDevices,
  getSharePending,
  sendShareFile,
} from '../api';
import { sendFileToDevices } from '../transfers';
import { toast, transfersStore, useStore } from '../store';
import { chipClass, chipLabel, copyText, fmtSize, isTaggedTaildropBlock, pct, receiveHint, stamp } from '../utils';
import type { Device } from '../types';

export default function Send() {
  const qc = useQueryClient();
  const { data: devices = [] } = useQuery({ queryKey: ['devices'], queryFn: getDevices });
  const { data: pending = [] } = useQuery({ queryKey: ['share'], queryFn: getSharePending });
  const { sending } = useStore(transfersStore, (s) => s);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [popoverOpen, setPopoverOpen] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  const [shareBusy, setShareBusy] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const tagged = devices.filter((d) => isTaggedTaildropBlock(d.taildrop));

  // Default to the first usable device so "send" works out of the box.
  useEffect(() => {
    setSelected((prev) => {
      if (prev.size > 0) return prev;
      const first =
        devices.find((d) => d.taildrop === 'available' && d.online) ?? devices.find((d) => d.taildrop === 'available');
      return first ? new Set([first.id]) : prev;
    });
  }, [devices]);

  // Refresh devices while this tab is open (the route unmounts it elsewhere).
  useEffect(() => {
    const t = window.setInterval(() => qc.invalidateQueries({ queryKey: ['devices'] }), 60000);
    return () => window.clearInterval(t);
  }, [qc]);

  const byId = new Map(devices.map((d) => [d.id, d]));
  const targets = [...selected]
    .map((id) => byId.get(id))
    .filter((d): d is Device => !!d && d.taildrop === 'available')
    .map((d) => ({ id: d.id, name: d.name }));
  const pickerLabel = targets.length
    ? 'Send to: ' + targets.map((t) => t.name).join(', ') + (targets.length > 1 ? ` (${targets.length})` : '')
    : 'Select devices…';

  // Close the popover on outside clicks.
  useEffect(() => {
    if (!popoverOpen) return;
    const onDoc = (e: PointerEvent) => {
      if (!(e.target as HTMLElement).closest('.picker-wrap')) setPopoverOpen(false);
    };
    document.addEventListener('click', onDoc);
    return () => document.removeEventListener('click', onDoc);
  }, [popoverOpen]);

  const handleFiles = (list: FileList | File[]) => {
    for (const f of Array.from(list)) sendFileToDevices(f, targets);
  };

  // Ctrl+V in the Send tab (outside inputs) pastes an image or text as a file.
  useEffect(() => {
    const onPaste = (e: ClipboardEvent) => {
      const tag = (e.target as HTMLElement).tagName;
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
      const dt = e.clipboardData;
      if (!dt) return;
      for (const it of dt.items) {
        if (it.kind === 'file' && it.type.startsWith('image/')) {
          e.preventDefault();
          const f = it.getAsFile();
          if (f) sendPastedImage(f, targets);
          return;
        }
      }
      const text = dt.getData('text/plain');
      if (text && text.trim()) {
        e.preventDefault();
        sendPastedText(text, targets);
      }
    };
    window.addEventListener('paste', onPaste);
    return () => window.removeEventListener('paste', onPaste);
  }, [targets]);

  const pasteButton = async () => {
    try {
      if (navigator.clipboard && navigator.clipboard.read) {
        const items = await navigator.clipboard.read();
        for (const it of items) {
          for (const type of it.types) {
            if (type.startsWith('image/')) {
              const blob = await it.getType(type);
              sendPastedImage(new File([blob], 'pasted', { type }), targets);
              return;
            }
          }
        }
      }
      const text = await navigator.clipboard.readText();
      if (text && text.trim()) {
        sendPastedText(text, targets);
        return;
      }
      toast('Clipboard is empty', undefined, 'err');
    } catch (e) {
      toast("Couldn't read the clipboard: " + (e instanceof Error ? e.message : e), undefined, 'err');
    }
  };

  const sendPending = async (id: string) => {
    if (targets.length === 0) {
      toast('Pick a device first', undefined, 'err');
      return;
    }
    setShareBusy(true);
    try {
      await sendShareFile(
        id,
        targets.map((t) => t.id),
      );
      toast('Sent to ' + targets.map((t) => t.name).join(', '));
      qc.invalidateQueries({ queryKey: ['share'] });
    } catch (e) {
      toast("Couldn't send: " + (e instanceof Error ? e.message : e), undefined, 'err');
      qc.invalidateQueries({ queryKey: ['share'] });
    }
    setShareBusy(false);
  };

  const copyBoxDropLink = async () => {
    try {
      const res = await createDropLink('This box', 60, 0, 4);
      await copyText(res.shareUrl || res.publicUrl || res.url, true);
      toast(
        'Drop link to this machine copied. Tailscale will not Taildrop to tagged devices — install Owldrop on the NAS and share a link from there to land files on that box.',
      );
    } catch (e) {
      toast("Couldn't create link: " + (e instanceof Error ? e.message : e), undefined, 'err');
    }
  };

  const toggleDevice = (id: string, checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  return (
    <section className="pane">
      <div className="toolbar">
        <div className="dir-field picker-wrap">
          <span>Send to</span>
          <button className="picker-btn" onClick={() => setPopoverOpen((v) => !v)}>
            {pickerLabel}
          </button>
          {popoverOpen && (
            <div className="popover">
              <div className="popover-list">
                {devices.map((d) => {
                  const usable = d.taildrop === 'available';
                  const taggedPeer = isTaggedTaildropBlock(d.taildrop);
                  return (
                    <label
                      key={d.id}
                      className={'popover-item' + (usable ? '' : taggedPeer ? ' tagged' : ' disabled')}
                    >
                      <input
                        type="checkbox"
                        checked={usable && selected.has(d.id)}
                        disabled={!usable}
                        onChange={(e) => toggleDevice(d.id, e.target.checked)}
                      />
                      <span className="pv-name">
                        {d.name + (d.os ? ' (' + d.os + ')' : '') + (!d.online ? ' · offline' : usable ? '' : taggedPeer ? '' : ' · ' + d.taildrop)}
                        {taggedPeer && (
                          <span className="tagged-hint">
                            Tailscale will not Taildrop to tagged devices. Install Owldrop on that box and share a drop
                            link from there — a link from here lands on this machine, not the NAS.
                          </span>
                        )}
                      </span>
                      {d.online && d.curAddr && <span className="badge transport" title="Direct connection">⚡</span>}
                      {d.online && !d.curAddr && d.relay && (
                        <span className="badge transport relay" title={'Relayed via ' + d.relay}>
                          {d.relay}
                        </span>
                      )}
                    </label>
                  );
                })}
              </div>
              <div className="popover-foot">
                <button
                  className="btn ghost mini"
                  onClick={() => {
                    setSelected(new Set(devices.filter((d) => d.taildrop === 'available' && d.online).map((d) => d.id)));
                  }}
                >
                  All online
                </button>
                <button className="btn ghost mini" onClick={() => setSelected(new Set())}>
                  Clear
                </button>
                <span className="spacer" />
                {tagged.length > 0 && (
                  <button className="btn ghost mini" onClick={copyBoxDropLink}>
                    Copy drop link to this machine
                  </button>
                )}
              </div>
            </div>
          )}
        </div>
        <button className="btn ghost" onClick={() => qc.invalidateQueries({ queryKey: ['devices'] })}>
          Refresh
        </button>
      </div>

      {pending.length > 0 && (
        <div className="share-banner">
          <p className="t">Shared from the OS — pick a device and send</p>
          {pending.map((f) => (
            <div key={f.id} className="share-row">
              <span className="name">
                {f.name} · {fmtSize(f.size)}
              </span>
              <button className="btn mini" disabled={shareBusy} onClick={() => sendPending(f.id)}>
                Send
              </button>
            </div>
          ))}
          <button className="btn ghost mini" disabled={shareBusy} onClick={() => clearSharePending().then(() => qc.invalidateQueries({ queryKey: ['share'] }))}>
            Dismiss
          </button>
        </div>
      )}

      {tagged.length > 0 && !popoverOpen && (
        <p className="s" style={{ margin: '0 0 12px', color: 'var(--muted)', fontSize: 13 }}>
          {tagged.length === 1 ? tagged[0].name + ' is tagged' : tagged.length + ' tagged devices'} — Tailscale will not
          Taildrop there.{' '}
          <button className="linkbtn" onClick={copyBoxDropLink}>
            Copy a drop link to this machine
          </button>
        </p>
      )}

      <div
        className={'drop-zone' + (dragOver ? ' over' : '')}
        onDragOver={(e) => {
          e.preventDefault();
          setDragOver(true);
        }}
        onDragLeave={() => setDragOver(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDragOver(false);
          if (e.dataTransfer.files.length) handleFiles(e.dataTransfer.files);
        }}
      >
        <svg viewBox="0 0 48 48" fill="none">
          <path
            d="M14 36a9.5 9.5 0 1 1 1.4-18.9A12 12 0 0 1 39 19a9 9 0 0 1-2 17H14z"
            stroke="#8a93a8"
            strokeWidth="2.5"
            strokeLinejoin="round"
          />
          <path d="M24 30.5v-11m0 0-4.2 4.2M24 19.5l4.2 4.2" stroke="#8a93a8" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
        <p className="t">Drag &amp; drop files here</p>
        <p className="sub">
          or{' '}
          <label className="link">
            browse
            <input
              ref={fileRef}
              type="file"
              multiple
              hidden
              onChange={(e) => {
                if (e.target.files?.length) handleFiles(e.target.files);
                e.target.value = '';
              }}
            />
          </label>{' '}
          · or{' '}
          <button className="linkbtn" onClick={pasteButton}>
            paste
          </button>{' '}
          an image or text from the clipboard (Ctrl+V)
        </p>
      </div>

      {sending.size > 0 && (
        <div className="list">
          {[...sending.entries()].map(([id, st]) => {
            const dev = st.peerName || byId.get(st.peer)?.name || 'device';
            return (
              <div key={id} className="row">
                <div className={'chip ' + chipClass(st.name)}>{chipLabel(st.name)}</div>
                <div className="meta">
                  <div className="name">{st.name}</div>
                  {st.err ? (
                    <div className="sub" style={{ color: 'var(--red)' }}>
                      {st.err}
                    </div>
                  ) : (
                    st.done && (
                      <>
                        <div className="sub" style={{ color: 'var(--green)' }}>
                          ✓ {byId.get(st.peer)?.os === 'android' || byId.get(st.peer)?.os === 'ios' ? 'Delivered' : 'Sent'} to {dev}
                        </div>
                        {receiveHint(byId.get(st.peer)?.os) && (
                          <div className="sub2">{receiveHint(byId.get(st.peer)?.os)}</div>
                        )}
                      </>
                    )
                  )}
                </div>
                {!st.done && !st.err && (
                  <div className="bar">
                    <div className="track">
                      <div className="fill" style={{ width: pct(st.sent, st.size) + '%' }} />
                    </div>
                    <div className="lbl">
                      {st.size > 0
                        ? `sending to ${dev}… ${fmtSize(st.sent)} / ${fmtSize(st.size)} (${pct(st.sent, st.size)}%)`
                        : `sending to ${dev}… ${fmtSize(st.sent)}`}
                    </div>
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

function sendPastedImage(file: File, targets: { id: string; name: string }[]) {
  const ext = file.type === 'image/jpeg' ? '.jpg' : file.type === 'image/gif' ? '.gif' : file.type === 'image/webp' ? '.webp' : '.png';
  const name = 'clipboard-' + stamp() + ext;
  sendFileToDevices(new File([file], name, { type: file.type }), targets);
}

function sendPastedText(text: string, targets: { id: string; name: string }[]) {
  const name = 'clipboard-' + stamp() + '.txt';
  sendFileToDevices(new File([text], name, { type: 'text/plain' }), targets);
}
