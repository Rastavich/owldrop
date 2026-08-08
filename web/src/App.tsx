import { Link, Outlet } from '@tanstack/react-router';
import { useQuery } from '@tanstack/react-query';
import { useEffect } from 'react';
import { getDropLinks, getInbox } from './api';
import { connectEvents } from './events';
import DaemonPill from './components/DaemonPill';
import ConnectBanner from './components/ConnectBanner';
import ConfirmModal from './components/ConfirmModal';
import FolderPicker from './components/FolderPicker';
import Toasts from './components/Toasts';

const TAB_CLASS = 'tab';

export default function App() {
  const { data: inbox = [] } = useQuery({ queryKey: ['inbox'], queryFn: getInbox });
  const { data: links = [] } = useQuery({ queryKey: ['droplinks'], queryFn: getDropLinks });
  const activeLinks = links.filter((l) => !l.expired && !l.revoked && (l.maxUses === 0 || l.uses < l.maxUses)).length;

  useEffect(() => connectEvents(), []);
  useEffect(() => {
    document.title = inbox.length ? `(${inbox.length}) Owldrop` : 'Owldrop';
  }, [inbox.length]);

  return (
    <>
      <header>
        <div className="brand">
          {/* The owl-eyes mark (same family as the tray/window/app icon). */}
          <svg className="logo" viewBox="0 0 512 512">
            <defs>
              <linearGradient id="owlbg" x1="0" y1="0" x2="1" y2="1">
                <stop offset="0" stopColor="#6d7bff" />
                <stop offset="1" stopColor="#9a5cff" />
              </linearGradient>
            </defs>
            <rect width="512" height="512" rx="112" fill="url(#owlbg)" />
            <g transform="translate(0,18)">
              <path d="M148 116 L214 178 L128 190 Z" fill="#efeaff" />
              <path d="M364 116 L298 178 L384 190 Z" fill="#efeaff" />
              <circle cx="192" cy="240" r="88" fill="#efeaff" />
              <circle cx="320" cy="240" r="88" fill="#efeaff" />
              <circle cx="192" cy="240" r="36" fill="#1a1440" />
              <circle cx="320" cy="240" r="36" fill="#1a1440" />
              <circle cx="205" cy="227" r="12" fill="#fff" />
              <circle cx="333" cy="227" r="12" fill="#fff" />
              <path d="M234 302 L278 302 L256 346 Z" fill="#ffb224" />
            </g>
          </svg>
          <div>
            <h1>Owldrop</h1>
            <p className="tag">files across your tailnet</p>
          </div>
        </div>
        <DaemonPill />
      </header>

      <nav className="tabs">
        <Link to="/" className={TAB_CLASS} activeProps={{ className: TAB_CLASS + ' active' }} activeOptions={{ exact: true }}>
          Inbox {inbox.length > 0 && <span className="badge">{inbox.length}</span>}
        </Link>
        <Link to="/send" className={TAB_CLASS} activeProps={{ className: TAB_CLASS + ' active' }}>
          Send
        </Link>
        <Link to="/history" className={TAB_CLASS} activeProps={{ className: TAB_CLASS + ' active' }}>
          History
        </Link>
        <Link to="/drops" className={TAB_CLASS} activeProps={{ className: TAB_CLASS + ' active' }}>
          Drop links {activeLinks > 0 && <span className="badge">{activeLinks}</span>}
        </Link>
        <Link to="/sync" className={TAB_CLASS} activeProps={{ className: TAB_CLASS + ' active' }}>
          Sync
        </Link>
        <Link to="/settings" className={TAB_CLASS} activeProps={{ className: TAB_CLASS + ' active' }}>
          Settings
        </Link>
      </nav>

      <ConnectBanner />

      <main>
        <Outlet />
      </main>

      <footer>Talks directly to your local tailscaled daemon — nothing leaves this machine except the files you send.</footer>

      <Toasts />
      <ConfirmModal />
      <FolderPicker />
    </>
  );
}
