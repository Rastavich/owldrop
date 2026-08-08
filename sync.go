// Sync: a shared clipboard/scratchpad across every device that can reach the
// app (localhost, LAN/tailnet). Paste text or upload a file from any client
// and every open page sees it immediately through the same SSE hub used for
// the inbox. Items persist on the host — it's one writer (the server), many
// readers (browsers anywhere on the tailnet).
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maxSyncTextLen = 64 << 10 // 64 KiB per text item
	maxSyncFileLen = 4 << 30  // 4 GiB per file, matching Taildrop's cap
	maxSyncItems   = 100      // oldest items are evicted beyond this
	syncDirName    = "sync"   // uploaded files live here
)

// syncItem is one entry on the shared board.
type syncItem struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // text | file
	Text      string    `json:"text,omitempty"`
	Name      string    `json:"name,omitempty"`
	Size      int64     `json:"size,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// syncStore persists the board as sync.json in the data dir; file payloads
// sit in <dataDir>/sync/<id>-<name>.
type syncStore struct {
	mu    sync.Mutex
	path  string
	dir   string
	items []syncItem
}

func newSyncStore(dir string) *syncStore {
	st := &syncStore{path: filepath.Join(dir, "sync.json"), dir: dir}
	st.load()
	return st
}

func (st *syncStore) load() {
	b, err := os.ReadFile(st.path)
	if err != nil {
		return
	}
	if json.Unmarshal(b, &st.items) != nil {
		st.items = nil
	}
	if len(st.items) > maxSyncItems {
		st.items = st.items[len(st.items)-maxSyncItems:]
	}
}

func (st *syncStore) saveLocked() {
	b, err := json.Marshal(st.items)
	if err != nil {
		return
	}
	if err := os.MkdirAll(st.dir, 0o755); err != nil {
		log.Printf("sync: mkdir: %v", err)
		return
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("sync: save: %v", err)
		return
	}
	os.Rename(tmp, st.path)
}

// unlinkFile removes a file item's payload, if any.
func (st *syncStore) unlinkFile(it *syncItem) {
	if it.Kind != "file" {
		return
	}
	os.Remove(filepath.Join(st.dir, syncDirName, it.ID+"-"+filepath.Base(it.Name)))
}

// evictLocked drops the oldest items beyond the cap, unlinking payloads.
func (st *syncStore) evictLocked() {
	for len(st.items) > maxSyncItems {
		st.unlinkFile(&st.items[0])
		st.items = st.items[1:]
	}
}

// list returns the board, newest first.
func (st *syncStore) list() []syncItem {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]syncItem, len(st.items))
	for i, it := range st.items {
		out[len(st.items)-1-i] = it
	}
	return out
}

func (st *syncStore) get(id string) (syncItem, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, it := range st.items {
		if it.ID == id {
			return it, true
		}
	}
	return syncItem{}, false
}

// addText appends a text item and persists.
func (st *syncStore) addText(text string) (syncItem, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return syncItem{}, errors.New("text is empty")
	}
	if len(text) > maxSyncTextLen {
		return syncItem{}, fmt.Errorf("text too long (max %d KiB)", maxSyncTextLen>>10)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	it := syncItem{ID: newSyncID(), Kind: "text", Text: text, CreatedAt: time.Now()}
	st.items = append(st.items, it)
	st.evictLocked()
	st.saveLocked()
	return it, nil
}

// addFile appends a file item for an already-stored payload.
func (st *syncStore) addFile(id, name string, size int64) syncItem {
	st.mu.Lock()
	defer st.mu.Unlock()
	it := syncItem{ID: id, Kind: "file", Name: name, Size: size, CreatedAt: time.Now()}
	st.items = append(st.items, it)
	st.evictLocked()
	st.saveLocked()
	return it
}

// remove deletes an item (and its payload) by ID.
func (st *syncStore) remove(id string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i, it := range st.items {
		if it.ID == id {
			st.unlinkFile(&st.items[i])
			st.items = append(st.items[:i], st.items[i+1:]...)
			st.saveLocked()
			return true
		}
	}
	return false
}

// clear empties the board and its payloads.
func (st *syncStore) clear() {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.items {
		st.unlinkFile(&st.items[i])
	}
	st.items = nil
	st.saveLocked()
}

func newSyncID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// syncChanged tells every connected page (any device) to refetch the board.
func (s *server) syncChanged() {
	s.hub.broadcast(map[string]any{"type": "sync"})
}

// --- HTTP handlers ---------------------------------------------------------

// handleSync serves the board: GET list, POST add text, DELETE clear.
func (s *server) handleSync(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"items": s.sync.list()})
	case http.MethodPost:
		var req struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		it, err := s.sync.addText(req.Text)
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		s.syncChanged()
		writeJSON(w, it)
	case http.MethodDelete:
		s.sync.clear()
		s.syncChanged()
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSyncItem routes the /api/sync/ subtree: file upload/download and
// per-item delete.
func (s *server) handleSyncItem(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/api/sync/")
	switch {
	case p == "file" && r.Method == http.MethodPost:
		s.handleSyncUpload(w, r)
	case strings.HasPrefix(p, "file/") && r.Method == http.MethodGet:
		s.handleSyncDownload(w, r, strings.TrimPrefix(p, "file/"))
	case strings.HasPrefix(p, "file/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(p, "file/")
		if s.sync.remove(id) {
			s.syncChanged()
		}
		writeJSON(w, map[string]any{"ok": true})
	case !strings.HasPrefix(p, "file") && r.Method == http.MethodDelete:
		if s.sync.remove(p) {
			s.syncChanged()
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSyncUpload stores a multipart upload as a sync file item.
func (s *server) handleSyncUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSyncFileLen)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "no file in multipart form"})
		return
	}
	defer f.Close()

	name := filepath.Base(hdr.Filename)
	if name == "" || name == "." {
		name = "file"
	}
	id := newSyncID()
	dest := filepath.Join(s.sync.dir, syncDirName, id+"-"+name)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": "cannot store file"})
		return
	}
	out, err := os.Create(dest)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": "cannot store file"})
		return
	}
	written, err := io.Copy(out, io.LimitReader(f, maxSyncFileLen))
	cerr := out.Close()
	if err != nil || cerr != nil {
		os.Remove(dest)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": "write failed"})
		return
	}
	if written == 0 {
		os.Remove(dest)
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "empty file"})
		return
	}
	it := s.sync.addFile(id, name, written)
	s.syncChanged()
	writeJSON(w, it)
}

// handleSyncDownload streams a synced file back to the client.
func (s *server) handleSyncDownload(w http.ResponseWriter, r *http.Request, id string) {
	it, ok := s.sync.get(id)
	if !ok || it.Kind != "file" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.sync.dir, syncDirName, it.ID+"-"+filepath.Base(it.Name))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": it.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, path)
}
