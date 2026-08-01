#!/usr/bin/env bash
# Expose ONLY the drop links publicly via Tailscale Funnel.
#
#   ./scripts/funnel.sh on      enable (prints your public URL)
#   ./scripts/funnel.sh off     disable
#   ./scripts/funnel.sh status  show current funnel state
#
# Requirements: `tailscale` on PATH, the node must be funnel-enabled in the
# admin console (https://login.tailscale.com/admin/dns → enable Funnel), and
# the app must be running (it listens on 127.0.0.1:8976).
#
# Security: the app serves only /drop/* pages on your public hostname —
# everything else (including the full UI and its session token) returns 404
# there. The drop-link URL token is the only thing protecting an upload, so
# keep links short-lived and revoke them when done.
set -euo pipefail
cd "$(dirname "$0")/.."

case "${1:-status}" in
  on)
    echo "enabling Funnel → https://<you>.<tailnet>.ts.net/ …"
    tailscale funnel --bg http://127.0.0.1:8976
    sleep 2
    HOST=$(tailscale status --self --json 2>/dev/null | grep -o '"DNSName":"[^"]*"' | head -1 | cut -d'"' -f4 | sed 's/\.$//')
    if [ -z "$HOST" ]; then
      echo "couldn't determine your hostname — check: tailscale status"
    else
      echo "public URL: https://$HOST/"
      echo "drop links live under https://$HOST/drop/<token> (create one in Settings → Drop links)"
    fi
    echo "stop anytime with: ./scripts/funnel.sh off"
    ;;
  off)
    tailscale funnel off
    echo "Funnel disabled."
    ;;
  status)
    tailscale funnel status || true
    ;;
  *)
    echo "usage: $0 [on|off|status]"
    exit 1
    ;;
esac
