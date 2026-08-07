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
]);

// /dl?platform=... → real artifact URL (raw.githubusercontent). Logged as a
// download event; 'nix' redirects to the install repo (the nix command on
// the site can't be counted any other way).
const DOWNLOADS: Record<string, string> = {
  windows:
    'https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-windows-amd64.exe',
  mac: 'https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-darwin-amd64.zip',
  linux_deb:
    'https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-linux-amd64.deb',
  linux_rpm:
    'https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-linux-x86_64.rpm',
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

async function stats(url: URL, env: Env): Promise<Response> {
  if (url.searchParams.get('token') !== env.STATS_TOKEN) {
    return json({ error: 'forbidden' }, 401);
  }
  const [dau, downloads, transfers, installs, versions] = await Promise.all([
    env.DB.prepare(
      `SELECT date(ts, 'unixepoch') AS d, COUNT(DISTINCT install_id) AS n
       FROM events WHERE name = 'heartbeat' AND ts >= unixepoch('now', '-13 days')
       GROUP BY d ORDER BY d`,
    ).all<{ d: string; n: number }>(),
    env.DB.prepare(
      `SELECT platform, COUNT(*) AS n FROM events WHERE name = 'download'
       AND ts >= unixepoch('now', '-30 days') GROUP BY platform ORDER BY n DESC`,
    ).all<Count & { platform: string }>(),
    env.DB.prepare(
      `SELECT date(ts, 'unixepoch') AS d,
              SUM(CASE WHEN name IN ('file_received','file_sent') THEN 1 ELSE 0 END) AS n
       FROM events WHERE ts >= unixepoch('now', '-13 days')
       GROUP BY d ORDER BY d`,
    ).all<{ d: string; n: number }>(),
    env.DB.prepare(
      `SELECT COUNT(DISTINCT install_id) AS n FROM events WHERE install_id != 'site'`,
    ).first<Count>(),
    env.DB.prepare(
      `SELECT version, COUNT(DISTINCT install_id) AS n FROM events
       WHERE install_id != 'site' AND version != '' GROUP BY version ORDER BY n DESC LIMIT 8`,
    ).all<Count & { version: string }>(),
  ]);

  return html(statsPage({ dau: dau.results ?? [], downloads: downloads.results ?? [], transfers: transfers.results ?? [], installs: installs?.n ?? 0, versions: versions.results ?? [] }));
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
  dau: { d: string; n: number }[];
  downloads: { platform: string; n: number }[];
  transfers: { d: string; n: number }[];
  installs: number;
  versions: { version: string; n: number }[];
}): string {
  const dauRows = d.dau.map((r) => [r.d, r.n]);
  const dlRows = d.downloads.map((r) => [r.platform, r.n]);
  const txRows = d.transfers.map((r) => [r.d, r.n]);
  const verRows = d.versions.map((r) => [r.version, r.n]);
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
</style></head><body>
<h1>Owldrop — usage stats</h1>
<p><span class="big">${esc(d.installs)}</span> installs seen (distinct anonymous install ids)</p>
${table('Daily active users (14 days)', ['date', 'users'], dauRows)}
${table('Downloads (30 days)', ['platform', 'count'], dlRows)}
${table('Files transferred (14 days)', ['date', 'files'], txRows)}
${table('Versions in the wild', ['version', 'installs'], verRows)}
</body></html>`;
}

function html(body: string): Response {
  return new Response(body, { headers: { 'content-type': 'text/html; charset=utf-8' } });
}
