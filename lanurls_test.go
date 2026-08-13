package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"tailscale.com/ipn/ipnstate"
)

func TestLanURLsForSelf(t *testing.T) {
	cases := []struct {
		name string
		self *ipnstate.PeerStatus
		want []string
	}{
		{
			name: "dns and ips",
			self: &ipnstate.PeerStatus{
				DNSName:      "desktop.taila4569.ts.net.",
				TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.112.233.3")},
			},
			want: []string{
				"http://desktop.taila4569.ts.net:8976/",
				"http://100.112.233.3:8976/",
			},
		},
		{
			name: "no dns name falls back to ips",
			self: &ipnstate.PeerStatus{
				DNSName:      "",
				TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")},
			},
			want: []string{"http://100.64.0.1:8976/"},
		},
		{
			name: "no ips and no dns",
			self: &ipnstate.PeerStatus{},
			want: nil,
		},
	}
	for _, c := range cases {
		got := lanURLsForSelf(c.self, 8976)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: url[%d] = %q, want %q", c.name, i, got[i], c.want[i])
			}
		}
	}
}

func TestPeerIsLoopback(t *testing.T) {
	cases := []struct {
		remote string
		want   bool
	}{
		{"127.0.0.1:54321", true},
		{"[::1]:54321", true},
		{"100.112.233.3:4432", false}, // tailnet peer
		{"fd7a:115c:a1e0::5336:54321", false},
		{"192.168.1.20:8976", false}, // plain LAN
		{"not-an-addr", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "http://x/", nil)
		r.RemoteAddr = c.remote
		if got := peerIsLoopback(r); got != c.want {
			t.Errorf("peerIsLoopback(%q) = %v, want %v", c.remote, got, c.want)
		}
	}
}

// TestGuardMagicDNSHost distinguishes Funnel traffic (loopback peer, /drop
// only) from a tailnet peer using the MagicDNS hostname (full app).
func TestGuardMagicDNSHost(t *testing.T) {
	s := newServerDir(&config{LAN: true}, t.TempDir())
	// Fire the once (no daemon -> empty), then pin the DNS name so
	// funnelHost() matches without needing a live tailscaled.
	s.selfDNSName()
	s.selfDNS = "desktop.taila4569.ts.net"
	s.serving = newServingManager(&fakeServeStore{cfg: &serveConfigWire{
		AllowFunnel: map[string]bool{"desktop.taila4569.ts.net:443": true},
	}})

	handler := func(t *testing.T, remote, path string) int {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://desktop.taila4569.ts.net:8976"+path, nil)
		r.RemoteAddr = remote
		s.guard(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "app served")
		})(w, r)
		return w.Code
	}

	// Funnel proxy (loopback) on the MagicDNS name: only /drop/* is served.
	if code := handler(t, "127.0.0.1:1234", "/"); code != http.StatusNotFound {
		t.Errorf("funnel /: got %d, want 404", code)
	}
	if code := handler(t, "127.0.0.1:1234", "/drop/abc"); code != http.StatusOK {
		t.Errorf("funnel /drop: got %d, want 200", code)
	}
	// Tailnet peer on the MagicDNS name: full app.
	if code := handler(t, "100.112.233.9:4567", "/"); code != http.StatusOK {
		t.Errorf("tailnet peer /: got %d, want 200", code)
	}
}
