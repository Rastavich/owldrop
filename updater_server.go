//go:build server

package main

// Server builds (Docker/Nix/`-tags server`) have no Wails desktop shell, so
// there is no self-updater: updates mean pulling a new image. The type
// exists so the server struct compiles, but s.update is never set — the
// handlers always take the version-poll path (updater_shared.go).

import (
	"net/http"
)

type updateManager struct{}

func (s *server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	s.writeVersionUpdateState(w)
}

func (s *server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.versionCheckNow(w, r)
}

func (s *server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "auto-update not available — download from https://github.com/Rastavich/owldrop-install/releases"})
}
