#!/usr/bin/env sh
# Stages the MSIX payload and packs it with tools/msixpack (spec-correct
# APPX container, cross-platform). Called by the `create:msix:package` task.
#
# Identity values come from MSIX_PACKAGE_NAME / MSIX_PUBLISHER /
# MSIX_PUBLISHER_DISPLAY_NAME (defaults below — match a Microsoft Partner
# Center product identity) and the version from build/config.yml.
set -e

ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
VERSION=${VERSION:-$(sed -n 's/^[[:space:]]*version: "\([^"]*\)".*/\1/p' "$ROOT/build/config.yml")}
NAME=${MSIX_PACKAGE_NAME:-Owldrop.Owldrop}
PUBLISHER=${MSIX_PUBLISHER:-CN=7599275B-E82D-4788-8B9A-D50F0AD67F5C}
PUBLISHER_DISPLAY=${MSIX_PUBLISHER_DISPLAY_NAME:-Owldrop}

STAGE="$ROOT/build/windows/msix/stage"
rm -rf "$STAGE"
mkdir -p "$STAGE/Assets"

cp "$ROOT/bin/owldrop.exe" "$STAGE/owldrop.exe"
cp "$ROOT"/build/windows/msix/Assets/*.png "$STAGE/Assets/"
sed -e "s/__MSIX_PACKAGE_NAME__/$NAME/" \
    -e "s/__MSIX_PUBLISHER__/$PUBLISHER/" \
    -e "s/__MSIX_PUBLISHER_DISPLAY_NAME__/$PUBLISHER_DISPLAY/" \
    -e "s/__VERSION__/$VERSION.0/" \
    "$ROOT/build/windows/msix/app_manifest.xml" > "$STAGE/AppxManifest.xml"

if grep -q "__" "$STAGE/AppxManifest.xml"; then
  echo "error: placeholder left in AppxManifest.xml" >&2
  exit 1
fi

cd "$ROOT"

# Prefer Microsoft's own makeappx (Windows SDK) when available — it validates
# the manifest against the package schema before packing. tools/msixpack is
# the cross-platform fallback (Linux/macOS), emitting the same container
# layout for Store ingestion.
PF86=$(cygpath -u 'C:\Program Files (x86)' 2>/dev/null || true)
MAKEAPPX=$(ls -1 "$PF86/Windows Kits/10/bin"/*/x64/makeappx.exe 2>/dev/null | sort -V | tail -1 || true)
if [ -n "$MAKEAPPX" ]; then
  STAGE_WIN=$(cygpath -w "$STAGE")
  OUT_WIN=$(cygpath -w "$ROOT/bin/owldrop-$VERSION-x64.msix")
  "$MAKEAPPX" pack /d "$STAGE_WIN" /p "$OUT_WIN" /nv
  echo "packed with makeappx: bin/owldrop-$VERSION-x64.msix"
else
  go run ./tools/msixpack -stage "$STAGE" -out "bin/owldrop-$VERSION-x64.msix"
fi
