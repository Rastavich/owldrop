//go:build !server

package main

// Desktop self-updater: the Wails updater replaces the binary in place and
// restarts the app. Server builds (Docker/Nix/`-tags server`) have no Wails
// at all — see updater_server.go for their stub, and updater_shared.go for
// the version-poll logic both builds share.

import (
	"context"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

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
	// Microsoft Store (MSIX) installs: the package directory is read-only and
	// the Store ships updates itself, so self-update is impossible and must
	// not be offered. Packaged desktop apps run from
	// C:\Program Files\WindowsApps\<PackageFullName>\.
	if runtime.GOOS == "windows" {
		if exe, err := os.Executable(); err == nil && strings.Contains(exe, `\WindowsApps\`) {
			log.Printf("updater: skipped (MSIX/Store install — updates are Store-managed)")
			return
		}
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
	if s.update != nil {
		st := s.update.state()
		st.AutoInstall = true
		writeJSON(w, st)
		return
	}
	// No Wails updater — return the server-side version poll result.
	s.writeVersionUpdateState(w)
}

func (s *server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.update != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.update.check(ctx); err != nil {
			writeJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, s.update.state())
		return
	}
	// No Wails updater — do a one-off version poll instead.
	s.versionCheckNow(w, r)
}

func (s *server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.update == nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "auto-update not available — download from https://github.com/Rastavich/owldrop-install/releases"})
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
