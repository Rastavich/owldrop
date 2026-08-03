import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { browse, CONFIG, mkdir as mkdirApi, patchConfig } from '../api';
import { saveFile } from '../transfers';
import { closeDirPicker, pickerStore, toast, useStore } from '../store';
import type { WaitingFile } from '../types';

const TITLES: Record<string, string> = {
  file: 'Save to folder',
  default: 'Default save folder',
  batch: 'Save all to folder',
};

export default function FolderPicker() {
  const picker = useStore(pickerStore, (s) => s);
  const qc = useQueryClient();
  const [typed, setTyped] = useState('');
  const [newName, setNewName] = useState('');
  const [showNew, setShowNew] = useState(false);

  // First open: start browsing at the default save folder.
  useEffect(() => {
    if (picker.open && picker.path === '') {
      pickerStore.set({ path: CONFIG.saveDir });
    }
  }, [picker.open, picker.path]);

  const { data: bres } = useQuery({
    queryKey: ['browse', picker.path],
    queryFn: () => browse(picker.path),
    enabled: picker.open && picker.path !== '',
  });

  if (!picker.open) return null;

  const useDir = async () => {
    let dir = picker.path;
    const t = typed.trim();
    if (t && t !== picker.path) {
      try {
        dir = (await browse(t)).path;
      } catch (e) {
        toast(e instanceof Error ? e.message : String(e), undefined, 'err');
        return;
      }
    }
    const file = picker.file;
    const mode = picker.mode;
    closeDirPicker();
    if (mode === 'file' && file) {
      saveFile(file.name, file.size, dir);
    } else if (mode === 'default') {
      try {
        const res = await patchConfig({ saveDir: dir });
        qc.setQueryData(['config'], res);
        toast('Default save folder: ' + res.saveDir);
      } catch (e) {
        toast(e instanceof Error ? e.message : String(e), undefined, 'err');
      }
    } else {
      const files: WaitingFile[] = qc.getQueryData(['inbox']) ?? [];
      for (const f of files) saveFile(f.name, f.size, dir);
    }
  };

  const createDir = async () => {
    const name = newName.trim();
    if (!name) return;
    try {
      await mkdirApi(picker.path, name);
      setShowNew(false);
      setNewName('');
      qc.invalidateQueries({ queryKey: ['browse', picker.path] });
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), undefined, 'err');
    }
  };

  const rows: { name: string; path: string }[] = [];
  if (bres?.parent) rows.push({ name: '..', path: bres.parent });
  for (const d of bres?.dirs ?? []) rows.push({ name: d, path: picker.path + '/' + d });

  return (
    <div className="modal-overlay">
      <div className="modal">
        <div className="modal-head">
          <span className="modal-title">{TITLES[picker.mode] ?? 'Choose a folder'}</span>
          <button className="btn x" onClick={closeDirPicker}>
            ✕
          </button>
        </div>
        <div className="modal-body">
          <input
            id="dir-path"
            type="text"
            spellCheck={false}
            autoComplete="off"
            placeholder="/home/you"
            value={typed || picker.path}
            onChange={(e) => setTyped(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') pickerStore.set({ path: typed.trim() || picker.path });
              else if (e.key === 'Escape') closeDirPicker();
            }}
          />
          <div className="dir-list">
            {rows.map((r) => (
              <div
                key={r.name}
                className="dir-row"
                onClick={() => {
                  setTyped('');
                  pickerStore.set({ path: r.path });
                }}
              >
                <svg viewBox="0 0 24 24" width="16" height="16">
                  <path
                    d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="1.8"
                    strokeLinejoin="round"
                  />
                </svg>
                <span>{r.name}</span>
              </div>
            ))}
            {!rows.length && <div className="dir-row" style={{ cursor: 'default' }}>(empty folder)</div>}
          </div>
        </div>
        <div className="modal-foot">
          {showNew ? (
            <input
              id="dir-new-name"
              type="text"
              placeholder="folder name…"
              autoFocus
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') createDir();
                else if (e.key === 'Escape') {
                  setShowNew(false);
                  setNewName('');
                }
              }}
            />
          ) : (
            <button
              className="btn ghost"
              onClick={() => {
                setShowNew(true);
                setNewName('');
              }}
            >
              New folder…
            </button>
          )}
          <span className="spacer" />
          <button className="btn ghost" onClick={closeDirPicker}>
            Cancel
          </button>
          <button className="btn" onClick={useDir}>
            Use this folder
          </button>
        </div>
      </div>
    </div>
  );
}
