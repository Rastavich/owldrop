import { useQuery } from '@tanstack/react-query';
import { getTailscaleState } from '../api';

// Tailnet connection pill. Reads the same /api/tailscale query as the
// ConnectBanner (shared cache, polled every few seconds), so it reflects
// `tailscale down` / logged out — not just daemon reachability.
const SHORT: Record<string, string> = {
  NeedsLogin: 'not logged in',
  NoState: 'not logged in',
  NeedsMachineAuth: 'awaiting approval',
  Stopped: 'tailscale stopped',
  Starting: 'connecting…',
};

export default function DaemonPill() {
  const { data } = useQuery({
    queryKey: ['tailscale'],
    queryFn: getTailscaleState,
    refetchInterval: (q) => (q.state.data?.connected ? 10000 : 5000),
    retry: false,
  });

  let cls = '';
  let msg = 'checking tailscale…';
  if (data?.connected) {
    cls = ' ok';
    msg = 'connected to your tailnet';
  } else if (data) {
    cls = ' err';
    msg = data.reachable ? SHORT[data.backendState] || 'not connected' : 'tailscaled unreachable';
  }

  return (
    <div className={'pill' + cls}>
      <span className="dot" />
      <span>{msg}</span>
    </div>
  );
}
