import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { createDropLink, getDropLinks, getFunnel, revokeDropLink, setFunnel } from '../api';
import { toast } from '../store';
import { copyText, fmtAge } from '../utils';
import type { DropLink } from '../types';

type LinkState = 'active' | 'revoked' | 'expired' | 'used';

function linkState(l: DropLink): LinkState {
  if (l.revoked) return 'revoked';
  if (l.expired) return 'expired';
  if (l.maxUses > 0 && l.uses >= l.maxUses) return 'used';
  return 'active';
}

export default function Drops() {
  const qc = useQueryClient();
  const { data: links = [] } = useQuery({ queryKey: ['droplinks'], queryFn: getDropLinks });
  const { data: funnel } = useQuery({ queryKey: ['funnel'], queryFn: getFunnel });
  const [name, setName] = useState('');
  const [ttl, setTtl] = useState(60);
  const [single, setSingle] = useState(true);
  const [funnelBusy, setFunnelBusy] = useState(false);
  const [showArchived, setShowArchived] = useState(false);
  const activeLinks = links.filter((l) => linkState(l) === 'active');
  const archivedCount = links.length - activeLinks.length;

  const toggleFunnel = async (want: boolean) => {
    setFunnelBusy(true);
    try {
      const res = await setFunnel(want);
      qc.setQueryData(['funnel'], res);
      qc.invalidateQueries({ queryKey: ['droplinks'] });
      toast(want ? 'Public access enabled — drop links are now reachable over the internet' : 'Public access disabled');
    } catch (e) {
      qc.invalidateQueries({ queryKey: ['funnel'] });
      toast('Funnel: ' + (e instanceof Error ? e.message : e), undefined, 'err');
    }
    setFunnelBusy(false);
  };

  const create = async () => {
    try {
      const res = await createDropLink(name.trim(), ttl, single ? 1 : 0);
      setName('');
      await copyText(res.url, true);
      toast('Link created');
      qc.invalidateQueries({ queryKey: ['droplinks'] });
    } catch (e) {
      toast("Couldn't create link: " + (e instanceof Error ? e.message : e), undefined, 'err');
    }
  };

  const revoke = async (token: string) => {
    try {
      await revokeDropLink(token);
      toast('Link revoked');
      qc.invalidateQueries({ queryKey: ['droplinks'] });
    } catch (e) {
      toast("Couldn't revoke link: " + (e instanceof Error ? e.message : e), undefined, 'err');
    }
  };

  return (
    <section className="pane">
      <div className="settings-hrow">
        <h3>Drop links</h3>
        {archivedCount > 0 && (
          <button className="btn ghost mini" onClick={() => setShowArchived(!showArchived)}>
            {showArchived ? 'Hide archived' : `Show archived (${archivedCount})`}
          </button>
        )}
      </div>
      <p className="sub2">
        Create a short-lived link — anyone who opens it can drop a file into your inbox from a browser, no Tailscale
        needed. It expires or dies after its first use, and you can revoke it anytime.
      </p>
      <div className="funnelrow">
        <div className="funnel-info">
          <label className="check">
            <input
              type="checkbox"
              checked={!!funnel?.enabled}
              disabled={funnelBusy}
              onChange={(e) => toggleFunnel(e.target.checked)}
            />
            Public access (Funnel)
          </label>
          {funnel?.url && (
            <p className="sub2">
              {funnel.url}
              {funnel.enabled ? ' — public drop links live here' : ''}
            </p>
          )}
          <p className="sub2">
            Makes drop links reachable at your public <code>*.ts.net</code> URL. Only the drop pages are ever exposed.
            Free — runs on your own machine.
          </p>
        </div>
        {funnel?.url && (
          <button className="btn ghost" onClick={() => copyText(funnel.url ?? '')}>
            Copy public URL
          </button>
        )}
      </div>
      <div className="dropform">
        <input
          className="search"
          type="text"
          placeholder="Their name (optional)"
          value={name}
          onChange={(e) => setName(e.target.value)}
          spellCheck={false}
          autoComplete="off"
        />
        <select className="search" style={{ flex: '0 0 auto', width: 'auto' }} value={ttl} onChange={(e) => setTtl(Number(e.target.value))}>
          <option value="5">5 minutes</option>
          <option value="60">1 hour</option>
          <option value="1440">1 day</option>
        </select>
        <label className="check">
          <input type="checkbox" checked={single} onChange={(e) => setSingle(e.target.checked)} /> single file
        </label>
        <button className="btn" onClick={create}>
          Create link
        </button>
      </div>
      {(showArchived ? links : activeLinks).length > 0 && (
        <div className="drop-links">
          {(showArchived ? links : activeLinks).map((l) => {
            const state = linkState(l);
            const publicUrl = l.publicUrl;
            const meta: string[] = [state];
            if (state === 'active') {
              meta.push(
                'expires ' + fmtAge(l.expires) + ' · ' + (l.maxUses > 0 ? l.uses + '/' + l.maxUses + ' used' : 'unlimited'),
              );
            }
            return (
              <div key={l.token} className={'droplink' + (state === 'active' ? '' : ' dead')}>
                <div className="dl-head">
                  <span className="dl-name">{l.name || 'unnamed link'}</span>
                  <span className="dl-meta">{meta.join(' · ')}</span>
                  {state === 'active' && (
                    <button className="btn mini danger" onClick={() => revoke(l.token)}>
                      Revoke
                    </button>
                  )}
                </div>
                {state === 'active' && (
                  <div className="dl-urls">
                    <div className="dl-url-row">
                      <span className="dl-url-label">Local</span>
                      <span className="dl-url">{l.url}</span>
                      <button className="dl-copy" onClick={() => copyText(l.url)}>
                        Copy
                      </button>
                    </div>
                    {funnel?.enabled && publicUrl && (
                      <div className="dl-url-row">
                        <span className="dl-url-label">Public</span>
                        <span className="dl-url">{publicUrl}</span>
                        <button className="dl-copy" onClick={() => copyText(publicUrl)}>
                          Copy
                        </button>
                      </div>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}
