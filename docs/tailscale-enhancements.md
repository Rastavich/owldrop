# Tailscale enhancement plan — owldrop

Scope: four opportunities identified from a sweep of tailscale.com/docs
(features/serve, features/tsnet, features/taildrop, reference/tailscale-cli).
Ordered by value/effort; each section is independently shippable.

Cross-cutting constraint for everything here: reach Tailscale only through
`tailscale.com/client/local` (LocalAPI) — the app already does this
(`localapi.go`), never shell out to the `tailscale` CLI in production code.
The desktop app talks to the host daemon; the container build (`-tags server`,
Docker image) talks to whatever daemon is available.

---

## #1 — HTTPS on the tailnet via Tailscale Serve

**User value.** The LAN URL becomes `https://desktop.tailnet.ts.net/` — a
valid Let's Encrypt cert, tailnet-only, no "not secure" warnings on phones,
no IP churn in bookmarks. Complements the MagicDNS work already shipped.

**Mechanism** (docs: `tailscale serve`): `tailscale serve --https=443 --bg
http://127.0.0.1:8976` makes tailscaled terminate TLS for the node's MagicDNS
name on 443 and reverse-proxy to our loopback listener. Only
`http://127.0.0.1` targets are supported — exactly our shape. Cert issuance
is automatic. `tailscale funnel` is the same config with `AllowFunnel` set —
so Serve and Funnel must share one config manager, not two code paths.

### Steps

1. **New `serving.go`** — one module managing the `ipn.ServeConfig` (this
   becomes the single owner of both the existing Funnel toggle and the new
   Serve toggle):
   - `serveState()` → `{enabled, httpsUrl}` from `GetServeConfig`.
   - `enableServe()/disableServe()` — build a `ServeConfig` with the HTTPS
     web handler `https://<magicdns>:443 → proxy 127.0.0.1:<port>` and
     `AllowFunnel` unset; `enableFunnel()` sets `AllowFunnel` (supersedes).
     Tests construct configs with `client.DummyServeConfig(ctx, …)`.
   - **Conflict policy (decision needed):** if `GetServeConfig` has a root
     handler that isn't ours, refuse the toggle with a clear message rather
     than clobbering another service's serve config.
2. **Guard change in `server.go`** (required, and subtle): today the guard
   404s non-`/drop/*` on the MagicDNS hostname when the peer is loopback —
    that was written for Funnel (public). With Serve-only, tailnet HTTPS
    traffic also arrives from loopback (tailscaled proxy) and must serve the
    **full app**; Funnel (public) traffic must stay restricted. New rule:
    apply the 404-restriction only when `AllowFunnel` is actually enabled in
    the current serve config. Safety argument: tailscaled only completes TLS
    for authenticated tailnet peers, so serve-mode traffic is never public.
3. **Settings UI** — "HTTPS access (tailnet-only)" toggle next to the LAN
   card; when enabled, the primary LAN URL shown is the `https://` one.
4. **Reconcile `funnel.go`** — move its Funnel toggle onto the new
   `serving.go` so the two can never disagree (today they're CLI/config
   based — audit).

### Edge cases / decisions

- Port 443 already owned by another serve/funnel config → refuse toggle.
- LAN-mode binding: serve proxies to loopback; works with LAN on or off.
  UX decision: gate the HTTPS toggle behind LAN mode (they serve the same
  audience) or keep independent? Lean: independent, HTTPS implies LAN
  exposure.
- Cert renewal is handled by tailscaled; nothing to do.

### Verification

- Unit: ServeConfig builder matches CLI output for the same input
  (`DummyServeConfig` golden tests).
- Daemon-gated E2E (tailscale_test.go pattern): enable → `curl
  https://<name>.ts.net/` returns 200 with the full app; enable Funnel →
  `/` returns 404, `/drop/<token>` 200; disable → cleared config.
- Browser E2E at phone viewport: https URL loads and the Sync tab works.

**Effort: M.** Depends on: nothing new (MagicDNS + guard already landed).

