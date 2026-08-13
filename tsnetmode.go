//go:build server

// tsnet mode (headless server build only): join the tailnet as the app's own
// node instead of borrowing a host tailscaled. Gives the container a stable
// MagicDNS hostname and lets the UI, drop links, Sync and HTTPS work without
// any host Tailscale install.
//
// NOTE: Taildrop's inbox/send protocol is implemented in tailscaled, not
// tsnet — a tsnet-only node has no inbox functionality (tsnet's LocalAPI
// serves status/prefs/whois but not the /localapi/v0/files endpoints the
// inbox and Send tab call). This mode is an *addition* to the existing
// listeners, not a replacement. See docs/tailscale-enhancements.md (#3).
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"tailscale.com/tsnet"
)

var (
	tsnetFlag     = flag.Bool("tsnet", envBool("OWLDROP_TSNET"), "join the tailnet as this app's own node (server build)")
	tsnetHostname = flag.String("tsnet-hostname", envOr("OWLDROP_HOSTNAME", "owldrop"), "tsnet node hostname")
)

func init() {
	startTsnet = startTsnetMode
}

func envBool(k string) bool {
	switch os.Getenv(k) {
	case "1", "true", "TRUE", "on":
		return true
	}
	return false
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// startTsnetMode starts the embedded node and returns a listener to serve
// the app on, or nil when tsnet mode is off.
func startTsnetMode(s *server) (net.Listener, error) {
	if !*tsnetFlag {
		return nil, nil
	}
	t := &tsnet.Server{
		Hostname: *tsnetHostname,
		Dir:      filepath.Join(filepath.Dir(configPath()), "tsnet"),
	}
	if key := os.Getenv("TS_AUTHKEY"); key != "" {
		t.AuthKey = key
	}
	log.Printf("tsnet: starting node %q (a login URL is printed if no TS_AUTHKEY is set)", *tsnetHostname)
	if err := t.Start(); err != nil {
		return nil, err
	}
	s.tsnet = t
	s.tsnetHost = *tsnetHostname
	// Point the app's local API client at the embedded node (in-memory, no
	// socket needed) so status, the tailnet-state pill and the self MagicDNS
	// name resolve against this node instead of a missing host socket.
	// Taildrop's file endpoints aren't served by tsnet, so inbox/send stay
	// unavailable in this mode regardless.
	if lc, err := t.LocalClient(); err != nil {
		log.Printf("tsnet: local client: %v", err)
	} else {
		tsClient = lc
	}
	ln, err := t.Listen("tcp", ":80")
	if err != nil {
		return nil, err
	}
	log.Printf("tsnet: listening on http://%s/ (tailnet only)", *tsnetHostname)
	// Public drop links from this node: ListenFunnel is a dedicated
	// listener, not the daemon's loopback proxy, so we serve ONLY /drop/*
	// here — never the full app or session token.
	if fln, err := t.ListenFunnel("tcp", ":443"); err != nil {
		log.Printf("tsnet: funnel (public drop links) unavailable: %v", err)
	} else {
		go func() {
			mux := http.NewServeMux()
			mux.HandleFunc("/drop/", s.handleDropPageOrUpload)
			log.Printf("tsnet: funnel listening on https://%s/drop/…", *tsnetHostname)
			if err := http.Serve(fln, mux); err != nil {
				log.Printf("tsnet: funnel serve: %v", err)
			}
		}()
	}
	return ln, nil
}
