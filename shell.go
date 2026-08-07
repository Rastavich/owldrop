// Owldrop desktop shell.
//
// A Wails v3 app wrapping the sidecar HTTP server (main.go): it opens a
// native window pointing at the local UI, adds a system tray with quick-send,
// turns the sidecar's event stream into native notifications, and registers
// a global shortcut. Everything runs in one process — no separate sidecar
// binary and no session-token plumbing between processes: the shell talks to
// the server through its in-memory hub and functions, and the browser UI
// keeps using the HTTP API unchanged (LAN mode and funnel drop links are
// untouched).
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

//go:embed icon.png
var trayIcon []byte

// runApp starts the Wails desktop shell and blocks until it exits. The HTTP
// server keeps serving the whole time; the shell is just another client of
// it. When the app quits — or ctx is cancelled by a signal, which also quits
// the app — the server is shut down.
func runApp(ctx context.Context, srv *server, httpSrv *http.Server, addr string) error {
	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()

	go srv.watchInbox(serverCtx)

	ns := notifications.New()

	var win *application.WebviewWindow
	// Create the app first: this acquires the single-instance lock. When
	// another instance already holds it (the installed service, a second
	// AppImage, ...), wails notifies that instance — which shows and focuses
	// its window — and exits this process before we ever touch the port.
	app := application.New(application.Options{
		Name:        "Owldrop",
		Description: "Owldrop — desktop app for Tailscale file sharing",
		Services: []application.Service{
			application.NewService(ns),
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "app.owldrop",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				if win != nil {
					win.Show()
					win.Focus()
				}
			},
		},
		Mac: application.MacOptions{
			// Tray app: no Dock icon while running from the tray.
			ActivationPolicy: application.ActivationPolicyAccessory,
		},
	})

	// Now bind. With the single-instance lock held, a busy port means some
	// other program owns it, not a second owldrop.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("port %s is already in use by another program (not an owldrop instance) — stop it or pass --port", addr)
		}
		return fmt.Errorf("can't listen on %s: %w", addr, err)
	}
	portNum := ln.Addr().(*net.TCPAddr).Port
	srv.setListenerPort(portNum)
	fmt.Printf("owldrop UI: http://127.0.0.1:%d/\n", portNum)
	fmt.Printf("inbox saved to: %s\n", srv.saveDir())
	if srv.lan {
		for _, u := range srv.lanURLs() {
			fmt.Printf("LAN UI: %s\n", u)
		}
		fmt.Println("note: anyone on your tailnet who knows the URL can control the app")
	}

	go func() {
		<-serverCtx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutCtx)
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.serveHTTP(httpSrv, ln) }()

	// Self-update wiring (release builds only): checks the public feed and
	// surfaces updater events over SSE so the UI can toast.
	srv.initUpdater(app)

	win = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Owldrop",
		Width:            980,
		Height:           720,
		MinWidth:         720,
		MinHeight:        480,
		BackgroundColour: application.NewRGB(11, 14, 20),
		// Window/taskbar icon. Without this the plain `go build` used by
		// the nix package never bakes an icon in (wails3 build does it via
		// its asset pipeline); the embedded tray PNG serves both.
		Linux: application.LinuxWindow{Icon: trayIcon},
		// ?shell=1 tells the UI we are the desktop shell: native notifications
		// come from Go, so the page skips its browser-notification path.
		URL:            fmt.Sprintf("http://127.0.0.1:%d/?shell=1", srv.port),
		EnableFileDrop: true,
	})

	// Close-to-tray: the window hides; quit comes from the tray menu.
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		win.Hide()
		e.Cancel()
	})

	tray := app.SystemTray.New()
	tray.SetTooltip("Owldrop")
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(trayIcon)
	} else {
		tray.SetIcon(trayIcon)
	}
	tray.OnClick(func() {
		win.Show()
		win.Focus()
	})

	sh := &shell{app: app, win: win, tray: tray, srv: srv, notif: ns}
	sh.rebuildTrayMenu()

	if err := app.GlobalShortcut.Register("CmdOrCtrl+Shift+T", func() {
		win.Show()
		win.Focus()
	}); err != nil {
		log.Printf("global shortcut Ctrl+Shift+T: %v", err)
	}

	go sh.notifyLoop(serverCtx)
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-serverCtx.Done():
				return
			case <-t.C:
				sh.rebuildTrayMenu()
			}
		}
	}()

	// A signal (SIGINT/SIGTERM) quits the GUI; from then on the shutdown
	// path below runs.
	go func() {
		<-ctx.Done()
		app.Quit()
	}()

	err = app.Run()
	serverCancel()
	<-serveErr
	return err
}

// shell bundles the Wails objects the tray/notifications logic needs.
type shell struct {
	app   *application.App
	win   *application.WebviewWindow
	tray  *application.SystemTray
	srv   *server
	notif *notifications.NotificationService
}

