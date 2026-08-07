#!/usr/bin/env bash
# Build the app, then either install it as a user service or run it.
#
#   ./install.sh            build + (re)start the user service
#   ./install.sh --run      build + launch in the foreground (no install)
set -e
cd "$(cd "$(dirname "$0")" && pwd)"

DEST="$HOME/.local/share/owldrop"
# Migrate the pre-rebrand install dir (app binary, history) if present.
if [ ! -d "$DEST" ] && [ -d "$HOME/.local/share/tailscale-drop" ]; then
  echo "migrating ~/.local/share/tailscale-drop → $DEST"
  mv "$HOME/.local/share/tailscale-drop" "$DEST"
fi
mkdir -p "$DEST"
# Clean up the old electron-era install layout if present.
rm -rf "$DEST/electron" "$DEST/run.sh" "$DEST/owldrop.new" "$DEST/owldrop" "$DEST/tailscale-drop.new" "$DEST/tailscale-drop"

echo "building owldrop…"
rm -f "$DEST/app.new"
nix build .#default -o "$DEST/app.new"

# Swap the out-link atomically. The old link may point into the store; the
# new build is a fresh store path, so the running binary is never touched.
rm -f "$DEST/app"
mv "$DEST/app.new" "$DEST/app"

if [ "$1" = "--run" ]; then
  exec "$DEST/app/bin/owldrop"
fi

# Remove the pre-rebrand user service before installing the new unit.
systemctl --user disable --now tailscale-drop.service 2>/dev/null || true
rm -f "$HOME/.config/systemd/user/tailscale-drop.service"

mkdir -p "$HOME/.config/systemd/user"
cp packaging/owldrop.service "$HOME/.config/systemd/user/"
systemctl --user daemon-reload

if systemctl --user is-active --quiet owldrop.service; then
  echo "restarting service…"
  systemctl --user restart owldrop.service
else
  echo "starting service…"
  systemctl --user enable --now owldrop.service
fi
echo "done"
