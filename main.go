// tailscale-drop is a small local server for Tailscale's Taildrop.
//
// It talks to the local tailscaled daemon through its LocalAPI (the same
// interface the `tailscale` CLI uses) and serves a UI on localhost: the
// bundled browser UI (web/index.html), typically wrapped in the Electron
// desktop shell (electron/), or used directly from a browser.
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

//go:embed web
var webFS embed.FS

func main() {
	var (
		port    = flag.Int("port", 8976, "port for the UI (0 = pick a free port)")
		saveDir = flag.String("save-dir", "", "default folder for received files (defaults to your Downloads folder)")
	)
	flag.Parse()

	cfg := loadConfig()
	if *saveDir != "" {
		cfg.SaveDir = *saveDir
		if err := cfg.save(); err != nil {
			log.Printf("saving config: %v", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := newServer(cfg)
	go srv.watchInbox(ctx)

	httpSrv := &http.Server{
		Handler:           srv.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("can't listen on %s: %v", addr, err)
	}
	if *port == 0 {
		addr = ln.Addr().String() // actual ephemeral port
	}
	fmt.Printf("tailscale-drop UI: http://%s/\n", addr)
	fmt.Printf("inbox saved to: %s\n", cfg.SaveDir)

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutCtx)
	}()
	if err := httpSrv.Serve(ln); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
	log.Println("bye")
}

// --- config ---------------------------------------------------------------

type config struct {
	SaveDir  string `json:"save_dir"`
	AutoSave bool   `json:"auto_save"`
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "tailscale-drop", "config.json")
}

func loadConfig() *config {
	c := &config{}
	if b, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(b, c)
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
