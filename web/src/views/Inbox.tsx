import { useQuery } from '@tanstack/react-query';
import { useEffect, useMemo, useRef, useState } from 'react';
import { CONFIG, getConfig, getInbox, patchConfig } from '../api';
import { deleteFile, saveFile } from '../transfers';
import { openDirPicker, toast, transfersStore, useStore } from '../store';
import { chipClass, chipLabel, fileType, fmtAge, fmtSize, pct } from '../utils';
import type { SaveProgress, WaitingFile } from '../types';

type InboxType = 'all' | 'img' | 'vid' | 'doc' | 'other';

const TYPE_LABELS: Record<InboxType, string> = {
  all: 'All',
  img: 'Images',
  vid: 'Videos',
  doc: 'Docs',
  other: 'Other',
};

function visibleInbox(inbox: WaitingFile[], filter: string, type: InboxType): WaitingFile[] {
  const q = filter.toLowerCase().trim();
  return inbox.filter((f) => {
    if (type !== 'all' && fileType(f.name) !== type) return false;
    if (q && !f.name.toLowerCase().includes(q)) return false;
    return true;
  });
}

export default function Inbox() {
  const { data: inbox = [] } = useQuery({ queryKey: ['inbox'], queryFn: getInbox });
  const { data: config } = useQuery({ queryKey: ['config'], queryFn: getConfig });
  const { saving } = useStore(transfersStore, (s) => s);
  const [filter, setFilter] = useState('');
  const [type, setType] = useState<InboxType>('all');
  const [selIdx, setSelIdx] = useState(-1);
  const [dir, setDir] = useState(CONFIG.saveDir);
  const searchRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (config?.saveDir) setDir(config.saveDir);
  }, [config?.saveDir]);

  const visible = useMemo(() => visibleInbox(inbox, filter, type), [inbox, filter, type]);
  const activeIdx = selIdx >= 0 && selIdx < visible.length ? selIdx : -1;

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement;
      const tag = el.tagName;
      if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA') {
        if (e.key === 'Escape' && el.id === 'inbox-search') {
          el.blur();
          setFilter('');
          setSelIdx(-1);
        }
        return;
      }
      switch (e.key) {
        case '/':
          e.preventDefault();
          searchRef.current?.focus();
          break;
        case 'j':
        case 'ArrowDown':
          e.preventDefault();
          if (visible.length) setSelIdx((i) => (i < 0 ? 0 : Math.min(i + 1, visible.length - 1)));
          break;
        case 'k':
        case 'ArrowUp':
          e.preventDefault();
          if (visible.length) setSelIdx((i) => (i < 0 ? 0 : Math.max(i - 1, 0)));
          break;
        case 's': {
          const f = visible[activeIdx];
          if (f) saveFile(f.name, f.size, dir);
          break;
        }
        case 'd': {
          const f = visible[activeIdx];
          if (f) deleteFile(f.name);
          break;
        }
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [visible, activeIdx, dir]);

  const setDefaultDir = async () => {
    const d = dir.trim();
    if (!d) return;
    try {
      const res = await patchConfig({ saveDir: d });
      toast('Default save folder: ' + res.saveDir);
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), undefined, 'err');
    }
  };

  const toggleAutoSave = async (checked: boolean) => {
    try {
      const res = await patchConfig({ autoSave: checked });
      toast(res.autoSave ? 'Auto-save on — incoming files go straight to ' + res.saveDir : 'Auto-save off');
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), undefined, 'err');
    }
  };

  const saveAll = async () => {
    for (const f of inbox) {
      if (saving.has(f.name)) continue;
      await saveFile(f.name, f.size, dir);
    }
  };

  const hint = inbox.length
    ? visible.length === inbox.length
      ? 'j/k select · s save · d delete · / search'
      : `${visible.length}/${inbox.length} shown · j/k select · s save · d delete · / search`
    : '';

  return (
    <section className="pane">
      <div className="toolbar">
        <label className="dir-field">
          <span>Save to</span>
          <input id="save-dir" type="text" value={dir} onChange={(e) => setDir(e.target.value)} spellCheck={false} autoComplete="off" />
        </label>
        <button className="btn ghost" onClick={setDefaultDir}>
          Set default
        </button>
        <button className="btn ghost" onClick={() => openDirPicker(null, 'default')}>
          Browse…
        </button>
        <button className="btn" onClick={saveAll}>
          Save all
        </button>
        <button className="btn ghost" onClick={() => openDirPicker(null, 'batch')}>
          Save all to…
        </button>
        <label className="check">
          <input type="checkbox" checked={!!config?.autoSave} onChange={(e) => toggleAutoSave(e.target.checked)} />
          Auto-save incoming
        </label>
      </div>
      <div className="toolbar searchbar">
        <input
          ref={searchRef}
          id="inbox-search"
          className="search"
          type="text"
          placeholder="Filter files…  ( / )"
          value={filter}
          onChange={(e) => {
            setFilter(e.target.value);
            setSelIdx(-1);
          }}
          spellCheck={false}
          autoComplete="off"
        />
        <span className="hint">{hint}</span>
      </div>
      <div className="chips">
        {(Object.keys(TYPE_LABELS) as InboxType[]).map((t) => (
          <button
            key={t}
            className={'chip-btn' + (type === t ? ' active' : '')}
            onClick={() => {
              setType(t);
              setSelIdx(-1);
            }}
          >
            {TYPE_LABELS[t]}
          </button>
        ))}
      </div>

      {inbox.length === 0 ? (
        <div className="empty">
          <svg viewBox="0 0 48 48" fill="none">
            <path
              d="M14 36a9.5 9.5 0 1 1 1.4-18.9A12 12 0 0 1 39 19a9 9 0 0 1-2 17H14z"
              stroke="#8a93a8"
              strokeWidth="2.5"
              strokeLinejoin="round"
            />
            <path d="M24 30.5v-11m0 0-4.2 4.2M24 19.5l4.2 4.2" stroke="#8a93a8" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          <p className="t">No files waiting</p>
          <p className="s">When someone on your tailnet sends you a file, it lands here for you to save or delete.</p>
        </div>
      ) : (
        <div className="list">
          {visible.map((f, i) => (
            <InboxRow key={f.name} file={f} selected={i === activeIdx} dir={dir} saving={saving.get(f.name)} />
          ))}
          {[...saving.entries()].map(([name, st]) =>
            st.done || visible.some((f) => f.name === name) ? null : <SavingRow key={'sav-' + name} name={name} st={st} />,
          )}
        </div>
      )}
    </section>
  );
}