---

## #2 — Connection quality in the Send picker

**User value.** Device rows show `⚡ direct · 12ms` or `relay · 340ms
(derp5-nrt)`. Users stop opening support tickets for slow transfers.

**Mechanism**: `GET /localapi/v0/status` returns per-peer
`ipnstate.PeerStatus{Relay, CurAddr, Online}` — direct vs relayed is
derivable from those two fields (verify empirically; some daemon versions
report the derp address in `CurAddr` when relayed). Latency requires a
`/localapi/v0/ping` round-trip — do it on demand, not per render.

### Steps

1. **`localapi.go`** — enrich the existing devices call: join
   `Status().Peer` by StableNodeID; add `relay` and `curAddr` to the
   `device` struct used by `/api/devices` and the tray.
2. **Pure helper** `peerTransport(relay, curAddr) → "direct" | "relay" |
   "offline"` (+ region extraction from `Relay`), unit-tested across the
   field combinations.
3. **Send.tsx** — badge on each device row: `⚡ direct` / `⇄ relay
   (region)`, dimmed when offline. On row tap (or a small "ping" affordance),
   fire one `/localapi/v0/ping` and show `latency: Nms`.
4. **api.ts/types.ts** — extend `Device`; add optional `pingDevice(id)`.

### Edge cases / decisions

- Status polling already exists (devices refresh); enrich in place, no new
  cadence.
- Ping rate limit: max one in-flight per row; never auto-ping the whole
  list.

### Verification

- Unit: `peerTransport` table test.
- E2E with daemon: mixed direct/relay peers render correct badges
  (real tailnet needed — daemon-gated test).

**Effort: S–M.** Depends on: nothing.

---

## #3 — Embedded Tailscale (`tsnet`) for the Docker/NAS image

**User value.** One container joins the tailnet as its own node
(`owldrop-nas`) — no "install Tailscale on the NAS first", stable MagicDNS
name, and optional `ListenFunnel` so public drop links work from the box.

**Important correction found during planning — read before building:**
Taildrop's inbox/send protocol is implemented in `tailscaled` (ipnlocal+file
APIs), NOT in `tsnet`. A tsnet-only node can serve the UI, LAN/HTTPS, drop
links, and Sync — but **cannot receive or send Taildrop files** (the inbox
would be empty). The drop-link upload path is plain HTTP to the app, so it
works; the inbox does not. Decision required:

- **Option A (recommended): tsnet as fallback networking.** In the
  container, if no host tailscaled socket is reachable at startup, start
  `tsnet.Server` (hostname `owldrop-<random>` or env
  `OWLDROP_HOSTNAME`), print/accept the auth URL or an `TS_AUTHKEY` env, and
  add its listener to the HTTP server. Inbox features detect "no daemon" and
  hide (existing behavior when tailscaled is unreachable), while drop links,
  Sync, and LAN/HTTPS fully work. This is a strict win over today's
  "container must borrow a host daemon or nothing works".
- **Option B: run a full tailscaled sidecar in the container** (current
  owldrop image keeps borrowing; a new image gets `tailscaled` as PID-1
  sibling) → full inbox, at the cost of a second process and auth plumbing.
- Option A first; B later if inbox-in-container demand is high.

### Steps

1. **New file `tsnetmode.go`** behind `-tags server`:
   - `startTsnet()` — `tsnet.Server{Hostname: cfg.Hostname}`; if
     `TS_AUTHKEY` set, use it; else capture the auth URL and log it
     (+ optionally surface it on the UI's daemon banner via the status
     event).
   - Return listeners: `srv.Listen("tcp", ":80")` and (option)
     `srv.ListenFunnel` for `/drop/*` public.
2. **Multi-listener HTTP** — `main.go` (server build): serve the same
   handler on loopback **and** the tsnet listener; port discovery gets
   messy → decision: tsnet listener uses port 80 (http) + ListenFunnel → the
   UI's LAN URL logic keys off the tsnet hostname.
