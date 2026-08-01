package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tailscale.com/tailcfg"
)

// --- event hub (Server-Sent Events) --------------------------------------

type hub struct {
	mu          sync.Mutex
	clients     map[chan []byte]struct{}
	lastInbox   []byte // replayed to late-joining pages
	lastDevices []byte
}

func newHub() *hub {
	return &hub{clients: map[chan []byte]struct{}{}}
}

func (h *hub) subscribe() chan []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := make(chan []byte, 64)
	h.clients[c] = struct{}{}
	return c
}

func (h *hub) unsubscribe(c chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
}

func (h *hub) broadcast(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch v.(type) {
	case inboxEvent:
		h.lastInbox = b
	case devicesEvent:
		h.lastDevices = b
	}
	for c := range h.clients {
		select {
		case c <- b:
		default: // drop for slow consumers
		}
	}
}

type inboxEvent struct {
	Type  string        `json:"type"`
	Files []waitingFile `json:"files"`
}

type devicesEvent struct {
	Type    string   `json:"type"`
	Devices []device `json:"devices"`
}

type saveEvent struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Written int64  `json:"written"`
	Size    int64  `json:"size"`
	Done    bool   `json:"done,omitempty"`
	Path    string `json:"path,omitempty"`
	Err     string `json:"err,omitempty"`
}

type sendEvent struct {
	Type string               `json:"type"`
	ID   string               `json:"id"`
	Peer tailcfg.StableNodeID `json:"peer"`
	Name string               `json:"name"`
	Sent int64                `json:"sent"`
	Size int64                `json:"size"`
	Done bool                 `json:"done,omitempty"`
	Err  string               `json:"err,omitempty"`
}

type statusEvent struct {
	Type string `json:"type"`
	Err  string `json:"err,omitempty"`
}

// --- server ---------------------------------------------------------------

type server struct {
	cfg       *config
	token     string
	hub       *hub
	refreshCh chan struct{} // ping the inbox watcher to re-poll now
}

func newServer(cfg *config) *server {
	return &server{
		cfg:       cfg,
		token:     newToken(),
		hub:       newHub(),
		refreshCh: make(chan struct{}, 1),
	}
}

func (s *server) refresh() {
	select {
	case s.refreshCh <- struct{}{}:
	default:
	}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/events", s.guard(false, s.handleEvents))
	mux.HandleFunc("/api/inbox", s.guard(false, s.handleInbox))
	mux.HandleFunc("/api/devices", s.guard(false, s.handleDevices))
	mux.HandleFunc("/api/config", s.guard(true, s.handleConfig))
	mux.HandleFunc("/api/save", s.guard(true, s.handleSave))
	mux.HandleFunc("/api/delete", s.guard(true, s.handleDelete))
	mux.HandleFunc("/api/send", s.guard(true, s.handleSend))
	mux.HandleFunc("/api/open", s.guard(true, s.handleOpen))
	return mux
}

// guard rejects cross-site requests (CSRF + DNS rebinding). Mutating
// endpoints additionally need the session token that was embedded in the
// served page — a malicious website can't read that page (CORS) and can't
// send the custom header (preflight).
func (s *server) guard(mutating bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !originAllowed(o) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if mutating {
			tok := r.Header.Get("X-Taildrop-Token")
			if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func hostAllowed(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	switch h {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return false
}

func originAllowed(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return hostAllowed(u.Host)
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	cfgJSON, _ := json.Marshal(struct {
		Token   string `json:"token"`
		SaveDir string `json:"saveDir"`
	}{s.token, s.cfg.SaveDir})
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "embedded UI missing", http.StatusInternalServerError)
		return
	}
	html := strings.Replace(string(b), "__CONFIG__", string(cfgJSON), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.WriteString(w, html)
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fl.Flush()

	c := s.hub.subscribe()
	defer s.hub.unsubscribe(c)

	// Replay the latest known state so a newly opened page is immediately
	// up to date.
	s.hub.mu.Lock()
	var replay [][]byte
	if len(s.hub.lastInbox) > 0 {
		replay = append(replay, s.hub.lastInbox)
	}
	if len(s.hub.lastDevices) > 0 {
		replay = append(replay, s.hub.lastDevices)
	}
	s.hub.mu.Unlock()
	for _, b := range replay {
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	fl.Flush()

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case b := <-c:
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *server) handleInbox(w http.ResponseWriter, r *http.Request) {
	files, err := tsInbox(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"files": files})
}

func (s *server) handleDevices(w http.ResponseWriter, r *http.Request) {
	devs, err := tsDevices(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.broadcast(devicesEvent{Type: "devices", Devices: devs})
	writeJSON(w, map[string]any{"devices": devs})
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"saveDir": s.cfg.SaveDir})
	case http.MethodPost:
		var req struct {
			SaveDir string `json:"saveDir"`
		}
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dir := req.SaveDir
		if dir == "" {
			dir = defaultDownloadsDir()
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			http.Error(w, "not a directory: "+abs, http.StatusBadRequest)
			return
		}
		s.cfg.SaveDir = abs
		if err := s.cfg.save(); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"saveDir": abs})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Dir  string `json:"dir"`
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !validBaseName(req.Name) {
		http.Error(w, "bad file name", http.StatusBadRequest)
		return
	}
	dir := req.Dir
	if dir == "" {
		dir = s.cfg.SaveDir
	}

	var lastEmit time.Time
	var size int64
	emit := func(written int64, done bool, path, errMsg string) {
		now := time.Now()
		if !done && now.Sub(lastEmit) < 150*time.Millisecond {
			return
		}
		lastEmit = now
		s.hub.broadcast(saveEvent{Type: "save", Name: req.Name, Written: written, Size: size, Done: done, Path: path, Err: errMsg})
	}

	path, size, err := tsSaveFile(r.Context(), req.Name, dir, func(written, sz int64) {
		size = sz
		emit(written, false, "", "")
	})
	if err != nil {
		emit(0, true, "", err.Error())
		writeErr(w, err)
		return
	}
	emit(0, true, path, "")
	s.refresh()
	writeJSON(w, map[string]any{"path": path})
}

