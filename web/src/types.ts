// Shared types for the Taildrop API and SSE event stream. Field names match
// the Go server's JSON (server.go, taildrop.go, history.go, drops.go).

export interface PageConfig {
  token: string;
  saveDir: string;
}

export interface AppConfig {
  saveDir: string;
  autoSave: boolean;
  lan: boolean;
  lanUrl?: string;
  relayUrl?: string; // set = relay mode: public drops via the seller's relay
  ntfyTopic?: string; // set = push a phone notification via ntfy after sends
  ntfyServer?: string; // empty = https://ntfy.sh
  notifyArrival: boolean;
  notifySave: boolean;
  notifySend: boolean;
  notifyError: boolean;
}

export interface Device {
  id: string;
  name: string;
  os: string;
  online: boolean;
  lastSeen?: string;
  taildrop: string; // "available" or a human reason
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

export interface PremiumState {
  configured: boolean;
  active: boolean;
  status: 'active' | 'trialing' | 'inactive' | 'unconfigured';
  priceLabel?: string;
  periodEnd?: number; // unix seconds
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
  | { type: 'update'; kind: 'available' | 'none' | 'downloading' | 'installed' | 'error'; detail?: unknown };

export interface UpdateState {
  current: string;
  latest?: string;
  available: boolean;
  state: 'idle' | 'checking' | 'available' | 'downloading' | 'installed' | 'error' | 'disabled';
  error?: string;
}
