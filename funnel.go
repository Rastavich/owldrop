package main

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Funnel integration: the app shows the machine's public MagicDNS URL and
// can start/stop `tailscale funnel` itself, so no manual scripting is
// needed to expose drop links to the public internet.

// funnelPublicURL is https://<machine>.<tailnet>.ts.net/ — the public base
// under which drop links are reachable while Funnel is enabled.
func (s *server) funnelPublicURL() string {
	dns := s.selfDNSName()
	if dns == "" {
		return ""
	}
	return "https://" + dns + "/"
}

// funnelEnabled reports whether a Funnel (or serve) config is currently
// active. `tailscale funnel status --json` prints {} when nothing is on.
func (s *server) funnelEnabled() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "funnel", "status", "--json").Output()
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(out))) > 2, nil
}

// setFunnel starts or stops public exposure of the app via Tailscale Funnel.
func (s *server) setFunnel(on bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if on {
		cmd = exec.CommandContext(ctx, "tailscale", "funnel", "--bg", "http://127.0.0.1:8976")
	} else {
		// funnel reset clears the serve/funnel config.
		cmd = exec.CommandContext(ctx, "tailscale", "funnel", "reset")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return &funnelError{msg}
	}
	return nil
}

type funnelError struct{ msg string }

func (e *funnelError) Error() string { return e.msg }

func (s *server) handleFunnel(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		enabled, err := s.funnelEnabled()
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"enabled": enabled, "url": s.funnelPublicURL()})
	case http.MethodPost:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.setFunnel(req.Enabled); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"enabled": req.Enabled, "url": s.funnelPublicURL()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
