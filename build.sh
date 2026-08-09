#!/usr/bin/env bash
# Build only: frontend + Go binary. Does not run.
set -e
cd "$(cd "$(dirname "$0")" && pwd)"
echo "=== building frontend ==="
nix develop --command bash -c 'cd web && npm run build'
echo "=== building Go binary ==="
nix develop --command go build -o owldrop .
echo "=== done: ./owldrop ==="
