//go:build server

// tsnet mode (headless server build only): join the tailnet as the app's own
// node instead of borrowing a host tailscaled. Gives the container a stable
// MagicDNS hostname and lets the UI, drop links, Sync and HTTPS work without
// any host Tailscale install.
//
// NOTE: Taildrop's inbox/send protocol is implemented in tailscaled, not
// tsnet — a tsnet-only node has no inbox functionality. This mode is an
// *addition* to the existing listeners, not a replacement. See
// docs/tailscale-enhancements.md (#3).
package main

import (
       "flag"
       "log"
       "net"
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
	ln, err := t.Listen("tcp", ":80")
	if err != nil {
		return nil, err
	}
	log.Printf("tsnet: listening on http://%s/ (tailnet only)", *tsnetHostname)
	// Funnel-from-container (public drop links) is deliberately not wired
	// yet: it needs tsnet.ListenFunnel plus ACL config, and the host guard
	// for /drop is written around the local daemon's tunnel. See the plan.
	return ln, nil
}