// rebuildTrayMenu regenerates the tray menu with the current device list
// (called at startup and on a 15s refresh).
func (sh *shell) rebuildTrayMenu() {
	menu := sh.app.NewMenu()
	menu.Add("Show Owldrop").OnClick(func(*application.Context) {
		sh.win.Show()
		sh.win.Focus()
	})
	menu.AddSeparator()

	sendMenu := menu.AddSubmenu("Send file to…")
	devs, err := tsDevices(context.Background())
	if err != nil {
		sendMenu.Add("(tailscaled unreachable)").SetEnabled(false)
	} else {
		n := 0
		for _, d := range devs {
			if d.Taildrop != "available" {
				continue
			}
			d := d
			label := d.Name
			if d.OS != "" {
				label += " (" + d.OS + ")"
			}
			if !d.Online {
				label += " · offline"
			}
			sendMenu.Add(label).OnClick(func(*application.Context) {
				sh.quickSend(d)
			})
			n++
		}
		if n == 0 {
			sendMenu.Add("(no devices — is your tailnet up?)").SetEnabled(false)
		}
	}
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(*application.Context) {
		sh.app.Quit()
	})
	sh.tray.SetMenu(menu)
}

// quickSend opens a native file picker and sends the chosen file to a device,
// reusing the server's in-process send path (no HTTP round trip).
func (sh *shell) quickSend(dev device) {
	fd := sh.app.Dialog.OpenFileWithOptions(&application.OpenFileDialogOptions{
		Title:                "Send to " + dev.Name,
		CanChooseFiles:       true,
		CanChooseDirectories: false,
		Window:               sh.win,
	})
	path, err := fd.PromptForSingleSelection()
	if err != nil || path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		sh.notify("Owldrop", "send failed: "+err.Error())
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		sh.notify("Owldrop", "send failed: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	name := filepath.Base(path)
	if err := sh.srv.sendOne(ctx, fmt.Sprintf("tray-%d", time.Now().UnixNano()), dev.ID, name, st.Size(), f, nil); err != nil {
		sh.notify("Owldrop: send failed", fmt.Sprintf("%s → %s: %v", name, dev.Name, err))
		return
	}
	sh.notify("Owldrop: sent", fmt.Sprintf("%s → %s", name, dev.Name))
}

// notify raises a native OS notification (silent, like the Electron shell).
func (sh *shell) notify(title, body string) {
	if err := sh.notif.SendNotification(notifications.NotificationOptions{
		ID:    fmt.Sprintf("owldrop-%d", time.Now().UnixNano()),
		Title: title,
		Body:  body,
		Sound: &notifications.NotificationSound{Silent: true},
	}); err != nil {
		log.Printf("notification: %v", err)
	}
}

// notifyLoop consumes the sidecar's event hub and raises native notifications
// for arrivals and save/send results, per the user's preferences.
func (sh *shell) notifyLoop(ctx context.Context) {
	ch := sh.srv.hub.subscribeWeb()
	defer sh.srv.hub.unsubscribeWeb(ch)
	known := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-ch:
			if !ok {
				return
			}
			sh.handleHubEvent(b, known)
		}
	}
}

func (sh *shell) handleHubEvent(b []byte, known map[string]bool) {
	var kind struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(b, &kind) != nil {
		return
	}
	sh.srv.cfgMu.Lock()
	arrival, save, send, errors := sh.srv.cfg.NotifyArrival, sh.srv.cfg.NotifySave, sh.srv.cfg.NotifySend, sh.srv.cfg.NotifyError
	sh.srv.cfgMu.Unlock()

	switch kind.Type {
	case "inbox":
		var ev inboxEvent
		if json.Unmarshal(b, &ev) != nil {
			return
		}
		names := map[string]bool{}
		var fresh []waitingFile
		for _, f := range ev.Files {
			names[f.Name] = true
			if !known[f.Name] {
				fresh = append(fresh, f)
			}
		}
		for k := range known {
			if !names[k] {
				delete(known, k)
			}
		}
		for k := range names {
			known[k] = true
		}
		if arrival {
			for _, f := range fresh {
				sh.notify("Owldrop: new file", fmt.Sprintf("%s (%s)", f.Name, fmtSize(f.Size)))
			}
		}
	case "save":
		var ev saveEvent
		if json.Unmarshal(b, &ev) != nil {
			return
		}
		if ev.Done && save {
			if ev.Err != "" {
				sh.notify("Owldrop: save failed", ev.Name+": "+ev.Err)
			} else {
				sh.notify("Owldrop: saved", ev.Name+" → "+ev.Path)
			}
		}
	case "send":
		var ev sendEvent
		if json.Unmarshal(b, &ev) != nil {
			return
		}
		if ev.Done && send {
			if ev.Err != "" {
				sh.notify("Owldrop: send failed", ev.Name+": "+ev.Err)
			} else {
				sh.notify("Owldrop: sent", ev.Name)
			}
		}
	case "status":
		var ev statusEvent
		if json.Unmarshal(b, &ev) != nil {
			return
		}
		if ev.Err != "" && errors {
			sh.notify("Owldrop", "tailscaled unreachable: "+ev.Err)
		}
	}
}

// fmtSize renders a byte count the way the UI does (KiB/MiB/GiB/TiB).
func fmtSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	i := -1
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
