// Typed wrapper around the sidecar's REST API. Every mutating call carries
// the session token the server injected into the page (window.__CONFIG__,
// declared in globals.d.ts).
import type {
  AppConfig,
  BrowseResult,
  Device,
  DropLink,
  FunnelState,
  HistoryEvent,
  HistoryStats,
  PageConfig,
  ServeState,
  SyncItem,
  TailscaleState,
  UpdateState,
  WaitingFile,
} from './types';

export const CONFIG: PageConfig = window.__CONFIG__ ?? { token: '', saveDir: '' };

// True when running inside the Wails shell (the window URL carries ?shell=1).
export const IS_SHELL = new URLSearchParams(window.location.search).has('shell');

interface ApiOpts {
  method?: string;
  json?: unknown;
  body?: BodyInit;
}

// After a container rebuild/update the server keeps the same session token
// (it's persisted in config.json), but if the volume was ever lost the
// token embedded in an already-open page goes stale and mutations 403.
// Reload once to pick up the freshly-embedded token; a second 403 in the
// same page is a real error, so we stop retrying rather than loop.
let reloadedOn403 = false;

export async function api<T>(path: string, opts: ApiOpts = {}): Promise<T> {
  const headers: Record<string, string> = { 'X-Owldrop-Token': CONFIG.token };
  if (opts.json !== undefined) headers['Content-Type'] = 'application/json';
  const res = await fetch(path, {
    method: opts.method || 'GET',
    headers,
    body: opts.json !== undefined ? JSON.stringify(opts.json) : opts.body,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    if (opts.method && opts.method !== 'GET' && res.status === 403 && !reloadedOn403) {
      reloadedOn403 = true;
      window.location.reload();
      return await new Promise<never>(() => {}); // the page is reloading
    }
    throw new Error(data.error || 'HTTP ' + res.status);
  }
  return data as T;
}

export const getInbox = () => api<{ files: WaitingFile[] }>('/api/inbox').then((r) => r.files);
export const getConfig = () => api<AppConfig>('/api/config');
export const patchConfig = (patch: Partial<AppConfig>) =>
  api<AppConfig>('/api/config', { method: 'POST', json: patch });
export const getDevices = () => api<{ devices: Device[] }>('/api/devices').then((r) => r.devices);
// Settings visibility list — includes hidden devices, each flagged.
export const getDevicesAll = () => api<{ devices: Device[] }>('/api/devices/all').then((r) => r.devices);
export const setDeviceHidden = (id: string, hidden: boolean) =>
  api('/api/devices/hidden', { method: 'POST', json: { id, hidden } });
export const getHistory = () =>
  api<{ events: HistoryEvent[]; stats: HistoryStats }>('/api/history');
export const clearHistory = () => api('/api/history', { method: 'DELETE' });
export const saveInboxFile = (name: string, dir: string, source: string) =>
  api<{ path: string }>('/api/save', { method: 'POST', json: { name, dir, source } });
export const deleteInboxFile = (name: string, source: string) =>
  api('/api/delete', { method: 'POST', json: { name, source } });
export const sendFile = (id: string, peer: string, name: string, body: Blob) =>
  api('/api/send?' + new URLSearchParams({ id, peer, name }), { method: 'POST', body });
export const openPath = (path: string) => api('/api/open', { method: 'POST', json: { path } });
export const browse = (path: string) =>
  api<BrowseResult>('/api/browse?path=' + encodeURIComponent(path));
export const mkdir = (path: string, name: string) =>
  api('/api/mkdir', { method: 'POST', json: { path, name } });
export const getDropLinks = () => api<{ links: DropLink[] }>('/api/droplinks').then((r) => r.links);
export const createDropLink = (name: string, ttlMinutes: number, maxUses: number, ratePerMin: number) =>
  api<{ url: string; publicUrl?: string; shareUrl: string; link: DropLink }>('/api/droplinks', {
    method: 'POST',
    json: { name, ttlMinutes, maxUses, ratePerMin },
  });
export const revokeDropLink = (token: string) =>
  api('/api/droplinks/' + token + '/revoke', { method: 'POST', json: {} });
export const getFunnel = () => api<FunnelState>('/api/funnel');
export const setFunnel = (enabled: boolean) =>
  api<FunnelState>('/api/funnel', { method: 'POST', json: { enabled } });
export const getServe = () => api<ServeState>('/api/serve');
export const setServe = (enabled: boolean) =>
  api<ServeState>('/api/serve', { method: 'POST', json: { enabled } });
export const setDropLinkAutoSave = (token: string, dir: string) =>
  api<{ ok: boolean; autoSaveDir: string }>('/api/droplinks/' + token + '/autosave', {
    method: 'POST',
    json: { dir },
  });
export const getTailscaleState = () => api<TailscaleState>('/api/tailscale');
export const downloadTailscale = () => api('/api/tailscale/download', { method: 'POST' });
export const testNtfy = () => api('/api/ntfy/test', { method: 'POST' });
export const openExternal = (url: string) => api('/api/open-external', { method: 'POST', json: { url } });
export const tailscaleUp = () => api('/api/tailscale/up', { method: 'POST', json: {} });
export const getSync = () => api<{ items: SyncItem[] }>('/api/sync').then((r) => r.items);
export const addSyncText = (text: string) =>
  api<SyncItem>('/api/sync', { method: 'POST', json: { text } });
export const uploadSyncFile = (file: File) => {
  const fd = new FormData();
  fd.append('file', file, file.name);
  return api<SyncItem>('/api/sync/file', { method: 'POST', body: fd });
};
export const deleteSyncItem = (id: string) => api('/api/sync/' + id, { method: 'DELETE' });
export const clearSync = () => api('/api/sync', { method: 'DELETE' });
export const getUpdateState = () => api<UpdateState>('/api/update');
export const checkUpdate = () => api<UpdateState>('/api/update/check', { method: 'POST', json: {} });
export const installUpdate = () => api('/api/update/install', { method: 'POST', json: {} });
export const getPhoneAccess = () => api<{ url: string }>('/api/phone');
export const getSharePending = () =>
  api<{ files: { id: string; name: string; size: number }[] }>('/api/share').then((r) => r.files);
export const clearSharePending = () => api('/api/share', { method: 'DELETE' });
export const sendShareFile = (id: string, peers: string[]) =>
  api('/api/share/send', { method: 'POST', json: { id, peers } });
