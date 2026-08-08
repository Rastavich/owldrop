package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

// --- event hub (Server-Sent Events) --------------------------------------

type hub struct {
	mu          sync.Mutex
	webClients  map[chan []byte]struct{}
	lastInbox   []byte // replayed to late-joining pages
	lastDevices []byte
}

func newHub() *hub {
	return &hub{webClients: map[chan []byte]struct{}{}}
}

func (h *hub) subscribeWeb() chan []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := make(chan []byte, 64)
	h.webClients[c] = struct{}{}
	return c
}

func (h *hub) unsubscribeWeb(c chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.webClients, c)
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
	for c := range h.webClients {
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

// distFS is the built frontend (web/dist, embedded into the binary).
var distFS fs.FS = func() fs.FS {
	d, err := fs.Sub(webFS, "web/dist")
	if err != nil {
		panic("embedded frontend missing: " + err.Error())
	}
	return d
}()

// --- server ---------------------------------------------------------------

type server struct {
	cfg         *config
	cfgMu       sync.Mutex
	token       string
	hub         *hub
	history     *history
	drops       *dropManager
	sync        *syncStore
	update      *updateManager
	port        int
	lan         bool
	selfDNS     string
	selfDNSOnce sync.Once

	autosaveMu   sync.Mutex
	autosaving   map[string]bool      // inbox files currently being auto-saved
	autosaveFail map[string]time.Time // failed auto-saves, for backoff

	// HTTP listener ownership: the shell hands the listener to serveHTTP,
	// and LAN-mode toggles rebind it without restarting the process.
	listenerMu sync.Mutex
	httpSrv    *http.Server
	listener   net.Listener
}

func newServer(cfg *config) *server {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return newServerDir(cfg, filepath.Join(dir, "owldrop"))
}

// newServerDir builds a server with an explicit data dir (tests use a temp
// dir; production uses the user config dir).
func newServerDir(cfg *config, dataDir string) *server {
	return &server{
		cfg:          cfg,
		token:        newToken(),
		hub:          newHub(),
		history:      newHistory(dataDir),
		drops:        newDropManager(dataDir),
		sync:         newSyncStore(dataDir),
		lan:          cfg.LAN,
		autosaving:   map[string]bool{},
		autosaveFail: map[string]time.Time{},
	}
}

func (s *server) dropBaseURL() string {
	if s.lan {
		if urls := s.lanURLs(); len(urls) > 0 {
			return urls[0]
		}
	}
	if s.port > 0 {
		return fmt.Sprintf("http://127.0.0.1:%d/", s.port)
	}
	return "http://127.0.0.1/"
}

// selfDNSName is this machine's MagicDNS name (for the funnel host check).
func (s *server) selfDNSName() string {
	s.selfDNSOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if st, err := tsClient.Status(ctx); err == nil && st.Self != nil {
			s.selfDNS = strings.TrimSuffix(st.Self.DNSName, ".")
		}
	})
	return s.selfDNS
}

// funnelHost reports whether the request came through the machine's own
// MagicDNS name (i.e. Tailscale Funnel). Only drop links are served there.
func (s *server) funnelHost(host string) bool {
	dns := s.selfDNSName()
	if dns == "" {
		return false
	}
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	return h == dns
}

// lanURLs returns the URLs other tailnet devices can use to open the UI.
// The MagicDNS hostname comes first: readable and stable across IP changes.
func (s *server) lanURLs() []string {
	if !s.lan || s.port == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := tsClient.Status(ctx)
	if err != nil || st.Self == nil {
		return nil
	}
	return lanURLsForSelf(st.Self, s.port)
}

// lanURLsForSelf builds the LAN URLs from the daemon's self status.
func lanURLsForSelf(self *ipnstate.PeerStatus, port int) []string {
	var urls []string
	if dns := strings.TrimSuffix(self.DNSName, "."); dns != "" {
		urls = append(urls, fmt.Sprintf("http://%s:%d/", dns, port))
	}
	for _, ip := range self.TailscaleIPs {
		urls = append(urls, fmt.Sprintf("http://%s:%d/", ip, port))
	}
	return urls
}

// peerIsLoopback reports whether the request came from this machine. Funnel
// traffic is proxied by the local tailscaled, so its peer is loopback; a
// tailnet peer connecting over LAN arrives from a non-loopback address.
func peerIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *server) setListenerPort(p int) {
	s.port = p
}

