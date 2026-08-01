#!/usr/bin/env bash
# Launch tailscale-drop inside the nix dev shell so the GL/X11/Wayland
# libraries it links against resolve on NixOS.
cd "$(dirname "$0")"
exec nix develop --command ./tailscale-drop "$@"
