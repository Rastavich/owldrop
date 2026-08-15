package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPeerTransport(t *testing.T) {
	cases := []struct {
		curAddr, relay string
		want           string
	}{
		{"172.19.0.2:41641", "", "direct"},
		{"100.112.233.9:1234", "", "direct"},
		{"", "syd", "syd"},
		{"", "lhr", "lhr"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := peerTransport(c.curAddr, c.relay); got != c.want {
			t.Errorf("peerTransport(%q, %q) = %q, want %q", c.curAddr, c.relay, got, c.want)
		}
	}
}

// TestAutosaveTargetRuleWins: a per-link rule forces auto-save to its folder
// even when global auto-save is off.
func TestAutosaveTargetRuleWins(t *testing.T) {
	cfgDir := t.TempDir()
	ruleDir := t.TempDir()
	s := newServerDir(&config{SaveDir: t.TempDir(), AutoSave: false}, cfgDir)
	l := s.drops.create("family", time.Hour, 0, 0)
	if err := s.drops.setAutoSaveDir(l.Token, ruleDir); err != nil {
		t.Fatal(err)
	}

	// Link file with token -> forced rule dir.
	dir, force := s.autosaveTarget(waitingFile{Source: "link", LinkToken: l.Token})
	if !force || dir != ruleDir {
		t.Fatalf("rule target: force=%v dir=%q, want true %q", force, dir, ruleDir)
	}
	// Link file without a rule -> default dir, not forced.
	dir, force = s.autosaveTarget(waitingFile{Source: "link", LinkToken: "nope"})
	if force || dir != s.saveDir() {
		t.Fatalf("no-rule target: force=%v dir=%q", force, dir)
	}
	// Daemon file -> default dir, not forced.
	dir, force = s.autosaveTarget(waitingFile{Name: "x"})
	if force || dir != s.saveDir() {
		t.Fatalf("daemon target: force=%v dir=%q", force, dir)
	}
}

// TestDropLinkAutosaveEndpoint: POST /api/droplinks/<token>/autosave sets,
// validates and clears the rule; rules survive a reload.
func TestDropLinkAutosaveEndpoint(t *testing.T) {
	cfg := &config{SaveDir: t.TempDir()}
	dataDir := t.TempDir()
	s := newServerDir(cfg, dataDir)
	s.port = 8976
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	l := s.drops.create("family", time.Hour, 0, 0)

	post := func(token, dir string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/droplinks/"+token+"/autosave", strings.NewReader(`{"dir":"`+dir+`"}`))
		req.Header.Set("X-Owldrop-Token", s.token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res
	}

	dir := t.TempDir()
	if res := post(l.Token, dir); res.StatusCode != http.StatusOK {
		t.Fatalf("set rule: %d", res.StatusCode)
	}
	if got := s.drops.get(l.Token).AutoSaveDir; got != dir {
		t.Fatalf("rule not persisted: %q", got)
	}
	// Nonexistent dir is rejected.
	if res := post(l.Token, filepath.Join(dir, "missing")); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad dir accepted: %d", res.StatusCode)
	}
	// Survives reload.
	s2 := newServerDir(cfg, dataDir)
	if got := s2.drops.get(l.Token).AutoSaveDir; got != dir {
		t.Fatalf("rule lost on reload: %q", got)
	}
	// Clear.
	if res := post(l.Token, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("clear rule: %d", res.StatusCode)
	}
	if got := s.drops.get(l.Token).AutoSaveDir; got != "" {
		t.Fatalf("rule not cleared: %q", got)
	}
}

// TestWaitingFileLinkToken flows through the inbox API.
func TestWaitingFileLinkToken(t *testing.T) {
	cfgDir := t.TempDir()
	s := newServerDir(&config{}, cfgDir)
	l := s.drops.create("family", time.Hour, 0, 0)
	src := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(src, []byte("hi"), 0o644)
	f, _ := os.Open(src)
	defer f.Close()
	s.drops.storeFile(l.Token, "a.txt", 2, f, func(string) bool { return false })

	files := s.drops.linkInbox()
	if len(files) != 1 || files[0].LinkToken != l.Token || files[0].Sender != "family" {
		t.Fatalf("link inbox item wrong: %+v", files)
	}
	b, _ := json.Marshal(files)
	if !strings.Contains(string(b), `"linkToken":"`+l.Token+`"`) {
		t.Fatalf("linkToken not on the wire: %s", b)
	}
}