// serveHTTP serves httpSrv on ln and records both so a LAN-mode toggle can
// rebind in place. Returns once ln is closed (rebind or shutdown).
func (s *server) serveHTTP(httpSrv *http.Server, ln net.Listener) error {
	s.listenerMu.Lock()
	s.httpSrv = httpSrv
	s.listener = ln
	s.listenerMu.Unlock()
	s.setListenerPort(ln.Addr().(*net.TCPAddr).Port)
	return httpSrv.Serve(ln)
}

// rebindHTTP moves the HTTP listener to a new address (LAN mode on/off)
// without restarting the process — the Wails shell has no respawn, so the
// old os.Exit approach simply killed the app. If the new bind fails the old
// address is restored so the app keeps serving.
func (s *server) rebindHTTP(addr string) {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()
	httpSrv, old := s.httpSrv, s.listener
	if httpSrv == nil || old == nil {
		return
	}
	oldAddr := old.Addr().String()
	old.Close()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("rebind to %s failed (%v) — restoring %s", addr, err, oldAddr)
		if restored, rerr := net.Listen("tcp", oldAddr); rerr == nil {
			s.listener = restored
			s.setListenerPort(restored.Addr().(*net.TCPAddr).Port)
			go httpSrv.Serve(restored)
		}
		return
	}
	s.listener = ln
	s.setListenerPort(ln.Addr().(*net.TCPAddr).Port)
	go httpSrv.Serve(ln)
	log.Printf("listener moved to %s", addr)
}

