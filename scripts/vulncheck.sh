#!/usr/bin/env bash
# Fail on high/critical advisories in the web UI and marketing site npm
# trees, and on reachable Go/Wails vulns (govulncheck).
#
# Used by:
#   - .github/workflows/ci.yml          (PRs + main)
#   - .github/workflows/release.yml     (before packaging)
#   - scripts/bump-release.sh           (local preflight)
#
#   ./scripts/vulncheck.sh
set -euo pipefail
cd "$(dirname "$0")/.."

section() { printf '\n=== %s ===\n' "$*"; }

need() {
  command -v "$1" >/dev/null || {
    echo "error: $1 not on PATH" >&2
    exit 1
  }
}

need npm
need go

# --- npm -----------------------------------------------------------------
# --audit-level=high exits non-zero for high and critical only.
audit_npm() {
  local dir=$1
  section "npm audit (high+) — ${dir}/"
  [[ -f "$dir/package-lock.json" ]] || {
    echo "error: missing $dir/package-lock.json — run npm install in $dir" >&2
    exit 1
  }
  (cd "$dir" && npm audit --audit-level=high)
}

audit_npm web
audit_npm site

# --- Go / Wails ----------------------------------------------------------
# Install a pinned govulncheck into the module cache when missing so CI and
# local shells behave the same without committing a tool binary.
if ! command -v govulncheck >/dev/null; then
  section "install govulncheck"
  go install golang.org/x/vuln/cmd/govulncheck@latest
  export PATH="$(go env GOPATH)/bin:$PATH"
fi
need govulncheck

# Headless server build (Docker/NAS/`-tags server`) — no CGO needed.
section "govulncheck — server build (-tags server)"
CGO_ENABLED=0 govulncheck -tags server ./...

# Windows desktop path covers shell.go + updater.go + Wails without needing
# Linux GTK/WebKit headers on the CI runner.
section "govulncheck — windows desktop (Wails)"
CGO_ENABLED=0 GOOS=windows govulncheck ./...

printf '\nvulncheck OK\n'
