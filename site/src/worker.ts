// Owldrop site worker: serves the static marketing site, ingests anonymous
// app telemetry into D1 (/api/t), counts downloads via redirects (/dl), and
// renders a token-protected stats dashboard (/stats).

export interface Env {
  DB: D1Database;
  STATS_TOKEN: string; // secret: ?token= for /stats
  ASSETS: Fetcher;
}

// The only telemetry event names the app may report.
const EVENT_NAMES = new Set([
  'heartbeat',
  'file_received',
  'file_saved',
  'file_deleted',
  'file_sent',
  'send_failed',
  'drop_link_created',
  'drop_link_used',
  'drop_link_failed',
  'sync_item_added',
]);

const SUCCESS_EVENTS = "('file_received','file_sent','sync_item_added','drop_link_used')";

// /dl?platform=... → real artifact URL (raw.githubusercontent). Logged as a
// download event; 'nix' redirects to the install repo (the nix command on
// the site can't be counted any other way).
const DOWNLOADS: Record<string, string> = {
  windows:
    'https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-windows-amd64.exe',
  mac: 'https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-darwin-universal.zip',
  linux_deb:
    'https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-linux-amd64.deb',
  linux_rpm:
    'https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-linux-x86_64.rpm',
  linux_deb_2404:
    'https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-linux-amd64-webkit41.deb',
  linux_rpm_2404:
    'https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-linux-x86_64-webkit41.rpm',
  nix: 'https://github.com/Rastavich/owldrop-install',
};

const ID_RE = /^[0-9a-f]{32}$/;

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    try {
      if (url.pathname === '/api/t' && request.method === 'POST') {
        return ingest(request, env);
      }
      if (url.pathname === '/dl' && request.method === 'GET') {
        return download(url, env);
      }
      if (url.pathname === '/stats' || url.pathname === '/stats/') {
        return stats(url, env);
      }
    } catch (e) {
      // Surface the real exception in Workers Logs (wrangler tail) instead
      // of swallowing it — 1101s here are invisible otherwise.
      console.error('owldrop worker exception:', e instanceof Error ? e.message + ' @ ' + e.stack : String(e));
      return json({ error: 'internal' }, 500);
    }
    return env.ASSETS.fetch(request);
  },
} satisfies ExportedHandler<Env>;

