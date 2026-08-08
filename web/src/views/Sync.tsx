// Sync: a shared clipboard/scratchpad. Pasted text and uploaded files are
// visible on every device that can open the app (tailnet/LAN) — changes
// arrive live over SSE while the page is open.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useRef, useState } from 'react';
import {
  addSyncText,
  clearSync,
  CONFIG,
  deleteSyncItem,
  getSync,
  uploadSyncFile,
} from '../api';
import { toast } from '../store';
import { fmtSize } from '../utils';
import type { SyncItem } from '../types';

export default function Sync() {
  const queryClient = useQueryClient();
  const { data: items = [] } = useQuery({ queryKey: ['sync'], queryFn: getSync, refetchInterval: 15000 });
  const [text, setText] = useState('');
  const fileRef = useRef<HTMLInputElement>(null);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['sync'] });

  const postText = useMutation({
    mutationFn: addSyncText,
    onSuccess: () => {
      setText('');
      invalidate();
    },
    onError: (e: Error) => toast(e.message, undefined, 'err'),
  });

  const uploadFile = useMutation({
    mutationFn: uploadSyncFile,
    onSuccess: invalidate,
    onError: (e: Error) => toast(e.message, undefined, 'err'),
  });

  const remove = useMutation({
    mutationFn: deleteSyncItem,
    onSuccess: invalidate,
  });

  const clearAll = useMutation({
    mutationFn: clearSync,
    onSuccess: invalidate,
  });

  const download = async (it: SyncItem) => {
    try {
      const res = await fetch('/api/sync/file/' + it.id, {
        headers: { 'X-Owldrop-Token': CONFIG.token },
      });
      if (!res.ok) throw new Error('HTTP ' + res.status);
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = it.name ?? 'file';
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      toast(String(e), undefined, 'err');
    }
  };

  return (
    <div className="pane">
      <div className="toolbar">
        <h2>Sync</h2>
        <span className="sub2 muted">
          Shared board — pasted text and files appear on every device that can reach this app
          ({items.length} item{items.length === 1 ? '' : 's'})
        </span>
        <button
          className="btn ghost mini"
          disabled={items.length === 0 || clearAll.isPending}
          onClick={() => clearAll.mutate()}
        >
          Clear all
        </button>
      </div>

      <div className="sync-compose">
        <textarea
          className="sync-input"
          placeholder="Paste text, a link, a code snippet… it shows up on every device instantly"
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
              e.preventDefault();
              if (text.trim()) postText.mutate(text);
            }
          }}
          rows={3}
        />
        <div className="row">
          <button className="btn" disabled={!text.trim() || postText.isPending} onClick={() => postText.mutate(text)}>
            Post
          </button>
          <button className="btn ghost" disabled={uploadFile.isPending} onClick={() => fileRef.current?.click()}>
            {uploadFile.isPending ? 'Uploading…' : 'Upload file'}
          </button>
          <input
            ref={fileRef}
            type="file"
            hidden
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) uploadFile.mutate(f);
              e.target.value = '';
            }}
          />
        </div>
      </div>

      <div className="list">
        {items.length === 0 && (
          <p className="muted">
            Nothing on the board yet. Post a link from your laptop and pick it up on your phone — or the
            other way around.
          </p>
        )}
        {items.map((it) => (
          <div key={it.id} className="row sync-item">
            {it.kind === 'text' ? (
              <div className="sync-text">
                <pre className="sync-pre">{it.text}</pre>
                <button
                  className="btn ghost mini"
                  onClick={() => {
                    navigator.clipboard.writeText(it.text ?? '').then(() => toast('Copied'));
                  }}
                >
                  Copy
                </button>
              </div>
            ) : (
              <div className="sync-file">
                <span className="name">{it.name}</span>
                <span className="meta muted">{fmtSize(it.size ?? 0)}</span>
                <button className="btn mini" onClick={() => download(it)}>
                  Download
                </button>
              </div>
            )}
            <button className="btn ghost mini" onClick={() => remove.mutate(it.id)} title="Remove">
              ✕
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}