// hostForLAN returns the bind host for the current LAN mode.
func hostForLAN(lan bool) string {
	if lan {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

// lanURLs returns the URLs other tailnet devices can use to open the UI.

func (s *server) saveDir() string {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.cfg.SaveDir
}

func (s *server) autoSave() bool {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.cfg.AutoSave
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.guard(s.handleIndex))
	// Vite build output; through the funnel hostname the guard 404s these,
	// so only /drop/* is ever public. distFS is rooted at web/dist, so the
	// request path ("assets/…") maps straight into it.
	mux.Handle("/assets/", s.guard(http.FileServer(http.FS(distFS)).ServeHTTP))
	mux.HandleFunc("/events", s.guard(s.handleEvents))
	mux.HandleFunc("/api/inbox", s.guard(s.handleInbox))
	mux.HandleFunc("/api/devices", s.guard(s.handleDevices))
	mux.HandleFunc("/api/browse", s.guard(s.handleBrowse))
	mux.HandleFunc("/api/config", s.guard(s.handleConfig))
	mux.HandleFunc("/api/mkdir", s.guard(s.handleMkdir))
	mux.HandleFunc("/api/save", s.guard(s.handleSave))
	mux.HandleFunc("/api/delete", s.guard(s.handleDelete))
	mux.HandleFunc("/api/send", s.guard(s.handleSend))
	mux.HandleFunc("/api/open", s.guard(s.handleOpen))
	mux.HandleFunc("/api/history", s.guard(s.handleHistory))
	mux.HandleFunc("/api/droplinks", s.guard(s.handleDropLinks))
	mux.HandleFunc("/api/droplinks/", s.guard(s.handleDropLinks))
	mux.HandleFunc("/api/funnel", s.guard(s.handleFunnel))
	mux.HandleFunc("/api/tailscale", s.guard(s.handleTailscale))
	mux.HandleFunc("/api/tailscale/up", s.guard(s.handleTailscaleUp))
	mux.HandleFunc("/api/tailscale/download", s.guard(s.handleTailscaleDownload))
	mux.HandleFunc("/api/ntfy/test", s.guard(s.handleNtfyTest))
	mux.HandleFunc("/api/open-external", s.guard(s.handleOpenExternal))
	mux.HandleFunc("/api/update", s.guard(s.handleUpdate))
	mux.HandleFunc("/api/update/check", s.guard(s.handleUpdateCheck))
	mux.HandleFunc("/api/update/install", s.guard(s.handleUpdateInstall))
	mux.HandleFunc("/api/sync", s.guard(s.handleSync))
	mux.HandleFunc("/api/sync/", s.guard(s.handleSyncItem))
	// Public drop-link pages: the URL token is the auth, not the session
	// token. Host checks still apply; through the funnel hostname ONLY drop
	// pages are reachable (everything else 404s there).
	mux.HandleFunc("/drop/", s.hostGuard(s.handleDropPageOrUpload))
	return mux
}

// hostGuard applies the host/origin checks without the session token.
func (s *server) hostGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *server) handleDropPageOrUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handleDropUpload(w, r)
		return
	}
	if r.Method == http.MethodGet {
		s.handleDropPage(w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *server) handleDropLinks(w http.ResponseWriter, r *http.Request) {
	// Revoke: POST /api/droplinks/<token>/revoke
	if rest := strings.TrimPrefix(r.URL.Path, "/api/droplinks/"); rest != r.URL.Path && strings.HasSuffix(rest, "/revoke") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token := strings.TrimSuffix(rest, "/revoke")
		s.drops.revoke(token)
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	switch r.Method {
	case http.MethodGet:
		links := s.drops.list()
		type row struct {
			*dropLink
			URL       string `json:"url"`
			PublicURL string `json:"publicUrl,omitempty"`
			Expired   bool   `json:"expired"`
		}
		base := s.dropBaseURL()
		pub := s.funnelPublicURL()
		rows := make([]row, 0, len(links))
		for _, l := range links {
			r := row{
				dropLink: l,
				URL:      base + "drop/" + l.Token,
				Expired:  time.Now().After(l.Expires),
			}
			if pub != "" {
				r.PublicURL = pub + "drop/" + l.Token
			}
			rows = append(rows, r)
		}
		writeJSON(w, map[string]any{"links": rows, "baseUrl": base, "publicUrl": pub})
	case http.MethodPost:
		var req struct {
			Name    string `json:"name"`
			TTLMin  int    `json:"ttlMinutes"`
			MaxUses int    `json:"maxUses"` // 0 = unlimited
		}
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.TTLMin <= 0 {
			req.TTLMin = 60
		}
		if req.TTLMin > 7*24*60 {
			req.TTLMin = 7 * 24 * 60
		}
		l := s.drops.create(req.Name, time.Duration(req.TTLMin)*time.Minute, req.MaxUses)
		resp := map[string]any{"link": l, "url": s.dropBaseURL() + "drop/" + l.Token}
		if pub := s.funnelPublicURL(); pub != "" {
			resp["publicUrl"] = pub + "drop/" + l.Token
		}
		writeJSON(w, resp)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// guard rejects cross-site requests (CSRF + DNS rebinding). Mutating
// methods additionally need the session token that was embedded in the
// served page — a malicious website can't read that page (CORS) and can't
// send the custom header (preflight). GET requests stay tokenless so the
// Electron main process can read config/devices for the tray and
// notifications.
func (s *server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !originAllowed(o, s) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// The MagicDNS hostname is shared with Tailscale Funnel (public), so
		// only the drop pages are served there for Funnel traffic — which
		// the local tailscaled proxies from loopback. A real tailnet peer
		// connecting over LAN is a non-loopback address and gets the full
		// app, exactly as it would via its tailnet IP.
		if s.funnelHost(r.Host) && peerIsLoopback(r) && !strings.HasPrefix(r.URL.Path, "/drop/") {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			tok := r.Header.Get("X-Owldrop-Token")
			if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

// hostAllowed: loopback always; this machine's own MagicDNS name (so Tailscale
// Funnel works); in LAN mode any IP literal (tailnet/LAN clients). Hostnames
// stay blocked against DNS rebinding except our own.
func (s *server) hostAllowed(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	switch h {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	if s.funnelHost(host) {
		return true
	}
	if s.lan {
		return net.ParseIP(strings.Trim(h, "[]")) != nil
	}
	return false
}

func originAllowed(origin string, s *server) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return s.hostAllowed(u.Host)
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	cfgJSON, _ := json.Marshal(struct {
		Token   string `json:"token"`
		SaveDir string `json:"saveDir"`
	}{s.token, s.saveDir()})
	b, err := webFS.ReadFile("web/dist/index.html")
	if err != nil {
		http.Error(w, "embedded UI missing", http.StatusInternalServerError)
		return
	}
	html := strings.Replace(string(b), "__OWLDROP_CONFIG__", string(cfgJSON), 1)
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

	c := s.hub.subscribeWeb()
	defer s.hub.unsubscribeWeb(c)

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
	files, err := s.combinedInbox(r.Context())
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

// handleBrowse lists the subdirectories of path (default: the configured
// save dir) for the in-UI folder picker.
func (s *server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p = s.saveDir()
	}
	if !filepath.IsAbs(p) {
		http.Error(w, "path must be absolute", http.StatusBadRequest)
		return
	}
	p = filepath.Clean(p)
	fi, err := os.Stat(p)
	if err != nil || !fi.IsDir() {
		http.Error(w, "not a directory: "+p, http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		writeErr(w, err)
		return
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	slices.SortFunc(dirs, func(a, b string) int {
		return strings.Compare(strings.ToLower(a), strings.ToLower(b))
	})
	parent := filepath.Dir(p)
	if parent == p {
		parent = "" // at the filesystem root
	}
	writeJSON(w, map[string]any{"path": p, "parent": parent, "dirs": dirs})
}

// handleMkdir creates a single new directory under path (used by the folder
// picker's "New folder" button).
func (s *server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !filepath.IsAbs(req.Path) {
		http.Error(w, "path must be absolute", http.StatusBadRequest)
		return
	}
	if !validBaseName(req.Name) {
		http.Error(w, "bad folder name", http.StatusBadRequest)
		return
	}
	if err := os.Mkdir(filepath.Join(req.Path, req.Name), 0o755); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.cfgMu.Lock()
		c := *s.cfg
		s.cfgMu.Unlock()
		writeJSON(w, s.configResponse(c))
	case http.MethodPost:
		var req struct {
			SaveDir       string  `json:"saveDir"`
			AutoSave      *bool   `json:"autoSave"`
			Lan           *bool   `json:"lan"`
			NotifyArrival *bool   `json:"notifyArrival"`
			NotifySave    *bool   `json:"notifySave"`
			NotifySend    *bool   `json:"notifySend"`
			NotifyError   *bool   `json:"notifyError"`
			Telemetry     *bool   `json:"telemetry"`
			NtfyTopic     *string `json:"ntfyTopic"`
			NtfyServer    *string `json:"ntfyServer"`
		}
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		restart := false
		s.cfgMu.Lock()
		if req.SaveDir != "" {
			abs, err := filepath.Abs(req.SaveDir)
			if err != nil {
				s.cfgMu.Unlock()
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
				s.cfgMu.Unlock()
				http.Error(w, "not a directory: "+abs, http.StatusBadRequest)
				return
			}
			s.cfg.SaveDir = abs
		}
		if req.AutoSave != nil {
			s.cfg.AutoSave = *req.AutoSave
		}
		if req.Lan != nil && *req.Lan != s.cfg.LAN {
			s.cfg.LAN = *req.Lan
			s.lan = *req.Lan
			restart = true // bind address changes; the Electron shell restarts us
		}
		if req.NotifyArrival != nil {
			s.cfg.NotifyArrival = *req.NotifyArrival
		}
		if req.NotifySave != nil {
			s.cfg.NotifySave = *req.NotifySave
		}
		if req.NotifySend != nil {
			s.cfg.NotifySend = *req.NotifySend
		}
		if req.NotifyError != nil {
			s.cfg.NotifyError = *req.NotifyError
		}
		if req.Telemetry != nil {
			s.cfg.Telemetry = *req.Telemetry
			tele.mu.Lock()
			tele.enabled = *req.Telemetry
			tele.mu.Unlock()
		}
		if req.NtfyTopic != nil {
			s.cfg.NtfyTopic = strings.TrimSpace(*req.NtfyTopic)
		}
		if req.NtfyServer != nil {
			s.cfg.NtfyServer = strings.TrimRight(strings.TrimSpace(*req.NtfyServer), "/")
		}
		c := *s.cfg
		s.cfgMu.Unlock()
		if err := s.cfg.save(); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, s.configResponse(c))
		if restart {
			// Response is on the wire; rebind the listener to the new
			// interface in place. No process restart: the Wails shell has
			// no respawn (the old Electron-era os.Exit killed the app).
			go func() {
				time.Sleep(300 * time.Millisecond)
				s.rebindHTTP(net.JoinHostPort(hostForLAN(s.lan), strconv.Itoa(s.port)))
			}()
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) configResponse(c config) map[string]any {
	resp := map[string]any{
		"saveDir":       c.SaveDir,
		"autoSave":      c.AutoSave,
		"lan":           c.LAN,
		"notifyArrival": c.NotifyArrival,
		"notifySave":    c.NotifySave,
		"notifySend":    c.NotifySend,
		"notifyError":   c.NotifyError,
		"telemetry":     c.Telemetry,
	}
	if c.LAN {
		if urls := s.lanURLs(); len(urls) > 0 {
			resp["lanUrl"] = urls[0]
			resp["lanUrls"] = urls
		}
	}
	resp["ntfyTopic"] = c.NtfyTopic
	resp["ntfyServer"] = ntfyServer(&c)
	return resp
}

func (s *server) handleSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Dir    string `json:"dir"`
		Source string `json:"source"`
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
		dir = s.saveDir()
	}
	if req.Source == "link" {
		path, err := s.linkSave(req.Name, dir)
		if err != nil {
			writeErr(w, err)
			return
		}
		s.broadcastInboxNow()
		writeJSON(w, map[string]any{"path": path})
		return
	}
	path, err := s.saveOne(r.Context(), req.Name, dir, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"path": path})
}

func (s *server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !validBaseName(req.Name) {
		http.Error(w, "bad file name", http.StatusBadRequest)
		return
	}
	if req.Source == "link" {
		lf := s.drops.file(req.Name)
		if lf == nil {
			http.Error(w, "no such file", http.StatusNotFound)
			return
		}
		os.Remove(lf.Path)
		s.drops.removeFile(req.Name)
		s.history.recordDeleted(req.Name)
		s.broadcastInboxNow()
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if err := s.deleteInboxFile(r.Context(), req.Name); err != nil {
		writeErr(w, err)
		return
	}
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
	err := s.sendOne(r.Context(), id, peer, name, size, r.Body, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// historyStats summarizes the event log for the History tab header.
type historyStats struct {
	Received      int   `json:"received"`
	ReceivedBytes int64 `json:"receivedBytes"`
	Sent          int   `json:"sent"`
	SentBytes     int64 `json:"sentBytes"`
	Failed        int   `json:"failed"`
}

func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.history.clear()
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.history.mu.Lock()
	events := append([]historyEvent(nil), s.history.events...)
	s.history.mu.Unlock()
	slices.Reverse(events) // most recent first

	var st historyStats
	seen := map[string]bool{}
	for _, e := range events {
		switch e.Kind {
		case "arrived":
			if !seen[e.ID] {
				seen[e.ID] = true
				st.Received++
				st.ReceivedBytes += e.Size
			}
		case "sent":
			st.Sent++
			st.SentBytes += e.Size
		case "send_failed":
			st.Failed++
		}
	}
	writeJSON(w, map[string]any{"events": events, "stats": st})
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
			if err == nil {
				files = append(files, s.drops.linkInbox()...)
			}
			ch <- res{files, err}
		}()
		select {
		case <-ctx.Done():
			return
		case r := <-ch:
			if r.err != nil {
				if ctx.Err() != nil {
					return
				}
				s.hub.broadcast(statusEvent{Type: "status", Err: r.err.Error()})
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			s.hub.broadcast(inboxEvent{Type: "inbox", Files: r.files})
			s.hub.broadcast(statusEvent{Type: "status"}) // daemon is reachable again
			s.history.recordArrivals(r.files)
			s.maybeAutoSave(ctx, r.files)
		}
	}
}

// maybeAutoSave saves incoming files automatically when auto-save is on.
// Failed attempts are remembered so the watcher doesn't retry in a hot loop.
func (s *server) maybeAutoSave(ctx context.Context, files []waitingFile) {
	if !s.autoSave() {
		return
	}
	dir := s.saveDir()
	for _, f := range files {
		s.autosaveMu.Lock()
		if s.autosaving[f.Name] {
			s.autosaveMu.Unlock()
			continue
		}
		if time.Since(s.autosaveFail[f.Name]) < time.Minute {
			s.autosaveMu.Unlock()
			continue
		}
		s.autosaving[f.Name] = true
		s.autosaveMu.Unlock()

		go func(f waitingFile) {
			defer func() {
				s.autosaveMu.Lock()
				delete(s.autosaving, f.Name)
				s.autosaveMu.Unlock()
			}()
			var err error
			if f.Source == "link" {
				_, err = s.linkSave(f.Name, dir)
			} else {
				_, err = s.saveOne(ctx, f.Name, dir, nil)
			}
			if err != nil {
				s.autosaveMu.Lock()
				s.autosaveFail[f.Name] = time.Now()
				s.autosaveMu.Unlock()
			}
		}(f)
	}
}

// --- Tailscale connection state ------------------------------------------

// tailscaleState is the JSON snapshot of the local tailscaled connection,
// served to the UI so it can offer to connect when the daemon is missing or
// the node is logged out / stopped.
type tailscaleState struct {
	Reachable    bool   `json:"reachable"`
	Connected    bool   `json:"connected"`
	LoggedIn     bool   `json:"loggedIn"`
	Installed    bool   `json:"installed"`
	BackendState string `json:"backendState"`
	Hint         string `json:"hint,omitempty"`
}

// tailscaleStatusInfo maps a LocalAPI Status response (or its error) to the
// state the UI needs. An unreachable daemon is the "no tailscaled" case the
// connect banner is for; every non-Running backend state gets a hint.
func tailscaleStatusInfo(st *ipnstate.Status, err error) tailscaleState {
	if err != nil || st == nil {
		s := tailscaleState{Reachable: false, Installed: tailscaleInstalled(), Hint: tailscaledHint()}
		if !s.Installed {
			s.Hint = "Tailscale isn't installed on this machine."
		}
		return s
	}
	s := tailscaleState{Reachable: true, Installed: true, BackendState: st.BackendState}
	switch st.BackendState {
	case "Running":
		s.Connected = true
		s.LoggedIn = true
	case "NeedsLogin", "NoState":
		s.Hint = "You're not logged in to a tailnet."
	case "NeedsMachineAuth":
		s.Hint = "This device is waiting for admin approval."
	case "Stopped":
		s.Hint = "Tailscale is stopped."
	case "Starting":
		s.Hint = "Connecting to your tailnet…"
	default:
		s.Hint = "Not connected to your tailnet."
	}
	return s
}

// tailscaledHintFor explains how to start the daemon, per platform.
func tailscaledHintFor(goos string) string {
	switch goos {
	case "darwin":
		return "tailscaled isn't running — open the Tailscale app, then retry."
	case "windows":
		return "tailscaled isn't running — start the Tailscale service, then retry."
	default:
		return "tailscaled isn't running — start it (e.g. `sudo systemctl start tailscaled` or the Tailscale app), then retry."
	}
}

func tailscaledHint() string { return tailscaledHintFor(runtime.GOOS) }

func (s *server) handleTailscale(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	st, err := tsClient.Status(ctx)
	writeJSON(w, tailscaleStatusInfo(st, err))
}

// handleTailscaleDownload opens the Tailscale download page in the system
// browser. Fixed URL — nothing user-controlled is ever passed to openPath.
func (s *server) handleTailscaleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := openPath("https://tailscale.com/download"); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// allowedExternalHosts are the only hosts the UI may hand to the system
// browser. The webview cannot open links by itself, so the page asks the
// server instead — same "nothing user-controlled reaches openPath" rule as
// handleTailscaleDownload, with a strict allowlist instead of a fixed URL.
var allowedExternalHosts = map[string]bool{
	"ko-fi.com":     true,
	"github.com":    true,
	"tailscale.com": true,
	"owldrop.app":   true,
}

// validateExternalURL returns url if it's https and its host is on the
// allowlist; the caller never passes anything else to openPath.
func validateExternalURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || !allowedExternalHosts[strings.ToLower(u.Hostname())] {
		return "", errors.New("bad url")
	}
	return raw, nil
}

// handleOpenExternal opens an allowlisted URL in the system browser
// (Settings → Support links and anything else the page needs to escape the
// webview for).
func (s *server) handleOpenExternal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	u, err := validateExternalURL(req.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := openPath(u); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleNtfyTest sends a test notification so the user can confirm their
// phone is subscribed before relying on it.
func (s *server) handleNtfyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.cfgMu.Lock()
	c := *s.cfg
	s.cfgMu.Unlock()
	if err := sendNtfy(r.Context(), &c, "Owldrop test", "Phone notifications are working — files sent to this phone will ping here."); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) handleTailscaleUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	st, err := tsClient.Status(ctx)
	if err != nil || st == nil {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": tailscaledHint()})
		return
	}
	if st.BackendState == "Running" {
		writeJSON(w, map[string]any{"ok": true, "already": true})
		return
	}
	// `tailscale up` brings the node up and, when logged out, starts the
	// interactive login flow (opens the browser). It can block until login
	// completes, so it runs in the background; the UI polls /api/tailscale.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := exec.CommandContext(ctx, "tailscale", "up").Run(); err != nil {
			log.Printf("tailscale up: %v", err)
		}
	}()
	writeJSON(w, map[string]any{"ok": true})
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

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
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