function InboxRow({
  file,
  selected,
  dir,
  saving,
}: {
  file: WaitingFile;
  selected: boolean;
  dir: string;
  saving?: SaveProgress;
}) {
  const source = file.source || '';
  if (saving && !saving.done) {
    return (
      <div className={'row' + (selected ? ' selected' : '')}>
        <div className={'chip ' + chipClass(file.name)}>{chipLabel(file.name)}</div>
        <div className="meta">
          <div className="name">{file.name}</div>
        </div>
        <div className="bar">
          <div className="track">
            <div className="fill" style={{ width: pct(saving.written, saving.size) + '%' }} />
          </div>
          <div className="lbl">
            {saving.size > 0
              ? `saving… ${fmtSize(saving.written)} / ${fmtSize(saving.size)} (${pct(saving.written, saving.size)}%)`
              : `saving… ${fmtSize(saving.written)}`}
          </div>
        </div>
      </div>
    );
  }
  return (
    <div className={'row' + (selected ? ' selected' : '')}>
      <div className={'chip ' + chipClass(file.name)}>{chipLabel(file.name)}</div>
      <div className="meta">
        <div className="name">{file.name}</div>
        <div className="sub">
          {fmtSize(file.size)} · {fmtAge(file.arrived)}
          {file.source === 'link' ? (file.sender ? ` · via drop link from ${file.sender}` : ' · via drop link') : ''}
        </div>
      </div>
      <div className="actions">
        <button className="btn mini primary" onClick={() => saveFile(file.name, file.size, dir, source)}>
          Save
        </button>
        <button className="btn mini" onClick={() => openDirPicker(file, 'file')}>
          Save to…
        </button>
        <button className="btn mini danger" onClick={() => deleteFile(file.name, source)}>
          Delete
        </button>
      </div>
    </div>
  );
}

function SavingRow({ name, st }: { name: string; st: SaveProgress }) {
  return (
    <div className="row">
      <div className={'chip ' + chipClass(name)}>{chipLabel(name)}</div>
      <div className="meta">
        <div className="name">{name}</div>
        {st.err && (
          <div className="sub" style={{ color: 'var(--red)' }}>
            {st.err}
          </div>
        )}
      </div>
      {!st.err && (
        <div className="bar">
          <div className="track">
            <div className="fill" style={{ width: pct(st.written, st.size) + '%' }} />
          </div>
          <div className="lbl">
            {st.size > 0
              ? `saving… ${fmtSize(st.written)} / ${fmtSize(st.size)} (${pct(st.written, st.size)}%)`
              : `saving… ${fmtSize(st.written)}`}
          </div>
        </div>
      )}
    </div>
  );
}
