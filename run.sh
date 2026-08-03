#!/usr/bin/env bash
# Development runner: build the app inside the nix dev shell and launch it.
set -e
cd "$(cd "$(dirname "$0")" && pwd)"
nix develop --command go build -o tailscale-drop .
exec nix develop --command ./tailscale-drop
