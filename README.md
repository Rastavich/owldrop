# tailscale-drop

A small cross-platform GUI for [Tailscale's Taildrop](https://tailscale.com/kb/1082/taildrop)
— see files sent to you, save or delete them with one click, and send files
back to any device on your tailnet. No more `tailscale file get .` on the
command line.

```
$ tailscale-drop

  tailscale-drop
  UI:    http://127.0.0.1:8976/
  inbox: 2 file(s) waiting, saved to /home/you/Downloads
```

## How it works

- A single Go binary talks to your **local `tailscaled` daemon** over its
  LocalAPI — the exact same interface the `tailscale` CLI uses
  (`tailscale.com/client/local`). The daemon socket/named pipe is discovered
  automatically on Linux, macOS, and Windows.
- The UI is a browser page served on `127.0.0.1` (embedded in the binary, no
  build step, no web server to install). It updates live over
  Server-Sent Events, using the daemon's long-poll (`?waitsec=`) so files
  appear the moment they arrive — with a desktop notification if the tab
  isn't focused.
- Received files stay in the daemon's inbox until you click **Save** (to your
  Downloads folder or any folder you choose) or **Delete** — the same inbox
  `tailscale file get` drains, so the CLI still works alongside.
- Sending uses the daemon's `PushFile` with per-file progress bars.

## Build & run

```sh
go build -o tailscale-drop .     # or: go run .
./tailscale-drop                 # opens your browser automatically
```

Cross-compile for the other platforms:

```sh
GOOS=windows GOARCH=amd64 go build -o tailscale-drop.exe .
GOOS=darwin  GOARCH=arm64 go build -o tailscale-drop-darwin .
```

Flags:

| Flag | Meaning |
|------|---------|
| `--port N` | UI port (default `8976`, bound to `127.0.0.1` only) |
| `--save-dir PATH` | default folder for received files (persisted) |
| `--no-open` | don't launch the browser on start |

You must be able to talk to `tailscaled`: on Linux that means your user is in
the `tailscale` group (or the socket is world-accessible), on macOS/Windows
the app just works for the logged-in user. The app runs per-user; received
files are saved under *your* home directory.

## Security

- The server binds to `127.0.0.1` only.
- Mutating API calls require a random session token embedded in the served
  page, plus an `Origin` check — a malicious website can't CSRF your inbox.
- `Host` header validation blocks DNS-rebinding attacks.
- Only the local daemon is ever contacted; nothing runs on a remote server.

## Why not just the CLI?

You still can — this is a frontend for the same inbox. The value is
convenience:

- files appear without polling or terminal commands,
- save to any folder, with Chrome-style `name (1).ext` conflict handling,
- send to any device from a drag & drop zone,
- it works from a phone browser pointed at your machine (same LAN/tailnet).

## Layout

```
main.go        flags, config, browser launch, OS helpers
taildrop.go    daemon interactions: inbox, save, delete, devices, send
server.go      HTTP API, SSE hub, CSRF/rebinding guards, inbox watcher
web/index.html the UI (embedded into the binary via go:embed)
main_test.go   unit tests for naming/validation logic
```

## Roadmap ideas

- Auto-accept incoming files into a folder (a GUI `file get --loop`)
- System tray + autostart so it's always running
- Reveal-in-file-manager after save (partially done via "Open")
- LAN binding with a password for phone access without localhost
- Sender display name (the daemon's API currently exposes name/size only)

## License

MIT — do whatever you like with it.
