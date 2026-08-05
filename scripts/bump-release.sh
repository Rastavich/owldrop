#!/usr/bin/env bash
# Bump the release version in every place it's pinned, commit, tag, and push
# so CI builds a release from the tag (see .github/workflows/release.yml).
#
#   scripts/bump-release.sh             bump patch: 0.5.5 -> 0.5.6
#   scripts/bump-release.sh 0.6.0       explicit version
#   scripts/bump-release.sh 0.6.0 "note"   commit message: "release 0.6.0: note"
#   scripts/bump-release.sh -n          dry run: print the plan, change nothing
#
# Touched files: build/config.yml (source of truth), the three
# build/*/Taskfile.yml appVersion ldflags, web/package.json, and the
# wails3-generated platform assets (regenerated via common:update:build-assets;
# flake.nix reads the version out of config.yml on its own).
#
# Safety: refuses to run on a dirty tree (the commit sweeps everything) or if
# the tag already exists locally or on origin. Tags are bare X.Y.Z, matching
# the repo's existing tags; a v-prefixed argument is normalized.
set -euo pipefail
cd "$(dirname "$0")/.."

DRY_RUN=0
if [[ "${1:-}" == "-n" || "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1
  shift
fi

CUR="$(sed -n 's/^[[:space:]]*version: "\([^"]*\)"/\1/p' build/config.yml | head -1)"
[[ -n "$CUR" ]] || { echo "error: can't read current version from build/config.yml" >&2; exit 1; }

NEW="${1:-}"
if [[ -z "$NEW" ]]; then
  IFS=. read -r MAJ MIN PATCH <<< "$CUR"
  NEW="$MAJ.$MIN.$((PATCH + 1))"
fi
NEW="${NEW#v}" # normalize v0.5.5 -> 0.5.5

[[ "$NEW" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "error: version must be X.Y.Z (got '$NEW')" >&2; exit 1; }
[[ "$NEW" != "$CUR" ]] || { echo "error: $NEW is already the current version" >&2; exit 1; }

if [[ -n "$(git status --porcelain)" ]]; then
  echo "error: working tree is dirty — commit or stash first (the release commit sweeps everything)" >&2
  exit 1
fi
if git rev-parse -q --verify "refs/tags/$NEW" >/dev/null 2>&1; then
  echo "error: tag $NEW already exists locally" >&2
  exit 1
fi
if git ls-remote --tags origin "$NEW" 2>/dev/null | grep -q .; then
  echo "error: tag $NEW already exists on origin" >&2
  exit 1
fi

command -v wails3 >/dev/null || { echo "error: wails3 not on PATH (needed to regenerate platform assets)" >&2; exit 1; }

run() { # run [desc] cmd...
  local desc="$1"; shift
  if (( DRY_RUN )); then echo "  would: $desc"; else "$@"; fi
}

echo "bump: $CUR -> $NEW"

# --- versioned files --------------------------------------------------------
if (( ! DRY_RUN )); then
  sed -i "s/version: \"$CUR\"/version: \"$NEW\"/" build/config.yml
  sed -i "s/main.appVersion=$CUR/main.appVersion=$NEW/" build/darwin/Taskfile.yml build/linux/Taskfile.yml build/windows/Taskfile.yml
  sed -i "s/\"version\": \".*\"/\"version\": \"$NEW\"/" web/package.json
else
  echo "  would: sed version in build/config.yml, build/*/Taskfile.yml, web/package.json"
fi

run "wails3 task common:update:build-assets (regenerate platform assets)" wails3 task common:update:build-assets

# --- verify every reference moved ------------------------------------------
if (( ! DRY_RUN )); then
  if grep -rn "$CUR" build/ web/package.json >/dev/null 2>&1; then
    echo "error: \"$CUR\" still referenced in build/ or web/package.json — fix before committing" >&2
    exit 1
  fi
  grep -q "\"version\": \"$NEW\"" web/package.json || { echo "error: web/package.json not updated" >&2; exit 1; }
  echo "ok: no \"$CUR\" left in build/ or web/package.json"
fi

# --- commit + tag + push ----------------------------------------------------
MSG="release $NEW${2:+: $2}"
if (( DRY_RUN )); then
  echo "  would: git add -A && git commit -m \"$MSG\""
  echo "  would: git tag $NEW && git push origin HEAD $NEW"
  echo "dry run — nothing changed."
  exit 0
fi

git add -A
git commit -m "$MSG"
git tag "$NEW"
git push origin HEAD
git push origin "$NEW"
echo "pushed $NEW — CI is building it: gh run list --limit 1"
