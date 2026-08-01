#!/usr/bin/env bash
# Quick dev launcher: build the sidecar if stale, then open the app window.
# For a proper install (systemd service, autostart) use ./install.sh instead.
set -e
cd "$(dirname "$0")"
if [ ! -x ./tailscale-drop ] \
   || [ main.go -nt tailscale-drop ] \
   || [ server.go -nt tailscale-drop ] \
   || [ taildrop.go -nt tailscale-drop ] \
   || [ ops.go -nt tailscale-drop ] \
   || [ web/index.html -nt tailscale-drop ]; then
  echo "building tailscale-drop…"
  nix develop --command go build -o tailscale-drop .
fi
exec nix develop --command electron ./electron