3. **Flags/env** — `--tsnet`, `OWLDROP_TSNET=1`, `OWLDROP_HOSTNAME`,
   `TS_AUTHKEY`; document device-approval interplay (pre-approved auth keys
   skip admin approval).
4. **Dockerfile / docs** — new image tag or env-gated behavior in the same
   image; README section "run without a Tailscale daemon on the host".
5. **Funnel-in-container (stretch):** `tsnet.Server.ListenFunnel` + our
   `/drop/*` host guard already restricts to drop pages — verify the guard's
   `selfDNSName()` works for the tsnet node (it reads the daemon status;
   tsnet exposes its own status — must branch).

### Edge cases / decisions

- tsnet identity ≠ host identity: devices sent to `owldrop-nas` via
  Taildrop go nowhere (Option A) — the UI must say "inbox unavailable:
  container runs its own node; enable a host daemon for Taildrop".
- Auth UX: print URL for first run; document auth keys.
- Funnel from a tsnet node needs ACL on the node — document.

### Verification

- Container smoke (docker run with no host socket): tsnet node appears in
  the tailnet (real tailnet), https URL serves UI, drop link creation works
  publicly, inbox shows unreachable banner.
- Unit: hostname resolution, env parsing.

**Effort: M–L (A) / L (B).** Depends on: none, but sequencing after #1 so
serve/funnel logic can be reused by the tsnet listener.

---

## #4 — Sender identity → BLOCKED at the API; pivot to per-drop-link rules

**Probe verdict (2026-08-08, live daemon v1.102.2 + current source): sender
attribution is NOT exposed.** Evidence:

- Live: `GET /localapi/v0/files/` (trailing slash) → `[]apitype.WaitingFile`.
- `apitype.WaitingFile` on v1.102.2 (current) = `{Name string, Size int64}`
  — identical to the pinned v1.98.9 module; no sender/ID/timestamp.
- `feature/taildrop/localapi.go` marshals that struct directly; no
  enrichment in `retrieve.go`/`ext.go`; inbox files are plain files on disk
  with no metadata sidecars.
- Official clients show "from <device>" from the receive-time notification
  path, not a retroactive API — the retroactive API is all owldrop can poll.

**Recommended replacement — per-drop-link auto-save (implementable now):**
drop-link deliveries already carry attribution in history (`source: "link"`
+ the link's label). Rules keyed on the drop link give the same UX value
("everything *I sent via the family link* lands in folder X") and unlock
attributed receiving without any sender API:

1. **Config**: `dropAutoSaveRules: [{linkToken|label, dir}]` — Settings →
   Drop links → "auto-save to…" per link (FolderPicker reuse).
2. **`drops.go`/`ops.go`**: on link upload completion, resolve the link →
   rule → save through the existing save machinery with history attribution
   (already records the link label/source).
3. **UI**: per-link row gets a folder affordance + current rule summary;
   global auto-save precedence stays "rule wins".

**Insurance:** keep `tools/probe-files-api.go` in the tree — one afternoon
to re-run on future daemon majors (Tailscale has refactored Taildrop into
`feature/taildrop`; WaitingFile may gain fields without fanfare).

**Effort: M (rules + UI).** Depends on: nothing (history already has the
link attribution).

---

## Sequencing

1. **#2** (days, independent, pure win).
2. **#1** (Serve + guard change + funnel reconciliation — biggest
   day-to-day UX jump; unblocks https URLs everywhere).
3. **#4-pivot (per-drop-link auto-save)** after #1 — replaces the blocked
   sender-identity feature with the implementable version.
4. **#3 Option A** after #1 (reuses the serving manager for the tsnet
   listener).

Open questions for the maintainer:
- #1 conflict policy: refuse toggle vs overwrite-existing-config.
- #1 gating: HTTPS toggle independent of LAN mode?
- #4: adopt the per-drop-link pivot, and re-probe WaitingFile on future
  daemon majors (tools/probe-files-api.go).
- #3: Option A (network-only node) vs B (tailscaled sidecar) for the
  container image.
