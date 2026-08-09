// Serving: Tailscale Serve (tailnet-only HTTPS) and Funnel (public) access
// to the app, managed through the daemon's serve-config LocalAPI instead of
// the CLI. Serve makes https://<machine>.<tailnet>.ts.net/ reachable with an
// automatically provisioned Let's Encrypt certificate; Funnel is the same
// config with AllowFunnel set, exposing it (via /drop/*) to the internet.
//
// The pinned tailscale client (v1.98.9) has no serve-config methods, so
// this talks to /localapi/v0/serve-config directly over the daemon socket
// (the same path `tailscale serve/funnel` use). Config shape follows the
// live daemon's ipn.ServeConfig (verified against v1.102.2 source):
//
//	{"Web":{"<dns>:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:<port>"}}}},
//	 "TCP":{"443":{"HTTPS":true}},
//	 "AllowFunnel":{"<dns>:443":true}}
package main

import (
       "bytes"
       "context"
       "encoding/json"
       "fmt"
       "io"
       "net/http"
       "strings"
       "sync"
       "time"
)

// --- raw LocalAPI serve-config client ---------------------------------------

// serveConfigWire mirrors the daemon's ipn.ServeConfig JSON (must match the
// live daemon, not the pinned module).
type serveConfigWire struct {
	TCP         map[string]*tcpPortWire  `json:"TCP,omitempty"`
	Web         map[string]*webCfgWire   `json:"Web,omitempty"`
	AllowFunnel map[string]bool          `json:"AllowFunnel,omitempty"`
	Services    map[string]any           `json:"Services,omitempty"`
	Foreground  map[string]any           `json:"Foreground,omitempty"`
}

type tcpPortWire struct {
	HTTPS bool `json:"HTTPS"`
}

type webCfgWire struct {
	Handlers map[string]*httpHandlerWire `json:"Handlers"`
}

type httpHandlerWire struct {
	Proxy  string `json:"Proxy,omitempty"`
	Path   string `json:"Path,omitempty"`
	Text   string `json:"Text,omitempty"`
}

// serveConfigStore is the daemon's serve-config endpoint; the raw client
// implements it, tests inject fakes. The etag is the daemon's optimistic
// concurrency token: writes must carry it (If-Match), or the daemon rejects
// them (405/412).
type serveConfigStore interface {
	getServeConfig(ctx context.Context) (cfg *serveConfigWire, etag string, err error)
       putServeConfig(ctx context.Context, cfg *serveConfigWire, etag string) error
}
// rawServeClient makes LocalAPI HTTP requests through the same cross-platform
// client used by the rest of the app (local.Client). No hardcoded socket paths.
type rawServeClient struct{}

func newRawServeClient() *rawServeClient {
       return &rawServeClient{}
}

func (c *rawServeClient) do(ctx context.Context, method, path string, body io.Reader, hdr http.Header) (int, http.Header, []byte, error) {
       req, err := http.NewRequestWithContext(ctx, method, "http://local-tailscaled.sock"+path, body)
       if err != nil {
               return 0, nil, nil, err
       }
       for k, vs := range hdr {
               for _, v := range vs {
                       req.Header.Add(k, v)
               }
       }
       res, err := tsClient.DoLocalRequest(req)
       if err != nil {
               return 0, nil, nil, err
       }
       defer res.Body.Close()
       b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
       return res.StatusCode, res.Header, b, nil
}

func (c *rawServeClient) getServeConfig(ctx context.Context) (*serveConfigWire, string, error) {
	code, hdr, b, err := c.do(ctx, http.MethodGet, "/localapi/v0/serve-config", nil, nil)
	if err != nil {
		return nil, "", err
	}
	if code != http.StatusOK {
		return nil, "", fmt.Errorf("serve-config: HTTP %d", code)
	}
	etag := hdr.Get("ETag")
	if len(bytes.TrimSpace(b)) == 0 {
		return &serveConfigWire{}, etag, nil
	}
	var cfg serveConfigWire
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, "", fmt.Errorf("serve-config parse: %w", err)
	}
	return &cfg, etag, nil
}

func (c *rawServeClient) putServeConfig(ctx context.Context, cfg *serveConfigWire, etag string) error {
	b, _ := json.Marshal(cfg)
	hdr := http.Header{}
	if etag != "" {
		hdr.Set("If-Match", etag)
	}
	code, _, body, err := c.do(ctx, http.MethodPost, "/localapi/v0/serve-config", bytes.NewReader(b), hdr)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("serve-config: HTTP %d: %s", code, strings.TrimSpace(string(body)))
	}
	return nil
}

// --- server-side serving manager --------------------------------------------

type servingManager struct {
	store serveConfigStore
	mu    sync.Mutex
	cache *serveConfigWire
	etag  string
	fetch time.Time
}

func newServingManager(store serveConfigStore) *servingManager {
	return &servingManager{store: store}
}

// get returns the current config, cached for 5s (the guard consults it on
// every request; a stale read only affects the funnel restriction, which
// toggles invalidate).
func (m *servingManager) get(ctx context.Context) *serveConfigWire {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cache != nil && time.Since(m.fetch) < 5*time.Second {
		return m.cache
	}
	cfg, etag, err := m.store.getServeConfig(ctx)
	if err != nil {
		return m.cache // stale cache or nil on first failure
	}
	m.cache = cfg
	m.etag = etag
	m.fetch = time.Now()
	return cfg
}

func (m *servingManager) invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache = nil
}

