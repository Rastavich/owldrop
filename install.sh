#!/usr/bin/env bash
# Build the app, then either install it as a user service or run it.
#
#   ./install.sh            build + (re)start the user service
#   ./install.sh --run      build + launch in the foreground (no install)
set -e
cd "$(cd "$(dirname "$0")" && pwd)"

DEST="$HOME/.local/share/tailscale-drop"
mkdir -p "$DEST"
# Clean up the old electron-era install layout if present.
rm -rf "$DEST/electron" "$DEST/run.sh" "$DEST/tailscale-drop.new" "$DEST/tailscale-drop"

echo "building tailscale-drop…"
rm -f "$DEST/app.new"
nix build .#default -o "$DEST/app.new"

# Swap the out-link atomically. The old link may point into the store; the
# new build is a fresh store path, so the running binary is never touched.
rm -f "$DEST/app"
mv "$DEST/app.new" "$DEST/app"

if [ "$1" = "--run" ]; then
  exec "$DEST/app/bin/tailscale-drop"
fi

mkdir -p "$HOME/.config/systemd/user"
cp packaging/tailscale-drop.service "$HOME/.config/systemd/user/"
systemctl --user daemon-reload

if systemctl --user is-active --quiet tailscale-drop.service; then
  echo "restarting service…"
  systemctl --user restart tailscale-drop.service
else
  echo "starting service…"
  systemctl --user enable --now tailscale-drop.service
fi
echo "done"
