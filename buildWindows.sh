#!/usr/bin/env bash
# Quick cross-compile for Windows testing (headless server build).
# Produces a terminal-only .exe — the UI runs at http://127.0.0.1:8976 in a browser.
set -e
cd "$(cd "$(dirname "$0")" && pwd)"
echo "=== building frontend ==="
nix develop --command bash -c 'cd web && npm run build'
echo "=== cross-compiling for Windows ==="
nix develop --command bash -c 'GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags server -o owldrop.exe .'
echo "=== done: ./owldrop.exe ==="
echo "Copy to Windows, run it, then open http://127.0.0.1:8976 in a browser."
