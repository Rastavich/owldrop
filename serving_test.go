package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeServeStore is an in-memory serveConfigStore for tests.
type fakeServeStore struct {
	cfg  *serveConfigWire
	etag string
	err  error
}

func (f *fakeServeStore) getServeConfig(context.Context) (*serveConfigWire, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	cp := *f.cfg
	return &cp, f.etag, nil
}

func (f *fakeServeStore) putServeConfig(_ context.Context, cfg *serveConfigWire, etag string) error {
	if f.err != nil {
		return f.err
	}
	if etag != f.etag {
		return errors.New("etag mismatch (concurrent change)")
	}
	cp := *cfg
	f.cfg = &cp
	return nil
}

// TestServeConfigWireShape locks the JSON shape expected by the live daemon
// (ipn.ServeConfig on v1.102.2): https root proxy on 443, AllowFunnel set.
func TestServeConfigWireShape(t *testing.T) {
	cfg := &serveConfigWire{
		TCP:         map[string]*tcpPortWire{"443": {HTTPS: true}},
		Web:         map[string]*webCfgWire{"desk.tail.ts.net:443": {Handlers: map[string]*httpHandlerWire{"/": {Proxy: "http://127.0.0.1:8976"}}}},
		AllowFunnel: map[string]bool{"desk.tail.ts.net:443": true},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var back serveConfigWire
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.TCP["443"].HTTPS {
		t.Error("TCP 443 HTTPS=true lost")
	}
	if back.Web["desk.tail.ts.net:443"].Handlers["/"].Proxy != "http://127.0.0.1:8976" {
		t.Error("root proxy lost")
	}
	if !back.AllowFunnel["desk.tail.ts.net:443"] {
		t.Error("AllowFunnel lost")
	}
}

// TestBuildAppConfigConflict refuses to clobber another service's root.
func TestBuildAppConfigConflict(t *testing.T) {
	other := &serveConfigWire{
		Web: map[string]*webCfgWire{"desk.tail.ts.net:443": {Handlers: map[string]*httpHandlerWire{"/": {Proxy: "http://127.0.0.1:9999"}}}},
	}
	s := newServerDir(&config{LAN: true}, t.TempDir())
	s.serving = newServingManager(&fakeServeStore{cfg: other})
	s.selfDNSName() // fire Once, then pin (test isolation from the live daemon)
	s.selfDNS = "desk.tail.ts.net"
	if _, err := s.buildAppConfig(context.Background(), false); err == nil {
		t.Fatal("clobbering another root handler should fail")
	}
}

// TestFunnelActiveAndGuard covers the guard's funnel-vs-serve distinction on
// the MagicDNS hostname.
func TestFunnelActiveAndGuard(t *testing.T) {
	s := newServerDir(&config{LAN: true}, t.TempDir())
	s.selfDNSName() // fire Once, then pin (test isolation from the live daemon)
	s.selfDNS = "desk.tail.ts.net"
	fs := &fakeServeStore{cfg: &serveConfigWire{}}
	s.serving = newServingManager(fs)

	guard := func(remote, path string) int {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://desk.tail.ts.net:8976"+path, nil)
		r.RemoteAddr = remote
		s.guard(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })(w, r)
		return w.Code
	}

	// Serve-only (no AllowFunnel): loopback traffic gets the full app.
	if code := guard("127.0.0.1:1234", "/"); code != http.StatusOK {
		t.Errorf("serve-mode / from loopback: got %d, want 200", code)
	}
	if s.funnelActive() {
		t.Error("funnelActive should be false before funnel is enabled")
	}

	// Enable funnel: restriction kicks in for loopback (proxy) peers.
	fs.cfg.AllowFunnel = map[string]bool{"desk.tail.ts.net:443": true}
	s.serving.invalidate()
	if !s.funnelActive() {
		t.Error("funnelActive should be true when AllowFunnel set")
	}
	if code := guard("127.0.0.1:1234", "/"); code != http.StatusNotFound {
		t.Errorf("funnel / from loopback: got %d, want 404", code)
	}
	if code := guard("127.0.0.1:1234", "/drop/abc"); code != http.StatusOK {
		t.Errorf("funnel /drop from loopback: got %d, want 200", code)
	}
	// Real tailnet peer still gets the full app on the DNS name.
	if code := guard("100.112.233.9:4567", "/"); code != http.StatusOK {
		t.Errorf("tailnet peer /: got %d, want 200", code)
	}
}

// TestSetServeToggles exercises the surgical enable/disable writes.
func TestSetServeToggles(t *testing.T) {
	s := newServerDir(&config{LAN: true}, t.TempDir())
	s.selfDNSName() // fire Once, then pin (test isolation from the live daemon)
	s.selfDNS = "desk.tail.ts.net"
	s.port = 8976
	s.serving = newServingManager(&fakeServeStore{cfg: &serveConfigWire{}})

	ctx := context.Background()
	if err := s.setServe(ctx, true); err != nil {
		t.Fatal(err)
	}
	enabled, url := s.serveState(ctx)
	if !enabled || url != "https://desk.tail.ts.net/" {
		t.Fatalf("serveState after enable: %v %q", enabled, url)
	}
	if s.funnelActive() {
		t.Fatal("plain serve must not activate funnel")
	}
	if err := s.setFunnel(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !s.funnelActive() {
		t.Fatal("funnel should be active after setFunnel(true)")
	}
	if err := s.setServe(ctx, false); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := s.serveState(ctx); enabled {
		t.Fatal("serve should be off after disable")
	}
}
