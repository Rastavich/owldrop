#!/usr/bin/env bash
# Install tailscale-drop as a user service (NixOS), or just run it.
# Usage: ./install.sh          → install & start the systemd user service
#         ./install.sh --run   → launch in the foreground (no install)
set -e
cd "$(cd "$(dirname "$0")" && pwd)"
ROOT="$(pwd)"

if [ ! -x ./tailscale-drop ] || [ main.go -nt tailscale-drop ] || [ server.go -nt tailscale-drop ] || [ taildrop.go -nt tailscale-drop ] || [ ops.go -nt tailscale-drop ] || [ web/index.html -nt tailscale-drop ]; then
  echo "building tailscale-drop…"
  nix develop --command go build -o tailscale-drop .
fi

if [ "$1" = "--run" ]; then
  exec nix develop --command electron ./electron
fi

# Install the wrapper + service into the user's systemd session.
DEST="$HOME/.local/share/tailscale-drop"
mkdir -p "$DEST"

# Stop the service while updating so we can replace the running binary
# (cp over an executing file fails with ETXTBSY).
systemctl --user stop tailscale-drop.service 2>/dev/null || true

# Copy via temp + rename: safe even if the old binary is still executing.
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
systemctl --user enable --now tailscale-drop.service
echo "installed: systemctl --user status tailscale-drop"
