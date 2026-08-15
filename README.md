# Owldrop

A native desktop app for [Tailscale's Taildrop](https://tailscale.com/kb/1082/taildrop):
see files sent to you, save or delete them with one click, send files back to
any device on your tailnet, and optionally auto-save everything that arrives.
No more `tailscale file get .` on the command line.

## Architecture

One Go binary, two halves:

- **Server** (`main.go` + the `server.go`/`localapi.go`/`ops.go`/`history.go`
  files) — talks to the **local** `tailscaled` daemon over its LocalAPI (the
  same interface the `tailscale` CLI uses) and serves the UI on
  `127.0.0.1:8976`. Received files stay in the daemon's inbox until you
  save/delete — the same inbox the CLI drains.
- **Desktop shell** (`shell.go`) — a [Wails v3](https://v3.wails.io/) app in
  the same process: a native window (WebKitGTK / WKWebView / WebView2)
  pointed at the local UI, a system tray with quick-send, native
  notifications driven by the server's event stream, a global shortcut
  (Ctrl+Alt+O by default, configurable in Settings) and close-to-tray.
  Everything talks in-process — there is
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
- **Hidden devices** — hide a device from the Send picker and the tray's
  quick-send menu (Settings → Hidden devices). Hidden devices can still
  *receive* files — this only removes them as a send target.
- **Reverse-proxy trusted domains** — serve the full app at your own
  hostname through a reverse proxy by listing the domain under Settings →
  Trusted domains. A domain covers all its subdomains; same trust posture
  as LAN mode (see Notes).
- **HTTPS access** — Settings toggle: serves the app at
  `https://<machine>.<tailnet>.ts.net/` via Tailscale Serve with an
  automatically issued/renewed Let's Encrypt certificate, tailnet-only
  (Funnel for public drop links is unchanged; when Funnel is on, only
  `/drop/*` is exposed to the internet).
- **Per-link auto-save** — Drop links can route their uploads straight
  into a folder: set it on the link row in Drop links, and files sent
  through that link are saved there automatically, even when global
  auto-save is off.
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
- **Global shortcut** — Ctrl+Alt+O (default) brings the window to the front;
  configurable in Settings → Shortcuts, applies live
- **Public drop links** — flip on Tailscale Funnel in Settings and anyone on
  the internet can drop a file into your inbox at your public `*.ts.net`
  URL — free, served from your own machine (only `/drop/*` is exposed)
- **Sync** — a shared clipboard/scratchpad: paste text or upload a file from
  any device that can reach the app (localhost, LAN, tailnet) and every open
  page sees it instantly over SSE. Items persist on the host (capped at 100,
  text ≤ 64 KiB, files ≤ 4 GiB). Never exposed through Funnel
- **History export** — one click dumps the full log as JSON

## Agent MCP (tailnet)

Agents on your tailnet (Hermes, a CI tsnet node, a coding agent on another
device) can send and receive files through Owldrop without opening the GTK
window. Enable **Agent access** in Settings (requires LAN or HTTPS Serve) and
copy the MCP URL and bearer token from the card.

Example Cursor `mcp.json` (or any MCP client that supports Streamable HTTP):

```json
{
  "mcpServers": {
    "owldrop": {
      "url": "http://100.x.x.x:8976/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

Use your tailnet IP from Settings — not the public `*.ts.net` hostname. Public
access (Funnel) exposes drop links only; `https://<machine>.ts.net/mcp` returns
404 even with a valid token.

## Docker / NAS (Unraid, Synology, …)

Taildrop cannot send to tagged devices
([tailscale/tailscale#10695](https://github.com/tailscale/tailscale/issues/10695))
— the exact machines homelab people tag. Owldrop on that box is the
workaround: drop links, LAN, and HTTPS land files in a real inbox when the
container uses the **host Tailscale socket**.

Run owldrop in a container on a machine that already runs Tailscale. The
container borrows the host's Tailscale — it doesn't need its own account.

1. Get the image:

   ```sh
   docker pull ghcr.io/rastavich/owldrop:latest
   ```

2. Run it (one command — adjust the two paths for your box):

   ```sh
   docker run -d --name owldrop --restart unless-stopped \
     -p 8976:8976 \
     -v /var/run/tailscale/tailscaled.sock:/var/run/tailscale/tailscaled.sock \
     -v owldrop-config:/data \
     -v /mnt/user/downloads:/data/downloads \
     ghcr.io/rastavich/owldrop:latest --lan --save-dir /data/downloads
   ```

   - `/var/run/tailscale/tailscaled.sock` — leave as-is on Linux; this is how
     the container talks to your Tailscale.
   - **No host Tailscale?** Run the container with `OWLDROP_TSNET=1` (and
     `OWLDROP_HOSTNAME=owldrop-nas`) and the app joins the tailnet as its
     own node — UI, drop links, Sync and HTTPS work without any host daemon
     (`TS_AUTHKEY` pre-approves the node). The tailnet-state indicator and
     status read from this node directly. Note: the Taildrop inbox is
     provided by a tailscaled daemon, so a tsnet-only node has no inbox
     (upload via drop links instead).
   - `/mnt/user/downloads` — the folder where saved files land. Point it at
     any share/folder you want files to arrive in (on Unraid, e.g.
     `/mnt/user/media`).

3. Open it from any device on your tailnet: `http://<nas-tailnet-ip>:8976/`

That's it. To let every device on your tailnet *send* files through it, keep
the `--lan` flag as above; drop links work the same way as the desktop app.

Settings, history, drop links, Sync items and the session token all live in
the `owldrop-config:/data` volume — so when you pull a new image and rebuild
or update the container, the already-open UI keeps working without re-opening
or re-authenticating: the session token is read from the volume, not minted
fresh per run.

### Reverse proxy / trusted domain

Serve the app at your own domain through a reverse proxy (Nginx Proxy
Manager, Traefik, Caddy, …):

1. Keep `--lan` in the run command — the app must bind all interfaces so the
   proxy container can reach it.
2. In **Settings → Trusted domains**, add your domain (e.g. `drop.example.com`;
   subdomains are covered automatically). No restart needed.
3. Create a proxy host in your proxy pointing the domain at the `owldrop`
   container (or `host-ip:8976`), HTTPS enabled, **no Host/Origin header
   rewrites**, and proxy at the **root path only** — the UI uses absolute
   `/api/...` and `/assets/...` paths, so a subpath will not work.
4. Point the domain's DNS at the proxy and open `https://drop.example.com/`.

Quick check before the proxy is involved: `curl -I -H 'Host: drop.example.com' http://<nas-ip>:8976/` should return 200 once the domain is saved.

> Security: a page served on a trusted domain can drive the app (same posture
> as LAN mode), so only ever add hostnames whose DNS you control.


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
cd .. && go build -o owldrop . && ./owldrop
```

Or use the wails taskfile, which builds the frontend automatically:
`wails3 task linux:build` (run: `wails3 task run`). For hot reload during
development: `wails3 dev` rebuilds everything; for UI work specifically,
run the app (`./owldrop`) and `cd web && npm run dev` — the Vite dev
server picks up the session config from the running app and proxies its API
calls to it. A headless server-only build (no window/tray — just the HTTP
server for LAN use) is available as `wails3 task build:server`
(`-tags server`).

## Install / package

- **End users (NixOS)**: `nix profile install github:Rastavich/owldrop-install`
  — a public binary-only repo (source lives here, in the public repo). CI
  pushes the built binary there on every release.
- Developers: `nix profile install .#default` (or `nix run .#default`)
  builds from this repo against nixpkgs' webkitgtk_6_0 and wraps it in an
  FHS environment so the dynamic libs resolve.
- Other distros: `wails3 task linux:package` produces DEB / RPM / AUR in
  `bin/` (Ubuntu 24.04 LTS: `wails3 task linux:package:gtk3`); Windows:
  `wails3 task windows:package` → NSIS installer; macOS:
  `wails3 task darwin:package:dmg:universal` → universal .dmg. Build each
  on its own OS.
- **GitHub Releases**: tag a commit `vX.Y.Z` (or `X.Y.Z`) and CI builds and
  publishes deb/rpm (Linux), dmg (macOS) and NSIS installer (Windows)
  automatically, and ships the raw binary to the public install repo.
  `.github/workflows/release.yml`. `scripts/bump-release.sh` does the whole
  release step for you: bumps the version in `build/config.yml`, the
  `build/*/Taskfile.yml` ldflags and `web/package.json`, regenerates the
  platform assets, commits, tags and pushes (run with `-n` to preview;
  bare `X.Y.Z` tags match the repo's existing ones).

## Notes & limitations

- **Linux system requirements**: default builds need WebKitGTK 6.0 (Debian
  13+, Ubuntu 24.10+, Fedora 40+). Ubuntu 24.04 LTS / Debian 12 packages are
  published as `owldrop-linux-*-webkit41.*` (`-tags gtk3`, WebKit2GTK 4.1).
  Local gtk3 builds: `wails3 task linux:package:gtk3`.
- **Linux desktop variance**: the tray icon uses the StatusNotifierItem
  protocol (GNOME needs an AppIndicator extension); global shortcuts on
  Wayland go through the XDG portal, so the compositor may re-map keys.
- Sender attribution: the daemon's file API (v1.98) exposes only name+size
  for waiting files — no sender identity — so auto-save applies to
  everything. A per-host trust list needs a daemon API that doesn't exist
  yet.
- The server binds `127.0.0.1:8976` with a session token + Origin and Host
  checks on mutating calls; LAN mode exposes it to your tailnet only, and
  Funnel exposes only the `/drop/*` pages. The token is persisted in the
  config file (mode 0600) so it survives restarts and container updates —
  the config directory is the trust boundary, same as the LocalAPI socket.
- A domain you add under
  Settings → Trusted domains is treated like LAN access: any page served on
  that hostname can drive the app, and the session token is embedded in the
  page. Only ever add hostnames whose DNS you control (a reverse proxy you
  run yourself).

## Layout

```
main.go        server: config, HTTP server, OS helpers
shell.go       Wails desktop shell: window, tray, notifications, shortcut
localapi.go    daemon interactions: inbox, save, delete, devices, send
ops.go         save/send operations with progress events
history.go     local event log (arrivals, saves, deletes, sends)
server.go      event hub, API, security guards, inbox watcher + auto-save
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

Builds the app and installs it into `~/.local/share/owldrop`, then
runs it as a systemd user service (`owldrop.service`), so it starts
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

## Public access is free

Public drop links are a free feature — no subscription, no accounts, no
server in between. The Funnel toggle runs `tailscale funnel` on your own
machine, and uploads go straight to you over Tailscale's infrastructure:
your files never transit a third-party server, and there is nothing to
billing-gate. (`./scripts/funnel.sh` still exists for manual control.)

## Support

Owldrop is MIT-licensed and free. If it saves you time, a coffee keeps the
owl fed: [ko-fi.com/X8X51XKA5G](https://ko-fi.com/X8X51XKA5G).

## Telemetry

Owldrop reports a minimal anonymous usage stream (app version, OS, event
name, timestamp, and a random per-install id) to the site's `/api/t`
endpoint. The stats page tracks an activation funnel (download → heartbeat
install → first successful transfer → 14-day repeat), not daily opens as
the product metric. No file names, sizes, content, or sender/recipient
information ever leaves the machine.

- Events: heartbeat (app start), file_received, file_saved, file_deleted,
  file_sent, send_failed, drop_link_created, drop_link_used,
  drop_link_failed, sync_item_added.
- A successful transfer is `file_received`, `file_sent`, `sync_item_added`,
  or `drop_link_used`.
- Opt out anytime in **Settings → Privacy** (writes `telemetry: false` to
  the config). Off means nothing leaves the machine.
- The site worker stores events in Cloudflare D1; the stats dashboard lives
  at `https://owldrop.app/stats?token=…` (STATS_TOKEN secret) and download
  counts come from the site's `/dl` redirect links.

## License

MIT — do whatever you like with it.
