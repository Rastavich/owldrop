package main

// Update-state plumbing shared by desktop (Wails updater) and server
// (version-poll only) builds. The Wails-dependent half lives in updater.go
// (!server); updater_server.go provides the server-build stubs.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// appVersion is the running build's version ("dev" in local builds).
var appVersion = "dev"

// defaultUpdateFeed is the manifest URL on the public install repo.
const defaultUpdateFeed = "https://raw.githubusercontent.com/Rastavich/owldrop-install/main/updates/stable.json"

var semverish = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+`)

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

// broadcastUpdate pushes an updater event onto the SSE hub.
func (s *server) broadcastUpdate(kind string, detail any) {
	s.hub.broadcast(map[string]any{"type": "update", "kind": kind, "detail": detail})
}

// writeVersionUpdateState serves the server-side version-poll state — the
// desktop handler's fallback when the Wails updater isn't running.
func (s *server) writeVersionUpdateState(w http.ResponseWriter) {
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

// versionCheckNow performs a one-off version poll and serves the result.
func (s *server) versionCheckNow(w http.ResponseWriter, r *http.Request) {
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
