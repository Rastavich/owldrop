package main

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// The LAN toggle used to os.Exit(0) hoping the shell would respawn — it
// doesn't (Wails), so toggling LAN access killed the app. The listener must
// move to the new bind address in place, and the app must keep serving.
func TestLanRebindKeepsServing(t *testing.T) {
	cfg := &config{
		SaveDir:       t.TempDir(),
		NotifyArrival: true, NotifySave: true, NotifySend: true, NotifyError: true,
		Telemetry: false, // no flusher goroutine in tests
	}
	s := newServerDir(cfg, t.TempDir())
	httpSrv := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	done := make(chan error, 1)
	go func() { done <- s.serveHTTP(httpSrv, ln) }()
	defer func() {
		httpSrv.Close()
		<-done
	}()

	get := func() error {
		res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			return err
		}
		res.Body.Close()
		return nil
	}
	// First serve must be up before we rebind.
	waitFor(t, get, "initial bind")

	// Simulate the LAN toggle: same port, all interfaces.
	s.lan = true
	s.rebindHTTP(fmt.Sprintf("0.0.0.0:%d", port))
	waitFor(t, get, "rebind to 0.0.0.0")

	// And back (LAN off).
	s.lan = false
	s.rebindHTTP(fmt.Sprintf("127.0.0.1:%d", port))
	waitFor(t, get, "rebind back to loopback")
}

func waitFor(t *testing.T, fn func() error, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := fn(); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: server unreachable after 3s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
