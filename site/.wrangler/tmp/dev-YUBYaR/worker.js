var __defProp = Object.defineProperty;
var __name = (target, value) => __defProp(target, "name", { value, configurable: true });

// src/worker.ts
var EVENT_NAMES = /* @__PURE__ */ new Set([
  "heartbeat",
  "file_received",
  "file_saved",
  "file_deleted",
  "file_sent",
  "send_failed",
  "drop_link_created"
]);
var DOWNLOADS = {
  windows: "https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-windows-amd64.exe",
  mac: "https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-darwin-amd64.zip",
  linux_deb: "https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-linux-amd64.deb",
  linux_rpm: "https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/owldrop-linux-x86_64.rpm",
  nix: "https://github.com/Rastavich/owldrop-install"
};
var ID_RE = /^[0-9a-f]{32}$/;
var worker_default = {
  async fetch(request, env) {
    const url = new URL(request.url);
    try {
      if (url.pathname === "/api/t" && request.method === "POST") {
        return ingest(request, env);
      }
      if (url.pathname === "/dl" && request.method === "GET") {
        return download(url, env);
      }
      if (url.pathname === "/stats" || url.pathname === "/stats/") {
        return stats(url, env);
      }
    } catch (e) {
      console.error("owldrop worker exception:", e instanceof Error ? e.message + " @ " + e.stack : String(e));
      return json({ error: "internal" }, 500);
    }
    return env.ASSETS.fetch(request);
  }
};
function esc(s) {
  return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}
