package main

import (
	"bytes"
	"image/png"
	"os"
	"strings"
	"testing"

	qrcode "github.com/skip2/go-qrcode"
)

func TestPickPhoneAccessURLSkipsFunnel404(t *testing.T) {
	serve := "https://desktop.taila4569.ts.net/"
	magic := "http://desktop.taila4569.ts.net:8976/"
	ip := "http://100.112.233.3:8976/"
	cases := []struct {
		name   string
		serve  string
		funnel bool
		lan    []string
		want   string
	}{
		{
			name:   "funnel+serve+lan: phone must not get https://*.ts.net/ (404 on /)",
			serve:  serve,
			funnel: true,
			lan:    []string{magic, ip},
			want:   ip,
		},
		{
			name:   "lan only: prefer tailnet IP over MagicDNS (HSTS on *.ts.net)",
			serve:  "",
			funnel: false,
			lan:    []string{magic, ip},
			want:   ip,
		},
		{
			name:   "serve without funnel, no lan: HTTPS is fine",
			serve:  serve,
			funnel: false,
			lan:    nil,
			want:   serve,
		},
		{
			name:   "funnel without lan: no full-app URL exists on the Funnel host",
			serve:  serve,
			funnel: true,
			lan:    nil,
			want:   "",
		},
		{
			name:   "funnel+magicdns lan only: :8976 not :443",
			serve:  serve,
			funnel: true,
			lan:    []string{magic},
			want:   magic,
		},
		{
			name:   "prefer IPv4 over IPv6 for phone browsers",
			serve:  "",
			funnel: false,
			lan:    []string{"http://desktop.taila4569.ts.net:8976/", "http://[fd7a:115c:a1e0::1]:8976/", "http://100.112.233.3:8976/"},
			want:   "http://100.112.233.3:8976/",
		},
	}
	for _, c := range cases {
		if got := pickPhoneAccessURL(c.serve, c.funnel, c.lan); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestQREncodeIsScannablePNG(t *testing.T) {
	raw, err := qrcode.Encode("http://100.64.0.1:8976/", qrcode.Medium, 256)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	dark := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r < 0x8000 && g < 0x8000 && bl < 0x8000 {
				dark++
			}
		}
	}
	if dark < 100 {
		t.Fatalf("QR PNG looks blank: %d dark pixels in %dx%d", dark, b.Dx(), b.Dy())
	}
}

// The empty-inbox QR is an <img src="/api/qr">. blob: object URLs are blocked
// by the page CSP (img-src 'self' data:) and show up as a white box.
func TestIndexCSPAllowsSelfImagesNotBlobs(t *testing.T) {
	b, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	csp := string(b)
	if !strings.Contains(csp, "img-src 'self' data:") {
		t.Fatalf("unexpected CSP img-src in web/index.html:\n%s", csp)
	}
	if strings.Contains(csp, "blob:") {
		t.Fatal("CSP lists blob: — EmptyInbox must not depend on that; keep img-src 'self'")
	}
}
