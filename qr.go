package main

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// handleQR returns a PNG QR of this machine's phone-access URL (HTTPS Serve
// if on, else a LAN URL). 404 when neither is available — the empty inbox
// then tells the user to enable HTTPS or LAN in Settings.
func (s *server) handleQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	u := s.phoneAccessURL()
	if u == "" {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"error": "enable HTTPS or LAN in Settings so a phone can reach this app"})
		return
	}
	png, err := qrcode.Encode(u, qrcode.Medium, 256)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": "qr encode failed"})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// handlePhoneAccess returns the URL a phone on the tailnet should open.
func (s *server) handlePhoneAccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"url": s.phoneAccessURL()})
}

// phoneAccessURL is the URL a phone on the tailnet should open. Funnel
// (and HSTS on *.ts.net) make https://<machine>.ts.net/ a 404 for `/` —
// only /drop/* is public — so this prefers a 100.x/fd7a LAN URL.
func (s *server) phoneAccessURL() string {
	lan := s.lanURLs()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	serve := ""
	if on, u := s.serveState(ctx); on {
		serve = u
	}
	return pickPhoneAccessURL(serve, s.funnelActive(), lan)
}

// pickPhoneAccessURL chooses a URL the phone can actually load:
//  1. a tailnet IP (avoids *.ts.net HSTS upgrading into Funnel's 404)
//  2. Serve HTTPS, but only when Funnel is off (Funnel 404s `/`)
//  3. any remaining LAN URL (MagicDNS:port)
func pickPhoneAccessURL(serve string, funnel bool, lan []string) string {
	if u := firstIPLanURL(lan); u != "" {
		return u
	}
	if serve != "" && !funnel {
		return serve
	}
	if len(lan) > 0 {
		return lan[0]
	}
	return ""
}

func firstIPLanURL(urls []string) string {
	var v6 string
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		ip := net.ParseIP(u.Hostname())
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			return raw
		}
		if v6 == "" {
			v6 = raw
		}
	}
	return v6
}
