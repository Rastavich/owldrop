package main

import (
	"context"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
)

// TestUIRenderInbox exercises the widget tree headlessly: an inbox event must
// produce rows with name/size/age, and a save event must show progress.
func TestUIRenderInbox(t *testing.T) {
	a := test.NewApp()
	cfg := &config{SaveDir: t.TempDir()}
	srv := newServer(cfg)
	u := newUI(a, cfg, srv, context.Background())

	now := time.Now()
	u.handleHubEvent(inboxEvent{
		Type: "inbox",
		Files: []waitingFile{
			{Name: "report.pdf", Size: 2048, Arrived: now.Add(-2 * time.Minute)},
			{Name: "photo.jpg", Size: 5 << 20, Arrived: now},
		},
	})

	u.mu.Lock()
	n := len(u.inboxRows)
	u.mu.Unlock()
	if n != 2 {
		t.Fatalf("renderInbox: got %d rows, want 2", n)
	}

	// A save event for an in-flight save must flip the row into progress.
	u.saveFile("photo.jpg", 5<<20, cfg.SaveDir)
	u.mu.Lock()
	st := u.saving["photo.jpg"]
	u.mu.Unlock()
	if st == nil {
		t.Fatal("saveFile: no save state registered")
	}
	u.handleHubEvent(saveEvent{Type: "save", Name: "photo.jpg", Written: 2 << 20, Size: 5 << 20})

	u.mu.Lock()
	row, ok := u.inboxRows["photo.jpg"]
	u.mu.Unlock()
	if !ok {
		t.Fatal("save event: row missing")
	}
	if row.bar.Hidden {
		t.Fatal("save event: progress bar not shown")
	}
}

// TestUIDeviceLabels ensures the send picker labels are readable.
func TestUIDeviceLabels(t *testing.T) {
	cases := []struct {
		d    device
		want string
	}{
		{device{Name: "laptop", OS: "linux", Online: true, Taildrop: "available"}, "laptop (linux)"},
		{device{Name: "laptop", OS: "linux", Online: false, Taildrop: "available"}, "laptop (linux) — offline"},
		{device{Name: "phone", OS: "android", Online: true, Taildrop: "offline"}, "phone (android) — offline"},
		{device{Name: "tv", Online: true, Taildrop: "os does not support taildrop"}, "tv — os does not support taildrop"},
	}
	for _, c := range cases {
		if got := deviceLabel(c.d); got != c.want {
			t.Errorf("deviceLabel(%+v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestFormatting locks in the human-readable size/age strings.
func TestFormatting(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{2048, "2.0 KiB"},
		{5 << 20, "5.0 MiB"},
		{109859119, "105 MiB"},
		{1 << 40, "1.0 TiB"},
	}
	for _, c := range cases {
		if got := fmtSize(c.n); got != c.want {
			t.Errorf("fmtSize(%d) = %q, want %q", c.n, got, c.want)
		}
	}
	if got := fmtAge(time.Now().Add(-30 * time.Second)); got != "30s ago" {
		t.Errorf("fmtAge = %q, want 30s ago", got)
	}
}
