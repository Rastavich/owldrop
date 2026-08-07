import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useMemo, useState } from 'react';
import { clearHistory as clearHistoryApi, getHistory } from '../api';
import { openPathWithWarning } from '../components/ConfirmModal';
import { toast } from '../store';
import { chipClass, chipLabel, fmtAge, fmtSize, receiveHint } from '../utils';
import type { HistoryEvent } from '../types';

type HistoryFilter = 'all' | 'received' | 'sent';

const EMPTY_STATS = { received: 0, receivedBytes: 0, sent: 0, sentBytes: 0, failed: 0 };

interface Session {
  id: string;
  events: HistoryEvent[];
  name: string;
  size: number;
  arrived: string;
}

function historySessions(events: HistoryEvent[]): Session[] {
  const byId = new Map<string, Session>();
  for (const e of events) {
    let s = byId.get(e.id);
    if (!s) {
      s = { id: e.id, events: [], name: e.name, size: e.size, arrived: e.ts };
      byId.set(e.id, s);
    }
    s.events.push(e);
    if (e.ts < s.arrived) s.arrived = e.ts;
    if (e.kind === 'arrived' && e.size > 0) s.size = e.size;
  }
  return [...byId.values()].sort((a, b) => (a.arrived < b.arrived ? 1 : -1));
}

export default function History() {
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: ['history'], queryFn: getHistory });
  const events = data?.events ?? [];
  const stats = data?.stats;
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState<HistoryFilter>('all');
  const [armed, setArmed] = useState(false);

  const sessions = useMemo(() => historySessions(events), [events]);
  const visible = useMemo(() => {
    const q = search.toLowerCase().trim();
    return sessions.filter((s) => {
      if (filter === 'received' && !s.events.some((e) => e.kind === 'arrived')) return false;
      if (filter === 'sent' && !s.events.some((e) => e.kind === 'sent' || e.kind === 'send_failed')) return false;
      if (q && !(s.name.toLowerCase().includes(q) || s.events.some((e) => (e.path ?? '').toLowerCase().includes(q)))) return false;
      return true;
    });
  }, [sessions, search, filter]);

  const clear = async () => {
    if (!armed) {
      setArmed(true);
      window.setTimeout(() => setArmed(false), 3000);
      return;
    }
    setArmed(false);
    try {
      await clearHistoryApi();
      qc.setQueryData(['history'], { events: [], stats: EMPTY_STATS });
      toast('History cleared');
    } catch (e) {
      toast("Couldn't clear history: " + (e instanceof Error ? e.message : e), undefined, 'err');
    }
  };

  const exportJson = () => {
    const blob = new Blob([JSON.stringify(events, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'owldrop-history-' + new Date().toISOString().slice(0, 10) + '.json';
    document.body.appendChild(a);
    a.click();
    a.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 5000);
    toast('History exported');
  };

  return (
    <section className="pane">
      <div className="toolbar">
        <input
          className="search"
          type="text"
          placeholder="Search history…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          spellCheck={false}
          autoComplete="off"
        />
        <span className="stats">
          {events.length
            ? `${stats?.received ?? 0} received · ${fmtSize(stats?.receivedBytes ?? 0)} · ${stats?.sent ?? 0} sent`
            : ''}
        </span>
        <button className="btn ghost" onClick={exportJson}>
          Export
        </button>
        <button className="btn ghost danger-text" onClick={clear}>
          {armed ? 'Really clear?' : 'Clear'}
        </button>
      </div>
      <div className="chips">
        {(
          [
            ['all', 'All'],
            ['received', 'Received'],
            ['sent', 'Sent'],
          ] as [HistoryFilter, string][]
        ).map(([f, label]) => (
          <button key={f} className={'chip-btn' + (filter === f ? ' active' : '')} onClick={() => setFilter(f)}>
            {label}
          </button>
        ))}
      </div>

      {events.length === 0 ? (
        <div className="empty">
          <svg viewBox="0 0 48 48" fill="none">
            <circle cx="24" cy="24" r="16" stroke="#8a93a8" strokeWidth="2.5" />
            <path d="M24 14v10l7 4" stroke="#8a93a8" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          <p className="t">No history yet</p>
          <p className="s">Files you receive or send will be logged here, along with where they ended up.</p>
        </div>
      ) : (
        <div className="list">
          {visible.map((s) => (
            <HistoryRow key={s.id} session={s} />
          ))}
        </div>
      )}
    </section>
  );
}

function HistoryRow({ session }: { session: Session }) {
  const saved = session.events.find((e) => e.kind === 'saved') ?? null;
  const deleted = session.events.some((e) => e.kind === 'deleted');
  const sent = session.events.find((e) => e.kind === 'sent') ?? null;
  const failed = session.events.find((e) => e.kind === 'send_failed') ?? null;
  const savedPath = saved?.path;
  const isSend = !!sent || !!failed;
  const mobile = sent ? sent.peerOS === 'android' || sent.peerOS === 'ios' : false;
  const status = failed ? 'failed' : sent ? (mobile ? 'delivered' : 'sent') : saved ? 'saved' : deleted ? 'deleted' : 'waiting';
  const sub: React.ReactNode[] = [];
  sub.push(
    <span key="st" className={'status ' + (mobile ? 'sent' : status)}>
      {status}
    </span>,
  );
  if (isSend) {
    sub.push(' to ' + (failed ? failed.peer : sent ? sent.peer : ''));
    const hint = sent ? receiveHint(sent.peerOS) : null;
    if (hint) sub.push(' · ' + hint);
  } else if (session.events.some((e) => e.source === 'link')) {
    sub.push(' via drop link');
  }
  if (session.size > 0) sub.push(' · ' + fmtSize(session.size));
  sub.push(' · ' + fmtAge(session.arrived));

  return (
    <div className="row">
      <div className={'chip ' + chipClass(session.name)}>{chipLabel(session.name)}</div>
      <div className="meta">
        <div className="name">{session.name}</div>
        <div className="sub">{sub}</div>
        {savedPath && <div className="hpath">{savedPath}</div>}
      </div>
      {savedPath && (
        <div className="actions">
          <button className="btn mini" onClick={() => openPathWithWarning(savedPath)}>
            Open
          </button>
          <button className="btn mini" onClick={() => openPathWithWarning(savedPath.slice(0, savedPath.lastIndexOf('/')))}>
            Reveal
          </button>
        </div>
      )}
    </div>
  );
}
