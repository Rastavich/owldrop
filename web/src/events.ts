// SSE event stream → React Query cache + transient stores. Mirrors the
// original UI's handleEvent, but the shell (Wails) raises native
// notifications instead of the webview, so browser notifications are only
// attempted when NOT running in the shell.
import { IS_SHELL } from './api';
import { queryClient } from './queryClient';
import { setDaemon, toast, transfersStore, updateSaving, updateSending } from './store';
import { fmtSize } from './utils';
import type { SseEvent } from './types';

let knownNames = new Set<string>();

export function handleSseEvent(ev: SseEvent) {
  switch (ev.type) {
    case 'inbox': {
      const names = new Set(ev.files.map((f) => f.name));
      const fresh = ev.files.filter((f) => !knownNames.has(f.name));
      knownNames = names;
      queryClient.setQueryData(['inbox'], ev.files);
      if (
        fresh.length &&
        document.hidden &&
        !IS_SHELL &&
        'Notification' in window &&
        Notification.permission === 'granted'
      ) {
        for (const f of fresh) {
          new Notification(f.name, { body: fmtSize(f.size) + ' — click to open Taildrop' });
        }
      }
      if (location.hash.startsWith('#/history')) {
        queryClient.invalidateQueries({ queryKey: ['history'] });
      }
      break;
    }
    case 'devices':
      queryClient.setQueryData(['devices'], ev.devices);
      break;
    case 'save': {
      const cur = transfersStore.get().saving.get(ev.name);
      if (!cur) break;
      updateSaving(ev.name, {
        written: ev.written,
        size: ev.size,
        done: ev.done ?? cur.done,
        path: ev.path,
        err: ev.err,
      });
      if (location.hash.startsWith('#/history')) {
        queryClient.invalidateQueries({ queryKey: ['history'] });
      }
      break;
    }
    case 'send': {
      const cur = transfersStore.get().sending.get(ev.id);
      if (!cur) break;
      updateSending(ev.id, {
        sent: ev.sent,
        size: ev.size >= 0 ? ev.size : cur.size,
        done: ev.done ?? cur.done,
        err: ev.err,
      });
      if (location.hash.startsWith('#/history')) {
        queryClient.invalidateQueries({ queryKey: ['history'] });
      }
      break;
    }
    case 'status':
      setDaemon(!ev.err, ev.err ? 'tailscaled unreachable: ' + ev.err : 'connected to tailscaled');
      break;
    case 'update':
      queryClient.invalidateQueries({ queryKey: ['update'] });
      if (ev.kind === 'available') toast('A new version is available — Settings → Updates to install');
      else if (ev.kind === 'none') toast('You are on the latest version');
      else if (ev.kind === 'downloading') toast('Downloading update…');
      else if (ev.kind === 'installed') toast('Update installed — restarting…');
      else if (ev.kind === 'error') toast('Update failed: ' + String(ev.detail ?? 'unknown error'), undefined, 'err');
      break;
  }
}

export function connectEvents(): () => void {
  const es = new EventSource('/events');
  es.onmessage = (e) => {
    try {
      handleSseEvent(JSON.parse(e.data));
    } catch {
      /* malformed frame */
    }
  };
  // EventSource reconnects on error automatically.
  return () => es.close();
}
