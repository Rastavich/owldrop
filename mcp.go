package main

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

func mcpPublicURL(base string) string {
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/mcp"
}

func (s *server) mcpURL() string {
	lan := s.lanURLs()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	serve := ""
	if on, u := s.serveState(ctx); on {
		serve = u
	}
	return mcpPublicURL(pickPhoneAccessURL(serve, s.funnelActive(), lan))
}

func (s *server) mcpGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if s.funnelHost(r.Host) && s.funnelActive() && peerIsLoopback(r) {
			http.NotFound(w, r)
			return
		}
		s.cfgMu.Lock()
		on, want := s.cfg.McpEnabled, s.cfg.McpToken
		s.cfgMu.Unlock()
		if !on || want == "" {
			http.NotFound(w, r)
			return
		}
		got := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(got), "bearer ") {
			got = strings.TrimSpace(got[7:])
		} else {
			got = r.Header.Get("X-Owldrop-Token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
