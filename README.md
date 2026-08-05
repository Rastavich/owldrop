# tailscale-drop

A native desktop app for [Tailscale's Taildrop](https://tailscale.com/kb/1082/taildrop):
see files sent to you, save or delete them with one click, send files back to
any device on your tailnet, and optionally auto-save everything that arrives.
No more `tailscale file get .` on the command line.

## Architecture

One Go binary, two halves:

- **Server** (`main.go` + the `server.go`/`taildrop.go`/`ops.go`/`history.go`
  files) — talks to the **local** `tailscaled` daemon over its LocalAPI (the
  same interface the `tailscale` CLI uses) and serves the UI on
  `127.0.0.1:8976`. Received files stay in the daemon's inbox until you
  save/delete — the same inbox the CLI drains.
- **Desktop shell** (`shell.go`) — a [Wails v3](https://v3.wails.io/) app in
  the same process: a native window (WebKitGTK / WKWebView / WebView2)
  pointed at the local UI, a system tray with quick-send, native
  notifications driven by the server's event stream, a global shortcut
  (Ctrl+Shift+T) and close-to-tray. Everything talks in-process — there is
  no separate sidecar binary and no session-token plumbing.

The UI is a **Vite + React + TanStack** app (`web/`) built to static assets
(`web/dist`) that are embedded into the binary. It talks REST + SSE to the
server — so the same UI works from a plain browser too: LAN mode (`--lan` or
the Settings toggle) serves it on your tailnet IP, and drop links can be
public via Tailscale Funnel. (The public `/drop/<token>` upload page is a
separate minimal page served by the Go binary — it must work on the Funnel
hostname where nothing else is reachable.)

## Features

- **Inbox** — files appear instantly (daemon long-poll), with size, arrival
  time, progress bars, Save / Save to… (native folder dialog) / Delete,
  and a filter box. Keyboard: `/` search, `j`/`k` select, `s` save, `d` delete.
- **History** — everything that happened is logged locally: arrivals,
  saves (with the destination path), deletes, and sends. Filter by
  received/sent, search, open or reveal a saved file, per-file stats, clear.
- **Send** — device picker (offline & can't-receive reasons shown), file
  picker, or drag & drop onto the window; **send to several devices at
  once** via the checklist popover; Save all / Save all to… for batch
  receiving
- **LAN mode** — Settings toggle (or `--lan`): other devices on your
  tailnet can open the app at `http://<tailnet-ip>:8976/` (token-
  protected, hostnames blocked against DNS rebinding).
- **Auto-save** — one checkbox: incoming files land in your folder the
  moment they arrive (like `tailscale file get --loop`), with notifications
- **Notifications** — arrival + save/send results, native OS notifications
  even when the window is hidden in the tray; toggle each event type in
  Settings
- **Paste-to-send** — copy an image or text and press Ctrl+V in the Send
  tab (or hit Paste) to send it as a file
- **Tray quick-send** — right-click the tray icon → Send file to… → pick a
  device → native file dialog
- **Safety** — opening executable/script files asks for confirmation first
- **Global shortcut** — Ctrl+Shift+T brings the window to the front
- **Premium (public access)** — public drop links via Tailscale Funnel are a
  Stripe subscription feature ($5/mo); subscribe/manage from Settings
- **History export** — one click dumps the full log as JSON

## Run

NixOS (or anywhere with the nix dev shell):

```sh
./run.sh            # nix develop + go build + run
```

Other distros need GTK4 + WebKitGTK 6.0 dev libraries (this app's Linux
rendering stack; `pkg-config` must find `webkitgtk-6.0`) plus Node for the
frontend build:

```sh
cd web && npm ci && npm run build   # → web/dist (embedded into the binary)
cd .. && go build -o tailscale-drop . && ./tailscale-drop
```

Or use the wails taskfile, which builds the frontend automatically:
`wails3 task linux:build` (run: `wails3 task run`). For hot reload during
development: `wails3 dev` rebuilds everything; for UI work specifically,
run the app (`./tailscale-drop`) and `cd web && npm run dev` — the Vite dev
server picks up the session config from the running app and proxies its API
calls to it. A headless server-only build (no window/tray — just the HTTP
server for LAN use) is available as `wails3 task build:server`
(`-tags server`).

## Install / package

- **End users (NixOS)**: `nix profile install github:Rastavich/taildrop-install`
  — a public binary-only repo (the source stays private). CI pushes the
  built binary there on every release.
- Developers: `nix profile install .#default` (or `nix run .#default`)
  builds from this repo against nixpkgs' webkitgtk_6_0 and wraps it in an
  FHS environment so the dynamic libs resolve.
- Other distros: `wails3 task linux:package` produces DEB / RPM / AUR in
  `bin/` (Windows: `wails3 task windows:package` → NSIS installer; macOS:
  `wails3 task darwin:package:dmg` → .dmg; build each on its own OS).
- **GitHub Releases**: tag a commit `vX.Y.Z` (or `X.Y.Z`) and CI builds and
  publishes deb/rpm (Linux), dmg (macOS) and NSIS installer (Windows)
  automatically, and ships the raw binary to the public install repo.
  `.github/workflows/release.yml`.

## Notes & limitations

- **Linux system requirements**: default builds need WebKitGTK 6.0 (Debian
  13+, Ubuntu 24.10+, Fedora 40+). Distros stuck on WebKit2GTK 4.1 (Ubuntu
  22.04/24.04, Debian 12) can build with `-tags gtk3` — supported through
  the v3.0.x line, removed in Wails v3.1.
- **Linux desktop variance**: the tray icon uses the StatusNotifierItem
  protocol (GNOME needs an AppIndicator extension); global shortcuts on
  Wayland go through the XDG portal, so the compositor may re-map keys.
- Sender attribution: the daemon's file API (v1.98) exposes only name+size
  for waiting files — no sender identity — so auto-save applies to
  everything. A per-host trust list needs a daemon API that doesn't exist
  yet.
- The server binds `127.0.0.1:8976` with a per-run session token + Origin
  and Host checks on mutating calls; LAN mode exposes it to your tailnet
  only, and Funnel exposes only the `/drop/*` pages.

## Layout

```
main.go        server: config, HTTP server, OS helpers
shell.go       Wails desktop shell: window, tray, notifications, shortcut
taildrop.go    daemon interactions: inbox, save, delete, devices, send
ops.go         save/send operations with progress events
history.go     local event log (arrivals, saves, deletes, sends)
server.go      event hub, API, security guards, inbox watcher + auto-save
stripe.go      Premium: Stripe checkout/portal, subscription polling, paywall
web/         Vite + React + TanStack frontend (built to web/dist, embedded)
build/         wails3 taskfiles + packaging assets (AppImage/deb/rpm/NSIS/dmg)
tools/genicon  regenerates the icon PNG
flake.nix      NixOS dev shell + package
install.sh     installs a systemd user service
docs/wails3-evaluation.md  why Wails v3 replaced Electron (evaluation)
main_test.go   unit tests (conflict naming, validation)
```

## Install as a service (NixOS)

```sh
./install.sh
```

Builds the app and installs it into `~/.local/share/tailscale-drop`, then
runs it as a systemd user service (`tailscale-drop.service`), so it starts
with your desktop session and restarts on failure. The window's close button
hides to the tray; quit from the tray menu. `./install.sh --run` launches in
the foreground instead.

## Drop links (send files TO this machine from anyone)

Create a short-lived link in Settings → Drop links (name it, pick a
lifetime, single-file or unlimited). Anyone who opens the URL in a browser
— no Tailscale account needed — can drop a file straight into your inbox,
with "via drop link" attribution. Links expire, can be single-use, and can
be revoked instantly.

- The URL token is the only auth, so keep links short-lived.
- Uploads are quarantined and go through the same save/delete and
  risky-open handling as anything else.
- **External users** (not on your tailnet): flip on "Public access
  (Funnel)" in Settings → Drop links — the app runs `tailscale funnel`
  itself and shows your public `https://<you>.ts.net/` URL. Only the
  `/drop/*` pages are reachable on that hostname; the full app (and its
  session token) is never exposed. (`./scripts/funnel.sh` still exists for
  manual control.)

## Premium (public access, via Stripe)

Public access — the Funnel toggle and the public `/drop/*` pages — is a
subscription feature. There are two modes:

- **Self-host mode** (this repo's default for your own install): the app
  talks to Stripe directly (no SDK, no webhooks — it polls the
  subscriptions API lazily and caches for 10 minutes). Gating is
  **fail-closed**: no verifiable subscription → public links show a
  "paused" page. This mode is only as strong as the client; fine for your
  own machine.
- **Relay mode** (distributed builds, `relay/`): the app is key-less and
  talks to the seller's relay (`relay_url` in config or
  `TAILDROP_RELAY_URL`). The relay holds the Stripe secret, creates
  Checkout sessions, and **enforces Premium server-side on every public
  request** — a patched client cannot get free public drops. Uploads are
  queued on the relay and delivered to the app over long-polling.

Self-host setup (needs a [Stripe account](https://dashboard.stripe.com/)):

1. Create a recurring **price** in Stripe (e.g. a $5/month price) and copy
   its ID (`price_…`).
2. Give the app your keys — either in the app config file
   (`~/.config/tailscale-drop/config.json`):

   ```json
   { "stripe_secret_key": "sk_live_…", "stripe_price_id": "price_…" }
   ```

   or as env vars (handy for the systemd service — `install.sh` copies a
   unit that reads `~/.config/tailscale-drop/env`):

   ```sh
   # ~/.config/tailscale-drop/env
   TAILDROP_STRIPE_SECRET_KEY=sk_test_…
   TAILDROP_STRIPE_PRICE_ID=price_…
   ```

   Test mode (`sk_test_…`) is fine while you're trying it out; the Stripe
   CLI can also generate test-mode checkout events locally.
3. Restart the app. Settings → **Premium** shows the state: subscribe
   (opens Stripe Checkout), manage/cancel (billing portal). Once active,
   the Funnel toggle unlocks and public links go live. Local and tailnet
   drop links are never affected — only the public hostname is gated.
4. Use the billing portal (or Stripe) to cancel; the next poll cycle then
   pauses public links again.

## Relay deployment (Railway)

The relay (`relay/`) deploys on Railway as a single Dockerfile service:

1. Create a Railway project, add a new service from this repo's GitHub
   integration, and set the service **root directory** to `relay/` (or
   deploy locally with `railway up` from `relay/`). Railway picks up the
   Dockerfile automatically (`relay/railway.json` sets the build, the
   `/healthz` healthcheck, and one replica).
2. Attach a **volume** to the service with mount path `/data`. The relay
   keeps registered devices, API keys, and queued uploads there — without
   the volume every deploy starts empty. The Dockerfile deliberately
   declares no `VOLUME` (Railway's builder rejects it): the Railway volume
   is what persists `/data`. Keep the service at **1 replica**:
   the store is a local filesystem, so multiple replicas would split queues.
3. Set variables on the service:
   - `BASE_URL` — the service's domain, currently
     `https://relay-production-62a6.up.railway.app` (or a custom domain
     you've pointed at it)
   - `STRIPE_SECRET_KEY`, `STRIPE_PRICE_ID` — the Stripe secret must never
     live in clients, only here
   - `PORT` is injected by Railway; `DATA_DIR=/data` comes from the
     Dockerfile.
4. Healthchecks hit `/healthz`; the service restarts on failure.

Release builds point at the relay via `-X main.defaultRelayURL=…` in
`flake.nix` and `build/*/Taskfile.yml` — currently
`https://relay-production-62a6.up.railway.app` (keep in sync with
`BASE_URL`). Installed apps override this with `relay_url` in their config
or the `TAILDROP_RELAY_URL` env var.

## License

MIT — do whatever you like with it.