func (s *server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !validBaseName(req.Name) {
		http.Error(w, "bad file name", http.StatusBadRequest)
		return
	}
	if err := tsDeleteFile(r.Context(), req.Name); err != nil {
		writeErr(w, err)
		return
	}
	s.refresh()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	peer := tailcfg.StableNodeID(r.URL.Query().Get("peer"))
	name := r.URL.Query().Get("name")
	if id == "" || peer == "" || name == "" {
		http.Error(w, "id, peer and name required", http.StatusBadRequest)
		return
	}
	if !validBaseName(name) {
		http.Error(w, "bad file name", http.StatusBadRequest)
		return
	}

	size := r.ContentLength
	cr := &countingReader{r: r.Body}
	done := make(chan error, 1)
	go func() {
		done <- tsClient.PushFile(r.Context(), peer, size, name, cr)
	}()

	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	var last int64
	for {
		select {
		case err := <-done:
			ev := sendEvent{Type: "send", ID: id, Peer: peer, Name: name, Sent: cr.n.Load(), Size: size, Done: true}
			if err != nil {
				ev.Err = err.Error()
			}
			s.hub.broadcast(ev)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, map[string]any{"ok": true})
			return
		case <-ticker.C:
			if n := cr.n.Load(); n != last {
				last = n
				s.hub.broadcast(sendEvent{Type: "send", ID: id, Peer: peer, Name: name, Sent: n, Size: size})
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *server) handleOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !filepath.IsAbs(req.Path) {
		http.Error(w, "path must be absolute", http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(req.Path); err != nil {
		http.Error(w, "no such file", http.StatusBadRequest)
		return
	}
	if err := openPath(req.Path); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// watchInbox long-polls the daemon for inbox changes and pushes the result
// to all connected pages. It keeps running even if the daemon is down,
// retrying with a backoff and reporting the outage via status events.
func (s *server) watchInbox(ctx context.Context) {
	for ctx.Err() == nil {
		type res struct {
			files []waitingFile
			err   error
		}
		ch := make(chan res, 1)
		go func() {
			pollCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			files, err := tsInboxWait(pollCtx, 30*time.Second)
			ch <- res{files, err}
		}()
		select {
		case <-ctx.Done():
			return
		case <-s.refreshCh:
			// Something mutated the inbox; re-poll immediately.
		case r := <-ch:
			if r.err != nil {
				if ctx.Err() != nil {
					return
				}
				s.hub.broadcast(statusEvent{Type: "status", Err: r.err.Error()})
				select {
				case <-ctx.Done():
					return
				case <-s.refreshCh:
				case <-time.After(3 * time.Second):
				}
				continue
			}
			s.hub.broadcast(inboxEvent{Type: "inbox", Files: r.files})
		}
	}
}

// --- helpers --------------------------------------------------------------

func validBaseName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return filepath.Base(name) == name
}

func decodeJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "denied") {
		status = http.StatusForbidden
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
