#!/usr/bin/env bash
# Build the Go sidecar if needed, then launch the desktop app (Electron)
# inside the nix dev shell so all runtime libraries resolve on NixOS.
set -e
cd "$(dirname "$0")"
if [ ! -x ./tailscale-drop ] || [ main.go -nt tailscale-drop ] || [ server.go -nt tailscale-drop ] || [ taildrop.go -nt tailscale-drop ] || [ ops.go -nt tailscale-drop ]; then
  echo "building tailscale-drop…"
  nix develop --command go build -o tailscale-drop .
fi
exec nix develop --command electron ./electron
