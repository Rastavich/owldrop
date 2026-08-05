package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ntfy push notifications: the official Tailscale app should notify on
// receive, but when it doesn't (permission, battery optimisation, quiet
// channel) the recipient has to hunt folders. When ntfy_topic is set, a
// successful send to a phone POSTs to ntfy (ntfy.sh or self-hosted) so the
// phone gets a real push notification. The user installs the ntfy app and
// subscribes to the topic — no account needed.
const defaultNtfyServer = "https://ntfy.sh"

func ntfyServer(c *config) string {
	if c.NtfyServer != "" {
		return c.NtfyServer
	}
	return defaultNtfyServer
}

// sendNtfy publishes one message to the configured topic. Errors surface to
// the caller (the test endpoint); the send path fire-and-forgets.
func sendNtfy(ctx context.Context, c *config, title, body string) error {
	if c.NtfyTopic == "" {
		return fmt.Errorf("ntfy topic not configured")
	}
	u := strings.TrimRight(ntfyServer(c), "/") + "/" + url.PathEscape(c.NtfyTopic)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	req.Header.Set("Tags", "package")
	res, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("ntfy: %s", res.Status)
	}
	return nil
}

// ntfySendDone pings the phone after a successful push to a mobile target,
// in the background — a slow ntfy must never delay the send completion.
func (s *server) ntfySendDone(peerName, peerOS, name string, size int64) {
	if peerOS != "android" && peerOS != "ios" {
		return
	}
	s.cfgMu.Lock()
	c := *s.cfg
	s.cfgMu.Unlock()
	if c.NtfyTopic == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := sendNtfy(ctx, &c, name, fmt.Sprintf("%s delivered to %s — open Tailscale or Files", fmtSize(size), peerName)); err != nil {
			log.Printf("ntfy: %v", err)
		}
	}()
}