__name(esc, "esc");
function dayStart() {
  return Math.floor(Date.now() / 864e5) * 86400;
}
__name(dayStart, "dayStart");
function json(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" }
  });
}
__name(json, "json");
async function ingest(request, env) {
  const len = Number(request.headers.get("content-length") ?? 0);
  if (len > 64 * 1024) return json({ error: "body too large" }, 413);
  let body;
  try {
    body = await request.json();
  } catch {
    return json({ error: "bad json" }, 400);
  }
  const installID = body.install_id ?? "";
  if (!ID_RE.test(installID)) return json({ error: "bad install_id" }, 400);
  const today = dayStart();
  const seen = await env.DB.prepare("SELECT COUNT(*) AS n FROM events WHERE install_id = ? AND ts >= ?").bind(installID, today).first();
  if ((seen?.n ?? 0) > 1e3) return json({ error: "rate limited" }, 429);
  const version = String(body.version ?? "").slice(0, 32);
  const platform = String(body.platform ?? "").slice(0, 16);
  const events = (body.events ?? []).slice(0, 100);
  if (events.length === 0) return json({ error: "no events" }, 400);
  const now = Math.floor(Date.now() / 1e3);
  const rows = events.filter((e) => EVENT_NAMES.has(e.name ?? "")).map((e) => {
    const ts = Number(e.ts);
    return [Number.isFinite(ts) && ts > 0 ? Math.min(ts, now) : now, installID, e.name, platform, version];
  });
  if (rows.length === 0) return json({ error: "no valid events" }, 400);
  const stmt = env.DB.prepare("INSERT INTO events (ts, install_id, name, platform, version) VALUES (?, ?, ?, ?, ?)");
  await env.DB.batch(rows.map((r) => stmt.bind(...r)));
  return new Response(null, { status: 204 });
}
__name(ingest, "ingest");
async function download(url, env) {
  const platform = (url.searchParams.get("platform") ?? "").toLowerCase();
  if (!Object.hasOwn(DOWNLOADS, platform)) return new Response("unknown platform", { status: 404 });
  const target = DOWNLOADS[platform];
  const today = dayStart();
  const seen = await env.DB.prepare("SELECT COUNT(*) AS n FROM events WHERE install_id = 'site' AND ts >= ?").bind(today).first();
  if ((seen?.n ?? 0) <= 5e3) {
    const now = Math.floor(Date.now() / 1e3);
    await env.DB.prepare("INSERT INTO events (ts, install_id, name, platform, version) VALUES (?, ?, ?, ?, ?)").bind(now, "site", "download", platform, "").run();
  }
  return Response.redirect(target, 302);
}
__name(download, "download");
async function stats(url, env) {
  if (url.searchParams.get("token") !== env.STATS_TOKEN) {
    return json({ error: "forbidden" }, 401);
  }
  const [dau, downloads, transfers, installs, versions] = await Promise.all([
    env.DB.prepare(
      `SELECT date(ts, 'unixepoch') AS d, COUNT(DISTINCT install_id) AS n
       FROM events WHERE name = 'heartbeat' AND ts >= unixepoch('now', '-13 days')
       GROUP BY d ORDER BY d`
    ).all(),
    env.DB.prepare(
      `SELECT platform, COUNT(*) AS n FROM events WHERE name = 'download'
       AND ts >= unixepoch('now', '-30 days') GROUP BY platform ORDER BY n DESC`
    ).all(),
    env.DB.prepare(
      `SELECT date(ts, 'unixepoch') AS d,
              SUM(CASE WHEN name IN ('file_received','file_sent') THEN 1 ELSE 0 END) AS n
       FROM events WHERE ts >= unixepoch('now', '-13 days')
       GROUP BY d ORDER BY d`
    ).all(),
    env.DB.prepare(
      `SELECT COUNT(DISTINCT install_id) AS n FROM events WHERE install_id != 'site'`
    ).first(),
    env.DB.prepare(
      `SELECT version, COUNT(DISTINCT install_id) AS n FROM events
       WHERE install_id != 'site' AND version != '' GROUP BY version ORDER BY n DESC LIMIT 8`
    ).all()
  ]);
  return html(statsPage({ dau: dau.results ?? [], downloads: downloads.results ?? [], transfers: transfers.results ?? [], installs: installs?.n ?? 0, versions: versions.results ?? [] }));
}
__name(stats, "stats");
function table(title, headers, rows) {
  const head = headers.map((h) => `<th>${esc(h)}</th>`).join("");
  const body = rows.map((r) => `<tr>${r.map((c) => `<td>${esc(c)}</td>`).join("")}</tr>`).join("");
  return `<h2>${esc(title)}</h2><table><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table>`;
}
__name(table, "table");
function statsPage(d) {
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
<h1>Owldrop \u2014 usage stats</h1>
<p><span class="big">${esc(d.installs)}</span> installs seen (distinct anonymous install ids)</p>
${table("Daily active users (14 days)", ["date", "users"], dauRows)}
${table("Downloads (30 days)", ["platform", "count"], dlRows)}
${table("Files transferred (14 days)", ["date", "files"], txRows)}
${table("Versions in the wild", ["version", "installs"], verRows)}
</body></html>`;
}
__name(statsPage, "statsPage");
function html(body) {
  return new Response(body, { headers: { "content-type": "text/html; charset=utf-8" } });
}
__name(html, "html");

// ../../../../../home/rasta/.npm/_npx/32026684e21afda6/node_modules/wrangler/templates/middleware/middleware-ensure-req-body-drained.ts
var drainBody = /* @__PURE__ */ __name(async (request, env, _ctx, middlewareCtx) => {
  try {
    return await middlewareCtx.next(request, env);
  } finally {
    try {
      if (request.body !== null && !request.bodyUsed) {
        const reader = request.body.getReader();
        while (!(await reader.read()).done) {
        }
      }
    } catch (e) {
      console.error("Failed to drain the unused request body.", e);
    }
  }
}, "drainBody");
var middleware_ensure_req_body_drained_default = drainBody;

// ../../../../../home/rasta/.npm/_npx/32026684e21afda6/node_modules/wrangler/templates/middleware/middleware-miniflare3-json-error.ts
function reduceError(e) {
  return {
    name: e?.name,
    message: e?.message ?? String(e),
    stack: e?.stack,
    cause: e?.cause === void 0 ? void 0 : reduceError(e.cause)
  };
}
__name(reduceError, "reduceError");
var jsonError = /* @__PURE__ */ __name(async (request, env, _ctx, middlewareCtx) => {
  try {
    return await middlewareCtx.next(request, env);
  } catch (e) {
    const error = reduceError(e);
    const body = JSON.stringify(error);
    const headers = {
      "Content-Type": "application/json",
      "MF-Experimental-Error-Stack": "true"
    };
    const encoded = encodeURIComponent(body);
    if (encoded.length <= 8192) {
      headers["MF-Experimental-Error-Stack-Payload"] = encoded;
    }
    return new Response(body, { status: 500, headers });
  }
}, "jsonError");
var middleware_miniflare3_json_error_default = jsonError;

// .wrangler/tmp/bundle-Sh8zRS/middleware-insertion-facade.js
var __INTERNAL_WRANGLER_MIDDLEWARE__ = [
  middleware_ensure_req_body_drained_default,
  middleware_miniflare3_json_error_default
];
var middleware_insertion_facade_default = worker_default;

// ../../../../../home/rasta/.npm/_npx/32026684e21afda6/node_modules/wrangler/templates/middleware/common.ts
var __facade_middleware__ = [];
function __facade_register__(...args) {
  __facade_middleware__.push(...args.flat());
}
__name(__facade_register__, "__facade_register__");
function __facade_invokeChain__(request, env, ctx, dispatch, middlewareChain) {
  const [head, ...tail] = middlewareChain;
  const middlewareCtx = {
    dispatch,
    next(newRequest, newEnv) {
      return __facade_invokeChain__(newRequest, newEnv, ctx, dispatch, tail);
    }
  };
  return head(request, env, ctx, middlewareCtx);
}
__name(__facade_invokeChain__, "__facade_invokeChain__");
function __facade_invoke__(request, env, ctx, dispatch, finalMiddleware) {
  return __facade_invokeChain__(request, env, ctx, dispatch, [
    ...__facade_middleware__,
    finalMiddleware
  ]);
}
__name(__facade_invoke__, "__facade_invoke__");

// .wrangler/tmp/bundle-Sh8zRS/middleware-loader.entry.ts
var __Facade_ScheduledController__ = class ___Facade_ScheduledController__ {
  constructor(scheduledTime, cron, noRetry) {
    this.scheduledTime = scheduledTime;
    this.cron = cron;
    this.#noRetry = noRetry;
  }
  scheduledTime;
  cron;
  static {
    __name(this, "__Facade_ScheduledController__");
  }
  #noRetry;
  noRetry() {
    if (!(this instanceof ___Facade_ScheduledController__)) {
      throw new TypeError("Illegal invocation");
    }
    this.#noRetry();
  }
};
function wrapExportedHandler(worker) {
  if (__INTERNAL_WRANGLER_MIDDLEWARE__ === void 0 || __INTERNAL_WRANGLER_MIDDLEWARE__.length === 0) {
    return worker;
  }
  for (const middleware of __INTERNAL_WRANGLER_MIDDLEWARE__) {
    __facade_register__(middleware);
  }
  const fetchDispatcher = /* @__PURE__ */ __name(function(request, env, ctx) {
    if (worker.fetch === void 0) {
      throw new Error("Handler does not export a fetch() function.");
    }
    return worker.fetch(request, env, ctx);
  }, "fetchDispatcher");
  return {
    ...worker,
    fetch(request, env, ctx) {
      const dispatcher = /* @__PURE__ */ __name(function(type, init) {
        if (type === "scheduled" && worker.scheduled !== void 0) {
          const controller = new __Facade_ScheduledController__(
            Date.now(),
            init.cron ?? "",
            () => {
            }
          );
          return worker.scheduled(controller, env, ctx);
        }
      }, "dispatcher");
      return __facade_invoke__(request, env, ctx, dispatcher, fetchDispatcher);
    }
  };
}
__name(wrapExportedHandler, "wrapExportedHandler");
function wrapWorkerEntrypoint(klass) {
  if (__INTERNAL_WRANGLER_MIDDLEWARE__ === void 0 || __INTERNAL_WRANGLER_MIDDLEWARE__.length === 0) {
    return klass;
  }
  for (const middleware of __INTERNAL_WRANGLER_MIDDLEWARE__) {
    __facade_register__(middleware);
  }
  return class extends klass {
    #fetchDispatcher = /* @__PURE__ */ __name((request, env, ctx) => {
      this.env = env;
      this.ctx = ctx;
      if (super.fetch === void 0) {
        throw new Error("Entrypoint class does not define a fetch() function.");
      }
      return super.fetch(request);
    }, "#fetchDispatcher");
    #dispatcher = /* @__PURE__ */ __name((type, init) => {
      if (type === "scheduled" && super.scheduled !== void 0) {
        const controller = new __Facade_ScheduledController__(
          Date.now(),
          init.cron ?? "",
          () => {
          }
        );
        return super.scheduled(controller);
      }
    }, "#dispatcher");
    fetch(request) {
      return __facade_invoke__(
        request,
        this.env,
        this.ctx,
        this.#dispatcher,
        this.#fetchDispatcher
      );
    }
  };
}
__name(wrapWorkerEntrypoint, "wrapWorkerEntrypoint");
var WRAPPED_ENTRY;
if (typeof middleware_insertion_facade_default === "object") {
  WRAPPED_ENTRY = wrapExportedHandler(middleware_insertion_facade_default);
} else if (typeof middleware_insertion_facade_default === "function") {
  WRAPPED_ENTRY = wrapWorkerEntrypoint(middleware_insertion_facade_default);
}
var middleware_loader_entry_default = WRAPPED_ENTRY;
export {
  __INTERNAL_WRANGLER_MIDDLEWARE__,
  middleware_loader_entry_default as default
};
//# sourceMappingURL=worker.js.map
