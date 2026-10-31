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
	"os"
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

// withPolledAvailability lets the version poll answer "is there a newer
// release?" until the desktop updater has checked the feed for itself: the
// Wails updater only runs on demand, so before that check it has no opinion.
// Once it has checked, its answer wins — otherwise a release the feed does not
// list yet would keep offering an update that the install path cannot find.
func withPolledAvailability(st, polled updateState, checked bool) updateState {
	if checked || st.Available || !polled.Available {
		return st
	}
	st.Latest = polled.Latest
	st.Available = true
	st.State = "available"
	return st
}

// versionCheck holds the result of the periodic version poll. It runs on
// every build (see startVersionCheck); server/Docker/Nix builds have no
// updater at all, desktop builds use it to notice releases between checks.
type versionCheck struct {
	mu        sync.Mutex
	current   string
	latest    string
	available bool
	enabled   bool // false when appVersion is not a release version (dev builds)
}

// newVersionCheck seeds the poll state for the running build. The server owns
// a single instance for its lifetime so handlers can read it while the poll
// goroutine writes it.
func newVersionCheck() *versionCheck {
	return &versionCheck{current: appVersion, enabled: semverish.MatchString(appVersion)}
}

// snapshot reports the poll result in the shape the UI expects.
func (v *versionCheck) snapshot() updateState {
	if v == nil || !v.enabled {
		return updateState{Current: appVersion, State: "disabled"}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	st := updateState{Current: v.current, Latest: v.latest, Available: v.available, State: "idle"}
	if st.Available {
		st.State = "available"
	}
	return st
}

// --- version poll (all builds, no Wails needed) ------------------------------

// updateFeedURL is the release manifest both the desktop updater and the
// version poll read, so they can never disagree about what "latest" is. Only
// an https override is allowed: an http:// feed would let a local network
// attacker serve a malicious manifest.
func updateFeedURL() string {
	if v := os.Getenv("OWLDROP_UPDATE_URL"); v != "" && strings.HasPrefix(v, "https://") {
		return v
	}
	return defaultUpdateFeed
}

// updateManifest is the subset of the release manifest the poll reads.
type updateManifest struct {
	Version string `json:"version"`
}

// queryLatestVersion reads the newest published version (without the "v"
// prefix) from the release manifest, or returns an error.
func queryLatestVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", updateFeedURL(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "owldrop-version-check/1.0")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		return "", fmt.Errorf("update feed returned %d", res.StatusCode)
	}

	var man updateManifest
	if err := json.NewDecoder(res.Body).Decode(&man); err != nil {
		return "", err
	}
	if man.Version == "" {
		return "", fmt.Errorf("update feed (%s) has no version", updateFeedURL())
	}
	return strings.TrimPrefix(man.Version, "v"), nil
}

// startVersionCheck polls the release manifest for a newer version and
// broadcasts an SSE update event when one is found. Runs on ALL builds
// (desktop and server); on desktop the Wails updater handles actual install.
func (s *server) startVersionCheck(ctx context.Context) {
	if s.version == nil || !s.version.enabled {
		return // dev build: nothing to compare against
	}

	check := func() {
		latest, err := queryLatestVersion(ctx)
		if err != nil {
			log.Printf("version check: %v", err)
			return
		}
		s.version.mu.Lock()
		wasAvailable := s.version.available
		s.version.latest = latest
		s.version.available = latest != "" && latest != appVersion && semverCompare(latest, appVersion) > 0
		available := s.version.available
		s.version.mu.Unlock()

		// Only a new finding is worth an event: the poll re-runs every six
		// hours, and repeating it would nag everyone still on the old version.
		if available && !wasAvailable {
			log.Printf("version check: %s is available (running %s)", latest, appVersion)
			s.broadcastUpdate("available", nil)
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

// writeVersionUpdateState serves the version-poll state — the only answer on
// server builds, and the desktop fallback when the Wails updater is off.
func (s *server) writeVersionUpdateState(w http.ResponseWriter) {
	writeJSON(w, s.version.snapshot())
}

// versionCheckNow performs a one-off version poll and serves the result.
func (s *server) versionCheckNow(w http.ResponseWriter, r *http.Request) {
	if s.version == nil || !s.version.enabled {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "updates not available in this build"})
		return
	}
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
	writeJSON(w, s.version.snapshot())
}
