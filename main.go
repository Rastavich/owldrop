// Owldrop is a small local server for Tailscale's Taildrop.
//
// It talks to the local tailscaled daemon through its LocalAPI (the same
// interface the `tailscale` CLI uses) and serves a UI on localhost: the
// bundled browser UI (web/index.html), wrapped in the Wails desktop shell
// (shell.go), or used directly from a browser (LAN mode, drop links).
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed web/dist
var webFS embed.FS

// startTsnet is replaced by tsnetmode.go in server builds (-tags server) to
// start an embedded Tailscale node; the desktop build never does.
var startTsnet = func(srv *server) (net.Listener, error) { return nil, nil }

func main() {
	var (
		port    = flag.Int("port", 8976, "port for the UI (0 = pick a free port)")
		saveDir = flag.String("save-dir", "", "default folder for received files (defaults to your Downloads folder)")
		lan     = flag.Bool("lan", false, "bind to all interfaces so other tailnet devices can open the UI (any tailnet device can then control the app — only enable on a trusted tailnet)")
	)
	flag.Parse()

	cfg := loadConfig()
	tele.init(cfg.InstallID, cfg.Telemetry)
	tele.event("heartbeat")
	if *saveDir != "" {
		cfg.SaveDir = *saveDir
		if err := cfg.save(); err != nil {
			log.Printf("saving config: %v", err)
		}
	}
	if *lan {
		cfg.LAN = true
		if err := cfg.save(); err != nil {
			log.Printf("saving config: %v", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := newServer(cfg)

       go srv.startVersionCheck(ctx)

	httpSrv := &http.Server{
		Handler:           srv.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	host := "127.0.0.1"
	if cfg.LAN {
		host = "0.0.0.0"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(*port))

	// runApp acquires the single-instance lock first, then binds the port —
	// a second instance (service + AppImage, two AppImages, ...) focuses the
	// running one instead of dying with "address already in use".
	if err := runApp(ctx, srv, httpSrv, addr); err != nil {
		log.Fatal(err)
	}
	log.Println("bye")
}

// --- config ---------------------------------------------------------------

type config struct {
	SaveDir       string `json:"save_dir"`
	AutoSave      bool   `json:"auto_save"`
	LAN           bool   `json:"lan"`
	NotifyArrival bool   `json:"notify_arrival"`
	NotifySave    bool   `json:"notify_save"`
	NotifySend    bool   `json:"notify_send"`
	NotifyError   bool   `json:"notify_error"`
	// Anonymous usage stats (Settings toggle). install_id is the anonymous
	// install identifier used for daily-active-user counting.
	Telemetry bool   `json:"telemetry"`
	InstallID string `json:"install_id"`
	// ntfy phone notifications: after a send to a phone, POST to this ntfy
	// topic so the phone gets a real push notification. Empty = off.
	NtfyTopic  string `json:"ntfy_topic"`
	NtfyServer string `json:"ntfy_server"` // empty = https://ntfy.sh
	// Session token for mutating API calls. Persisted (like install_id) so
	// the same token survives restarts *and* container rebuilds/updates —
	// a browser tab that's already open keeps working instead of 403ing its
	// mutations after an image update (the config volume is the source of
	// truth, not the process lifetime).
	Token string `json:"token"`
	// Tailnet devices hidden from the send picker / tray quick-send. They
	// can still receive files — this only removes them from where you can
	// choose a target. Keyed by the tailnet node ID (stable), not the name.
	HiddenDevices map[string]bool `json:"hidden_devices"`
	// Hostnames approved for reverse-proxy access (bedrock against DNS
	// rebinding: a bare IP in front of a proxy isn't reachable, so the
	// operator lists the hostname the proxy serves). A trusted domain and
	// all of its subdomains pass the host check.
	TrustedDomains []string `json:"trusted_domains"`
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "owldrop", "config.json")
}

// migrateConfigDir renames the pre-rebrand config dir to the new one,
// carrying config.json and history.jsonl across. Best-effort.
func migrateConfigDir() {
	dir, err := os.UserConfigDir()
	if err != nil {
		return
	}
	oldDir, newDir := filepath.Join(dir, "tailscale-drop"), filepath.Join(dir, "owldrop")
	if _, err := os.Stat(newDir); err == nil {
		return // already migrated (or a fresh install)
	}
	if _, err := os.Stat(oldDir); err != nil {
		return
	}
	if err := os.Rename(oldDir, newDir); err == nil {
		log.Printf("migrated config dir %s → %s", oldDir, newDir)
	}
}

func loadConfig() *config {
	migrateConfigDir()
	// Notify prefs default ON; pointers distinguish "unset" from "false" so
	// configs written before they existed keep the defaults.
	var f struct {
		SaveDir        string            `json:"save_dir"`
		AutoSave       *bool             `json:"auto_save"`
		LAN            *bool             `json:"lan"`
		NotifyArrival  *bool             `json:"notify_arrival"`
		NotifySave     *bool             `json:"notify_save"`
		NotifySend     *bool             `json:"notify_send"`
		NotifyError    *bool             `json:"notify_error"`
		Telemetry      *bool             `json:"telemetry"`
		InstallID      string            `json:"install_id"`
		NtfyTopic      string            `json:"ntfy_topic"`
		NtfyServer     string            `json:"ntfy_server"`
		Token          string            `json:"token"`
		HiddenDevices  map[string]bool   `json:"hidden_devices"`
		TrustedDomains []string          `json:"trusted_domains"`
	}
	if b, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	c := &config{
		SaveDir:        f.SaveDir,
		InstallID:      f.InstallID,
		NtfyTopic:      f.NtfyTopic,
		NtfyServer:     f.NtfyServer,
		Token:          f.Token,
		HiddenDevices:  f.HiddenDevices,
		TrustedDomains: normalizeDomains(f.TrustedDomains),
		NotifyArrival:  true,
		NotifySave:     true,
		NotifySend:     true,
		NotifyError:    true,
		Telemetry:      true,
	}
	if f.Telemetry != nil {
		c.Telemetry = *f.Telemetry
	}
	// First run: mint the anonymous install id and persist it.
	if c.InstallID == "" {
		c.InstallID = newInstallID()
		if c.InstallID != "" {
			if err := c.save(); err != nil {
				log.Printf("saving install id: %v", err)
			}
		}
	}
	// Session token: minted once and persisted in config.json (the config
	// volume on NAS installs), so an open UI keeps its token across restarts
	// AND container updates — unlike the old per-process mint, which made
	// every image update 403 the already-open tab's mutations.
	if c.Token == "" {
		c.Token = newToken()
		if err := c.save(); err != nil {
			log.Printf("saving session token: %v", err)
		}
	}
	if f.AutoSave != nil {
		c.AutoSave = *f.AutoSave
	}
	if f.LAN != nil {
		c.LAN = *f.LAN
	}
	if f.NotifyArrival != nil {
		c.NotifyArrival = *f.NotifyArrival
	}
	if f.NotifySave != nil {
		c.NotifySave = *f.NotifySave
	}
	if f.NotifySend != nil {
		c.NotifySend = *f.NotifySend
	}
	if f.NotifyError != nil {
		c.NotifyError = *f.NotifyError
	}
	if c.SaveDir == "" {
		c.SaveDir = defaultDownloadsDir()
	}
	return c
}

func (c *config) save() error {
	if err := os.MkdirAll(filepath.Dir(configPath()), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := configPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, configPath())
}

// --- OS helpers -----------------------------------------------------------

func defaultDownloadsDir() string {
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("USERPROFILE"); d != "" {
			return filepath.Join(d, "Downloads")
		}
	case "darwin":
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, "Downloads")
		}
	default:
		if out, err := exec.Command("xdg-user-dir", "DOWNLOAD").Output(); err == nil {
			if dir := strings.TrimSpace(string(out)); dir != "" && strings.HasPrefix(dir, "/") {
				return dir
			}
		}
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, "Downloads")
		}
	}
	return "."
}

func openPath(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("explorer", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// normalizeDomains lowercases, strips a leading "*." (a trusted domain
// always covers its subdomains) or trailing dot, drops empties and rejects
// anything that can't be a hostname (contains whitespace or a path/port
// separator). Returns a deduplicated list in input order.
func normalizeDomains(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "*.")
		s = strings.ToLower(strings.TrimSuffix(s, "."))
		if s == "" || strings.ContainsAny(s, " /\\:") {
			continue
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
