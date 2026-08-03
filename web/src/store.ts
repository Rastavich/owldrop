// Transient UI state that lives outside the render cycle: SSE handlers and
// async flows update it, React components subscribe via useSyncExternalStore.
import { useSyncExternalStore } from 'react';
import type { SaveProgress, SendProgress, WaitingFile } from './types';

export function createStore<T>(init: T) {
  let state = init;
  const listeners = new Set<() => void>();
  return {
    get: () => state,
    set: (patch: Partial<T> | ((s: T) => T)) => {
      state = typeof patch === 'function' ? (patch as (s: T) => T)(state) : { ...state, ...patch };
      for (const l of listeners) l();
    },
    subscribe: (l: () => void) => {
      listeners.add(l);
      return () => {
        listeners.delete(l);
      };
    },
  };
}

export function useStore<T, S>(
  store: { get: () => T; subscribe: (l: () => void) => () => void },
  select: (s: T) => S,
): S {
  return useSyncExternalStore(store.subscribe, () => select(store.get()));
}

// --- toasts ---------------------------------------------------------------

export interface ToastAction {
  label: string;
  fn: () => void;
}

export interface Toast {
  id: number;
  msg: string;
  kind: 'ok' | 'err';
  out?: boolean;
  actions?: ToastAction[];
}

let toastId = 0;
export const toastsStore = createStore<Toast[]>([]);

export function toast(msg: string, actions?: ToastAction | ToastAction[], kind: 'ok' | 'err' = 'ok') {
  const id = ++toastId;
  const list = actions ? (Array.isArray(actions) ? actions : [actions]) : undefined;
  toastsStore.set((s) => [...s, { id, msg, kind, actions: list }]);
  window.setTimeout(() => toastsStore.set((s) => s.map((t) => (t.id === id ? { ...t, out: true } : t))), 4500);
  window.setTimeout(() => toastsStore.set((s) => s.filter((t) => t.id !== id)), 5000);
}

export function dismissToast(id: number) {
  toastsStore.set((s) => s.filter((t) => t.id !== id));
}

// --- daemon status --------------------------------------------------------

export const daemonStore = createStore<{ ok: boolean | null; msg: string }>({
  ok: null,
  msg: 'connecting…',
});

export function setDaemon(ok: boolean, msg: string) {
  daemonStore.set({ ok, msg });
}

// --- risky-open confirmation ----------------------------------------------

export const confirmStore = createStore<{
  open: boolean;
  title: string;
  text: string;
  yesLabel: string;
  onYes: (() => void) | null;
}>({ open: false, title: 'Confirm', text: '', yesLabel: 'Open anyway', onYes: null });

export function confirmDialog(title: string, text: string, yesLabel: string, onYes: () => void) {
  confirmStore.set({ open: true, title, text, yesLabel, onYes });
}

export function closeConfirm() {
  confirmStore.set({ open: false, onYes: null });
}

// --- folder picker --------------------------------------------------------

export type PickerMode = 'file' | 'default' | 'batch';

export const pickerStore = createStore<{
  open: boolean;
  file: WaitingFile | null;
  mode: PickerMode;
  path: string;
}>({ open: false, file: null, mode: 'file', path: '' });

export function openDirPicker(file: WaitingFile | null, mode: PickerMode) {
  pickerStore.set({ open: true, file, mode, path: '' });
}

export function closeDirPicker() {
  pickerStore.set({ open: false, file: null, mode: 'file', path: '' });
}

// --- in-flight transfers (save/send progress) ------------------------------

export const transfersStore = createStore<{
  saving: Map<string, SaveProgress>;
  sending: Map<string, SendProgress>;
}>({ saving: new Map(), sending: new Map() });

export function putSaving(name: string, progress: SaveProgress) {
  transfersStore.set((s) => {
    const next = new Map(s.saving);
    next.set(name, progress);
    return { ...s, saving: next };
  });
}

export function updateSaving(name: string, patch: Partial<SaveProgress>) {
  transfersStore.set((s) => {
    const next = new Map(s.saving);
    const cur = next.get(name);
    if (cur) next.set(name, { ...cur, ...patch });
    return { ...s, saving: next };
  });
}

export function removeSaving(name: string) {
  transfersStore.set((s) => {
    const next = new Map(s.saving);
    next.delete(name);
    return { ...s, saving: next };
  });
}

export function putSending(id: string, progress: SendProgress) {
  transfersStore.set((s) => {
    const next = new Map(s.sending);
    next.set(id, progress);
    return { ...s, sending: next };
  });
}

export function updateSending(id: string, patch: Partial<SendProgress>) {
  transfersStore.set((s) => {
    const next = new Map(s.sending);
    const cur = next.get(id);
    if (cur) next.set(id, { ...cur, ...patch });
    return { ...s, sending: next };
  });
}

export function removeSending(id: string) {
  transfersStore.set((s) => {
    const next = new Map(s.sending);
    next.delete(id);
    return { ...s, sending: next };
  });
}
