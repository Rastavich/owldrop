// Anonymous usage telemetry: the app reports a tiny, privacy-safe event
// stream (app version, OS, event name, timestamp) to the Owldrop site's
// /api/t endpoint, which stores it in Cloudflare D1 and powers the public
// stats page. No file names, no sizes, no IPs, no content — and never
// anything about what you sent or who you sent it to.
//
// Events are buffered and flushed in batches; failures are dropped silently
// (telemetry is best-effort and must never affect the app). The user can
// opt out in Settings (config telemetry=false).
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// telemetryURL is where batched events are POSTed (the site worker).
const telemetryURL = "https://owldrop.app/api/t"

const (
	telemetryFlushEvery  = 60 * time.Second
	telemetryFlushAt     = 20  // events in buffer
	telemetryBatchMax    = 100 // events per request
	telemetrySendTimeout = 5 * time.Second
)

type tEvent struct {
	Name string `json:"name"`
	TS   int64  `json:"ts"` // unix seconds
}

type telemetry struct {
	mu        sync.Mutex
	installID string
	enabled   bool
	platform  string
	version   string
	events    []tEvent
	client    *http.Client
}

// tele is the process-wide telemetry sender. The zero value is inert until
// init is called (dev builds without a server still work, just no events).
var tele = &telemetry{}

// init wires telemetry to the running app. installID is the anonymous
// install identifier (generated on first run, stored in config); enabled
// mirrors the user's opt-out preference.
func (t *telemetry) init(installID string, enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.installID = installID
	t.enabled = enabled
	t.platform = runtime.GOOS
	t.version = appVersion
	if t.client == nil {
		t.client = &http.Client{Timeout: telemetrySendTimeout}
	}
	go t.flushLoop()
}

// event records one anonymous event, flushing a batch early if it fills up.
func (t *telemetry) event(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.enabled || t.installID == "" {
		return
	}
	t.events = append(t.events, tEvent{Name: name, TS: time.Now().Unix()})
	if len(t.events) >= telemetryFlushAt {
		go t.flush()
	}
}

// flushLoop drains the buffer on an interval (also covers the tail events
// that never fill a batch).
func (t *telemetry) flushLoop() {
	for range time.Tick(telemetryFlushEvery) {
		t.flush()
	}
}

// flush sends the buffered events in one batched POST. Best-effort: on any
// error the batch is dropped rather than retried (avoid hammering a dead
// endpoint forever).
func (t *telemetry) flush() {
	t.mu.Lock()
	if len(t.events) == 0 || t.installID == "" {
		t.mu.Unlock()
		return
	}
	n := min(len(t.events), telemetryBatchMax)
	batch := t.events[:n]
	t.events = t.events[n:]
	installID, version, platform := t.installID, t.version, t.platform
	t.mu.Unlock()

	body, err := json.Marshal(map[string]any{
		"install_id": installID,
		"version":    version,
		"platform":   platform,
		"events":     batch,
	})
	if err != nil {
		return
	}
	res, err := t.client.Post(telemetryURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	res.Body.Close()
	if res.StatusCode/100 != 2 {
		log.Printf("telemetry: dropped %d events (%s)", len(batch), res.Status)
	}
}

// newInstallID returns a fresh 32-hex-char anonymous install identifier.
func newInstallID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "" // no entropy: telemetry stays off this run
	}
	return hex.EncodeToString(b)
}
