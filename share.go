package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tailscale.com/tailcfg"
)

// pendingShare is a file the OS handed the app (share sheet / Open With /
// `owldrop file.pdf`). It sits until the Send tab picks a device.
type pendingShare struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	path string
}

func (s *server) enqueueShare(paths []string) {
	var added []pendingShare
	for _, p := range fileArgs(paths) {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		added = append(added, pendingShare{
			ID:   newSyncID(),
			Name: filepath.Base(abs),
			Size: st.Size(),
			path: abs,
		})
	}
	if len(added) == 0 {
		return
	}
	s.shareMu.Lock()
	s.shares = append(s.shares, added...)
	s.shareMu.Unlock()
	s.hub.broadcast(map[string]any{"type": "share", "n": len(s.pendingShares())})
}

func (s *server) pendingShares() []pendingShare {
	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	out := make([]pendingShare, len(s.shares))
	copy(out, s.shares)
	return out
}

func (s *server) takeShare(id string) (pendingShare, bool) {
	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	for i, it := range s.shares {
		if it.ID == id {
			s.shares = append(s.shares[:i], s.shares[i+1:]...)
			return it, true
		}
	}
	return pendingShare{}, false
}

func (s *server) handleSharePending(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"files": s.pendingShares()})
	case http.MethodDelete:
		s.shareMu.Lock()
		s.shares = nil
		s.shareMu.Unlock()
		s.hub.broadcast(map[string]any{"type": "share", "n": 0})
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleShareSend POSTs {id, peer} to send a pending shared file through the
// existing send path, then drops it from the queue.
func (s *server) handleShareSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID    string   `json:"id"`
		Peer  string   `json:"peer"`
		Peers []string `json:"peers"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "id and peer required"})
		return
	}
	peers := append([]string{}, req.Peers...)
	if req.Peer != "" {
		peers = append([]string{req.Peer}, peers...)
	}
	if len(peers) == 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "id and peer required"})
		return
	}
	it, ok := s.takeShare(req.ID)
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"error": "no such shared file"})
		return
	}
	f, err := os.Open(it.path)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	for i, p := range peers {
		if _, err := f.Seek(0, 0); err != nil {
			s.shareMu.Lock()
			s.shares = append(s.shares, it)
			s.shareMu.Unlock()
			writeJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		peer := tailcfg.StableNodeID(p)
		if err := s.sendOne(ctx, "share-"+it.ID+"-"+strconv.Itoa(i), peer, it.Name, it.Size, f, nil); err != nil {
			s.shareMu.Lock()
			s.shares = append(s.shares, it)
			s.shareMu.Unlock()
			writeJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
	}
	s.hub.broadcast(map[string]any{"type": "share", "n": len(s.pendingShares())})
	writeJSON(w, map[string]any{"ok": true})
}

func fileArgs(args []string) []string {
	var out []string
	for _, a := range args {
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		out = append(out, a)
	}
	return out
}
