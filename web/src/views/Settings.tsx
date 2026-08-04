import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import {
  createDropLink,
  getConfig,
  getDropLinks,
  getFunnel,
  getPremium,
  openPortal,
  patchConfig,
  refreshPremium,
  revokeDropLink,
  setFunnel,
  startCheckout,
} from '../api';
import { toast } from '../store';
import { copyText, fmtAge } from '../utils';
import type { AppConfig, DropLink } from '../types';

type LinkState = 'active' | 'revoked' | 'expired' | 'used';

function linkState(l: DropLink): LinkState {
  if (l.revoked) return 'revoked';
  if (l.expired) return 'expired';
  if (l.maxUses > 0 && l.uses >= l.maxUses) return 'used';
  return 'active';
}

export default function Settings() {
  const qc = useQueryClient();
  const { data: config } = useQuery({ queryKey: ['config'], queryFn: getConfig });
  const { data: links = [] } = useQuery({ queryKey: ['droplinks'], queryFn: getDropLinks });
  const { data: funnel } = useQuery({ queryKey: ['funnel'], queryFn: getFunnel });
  const { data: premium } = useQuery({ queryKey: ['premium'], queryFn: getPremium, refetchInterval: 30_000 });
  const relayMode = !!config?.relayUrl;
  const activeLinks = links.filter((l) => linkState(l) === 'active');
  const archivedCount = links.length - activeLinks.length;
  const [name, setName] = useState('');
  const [ttl, setTtl] = useState(60);
  const [single, setSingle] = useState(true);
  const [funnelBusy, setFunnelBusy] = useState(false);
  const [showArchived, setShowArchived] = useState(false);

  const patch = async (body: Partial<AppConfig>, okMsg?: string) => {
    try {
      const res = await patchConfig(body);
      qc.setQueryData(['config'], res);
      if (okMsg) toast(okMsg);
    } catch (e) {
      qc.invalidateQueries({ queryKey: ['config'] });
      toast(e instanceof Error ? e.message : String(e), undefined, 'err');
    }
  };

  const toggleFunnel = async (want: boolean) => {
    if (want && !premium?.active) {
      // Public access is a paid feature: re-check Stripe before trusting a
      // stale cache, then refuse with a pointer to Premium.
      try {
        const fresh = await refreshPremium();
        qc.setQueryData(['premium'], fresh);
        if (!fresh.active) {
          toast('Public access is a Premium feature — subscribe to enable it.', undefined, 'err');
          return;
        }
      } catch (e) {
        toast('Could not verify subscription: ' + (e instanceof Error ? e.message : e), undefined, 'err');
        return;
      }
    }
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

  const subscribe = async () => {
    try {
      await startCheckout(); // the app opens the Stripe checkout page itself
      toast('Opening Stripe checkout…');
    } catch (e) {
      toast('Checkout: ' + (e instanceof Error ? e.message : e), undefined, 'err');
    }
  };

  const manage = async () => {
    try {
      await openPortal();
      toast('Opening billing portal…');
    } catch (e) {
      toast('Billing portal: ' + (e instanceof Error ? e.message : e), undefined, 'err');
    }
  };

  // Landed back from Stripe Checkout after paying (success_url carries
  // ?premium=success): confirm and re-fetch the subscription state.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    if (params.get('premium') === 'success') {
      toast('Subscription active — public access enabled');
      qc.invalidateQueries({ queryKey: ['premium'] });
      window.history.replaceState({}, '', window.location.pathname + window.location.hash);
    }
  }, [qc]);

  const periodEndLabel = premium?.periodEnd
    ? new Date(premium.periodEnd * 1000).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
    : undefined;

  return (
    <section className="pane settings">
      <h3>Notifications</h3>
      <label className="check">
        <input type="checkbox" checked={config?.notifyArrival !== false} onChange={(e) => patch({ notifyArrival: e.target.checked })} />
        New file arrival
      </label>
      <label className="check">
        <input type="checkbox" checked={config?.notifySave !== false} onChange={(e) => patch({ notifySave: e.target.checked })} />
        Save finished
      </label>
      <label className="check">
        <input type="checkbox" checked={config?.notifySend !== false} onChange={(e) => patch({ notifySend: e.target.checked })} />
        Send finished
      </label>
      <label className="check">
        <input type="checkbox" checked={config?.notifyError !== false} onChange={(e) => patch({ notifyError: e.target.checked })} />
        Errors
      </label>

      <h3>Save folder</h3>
      <p className="sub2">Incoming files are saved to <b>{config?.saveDir}</b></p>

      <h3>LAN access</h3>
      <label className="check">
        <input type="checkbox" checked={!!config?.lan} onChange={(e) => patch({ lan: e.target.checked }, 'Restarting to apply LAN mode…')} />
        Allow other devices on my tailnet to open this app
      </label>
      {config?.lan && config.lanUrl && <p className="sub2">{config.lanUrl}</p>}
      <p className="sub2">Opening this app from another device is as powerful as being at this machine — only enable it if you trust your tailnet.</p>

      <h3>Premium</h3>
      {!premium?.configured ? (
        relayMode ? (
          <p className="sub2">
            Public drop links are a Premium feature. Payments aren't set up on the relay yet — subscribe here once they
            are.
          </p>
        ) : (
          <p className="sub2">
            Public drop links are a Premium feature. Stripe isn't configured yet — set{' '}
            <code>TAILDROP_STRIPE_SECRET_KEY</code> and <code>TAILDROP_STRIPE_PRICE_ID</code> (or{' '}
            <code>stripe_secret_key</code>/<code>stripe_price_id</code> in the app config file) and restart to enable it.
          </p>
        )
      ) : premium.active ? (
        <div className="funnelrow">
          <div className="funnel-info">
            <p className="sub2">
              <b>Premium active</b>
              {premium.priceLabel ? ` — ${premium.priceLabel}` : ''}
              {periodEndLabel ? ` · renews ${periodEndLabel}` : ''}
              {premium.status === 'trialing' ? ' (trial)' : ''}
            </p>
            <p className="sub2">Public drop links are enabled.</p>
          </div>
          <button className="btn ghost" onClick={manage}>
            Manage subscription
          </button>
        </div>
      ) : (
        <div className="funnelrow">
          <div className="funnel-info">
            <p className="sub2">
              <b>Public access is a Premium feature</b> — {premium.priceLabel ?? 'a small monthly subscription'}. Enabling
              public drop links requires an active subscription.
            </p>
          </div>
          <button className="btn" onClick={subscribe}>
            Subscribe
          </button>
        </div>
      )}

      <div className="settings-hrow">
        <h3>Drop links</h3>
        {archivedCount > 0 && (
          <button className="btn ghost mini" onClick={() => setShowArchived(!showArchived)}>
            {showArchived ? 'Hide archived' : `Show archived (${archivedCount})`}
          </button>
        )}
      </div>
      <p className="sub2">
        Create a short-lived link — anyone who opens it can drop a file into your inbox from a browser, no Tailscale needed. It
        expires or dies after its first use, and you can revoke it anytime.
      </p>
      {!relayMode && (
        <div className="funnelrow">
          <div className="funnel-info">
            <label className="check">
              <input type="checkbox" checked={!!funnel?.enabled} disabled={funnelBusy} onChange={(e) => toggleFunnel(e.target.checked)} />
              Public access (Funnel)
            </label>
            {funnel?.url && (
              <p className="sub2">
                {funnel.url}
                {funnel.enabled ? ' — public drop links live here' : ''}
              </p>
            )}
            <p className="sub2">Makes drop links reachable at your public <code>*.ts.net</code> URL. Only the drop pages are ever exposed. Requires an active Premium subscription.</p>
          </div>
          {funnel?.url && (
            <button className="btn ghost" onClick={() => copyText(funnel.url ?? '')}>
              Copy public URL
            </button>
          )}
        </div>
      )}
      {relayMode && (
        <p className="sub2">
          Public drop links are hosted on the <code>{config?.relayUrl}</code> relay — they work whenever you have an active
          subscription; no funnel setup needed.
        </p>
      )}
      <div className="dropform">
        <input className="search" type="text" placeholder="Their name (optional)" value={name} onChange={(e) => setName(e.target.value)} spellCheck={false} autoComplete="off" />
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
              meta.push('expires ' + fmtAge(l.expires) + ' · ' + (l.maxUses > 0 ? l.uses + '/' + l.maxUses + ' used' : 'unlimited'));
            }
            return (
              <div key={l.token} className={'droplink' + (state === 'active' ? '' : ' dead')}>
                <span>{l.name || 'unnamed link'}</span>
                <span className="dl-meta">{meta.join(' · ')}</span>
                {state === 'active' && (
                  <>
                    <span className="dl-url">
                      {l.url}
                      {funnel?.enabled && publicUrl ? '  ·  ' + publicUrl : ''}
                    </span>
                    <button className="dl-copy" onClick={() => copyText(l.url)}>
                      Copy
                    </button>
                    {funnel?.enabled && publicUrl && (
                      <button className="dl-copy" onClick={() => copyText(publicUrl)}>
                        Copy public
                      </button>
                    )}
                    <button className="btn mini danger" onClick={() => revoke(l.token)}>
                      Revoke
                    </button>
                  </>
                )}
              </div>
            );
          })}
        </div>
      )}

      <h3>Shortcuts</h3>
      <p className="sub2">
        <code>/</code> filter · <code>j</code>/<code>k</code> select · <code>s</code> save · <code>d</code> delete ·{' '}
        <code>Ctrl+V</code> paste to send
      </p>
      <h3>About</h3>
      <p className="sub2">Talks directly to your local tailscaled daemon. Nothing leaves this machine except files you send.</p>
    </section>
  );
}
