// Shared types for the Owldrop API and SSE event stream. Field names match
// the Go server's JSON (server.go, localapi.go, history.go, drops.go).

export interface PageConfig {
  token: string;
  saveDir: string;
}

export interface AppConfig {
  saveDir: string;
  autoSave: boolean;
  lan: boolean;
  lanUrl?: string;
  lanUrls?: string[];
  ntfyTopic?: string; // set = push a phone notification via ntfy after sends
  ntfyServer?: string; // empty = https://ntfy.sh
  notifyArrival: boolean;
  notifySave: boolean;
  notifySend: boolean;
  notifyError: boolean;
  telemetry: boolean; // anonymous usage stats (opt-out)
  trustedDomains?: string[]; // reverse-proxy hostnames approved in Settings
}

export interface Device {
  id: string;
  name: string;
  os: string;
  online: boolean;
  lastSeen?: string;
  taildrop: string; // "available" or a human reason
  relay?: string; // DERP region ("syd") when reached via relay
  curAddr?: string; // direct address when connected directly
  hidden?: boolean; // operator hid it from the Send picker (Settings)
}

export interface WaitingFile {
  name: string;
  size: number;
  arrived: string;
  source?: string; // "" = taildrop, "link" = drop link
  sender?: string;
}

export type HistoryKind = 'arrived' | 'saved' | 'deleted' | 'sent' | 'send_failed';

export interface HistoryEvent {
  id: string;
  ts: string;
  kind: HistoryKind;
  name: string;
  size: number;
  path?: string;
  peer?: string;
  peerOS?: string;
  source?: string;
}

export interface HistoryStats {
  received: number;
  receivedBytes: number;
  sent: number;
  sentBytes: number;
  failed: number;
}

export interface DropLink {
  token: string;
  name: string;
  expires: string;
  maxUses: number;
  uses: number;
  revoked: boolean;
  expired: boolean;
  url: string;
  publicUrl?: string;
  autoSaveDir?: string;
  ratePerMin: number;
}

export interface ServeState {
  enabled: boolean;
  url?: string;
}

export interface FunnelState {
  enabled: boolean;
  url?: string;
}

export interface TailscaleState {
  reachable: boolean;
  connected: boolean;
  loggedIn: boolean;
  installed: boolean; // false = no Tailscale client found on this machine
  backendState: string;
  hint?: string;
}

export interface BrowseResult {
  path: string;
  parent?: string;
  dirs: string[];
}

export interface SaveProgress {
  written: number;
  size: number;
  done: boolean;
  path?: string;
  err?: string;
}

export interface SendProgress {
  name: string;
  peer: string;
  peerName?: string;
  sent: number;
  size: number;
  done: boolean;
  err?: string;
}

export type SseEvent =
  | { type: 'inbox'; files: WaitingFile[] }
  | { type: 'devices'; devices: Device[] }
  | {
      type: 'save';
      name: string;
      written: number;
      size: number;
      done?: boolean;
      path?: string;
      err?: string;
    }
  | {
      type: 'send';
      id: string;
      peer: string;
      name: string;
      sent: number;
      size: number;
      done?: boolean;
      err?: string;
    }
  | { type: 'status'; err?: string }
  | { type: 'sync' }
  | { type: 'update'; kind: 'available' | 'none' | 'downloading' | 'installed' | 'error'; detail?: unknown };

// One item on the shared Sync board (visible on every device on the tailnet).
export interface SyncItem {
  id: string;
  kind: 'text' | 'file';
  text?: string;
  name?: string;
  size?: number;
  createdAt: string;
}
export interface UpdateState {
  current: string;
  latest?: string;
  available: boolean;
  autoInstall: boolean;
  state: 'idle' | 'checking' | 'available' | 'downloading' | 'installed' | 'error' | 'disabled';
  error?: string;
}
