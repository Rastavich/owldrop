// Mullvad: route this machine's internet traffic through a Mullvad VPN exit
// node. Mullvad servers show up in the tailnet as exit-node peers; the
// server lists them from LocalAPI status and switches via EditPrefs — the
// same machinery `tailscale set --exit-node` drives.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useMemo, useState } from 'react';
import { connectMullvad, disconnectMullvad, getMullvad, setMullvadLan } from '../api';
import { toast } from '../store';
import type { MullvadNode } from '../types';

// Chip label for one server. Mullvad hosts are "<cc>-<city>-<type>-<num>"
// ("de-fra-wg-001": WireGuard relay #1 in Frankfurt). The row already gives
// country and city, so chips keep the meaningful tail ("wg-001") — the
// protocol type plus server number that Mullvad's own server list uses. A
// city's lone server keeps its full host name.
function serverChipLabel(n: MullvadNode, servers: MullvadNode[]): string {
  if (servers.length === 1) return n.host || n.city;
  const prefix = `${n.countryCode.toLowerCase()}-${n.cityCode.toLowerCase()}-`;
  return n.host.startsWith(prefix) ? n.host.slice(prefix.length) : n.host || n.city;
}


export default function Mullvad() {
  const queryClient = useQueryClient();
  const { data: mv } = useQuery({ queryKey: ['mullvad'], queryFn: getMullvad, refetchInterval: 10000 });
  const [query, setQuery] = useState('');

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ['mullvad'] });
    queryClient.invalidateQueries({ queryKey: ['tailscale'] });
  };
  const onErr = (e: Error) => toast(e.message, undefined, 'err');

  const connect = useMutation({ mutationFn: connectMullvad, onSuccess: invalidate, onError: onErr });
  const disconnect = useMutation({ mutationFn: disconnectMullvad, onSuccess: invalidate, onError: onErr });
  const setLan = useMutation({ mutationFn: setMullvadLan, onSuccess: invalidate, onError: onErr });

  // country → city → servers in that city. Many Mullvad servers share one
  // city; only the host code tells them apart.
  const grouped = useMemo(() => {
    const q = query.trim().toLowerCase();
    const byCountry = new Map<string, Map<string, MullvadNode[]>>();
    for (const n of mv?.nodes ?? []) {
      if (q && !`${n.country} ${n.countryCode} ${n.city} ${n.cityCode} ${n.host}`.toLowerCase().includes(q)) continue;
      const cities = byCountry.get(n.country) ?? new Map<string, MullvadNode[]>();
      const servers = cities.get(n.city) ?? [];
      servers.push(n);
      cities.set(n.city, servers);
      byCountry.set(n.country, cities);
    }
    return [...byCountry.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([country, cities]) => ({
        country,
        code: [...cities.values()][0]?.[0]?.countryCode ?? '',
        cities: [...cities.entries()].sort(([a], [b]) => a.localeCompare(b)),
      }));
  }, [mv, query]);

  if (mv && !mv.reachable) {
    return (
      <div className="pane">
        <div className="toolbar">
          <h2>Mullvad</h2>
        </div>
        <p className="muted">{mv.hint || 'tailscaled isn’t running.'}</p>
      </div>
    );
  }

  return (
    <div className="pane">
      <div className="toolbar">
        <h2>Mullvad</h2>
        <span className="sub2 muted">
          Route all internet traffic through a Mullvad VPN server — same WireGuard exit nodes the
          Tailscale app offers
        </span>
      </div>

      {mv && (
        <>
          <div className="row mullvad-status">
            <span className={'pill ' + (mv.connected ? (mv.exitOnline ? 'ok' : 'err') : '')}>
              <span className="dot" />
              {mv.connected
                ? mv.current
                  ? `${mv.current.city} · ${mv.current.host}${mv.exitOnline ? '' : ' (unreachable)'}`
                  : 'Custom exit node'
                : 'Not using an exit node'}
            </span>
            {mv.connected && (
              <button className="btn ghost mini" disabled={disconnect.isPending} onClick={() => disconnect.mutate()}>
                Disconnect
              </button>
            )}
            <label className="check" title="Keep access to printers, file shares and other local devices while connected">
              <input type="checkbox" checked={mv.allowLan} onChange={(e) => setLan.mutate(e.target.checked)} />
              Allow LAN access
            </label>
          </div>

          <div className="toolbar">
            <input
              className="search"
              placeholder="Filter by country, city or server…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              disabled={mv.nodes.length === 0}
            />
            <span className="meta muted">
              {mv.nodes.length} server{mv.nodes.length === 1 ? '' : 's'} in {grouped.length} countr
              {grouped.length === 1 ? 'y' : 'ies'}
            </span>
          </div>

          <div className="list">
            {mv.nodes.length === 0 && (
              <p className="muted">
                No Mullvad exit nodes available. Mullvad servers appear here once the Mullvad add-on is enabled for
                your tailnet in the Tailscale admin console.
              </p>
            )}
            {grouped.map(({ country, code, cities }) => (
              <div key={country} className="mullvad-country">
                <div className="sub2 muted mullvad-country-name">
                  {country} <span className="meta">({code})</span>
                </div>
                {cities.map(([city, servers]) => (
                  <div key={city} className="row mullvad-city">
                    <span className="mullvad-city-name">{city}</span>
                    <div className="mullvad-servers">
                      {servers.map((n) => (
                        <button
                          key={n.id}
                          className={'chip-btn' + (n.current ? ' active' : '')}
                          disabled={n.current || connect.isPending || (!n.online && !n.current)}
                          title={
                            (n.current ? 'Currently connected — ' : 'Connect via ') +
                            n.host +
                            (n.ip ? ` (${n.ip})` : '')
                          }
                          onClick={() => connect.mutate(n.id)}
                        >
                          {serverChipLabel(n, servers)}
                          {!n.online && !n.current ? ' (offline)' : ''}
                        </button>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}
