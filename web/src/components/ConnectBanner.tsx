import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { downloadTailscale, getTailscaleState, tailscaleUp } from '../api';
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

  const download = useMutation({
    mutationFn: downloadTailscale,
    onError: (e) => toast(e instanceof Error ? e.message : String(e), undefined, 'err'),
  });

  if (!data || data.connected) return null;

  const retry = () => qc.invalidateQueries({ queryKey: ['tailscale'] });

  // No Tailscale client at all: a fresh machine. Send the user to the
  // download page — "start the service" guidance is meaningless here.
  if (!data.reachable && !data.installed) {
    return (
      <div className="connect-banner">
        <span className="dot" />
        <div className="col">
          <p>Taildrop needs Tailscale to reach your devices — it isn't installed yet.</p>
          <p className="sub2">Install it, sign in, and this window connects on its own.</p>
        </div>
        <button className="btn" disabled={download.isPending} onClick={() => download.mutate()}>
          {download.isPending ? 'Opening…' : 'Download Tailscale'}
        </button>
        <button className="btn ghost" onClick={retry}>
          Retry
        </button>
      </div>
    );
  }

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
