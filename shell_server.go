//go:build server

// Headless entry point for `-tags server` builds (Docker, NAS boxes like
// Unraid). No Wails app, window, tray, notifications, or self-updater —
// just the HTTP server and the daemon inbox watcher. Updating a container
// means pulling a new image, so the binary-swap updater stays off here.
package main

import (
       "context"
       "errors"
       "fmt"
       "log"
       "net"
       "net/http"
       "os"
       "syscall"
       "time"
)

func runApp(ctx context.Context, srv *server, httpSrv *http.Server, addr string) error {
	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()

	go srv.watchInbox(serverCtx)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("port %s is already in use — stop the other process or pass --port", addr)
		}
		return fmt.Errorf("can't listen on %s: %w", addr, err)
	}
	portNum := ln.Addr().(*net.TCPAddr).Port
	srv.setListenerPort(portNum)
	fmt.Printf("owldrop UI: http://127.0.0.1:%d/\n", portNum)
       fmt.Printf("inbox saved to: %s\n", srv.saveDir())
       if err := checkDirWritable(srv.saveDir()); err != nil {
               log.Printf("WARNING: save directory %s is not writable: %v — saves will fail", srv.saveDir(), err)
       }
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

	errCh := make(chan error, 2)
	go func() { errCh <- srv.serveHTTP(httpSrv, ln) }()
	if tln, terr := startTsnet(srv); terr != nil {
		log.Printf("tsnet: %v", terr)
	} else if tln != nil {
		go func() { errCh <- httpSrv.Serve(tln) }()
	}
	err = <-errCh
	if errors.Is(err, http.ErrServerClosed) {
		return nil // graceful SIGINT/SIGTERM shutdown
	}
	return err
}

// checkDirWritable creates and removes a temp file in dir to verify it's usable.
func checkDirWritable(dir string) error {
       if err := os.MkdirAll(dir, 0o755); err != nil {
               return err
       }
       f, err := os.CreateTemp(dir, ".owldrop-write-test-*")
       if err != nil {
               return err
       }
       name := f.Name()
       f.Close()
       os.Remove(name)
       return nil
}
