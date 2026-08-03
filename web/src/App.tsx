import { Link, Outlet } from '@tanstack/react-router';
import { useQuery } from '@tanstack/react-query';
import { useEffect } from 'react';
import { getInbox } from './api';
import { connectEvents } from './events';
import { setDaemon } from './store';
import DaemonPill from './components/DaemonPill';
import ConfirmModal from './components/ConfirmModal';
import FolderPicker from './components/FolderPicker';
import Toasts from './components/Toasts';

const TAB_CLASS = 'tab';

export default function App() {
  const { data: inbox = [], isSuccess, error } = useQuery({ queryKey: ['inbox'], queryFn: getInbox });

  // The initial inbox fetch proves the daemon is reachable (same as the
  // original boot()); later SSE status events take over.
  useEffect(() => {
    if (isSuccess) setDaemon(true, 'connected to tailscaled');
  }, [isSuccess]);
  useEffect(() => {
    if (error) setDaemon(false, 'tailscaled unreachable: ' + (error instanceof Error ? error.message : error));
  }, [error]);

  useEffect(() => connectEvents(), []);
  useEffect(() => {
    document.title = inbox.length ? `(${inbox.length}) Taildrop` : 'Taildrop';
  }, [inbox.length]);

  return (
    <>
      <header>
        <div className="brand">
          <svg className="logo" viewBox="0 0 40 40" fill="none">
            <defs>
              <linearGradient id="g" x1="0" y1="0" x2="40" y2="40">
                <stop stopColor="#5f6cff" />
                <stop offset="1" stopColor="#8b5cf6" />
              </linearGradient>
            </defs>
            <path
              d="M12 30a8 8 0 1 1 1.2-15.9A10 10 0 0 1 32.5 16 7.5 7.5 0 0 1 31 30.5H12z"
              fill="url(#g)"
              opacity=".92"
            />
            <path
              d="M20 24.5v-9m0 0-3.4 3.4M20 15.5l3.4 3.4"
              stroke="#fff"
              strokeWidth="2.4"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
          <div>
            <h1>Taildrop</h1>
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
        <Link to="/settings" className={TAB_CLASS} activeProps={{ className: TAB_CLASS + ' active' }}>
          Settings
        </Link>
      </nav>

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
