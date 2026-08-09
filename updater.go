package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"runtime"
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


type updateManager struct {
       app *application.App
       mu  sync.Mutex
       st  updateState
}

type updateState struct {
       Current     string `json:"current"`
       Latest      string `json:"latest,omitempty"`
       Available   bool   `json:"available"`
       AutoInstall bool   `json:"autoInstall"` // true when self-update can replace the binary
       State       string `json:"state"`
       Error       string `json:"error,omitempty"`
}
// versionCheck holds the result of the periodic server-side version poll.
// Used when no Wails updater is present (server/Docker/Nix builds).
type versionCheck struct {
       mu        sync.Mutex
       current   string
       latest    string
       available bool
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

// --- server-side version poll (all builds, no Wails needed) -------------------

// ghRelease is the subset of the GitHub releases API we care about.
type ghRelease struct {
	TagName string `json:"tag_name"`
}

// queryLatestVersion hits the GitHub releases API and returns the latest
// version string (without the "v" prefix), or an error.
func queryLatestVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/Rastavich/owldrop-install/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "owldrop-version-check/1.0")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		return "", fmt.Errorf("github api returned %d", res.StatusCode)
	}

	var rel ghRelease
	if err := json.NewDecoder(res.Body).Decode(&rel); err != nil {
		return "", err
	}
	return strings.TrimPrefix(rel.TagName, "v"), nil
}

// startVersionCheck polls the GitHub releases API for a newer version and
// broadcasts an SSE update event when one is found. Runs on ALL builds
// (desktop and server); on desktop the Wails updater handles actual install.
func (s *server) startVersionCheck(ctx context.Context) {
	if !semverish.MatchString(appVersion) {
		return // dev build, skip
	}
	s.version = &versionCheck{current: appVersion}

	check := func() {
		latest, err := queryLatestVersion(ctx)
		if err != nil {
			log.Printf("version check: %v", err)
			return
		}
		s.version.mu.Lock()
		s.version.latest = latest
		s.version.available = latest != "" && latest != appVersion && semverCompare(latest, appVersion) > 0
		s.version.mu.Unlock()

		if s.version.available {
			log.Printf("version check: %s is available (running %s)", latest, appVersion)
			// Only broadcast when the Wails updater isn't already handling it.
			if s.update == nil {
				s.broadcastUpdate("available", nil)
			}
		}
	}

	check() // run immediately

	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			check()
		case <-ctx.Done():
			return
		}
	}
}

// semverCompare returns -1, 0, or 1 like strings.Compare for semver strings "X.Y.Z".
func semverCompare(a, b string) int {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := 0; i < 3 && i < len(pa) && i < len(pb); i++ {
		var na, nb int
		fmt.Sscanf(pa[i], "%d", &na)
		fmt.Sscanf(pb[i], "%d", &nb)
		if na < nb {
			return -1
		}
		if na > nb {
			return 1
		}
	}
	return 0
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
	if s.version != nil {
		s.version.mu.Lock()
		st := updateState{
			Current:   s.version.current,
			Latest:    s.version.latest,
			Available: s.version.available,
			State:     "idle",
		}
		if st.Available {
			st.State = "available"
		}
		s.version.mu.Unlock()
		writeJSON(w, st)
		return
	}
	writeJSON(w, updateState{Current: appVersion, State: "disabled"})
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
	if s.version != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		latest, err := queryLatestVersion(ctx)
		if err != nil {
			writeJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		s.version.mu.Lock()
		s.version.latest = latest
		s.version.available = latest != "" && latest != appVersion && semverCompare(latest, appVersion) > 0
		avail := s.version.available
		s.version.mu.Unlock()
		if avail {
			s.broadcastUpdate("available", nil)
		} else {
			s.broadcastUpdate("none", nil)
		}
		writeJSON(w, updateState{Current: appVersion, Latest: latest, Available: avail, State: "idle"})
		return
	}
	writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "updates not available in this build"})
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

// broadcastUpdate pushes an updater event onto the SSE hub.
func (s *server) broadcastUpdate(kind string, detail any) {
	s.hub.broadcast(map[string]any{"type": "update", "kind": kind, "detail": detail})
}
