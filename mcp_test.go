package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMcpURLNeverFunnelHost(t *testing.T) {
	serve := "https://desktop.taila4569.ts.net/"
	ip := "http://100.112.233.3:8976/"
	got := mcpPublicURL(pickPhoneAccessURL(serve, true, []string{
		"http://desktop.taila4569.ts.net:8976/", ip,
	}))
	if got != "http://100.112.233.3:8976/mcp" {
		t.Fatalf("got %q", got)
	}
	if strings.HasPrefix(got, "https://") && strings.Contains(got, ".ts.net/") {
		t.Fatal("Funnel hostname advertised for MCP")
	}
}

func TestMcpGuard(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: false, McpToken: "aabbcc"}, t.TempDir())
	s.port = 8976
	ok := s.mcpGuard(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	hit := func(enabled bool, token, host, remote, path string) int {
		s.cfg.McpEnabled = enabled
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "http://"+host+path, nil)
		r.Host = host
		r.RemoteAddr = remote
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		ok(w, r)
		return w.Code
	}

	if code := hit(false, "aabbcc", "100.64.0.1:8976", "100.1.2.3:9", "/mcp"); code != http.StatusNotFound {
		t.Errorf("disabled: got %d, want 404", code)
	}
	s.cfg.McpEnabled = true
	s.selfDNSName()
	s.selfDNS = "desktop.taila4569.ts.net"
	s.serving = newServingManager(&fakeServeStore{cfg: &serveConfigWire{
		AllowFunnel: map[string]bool{"desktop.taila4569.ts.net:443": true},
	}})
	if code := hit(true, "aabbcc", "desktop.taila4569.ts.net", "127.0.0.1:1", "/mcp"); code != http.StatusNotFound {
		t.Errorf("funnel /mcp: got %d, want 404", code)
	}
	s.cfg.McpEnabled = true
	if code := hit(true, "wrong", "100.64.0.1:8976", "100.1.2.3:9", "/mcp"); code != http.StatusUnauthorized {
		t.Errorf("bad token: got %d, want 401", code)
	}
	if code := hit(true, "aabbcc", "100.64.0.1:8976", "100.1.2.3:9", "/mcp"); code != http.StatusOK {
		t.Errorf("good: got %d, want 200", code)
	}
}
