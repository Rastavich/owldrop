package main

import (
	"context"
	"net/http"
	"time"
)

// Funnel integration: public (internet-visible) drop links via Tailscale
// Funnel. Managed through the serve-config LocalAPI (serving.go), the same
// config surface `tailscale funnel` uses — so CLI-configured funnels and the
// in-app toggle can never disagree. Funnel is a superset of Serve: when
// enabled, https://<machine>.<tailnet>.ts.net/ terminates TLS for the app
// and the guard restricts it to /drop/* (public pages only); when only
// Serve is on, the full app stays tailnet-only.

// funnelPublicURL is https://<machine>.<tailnet>.ts.net/ — the public base
// under which drop links are reachable while Funnel is enabled.
func (s *server) funnelPublicURL() string {
	dns := s.selfDNSName()
	if dns == "" {
		return ""
	}
	return "https://" + dns + "/"
}

// funnelEnabled reports whether Funnel is active for this app.
func (s *server) funnelEnabled() bool {
	hp := s.hostPort()
	if hp == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := s.serving.get(ctx)
	return cfg != nil && cfg.AllowFunnel[hp]
}

func (s *server) handleFunnel(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"enabled": s.funnelEnabled(), "url": s.funnelPublicURL()})
	case http.MethodPost:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.setFunnel(ctx, req.Enabled); err != nil {
			writeErr(w, err)
			return
		}
		s.serving.invalidate()
		writeJSON(w, map[string]any{"enabled": req.Enabled, "url": s.funnelPublicURL()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
