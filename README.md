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

- NixOS: `nix profile install .#default` (or `nix run .#default`) — builds
  the binary against nixpkgs' webkitgtk_6_0 and wraps it in an FHS
  environment so the dynamic libs resolve.
- Other distros: `wails3 task linux:package` produces AppImage / DEB / RPM /
  AUR in `bin/` (Windows: `wails3 task windows:package` → NSIS installer;
  macOS: `wails3 task darwin:package:dmg` → .dmg; build each on its own OS).
- **GitHub Releases**: tag a commit `vX.Y.Z` and CI builds and publishes
  AppImage/deb/rpm (Linux), dmg (macOS) and NSIS installer (Windows)
  automatically. `.github/workflows/release.yml`.

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

## License

MIT — do whatever you like with it.
