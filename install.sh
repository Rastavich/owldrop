#!/usr/bin/env bash
# Build the sidecar, then either install it as a user service or hot-reload
# the running app.
#
#   ./install.sh            build + refresh the running app (fast path:
#                           only the Go sidecar is restarted; the window
#                           stays open and reloads itself)
#   ./install.sh --full     force a full service restart (needed only when
#                           electron/main.js changed)
#   ./install.sh --run      launch in the foreground (no install)
set -e
cd "$(cd "$(dirname "$0")" && pwd)"

echo "building tailscale-drop…"
nix develop --command go build -o tailscale-drop .

if [ "$1" = "--run" ]; then
  exec nix develop --command electron ./electron
fi

DEST="$HOME/.local/share/tailscale-drop"
mkdir -p "$DEST"

# Replace the running sidecar via temp+rename (rename over an executing
# binary is fine; plain cp fails with ETXTBSY).
cp tailscale-drop "$DEST/tailscale-drop.new"
mv -f "$DEST/tailscale-drop.new" "$DEST/tailscale-drop"
cp -r electron "$DEST/"
cp flake.nix "$DEST/"

cat > "$DEST/run.sh" <<'RUNEOF'
#!/usr/bin/env bash
NIX_BIN="$(command -v nix || echo /run/current-system/sw/bin/nix)"
DEST="$HOME/.local/share/tailscale-drop"
cd "$DEST"
exec "$NIX_BIN" develop --command electron "$DEST/electron" --enable-logging ${TSD_DEBUG_PORT:+--remote-debugging-port=$TSD_DEBUG_PORT}
RUNEOF
chmod +x "$DEST/run.sh"

mkdir -p "$HOME/.config/systemd/user"
cp packaging/tailscale-drop.service "$HOME/.config/systemd/user/"
systemctl --user daemon-reload

if systemctl --user is-active --quiet tailscale-drop.service; then
  if [ "$1" = "--full" ]; then
    echo "full restart…"
    systemctl --user restart tailscale-drop.service
  else
    echo "hot-reloading sidecar… (window stays open)"
    # Electron's exit handler restarts the sidecar and reloads the window
    # on the new port within a couple of seconds.
    pkill -x tailscale-drop || true
  fi
else
  echo "starting service…"
  systemctl --user enable --now tailscale-drop.service
fi
echo "done"
