# tailscale-drop

A native, cross-platform desktop app for [Tailscale's Taildrop](https://tailscale.com/kb/1082/taildrop):
see files sent to you, save or delete them with one click, send files back to
any device on your tailnet, and optionally auto-save everything that arrives.
No more `tailscale file get .` on the command line.

Built with [Fyne](https://fyne.io/) (pure Go) on top of the same LocalAPI the
`tailscale` CLI uses, so it works identically on Linux, macOS, and Windows.

## Features

- **Inbox** — files sent to this machine appear instantly (the daemon's
  long-poll, not polling), with size, arrival time, per-file progress bars,
  and Save / Save to… / Delete actions
- **Send** — pick a device (offline ones are marked), choose a file or drag &
  drop it onto the window, watch the progress
- **Auto-save** — toggle in Settings or the tray: incoming files land in your
  chosen folder the moment they arrive, like `tailscale file get --loop`,
  with a desktop notification per file
- **Desktop notifications** — arrival (when auto-save is off) and save/send
  results; the app lives in the system tray (closing the window hides it)
- **Native file dialogs** — folder picker for the save location, file picker
  for sending
- **Optional browser UI** — `--web` also serves the earlier web frontend on
  `127.0.0.1` (handy from a phone on the same network)

## Build & run

The app needs a GL-capable desktop and (on Linux) X11/Wayland dev headers to
build. On NixOS, use the included dev shell:

```sh
nix develop --command go build -tags migrated_fynedo -o tailscale-drop .
./tailscale-drop            # or: ./run.sh (NixOS: builds & launches)
```

On any distro with pkg-config + X11/Wayland headers installed, plain
`go build -tags migrated_fynedo .` works. Cross-compile for other platforms
(with their toolchains):

```sh
GOOS=windows GOARCH=amd64 go build -tags migrated_fynedo -o tailscale-drop.exe .
GOOS=darwin  GOARCH=arm64 go build -tags migrated_fynedo -o tailscale-drop .
```

Flags:

| Flag | Meaning |
|------|---------|
| `--save-dir PATH` | default folder for received files (persisted) |
| `--web` | also serve the browser UI (see above) |
| `--port N` | port for the browser UI (default `8976`, 127.0.0.1 only) |

You must be able to talk to `tailscaled`: on Linux your user needs access to
its socket (the `tailscale` group on most distros); on macOS/Windows the app
works for the logged-in user.

## How it works

- The app talks to the **local** `tailscaled` daemon over its LocalAPI
  (`tailscale.com/client/local`) — the identical interface the CLI uses.
  Nothing is proxied through a server; only the daemon socket is touched.
- Received files stay in the daemon's inbox until you save or delete them —
  the same inbox `tailscale file get` drains, so the CLI still works
  alongside.
- Sender attribution: the daemon's file API currently exposes only
  name+size (no sender identity), so auto-save applies to everything that
  arrives. The Settings layout leaves room for a per-host trust list the
  moment the daemon exposes senders.

## Layout

```
main.go        entry point, config, OS helpers, optional web server
taildrop.go    daemon interactions: inbox, save, delete, devices, send
ops.go         shared save/send operations with progress events
server.go      event hub, browser-UI API, security guards, inbox watcher
ui.go          the native Fyne app: window, tray, tabs, notifications
web/index.html the optional browser UI (embedded via go:embed)
flake.nix      NixOS dev shell (Go + GL/X11/Wayland headers)
main_test.go   conflict-naming / validation unit tests
ui_test.go     headless widget tests (Fyne test driver)
```

## Security

- The optional browser UI binds to `127.0.0.1` only, with a per-run session
  token, Origin checks, and Host-header validation (no CSRF / DNS rebinding).
- The native app makes no network listeners at all.

## Roadmap ideas

- Per-host trust list for auto-save (blocked on daemon API exposing senders)
- Reveal-in-file-manager after save
- App icon + proper packaging (`fyne package`, Flatpak)
- macOS/iOS-style "open the file" action on notification click

## License

MIT — do whatever you like with it.