// esc HTML-escapes a value interpolated into the stats page. Telemetry
// fields (version, platform) are attacker-controlled, so every string cell
// must be escaped (stored-XSS hardening).
function esc(s: unknown): string {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

// dayStart returns the start of the current UTC day in unix seconds, for
// per-day rate caps.
function dayStart(): number {
  return Math.floor(Date.now() / 86400000) * 86400;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

// --- ingest ---------------------------------------------------------------

interface IngestBody {
  install_id?: string;
  version?: string;
  platform?: string;
  events?: { name?: string; ts?: number }[];
}

async function ingest(request: Request, env: Env): Promise<Response> {
  // Cap the body before parsing so a huge JSON can't burn memory/CPU.
  const len = Number(request.headers.get('content-length') ?? 0);
  if (len > 64 * 1024) return json({ error: 'body too large' }, 413);
  let body: IngestBody;
  try {
    body = await request.json();
  } catch {
    return json({ error: 'bad json' }, 400);
  }
  const installID = body.install_id ?? '';
  if (!ID_RE.test(installID)) return json({ error: 'bad install_id' }, 400);
  // Per-install daily write cap: one spamming client can't exhaust the D1
  // free-tier quota or inflate the stats.
  const today = dayStart();
  const seen = await env.DB
    .prepare('SELECT COUNT(*) AS n FROM events WHERE install_id = ? AND ts >= ?')
    .bind(installID, today)
    .first<Count>();
  if ((seen?.n ?? 0) > 1000) return json({ error: 'rate limited' }, 429);
  const version = String(body.version ?? '').slice(0, 32);
  const platform = String(body.platform ?? '').slice(0, 16);
  const events = (body.events ?? []).slice(0, 100);
  if (events.length === 0) return json({ error: 'no events' }, 400);

  const now = Math.floor(Date.now() / 1000);
  const rows = events
    .filter((e) => EVENT_NAMES.has(e.name ?? ''))
    .map((e) => {
      const ts = Number(e.ts);
      return [Number.isFinite(ts) && ts > 0 ? Math.min(ts, now) : now, installID, e.name, platform, version] as const;
    });
  if (rows.length === 0) return json({ error: 'no valid events' }, 400);

  const stmt = env.DB.prepare('INSERT INTO events (ts, install_id, name, platform, version) VALUES (?, ?, ?, ?, ?)');
  await env.DB.batch(rows.map((r) => stmt.bind(...r)));
  return new Response(null, { status: 204 });
}

// --- download counting ----------------------------------------------------

async function download(url: URL, env: Env): Promise<Response> {
  const platform = (url.searchParams.get('platform') ?? '').toLowerCase();
  // Object.hasOwn: DOWNLOADS[platform] would otherwise walk the prototype
  // chain (platform=constructor → truthy → 500).
  if (!Object.hasOwn(DOWNLOADS, platform)) return new Response('unknown platform', { status: 404 });
  const target = DOWNLOADS[platform];
  // Count downloads only while under a generous daily cap so a bot can't
  // burn the D1 write quota; the redirect still works either way.
  const today = dayStart();
  const seen = await env.DB
    .prepare("SELECT COUNT(*) AS n FROM events WHERE install_id = 'site' AND ts >= ?")
    .bind(today)
    .first<Count>();
  if ((seen?.n ?? 0) <= 5000) {
    const now = Math.floor(Date.now() / 1000);
    await env.DB
      .prepare('INSERT INTO events (ts, install_id, name, platform, version) VALUES (?, ?, ?, ?, ?)')
      .bind(now, 'site', 'download', platform, '')
      .run();
  }
  return Response.redirect(target, 302);
}

// --- stats dashboard ------------------------------------------------------

interface Count {
  n: number;
}

// One merged day-by-day row: all views keyed by date are folded into this.
interface DayRow {
  d: string;
  dau: number; // distinct installs that sent a heartbeat
  active: number; // distinct installs with any event
  received: number;
  sent: number;
  sync: number; // sync items added
  drop_used: number; // drop-link uploads used
  drop_created: number;
  downloads: number;
  new_installs: number; // first-ever event seen that day
}

async function stats(url: URL, env: Env): Promise<Response> {
  if (url.searchParams.get('token') !== env.STATS_TOKEN) {
    return json({ error: 'forbidden' }, 401);
  }
  // ?days= controls the day-by-day window (clamped to 7–365, default 30).
  const raw = parseInt(url.searchParams.get('days') ?? '', 10);
  const days = Number.isFinite(raw) ? Math.min(365, Math.max(7, raw)) : 30;

  const [dau, daily, downloads, newInstalls, installs, versions, funnel, jobs, downloadDaily] = await Promise.all([
    env.DB.prepare(
      `SELECT date(ts, 'unixepoch') AS d, COUNT(DISTINCT install_id) AS n
       FROM events WHERE name = 'heartbeat' AND ts >= unixepoch('now', '-${days} days')
       GROUP BY d ORDER BY d`,
    ).all<{ d: string; n: number }>(),
    env.DB.prepare(
      `SELECT date(ts, 'unixepoch') AS d,
              COUNT(DISTINCT install_id) AS active,
              SUM(CASE WHEN name = 'file_received' THEN 1 ELSE 0 END) AS received,
              SUM(CASE WHEN name = 'file_sent' THEN 1 ELSE 0 END) AS sent,
              SUM(CASE WHEN name = 'sync_item_added' THEN 1 ELSE 0 END) AS sync,
              SUM(CASE WHEN name = 'drop_link_used' THEN 1 ELSE 0 END) AS drop_used,
              SUM(CASE WHEN name = 'drop_link_created' THEN 1 ELSE 0 END) AS drop_created
       FROM events WHERE install_id != 'site' AND ts >= unixepoch('now', '-${days} days')
       GROUP BY d ORDER BY d`,
    ).all<{ d: string; active: number; received: number; sent: number; sync: number; drop_used: number; drop_created: number }>(),
    env.DB.prepare(
      `SELECT platform, COUNT(*) AS n FROM events WHERE name = 'download'
       AND ts >= unixepoch('now', '-${days} days') GROUP BY platform ORDER BY n DESC`,
    ).all<Count & { platform: string }>(),
    // True "new installs per day": each install's first-ever event, bucketed
    // by day and restricted to the window.
    env.DB.prepare(
      `SELECT date(m, 'unixepoch') AS d, COUNT(*) AS n FROM (
         SELECT install_id, MIN(ts) AS m FROM events
         WHERE install_id != 'site' GROUP BY install_id
       ) t WHERE m >= unixepoch('now', '-${days} days') GROUP BY d ORDER BY d`,
    ).all<{ d: string; n: number }>(),
    env.DB.prepare(
      `SELECT COUNT(DISTINCT install_id) AS n FROM events WHERE install_id != 'site'`,
    ).first<Count>(),
    env.DB.prepare(
      `SELECT version, COUNT(DISTINCT install_id) AS n FROM events
       WHERE install_id != 'site' AND version != '' GROUP BY version ORDER BY n DESC LIMIT 8`,
    ).all<Count & { version: string }>(),
    env.DB.prepare(
      `SELECT
         (SELECT COUNT(*) FROM events WHERE name = 'download' AND ts >= unixepoch('now', '-30 days')) AS downloads_30d,
         (SELECT COUNT(DISTINCT install_id) FROM events WHERE name = 'heartbeat' AND install_id != 'site') AS heartbeat_installs,
         (SELECT COUNT(DISTINCT install_id) FROM events WHERE name IN ${SUCCESS_EVENTS} AND install_id != 'site') AS activated,
         (SELECT COUNT(*) FROM (
            SELECT install_id FROM events
            WHERE name = 'heartbeat' AND install_id != 'site' AND ts >= unixepoch('now', '-13 days')
            GROUP BY install_id HAVING COUNT(DISTINCT date(ts, 'unixepoch')) >= 2
         )) AS repeat_14d`,
    ).first<{ downloads_30d: number; heartbeat_installs: number; activated: number; repeat_14d: number }>(),
    env.DB.prepare(
      `SELECT
         SUM(CASE WHEN name = 'sync_item_added' THEN 1 ELSE 0 END) AS sync_n,
         SUM(CASE WHEN name = 'drop_link_used' THEN 1 ELSE 0 END) AS drop_used_n,
         SUM(CASE WHEN name IN ('file_received','file_sent') THEN 1 ELSE 0 END) AS files_n
       FROM events WHERE ts >= unixepoch('now', '-${days} days') AND install_id != 'site'`,
    ).first<{ sync_n: number; drop_used_n: number; files_n: number }>(),
    env.DB.prepare(
      `SELECT date(ts, 'unixepoch') AS d, COUNT(*) AS n FROM events
       WHERE name = 'download' AND ts >= unixepoch('now', '-${days} days')
       GROUP BY d ORDER BY d`,
    ).all<{ d: string; n: number }>(),
  ]);

  // Merge the date-keyed views into one row per day.
  const empty = (): DayRow => ({ d: '', dau: 0, active: 0, received: 0, sent: 0, sync: 0, drop_used: 0, drop_created: 0, downloads: 0, new_installs: 0 });
  const byDay = new Map<string, DayRow>();
  const day = (d: string): DayRow => {
    let r = byDay.get(d);
    if (!r) byDay.set(d, (r = empty()));
    return r;
  };
  for (const x of dau.results ?? []) day(x.d).dau = x.n;
  for (const x of daily.results ?? []) {
    const r = day(x.d);
    r.active = x.active;
    r.received = x.received;
    r.sent = x.sent;
    r.sync = x.sync;
    r.drop_used = x.drop_used;
    r.drop_created = x.drop_created;
  }
  for (const x of downloadDaily.results ?? []) day(x.d).downloads = x.n;
  for (const x of newInstalls.results ?? []) day(x.d).new_installs = x.n;
  const daySeries: DayRow[] = [...byDay.entries()]
    .sort((a, b) => (a[0] < b[0] ? -1 : 1))
    .map(([d, r]) => ({ ...r, d }));

  return html(
    statsPage({
      daySeries,
      downloads: downloads.results ?? [],
      installs: installs?.n ?? 0,
      versions: versions.results ?? [],
      funnel: {
        downloads_30d: funnel?.downloads_30d ?? 0,
        heartbeat_installs: funnel?.heartbeat_installs ?? 0,
        activated: funnel?.activated ?? 0,
        repeat_14d: funnel?.repeat_14d ?? 0,
      },
      jobs: {
        sync_n: jobs?.sync_n ?? 0,
        drop_used_n: jobs?.drop_used_n ?? 0,
        files_n: jobs?.files_n ?? 0,
      },
      days,
      token: env.STATS_TOKEN,
    }),
  );
}

// --- rendering ------------------------------------------------------------

function table(title: string, headers: string[], rows: (string | number)[][]): string {
  const head = headers.map((h) => `<th>${esc(h)}</th>`).join('');
  const body = rows
    .map((r) => `<tr>${r.map((c) => `<td>${esc(c)}</td>`).join('')}</tr>`)
    .join('');
  return `<h2>${esc(title)}</h2><table><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table>`;
}

function statsPage(d: {
  daySeries: DayRow[];
  downloads: { platform: string; n: number }[];
  installs: number;
  versions: { version: string; n: number }[];
  funnel: { downloads_30d: number; heartbeat_installs: number; activated: number; repeat_14d: number };
  jobs: { sync_n: number; drop_used_n: number; files_n: number };
  days: number;
  token: string;
}): string {
  const dayRows = d.daySeries.map((r) => [r.d, r.dau, r.active, r.received, r.sent, r.sync, r.drop_used, r.drop_created, r.downloads, r.new_installs]);
  const dlRows = d.downloads.map((r) => [r.platform, r.n]);
  const verRows = d.versions.map((r) => [r.version, r.n]);
  const range = `<p class="muted">Range: ${[7, 14, 30, 60, 90]
    .map((n) => `<a class="${n === d.days ? 'sel' : ''}" href="/stats?days=${n}&amp;token=${esc(d.token)}">${n}d</a>`)
    .join(' · ')}</p>`;
  const f = d.funnel;
  const pct = (num: number, den: number) => (den > 0 ? Math.round((num * 100) / den) + '%' : '—');
  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Owldrop stats</title>
<style>
  body { margin: 0; background: #090c13; color: #e8ebf3; font: 14px/1.6 system-ui, sans-serif; padding: 32px; }
  h1 { font-size: 20px; }
  h2 { font-size: 15px; margin: 28px 0 8px; color: #97a0b6; text-transform: uppercase; letter-spacing: 1px; }
  table { border-collapse: collapse; min-width: 360px; }
  th, td { text-align: left; padding: 6px 14px 6px 0; border-bottom: 1px solid #232b40; }
  th { color: #97a0b6; font-weight: 600; }
  .big { font-size: 26px; font-weight: 700; color: #6d7bff; }
  .funnel { display: flex; flex-wrap: wrap; gap: 28px; margin: 16px 0 8px; }
  .funnel div { min-width: 140px; }
  .funnel .lbl { color: #97a0b6; font-size: 12px; text-transform: uppercase; letter-spacing: 1px; }
  .muted { color: #97a0b6; }
  .muted a { color: #6d7bff; text-decoration: none; margin: 0 4px; }
  .muted a.sel { color: #e8ebf3; font-weight: 700; }
</style></head><body>
<h1>Owldrop — usage stats</h1>
<p><span class="big">${esc(d.installs)}</span> installs seen (distinct anonymous install ids)</p>
<h2>Activation funnel</h2>
<p class="muted">File send is infrequent — judge activation (first successful transfer) and 14-day repeat, not daily opens.</p>
<div class="funnel">
  <div><div class="lbl">Downloads (30d)</div><div class="big">${esc(f.downloads_30d)}</div></div>
  <div><div class="lbl">Heartbeat installs</div><div class="big">${esc(f.heartbeat_installs)}</div><div class="muted">ran the app at least once</div></div>
  <div><div class="lbl">Activated (any success)</div><div class="big">${esc(f.activated)}</div><div class="muted">${esc(pct(f.activated, f.heartbeat_installs))} of installs · file / sync / drop-link</div></div>
  <div><div class="lbl">14-day repeat</div><div class="big">${esc(f.repeat_14d)}</div><div class="muted">${esc(pct(f.repeat_14d, f.heartbeat_installs))} opened on 2+ days</div></div>
</div>
<h2>Jobs (last ${d.days} days)</h2>
<div class="funnel">
  <div><div class="lbl">Files transferred</div><div class="big">${esc(d.jobs.files_n)}</div></div>
  <div><div class="lbl">Sync items</div><div class="big">${esc(d.jobs.sync_n)}</div></div>
  <div><div class="lbl">Drop-link uploads</div><div class="big">${esc(d.jobs.drop_used_n)}</div></div>
</div>
${range}
${table(`Day by day (last ${d.days} days)`, ['date', 'DAU', 'active', 'received', 'sent', 'sync', 'drop uploads', 'drop links created', 'downloads', 'new installs'], dayRows)}
${table(`Downloads by platform (last ${d.days} days)`, ['platform', 'count'], dlRows)}
${table('Versions in the wild', ['version', 'installs'], verRows)}
</body></html>`;
}

function html(body: string): Response {
  return new Response(body, { headers: { 'content-type': 'text/html; charset=utf-8' } });
}
