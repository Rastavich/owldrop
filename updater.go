//go:build !server

package main

// Desktop self-updater: the Wails updater replaces the binary in place and
// restarts the app. Server builds (Docker/Nix/`-tags server`) have no Wails
// at all — see updater_server.go for their stub, and updater_shared.go for
// the version-poll logic both builds share.

import (
	"context"
	"errors"
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

// errUpdateNotFound is returned when an install was asked for but the feed has
// nothing newer than the running build.
var errUpdateNotFound = errors.New("no update available — this build is already the latest")

type updateManager struct {
	app     *application.App
	mu      sync.Mutex
	st      updateState
	checked bool // the Wails updater has given a definitive answer this session
}

// selfUpdateSupported reports whether the release feed carries an artifact this
// platform can install over itself. The updater swaps the executable on disk
// and relaunches it, which only holds for the Windows and macOS builds — Linux
// installs come from a package manager or the read-only nix store, and the feed
// has no linux/amd64 artifact to resolve.
func selfUpdateSupported() bool {
	return runtime.GOOS == "windows" || runtime.GOOS == "darwin"
}

// initUpdater configures the Wails updater and routes its events onto the
// SSE hub so the UI can toast check/download results.
func (s *server) initUpdater(app *application.App) {
	if !semverish.MatchString(appVersion) {
		log.Printf("updater: skipped (appVersion %q, dev build)", appVersion)
		return
	}
	if !selfUpdateSupported() {
		log.Printf("updater: skipped (no in-place update artifact on %s — the version poll and download page handle it)", runtime.GOOS)
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
	feed := updateFeedURL()
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
	// Download completed but is not installed yet: verification and the binary
	// swap happen between now and EventUpdateReady, so report them separately
	// rather than claiming success early.
	app.Event.On(updater.EventUpdateReady, func(e *application.CustomEvent) {
		s.update.set(updateState{Current: appVersion, Latest: s.update.latest(), State: "installed"})
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

func (m *updateManager) markChecked() {
	m.mu.Lock()
	m.checked = true
	m.mu.Unlock()
}

// hasChecked reports whether the Wails updater has answered "is there a newer
// release?" itself. Until it has, the version poll is the only source.
func (m *updateManager) hasChecked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checked
}

// check runs a manual update check against the feed.
func (m *updateManager) check(ctx context.Context) error {
	m.set(updateState{Current: appVersion, State: "checking"})
	rel, err := m.app.Updater.Check(ctx)
	m.markChecked()
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

// install downloads the pending release and restarts into it. It checks first:
// an install started from the update popup has had no manual check yet, and
// DownloadAndInstall only installs what Check resolved.
func (m *updateManager) install(ctx context.Context) error {
	if err := m.check(ctx); err != nil {
		return err
	}
	if !m.state().Available {
		return errUpdateNotFound
	}
	if err := m.app.Updater.DownloadAndInstall(ctx); err != nil {
		return err
	}
	return m.app.Updater.Restart(ctx)
}

// --- HTTP handlers ----------------------------------------------------------

func (s *server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		// No Wails updater — the version poll is the whole answer.
		s.writeVersionUpdateState(w)
		return
	}
	st := withPolledAvailability(s.update.state(), s.version.snapshot(), s.update.hasChecked())
	st.AutoInstall = true
	writeJSON(w, st)
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
		// No in-place updater on this build — Linux packages, Docker/Nix installs,
		// MSIX/Store builds — so point at the download page.
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "auto-update not available — download from https://owldrop.app/#downloads"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.update.install(ctx); err != nil {
		if errors.Is(err, errUpdateNotFound) {
			writeJSONStatus(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
