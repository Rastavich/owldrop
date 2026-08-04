import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { getTailscaleState, tailscaleUp } from '../api';
import { toast } from '../store';

// Full-width banner shown while the machine has no working tailnet
// connection: tailscaled unreachable, logged out, stopped, or still
// connecting. Polls every few seconds so it disappears on its own once the
// daemon comes up (e.g. the user starts the Tailscale app).
export default function ConnectBanner() {
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ['tailscale'],
    queryFn: getTailscaleState,
    refetchInterval: (q) => (q.state.data?.connected ? 10000 : 5000),
    retry: false,
  });

  const up = useMutation({
    mutationFn: tailscaleUp,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['tailscale'] }),
    onError: (e) => toast(e instanceof Error ? e.message : String(e), undefined, 'err'),
  });

  if (!data || data.connected) return null;

  const retry = () => qc.invalidateQueries({ queryKey: ['tailscale'] });

  return (
    <div className="connect-banner">
      <span className="dot" />
      <p>{data.hint || 'Not connected to your tailnet.'}</p>
      {!data.reachable ? (
        <button className="btn ghost" onClick={retry}>
          Retry
        </button>
      ) : ['NeedsLogin', 'NoState', 'Stopped'].includes(data.backendState) ? (
        <button className="btn" disabled={up.isPending} onClick={() => up.mutate()}>
          {up.isPending ? 'Connecting…' : 'Connect'}
        </button>
      ) : (
        <span className="sub2">waiting…</span>
      )}
    </div>
  );
}
