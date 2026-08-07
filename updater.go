// Self-update support via the Wails v3 updater.
//
// The feed is a Wails Update Manifest (schemaVersion 1) hosted on the public
// owldrop-install repo; the updater picks the artifact matching the running
// platform, verifies the sha256 digest from the manifest, downloads it, and
// swaps the running binary (Windows exe, macOS .app bundle). NixOS users
// keep using `nix profile upgrade` instead — no Linux artifact is published.
//
// The app version comes from -ldflags "-X main.appVersion=…" (injected in
// release builds); dev builds skip the updater entirely.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

// appVersion is the running build's version ("dev" in local builds).
var appVersion = "dev"

// defaultUpdateFeed is the manifest URL on the public install repo.
const defaultUpdateFeed = "https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/stable.json"

var semverish = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+`)

// updateState is the JSON served to the UI.
type updateState struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
	State     string `json:"state"` // idle | checking | available | downloading | installed | error
	Error     string `json:"error,omitempty"`
}

type updateManager struct {
	app *application.App
	mu  sync.Mutex
	st  updateState
}

// initUpdater configures the Wails updater and routes its events onto the
// SSE hub so the UI can toast check/download results.
func (s *server) initUpdater(app *application.App) {
	if !semverish.MatchString(appVersion) {
		log.Printf("updater: skipped (appVersion %q, dev build)", appVersion)
		return
	}
	feed := defaultUpdateFeed
	// Only allow an https override: an http:// feed would let a local
	// network attacker serve a malicious manifest.
	if v := os.Getenv("OWLDROP_UPDATE_URL"); v != "" && strings.HasPrefix(v, "https://") {
		feed = v
	}
	p, err := endpoint.New(endpoint.Config{URL: feed})
	if err != nil {
		log.Printf("updater: %v", err)
		return
	}
	cfg := updater.Config{
		CurrentVersion: appVersion,
		Providers:      []updater.Provider{p},
	}
	if err := app.Updater.Init(cfg); err != nil {
		log.Printf("updater: init: %v", err)
		return
	}
	s.update = &updateManager{app: app}
	s.update.set(updateState{Current: appVersion, State: "idle"})

	app.Event.On(updater.EventUpdateAvailable, func(e *application.CustomEvent) {
		if rel, ok := e.Data.(*updater.Release); ok {
			s.update.set(updateState{Current: appVersion, Latest: rel.Version, Available: true, State: "available"})
		}
		s.broadcastUpdate("available", nil)
	})
	app.Event.On(updater.EventNoUpdate, func(e *application.CustomEvent) {
		s.update.set(updateState{Current: appVersion, State: "idle"})
		s.broadcastUpdate("none", nil)
	})
	app.Event.On(updater.EventDownloadStarted, func(e *application.CustomEvent) {
		s.update.set(updateState{Current: appVersion, Latest: s.update.latest(), State: "downloading"})
		s.broadcastUpdate("downloading", nil)
	})
	app.Event.On(updater.EventDownloadComplete, func(e *application.CustomEvent) {
		s.broadcastUpdate("installed", nil)
	})
	app.Event.On(updater.EventError, func(e *application.CustomEvent) {
		msg := ""
		if ei, ok := e.Data.(*updater.ErrorInfo); ok {
			msg = ei.Message
		}
		s.update.set(updateState{Current: appVersion, Latest: s.update.latest(), State: "error", Error: msg})
		s.broadcastUpdate("error", msg)
	})
}

func (m *updateManager) set(st updateState) {
	m.mu.Lock()
	m.st = st
	m.mu.Unlock()
}

func (m *updateManager) state() updateState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

func (m *updateManager) latest() string {
	return m.state().Latest
}

// check runs a manual update check against the feed.
func (m *updateManager) check(ctx context.Context) error {
	m.set(updateState{Current: appVersion, State: "checking"})
	rel, err := m.app.Updater.Check(ctx)
	if err != nil {
		m.set(updateState{Current: appVersion, State: "error", Error: err.Error()})
		return err
	}
	if rel == nil {
		m.set(updateState{Current: appVersion, State: "idle"})
		return nil
	}
	m.set(updateState{Current: appVersion, Latest: rel.Version, Available: true, State: "available"})
	return nil
}

// install downloads the update and restarts into it.
func (m *updateManager) install(ctx context.Context) error {
	if err := m.app.Updater.DownloadAndInstall(ctx); err != nil {
		return err
	}
	return m.app.Updater.Restart(ctx)
}

// --- HTTP handlers ----------------------------------------------------------

func (s *server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		writeJSON(w, updateState{Current: appVersion, State: "disabled"})
		return
	}
	writeJSON(w, s.update.state())
}

func (s *server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.update == nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "updates not available in this build"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.update.check(ctx); err != nil {
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, s.update.state())
}

func (s *server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.update == nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "updates not available in this build"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.update.install(ctx); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// broadcastUpdate pushes an updater event onto the SSE hub.
func (s *server) broadcastUpdate(kind string, detail any) {
	s.hub.broadcast(map[string]any{"type": "update", "kind": kind, "detail": detail})
}