// set writes the config with the daemon's current etag (If-Match), so a
// concurrent change by another tool fails loudly instead of silently
// clobbering. The etag comes from the live fetch, never the cache.
func (m *servingManager) set(ctx context.Context, cfg *serveConfigWire) error {
	m.mu.Lock()
	etag, err := m.liveEtagLocked(ctx)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if err := m.store.putServeConfig(ctx, cfg, etag); err != nil {
		m.invalidate()
		return err
	}
	m.invalidate()
	return nil
}

// liveEtagLocked unconditionally refetches the daemon config and returns
// its etag, refreshing the cache in the process.
func (m *servingManager) liveEtagLocked(ctx context.Context) (string, error) {
	cfg, etag, err := m.store.getServeConfig(ctx)
	if err != nil {
		return "", err
	}
	m.cache = cfg
	m.etag = etag
	m.fetch = time.Now()
	return etag, nil
}

// hostPort is the serve/funnel config key for this machine's HTTPS listener.
func (s *server) hostPort() string {
	dns := s.selfDNSName()
	if dns == "" {
		return ""
	}
	return dns + ":443"
}

// proxyTarget is the backend URL serve forwards to (loopback only, per
// Tailscale's serve constraints).
func (s *server) proxyTarget() string {
	port := s.port
	if port == 0 {
		port = 8976
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// serveState reports whether tailnet-only HTTPS serve is active and its URL.
func (s *server) serveState(ctx context.Context) (enabled bool, url string) {
	hp := s.hostPort()
	if hp == "" {
		return false, ""
	}
	cfg := s.serving.get(ctx)
	if cfg == nil || cfg.Web[hp] == nil {
		return false, ""
	}
	root := cfg.Web[hp].Handlers["/"]
	if root == nil || root.Proxy == "" {
		return false, ""
	}
	return true, "https://" + s.selfDNSName() + "/"
}

// funnelActive reports whether public Funnel exposure is on for this app.
// Cache-safe for per-request use by the HTTP guard.
func (s *server) funnelActive() bool {
	hp := s.hostPort()
	if hp == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cfg := s.serving.get(ctx)
	return cfg != nil && cfg.AllowFunnel[hp]
}

// buildAppConfig builds the serve config for this app: HTTPS root proxy on
// 443, optionally public (AllowFunnel). Refuses to clobber an existing root
// handler that isn't ours on the same host:port.
func (s *server) buildAppConfig(ctx context.Context, funnel bool) (*serveConfigWire, error) {
	hp := s.hostPort()
	if hp == "" {
		return nil, fmt.Errorf("no MagicDNS name available — is tailscaled connected?")
	}
	cur := s.serving.get(ctx)
	if cur != nil {
		if w := cur.Web[hp]; w != nil {
			if root := w.Handlers["/"]; root != nil && root.Proxy != "" && root.Proxy != s.proxyTarget() {
				return nil, fmt.Errorf("another service is already serving %s", hp)
			}
		}
	}
	cfg := &serveConfigWire{
		TCP: map[string]*tcpPortWire{"443": {HTTPS: true}},
		Web: map[string]*webCfgWire{hp: {Handlers: map[string]*httpHandlerWire{
			"/": {Proxy: s.proxyTarget()},
		}}},
		AllowFunnel: map[string]bool{},
	}
	if funnel {
		cfg.AllowFunnel[hp] = true
	}
	return cfg, nil
}

// setServe enables or disables tailnet-only HTTPS access.
func (s *server) setServe(ctx context.Context, on bool) error {
	if !on {
		// Clear our web+tcp entry (keep AllowFunnel false so nothing leaks).
		cfg := s.serving.get(ctx)
		hp := s.hostPort()
		if cfg == nil {
			cfg = &serveConfigWire{}
		}
		delete(cfg.Web, hp)
		delete(cfg.TCP, "443")
		cfg.AllowFunnel[hp] = false
		if len(cfg.Web) == 0 && len(cfg.TCP) == 0 {
			cfg = &serveConfigWire{}
		}
		return s.serving.set(ctx, cfg)
	}
	cfg, err := s.buildAppConfig(ctx, false)
	if err != nil {
		return err
	}
	return s.serving.set(ctx, cfg)
}

// setFunnel enables or disables public exposure (superset of serve).
func (s *server) setFunnel(ctx context.Context, on bool) error {
	hp := s.hostPort()
	if on {
		cfg, err := s.buildAppConfig(ctx, true)
		if err != nil {
			return err
		}
		return s.serving.set(ctx, cfg)
	}
	cfg := s.serving.get(ctx)
	if cfg == nil {
		cfg = &serveConfigWire{}
	}
	cfg.AllowFunnel[hp] = false
	if cfg.TCP["443"] == nil && cfg.Web[hp] == nil && !keyTrue(cfg.AllowFunnel) {
		cfg = &serveConfigWire{}
	}
	return s.serving.set(ctx, cfg)
}

func keyTrue(m map[string]bool) bool {
	for _, v := range m {
		if v {
			return true
		}
	}
	return false
}

// handleServe serves GET (state) and POST ({enabled}) for tailnet-only HTTPS.
func (s *server) handleServe(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		enabled, url := s.serveState(r.Context())
		writeJSON(w, map[string]any{"enabled": enabled, "url": url})
	case http.MethodPost:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.setServe(r.Context(), req.Enabled); err != nil {
			writeErr(w, err)
			return
		}
		s.serving.invalidate()
		enabled, url := s.serveState(r.Context())
		writeJSON(w, map[string]any{"enabled": enabled, "url": url})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
