package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestSessionTokenPinnedByConfig guards the Feature-1 contract: the session
// token comes from the persisted config (not a per-run mint), so an image
// update with the same config volume keeps the already-open tab's token
// valid and mutations stop 403ing.
func TestSessionTokenPinnedByConfig(t *testing.T) {
	if got := sessionToken(""); got == "" {
		t.Fatal("sessionToken(\"\") must mint a token")
	}
	if got, want := sessionToken("pinned-x"), "pinned-x"; got != want {
		t.Fatalf("sessionToken pinned = %q, want %q", got, want)
	}
	s := newServerDir(&config{Token: "persisted-tok"}, t.TempDir())
	if s.token != "persisted-tok" {
		t.Fatalf("server token = %q, want persisted-tok", s.token)
	}
}

func TestNormalizeDomains(t *testing.T) {
	got := normalizeDomains([]string{" Drop.Example.COM ", "*.nas.local", "", "bad/path", "x.example.com", "drop.example.com", "drop.example.com"})
	want := []string{"drop.example.com", "nas.local", "x.example.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("normalizeDomains = %v, want %v", got, want)
	}
}

// TestTrustedDomainMatching verifies the label-boundary matching: a trusted
// domain covers itself and subdomains only — never a host that merely ends
// in the same labels (DNS-rebinding defense).
func TestTrustedDomainMatching(t *testing.T) {
	s := newServerDir(&config{TrustedDomains: []string{"drop.example.com", "*.nas.local"}}, t.TempDir())
	cases := []struct {
		host string
		want bool
	}{
		{"drop.example.com", true},
		{"sub.drop.example.com", true},
		{"example.com", false},
		{"evilowldrop.com", false},
		{"drop.example.com.evil.org", false},
		{"a.nas.local", true}, // "*." prefix is tolerated and means subdomains
		{"nas.local", true},
		{"b.nas.local.evil", false},
	}
	for _, c := range cases {
		if got := s.trustedDomain(c.host); got != c.want {
			t.Errorf("trustedDomain(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// TestHostAllowedTrustedDomain exercises the full host check (port split
// included) with LAN mode off, so only loopback and trusted domains pass.
func TestHostAllowedTrustedDomain(t *testing.T) {
	s := newServerDir(&config{TrustedDomains: []string{"drop.example.com"}}, t.TempDir())
	if !s.hostAllowed("drop.example.com") {
		t.Error("exact trusted host must pass")
	}
	if !s.hostAllowed("drop.example.com:8976") {
		t.Error("trusted host with port must pass")
	}
	if !s.hostAllowed("sub.drop.example.com") {
		t.Error("trusted subdomain must pass")
	}
	if s.hostAllowed("drop.example.com.evil.org") {
		t.Error("host ending in the trusted labels inside a longer name must not pass")
	}
	if s.hostAllowed("100.64.0.1") {
		t.Error("LAN off: a bare tailnet IP must not pass without a trusted domain")
	}
}

func TestFilterHiddenDevices(t *testing.T) {
	devs := []device{
		{ID: "a", Name: "alpha"},
		{ID: "b", Name: "beta"},
		{ID: "c", Name: "gamma"},
	}
	got := filterHidden(devs, map[string]bool{"b": true})
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "gamma" {
		t.Fatalf("filter result = %+v", got)
	}
	if len(filterHidden(devs, nil)) != 3 {
		t.Fatal("nil hidden set must not filter")
	}
}

// TestDeviceHiddenEndpointPersists exercises the toggle endpoint and asserts
// the hidden set lands in config.json — the survival-across-update property.
func TestDeviceHiddenEndpointPersists(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // keep config.json writes out of the real home
	cfg := &config{SaveDir: t.TempDir()}
	s := newServerDir(cfg, t.TempDir())
	s.port = 8976
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)

	toggle := func(id string, hidden bool) {
		t.Helper()
		body := `{"id":"` + id + `","hidden":false}`
		if hidden {
			body = `{"id":"` + id + `","hidden":true}`
		}
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/devices/hidden", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Owldrop-Token", s.token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("toggle %q hidden=%v: status %d", id, hidden, res.StatusCode)
		}
	}

	toggle("node-alpha", true)
	toggle("node-beta", true)
	if !cfg.HiddenDevices["node-alpha"] || !cfg.HiddenDevices["node-beta"] {
		t.Fatalf("in-memory hidden set = %v", cfg.HiddenDevices)
	}
	toggle("node-alpha", false)
	if cfg.HiddenDevices["node-alpha"] {
		t.Fatal("unhide must remove the entry")
	}

	// Persisted to config.json, so it survives a restart/update.
	b, err := os.ReadFile(configPath())
	if err != nil {
		t.Fatalf("config.json not written: %v", err)
	}
	var onDisk struct {
		Hidden map[string]bool `json:"hidden_devices"`
	}
	if err := json.Unmarshal(b, &onDisk); err != nil {
		t.Fatalf("parse config.json: %v", err)
	}
	if !onDisk.Hidden["node-beta"] || onDisk.Hidden["node-alpha"] {
		t.Fatalf("on-disk hidden set = %v", onDisk.Hidden)
	}
}
