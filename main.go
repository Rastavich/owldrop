// tailscale-drop is a small local server for Tailscale's Taildrop.
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
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

//go:embed web/dist
var webFS embed.FS

func main() {
	var (
		port    = flag.Int("port", 8976, "port for the UI (0 = pick a free port)")
		saveDir = flag.String("save-dir", "", "default folder for received files (defaults to your Downloads folder)")
		lan     = flag.Bool("lan", false, "bind to all interfaces so other tailnet devices can open the UI (token-protected)")
	)
	flag.Parse()

	cfg := loadConfig()
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

	httpSrv := &http.Server{
		Handler:           srv.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	host := "127.0.0.1"
	if cfg.LAN {
		host = "0.0.0.0"
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, *port))
	if err != nil {
		log.Fatalf("can't listen on %s:%d: %v", host, *port, err)
	}
	portNum := ln.Addr().(*net.TCPAddr).Port
	srv.setListenerPort(portNum)

	fmt.Printf("tailscale-drop UI: http://127.0.0.1:%d/\n", portNum)
	fmt.Printf("inbox saved to: %s\n", cfg.SaveDir)
	if cfg.LAN {
		for _, u := range srv.lanURLs() {
			fmt.Printf("LAN UI: %s\n", u)
		}
		fmt.Println("note: anyone on your tailnet who knows the URL can control the app")
	}

	if err := runApp(ctx, srv, httpSrv, ln); err != nil {
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
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "tailscale-drop", "config.json")
}

func loadConfig() *config {
	// Notify prefs default ON; pointers distinguish "unset" from "false" so
	// configs written before they existed keep the defaults.
	var f struct {
		SaveDir       string `json:"save_dir"`
		AutoSave      *bool  `json:"auto_save"`
		LAN           *bool  `json:"lan"`
		NotifyArrival *bool  `json:"notify_arrival"`
		NotifySave    *bool  `json:"notify_save"`
		NotifySend    *bool  `json:"notify_send"`
		NotifyError   *bool  `json:"notify_error"`
	}
	if b, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	c := &config{SaveDir: f.SaveDir, NotifyArrival: true, NotifySave: true, NotifySend: true, NotifyError: true}
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
