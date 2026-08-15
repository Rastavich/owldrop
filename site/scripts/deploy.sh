#!/usr/bin/env bash
# Bake the latest release tag into index.html, then deploy the site.
#
#   ./scripts/deploy.sh          resolve latest tag, bake, deploy
#   ./scripts/deploy.sh -n       dry run: print the version, change nothing
#
# The latest release tag is resolved from the public app repo
# (github.com/Rastavich/owldrop) via git ls-remote. The version only changes
# via scripts/bump-release.sh in the app repo, so resolving it at deploy
# time is exact.
set -euo pipefail
cd "$(dirname "$0")/.."

DRY_RUN=0
if [[ "${1:-}" == "-n" || "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1
fi

# Latest bare X.Y.Z tag on origin (peeled ^{} lines and v-prefixed tags
# excluded; numeric sort so 0.10.2 > 0.9.0).
V="$(git ls-remote --tags origin 'refs/tags/*' \
  | sed 's|.*refs/tags/||' \
  | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' \
  | sort -t. -k1,1n -k2,2n -k3,3n \
  | tail -1)"
[[ -n "$V" ]] || { echo "error: no semver tags found on origin" >&2; exit 1; }

if (( DRY_RUN )); then
  echo "latest tag: $V (dry run, nothing changed)"
  exit 0
fi

sed -i "s/\"softwareVersion\": \"[0-9.]*\"/\"softwareVersion\": \"$V\"/" public/index.html
sed -i "s/<span class=\"pill\" id=\"version-pill\">v[0-9.]*/<span class=\"pill\" id=\"version-pill\">v$V/" public/index.html

grep -q "\"softwareVersion\": \"$V\"" public/index.html || { echo "error: failed to bake softwareVersion" >&2; exit 1; }
grep -q "id=\"version-pill\">v$V" public/index.html || { echo "error: failed to bake version pill" >&2; exit 1; }

echo "baked v$V into public/index.html"

# Prefer the lockfile-pinned wrangler so deploy matches npm audit / CI.
if [[ ! -x node_modules/.bin/wrangler ]]; then
  echo "installing pinned wrangler (npm ci)"
  npm ci
fi
exec ./node_modules/.bin/wrangler deploy
