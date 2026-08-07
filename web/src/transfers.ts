// Save/delete/send operations shared by the views. Progress lives in the
// transfers store; the SSE stream keeps it moving.
import { deleteInboxFile, openPath, saveInboxFile, sendFile as sendFileApi } from './api';
import { openPathWithWarning } from './components/ConfirmModal';
import { queryClient } from './queryClient';
import { putSaving, putSending, removeSaving, removeSending, toast, transfersStore, updateSaving, updateSending } from './store';
import type { WaitingFile } from './types';

export async function saveFile(name: string, size: number, dir: string, source = '') {
  if (transfersStore.get().saving.has(name)) {
    toast(`Already saving ${name}`);
    return;
  }
  putSaving(name, { written: 0, size, done: false });
  try {
    const res = await saveInboxFile(name, dir, source);
    updateSaving(name, { done: true, path: res.path });
    queryClient.setQueryData<WaitingFile[]>(['inbox'], (files = []) => files.filter((f) => f.name !== name));
    toast(`Saved ${name}`, [
      {
        label: 'Open',
        fn: () => {
          openPathWithWarning(res.path);
        },
      },
      {
        label: 'Reveal',
        fn: () => {
          openPath(res.path.slice(0, res.path.lastIndexOf('/'))).catch(() => {});
        },
      },
    ]);
    queryClient.invalidateQueries({ queryKey: ['history'] });
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    updateSaving(name, { done: true, err: msg });
    toast(`Couldn't save ${name}: ${msg}`, undefined, 'err');
  }
  window.setTimeout(() => removeSaving(name), 2500);
}

export async function deleteFile(name: string, source = '') {
  try {
    await deleteInboxFile(name, source);
    queryClient.setQueryData<WaitingFile[]>(['inbox'], (files = []) => files.filter((f) => f.name !== name));
    toast(`Deleted ${name} from the inbox`);
    queryClient.invalidateQueries({ queryKey: ['history'] });
  } catch (e) {
    toast(`Couldn't delete: ${e instanceof Error ? e.message : e}`, undefined, 'err');
  }
}

export async function sendFileToDevices(file: File, targets: { id: string; name: string }[]) {
  if (!targets.length) {
    toast('Pick a device to send to first', undefined, 'err');
    return;
  }
  for (const t of targets) {
    const id = uuid();
    putSending(id, { name: file.name, peer: t.id, peerName: t.name, sent: 0, size: file.size, done: false });
    (async () => {
      try {
        await sendFileApi(id, t.id, file.name, file);
        updateSending(id, { done: true });
        toast(`Sent ${file.name} to ${t.name}`);
        queryClient.invalidateQueries({ queryKey: ['history'] });
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e);
        updateSending(id, { done: true, err: msg });
        toast(`Failed to send ${file.name} to ${t.name}: ${msg}`, undefined, 'err');
      }
      window.setTimeout(() => {
        if (transfersStore.get().sending.get(id)?.done) removeSending(id);
      }, 5000);
    })();
  }
}
