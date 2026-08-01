# tailscale-drop

A native desktop app for [Tailscale's Taildrop](https://tailscale.com/kb/1082/taildrop):
see files sent to you, save or delete them with one click, send files back to
any device on your tailnet, and optionally auto-save everything that arrives.
No more `tailscale file get .` on the command line.

## Architecture

Two pieces, one app:

- **Go sidecar** (`tailscale-drop`) — talks to the **local** `tailscaled`
  daemon over its LocalAPI (the same interface the `tailscale` CLI uses) and
  serves the UI on a random `127.0.0.1` port. Received files stay in the
  daemon's inbox until you save/delete — the same inbox the CLI drains.
- **Electron shell** (`electron/`) — a native window (Chromium), system tray
  with Show/Quit, native desktop notifications driven by the sidecar's
  event stream, close-to-tray behavior. The UI inside is the same one you'd
  get in a browser at the sidecar's URL — handy from a phone too.

The two talk over localhost only; nothing is proxied through a remote server.

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

## Run (NixOS)

```sh
./run.sh
```

Builds the Go sidecar if needed, then launches Electron inside the nix dev
shell (so Chromium's runtime libraries resolve). On other distros:

```sh
go build -o tailscale-drop .          # build the sidecar
cd electron && npm install && npm start
```

## Install / package

- NixOS: `nix profile install .#default` (or `nix run .#default`) — builds
  the Go sidecar + wraps nixpkgs' electron.
- Other distros: `cd electron && npm install && npm run dist` produces an
  AppImage / .deb on Linux, dmg on macOS, and an NSIS installer on Windows
  (build each on its own OS). On NixOS, run AppImages with
  `nix run nixpkgs#appimage-run -- Taildrop.AppImage`.

## Notes & limitations

- Sender attribution: the daemon's file API (v1.98) exposes only name+size
  for waiting files — no sender identity — so auto-save applies to
  everything. A per-host trust list needs a daemon API that doesn't exist
  yet.
- The sidecar binds to `127.0.0.1` with a per-run session token + Origin and
  Host checks on mutating calls; the Electron shell adds no network surface.

## Layout

```
main.go        sidecar: config, HTTP server, OS helpers
taildrop.go    daemon interactions: inbox, save, delete, devices, send
ops.go         save/send operations with progress events
history.go     local event log (arrivals, saves, deletes, sends)
server.go      event hub, API, security guards, inbox watcher + auto-save
web/index.html the UI (embedded into the sidecar binary)
electron/      desktop shell: main.js, package.json, icon
tools/genicon  regenerates the icon PNG
flake.nix      NixOS dev shell (go + electron)
main_test.go   unit tests (conflict naming, validation)
```

## Packaging / roadmap

- `electron-builder` for proper .deb/AppImage/Windows/macOS installers
- Per-host trust list for auto-save (blocked on daemon API)
- Reveal-in-file-manager after save

## License

MIT — do whatever you like with it.

## Install as a service (NixOS)

```sh
./install.sh
```

Installs the app into `~/.local/share/tailscale-drop` and runs it as a
systemd user service (`tailscale-drop.service`), so it starts with your
desktop session and restarts on failure. The window's close button hides
to the tray; quit from the tray menu. `./install.sh --run` launches in the
foreground instead.

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
