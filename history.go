package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// historyEvent is one record in the local history log. Events for the same
// file share an ID: an "arrived" event opens a session, and "saved"/"deleted"
// close it. "sent"/"send_failed" are their own sessions.
type historyEvent struct {
	ID     string    `json:"id"`
	Ts     time.Time `json:"ts"`
	Kind   string    `json:"kind"` // arrived | saved | deleted | sent | send_failed
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	Path   string    `json:"path,omitempty"`
	Peer   string    `json:"peer,omitempty"`
	Source string    `json:"source,omitempty"` // "" = taildrop, "link" = drop link
}

// history is a small append-only log of everything that happened to files.
// Persisted as JSONL next to the config; rebuilt in memory at startup.
type history struct {
	mu        sync.Mutex
	path      string
	events    []historyEvent
	active    map[string]string    // waiting filename → open session ID (awaiting save/delete)
	lastSeen  map[string]time.Time // filename → last poll where it was still waiting
	goneGrace time.Duration        // how long a missing file must stay absent before being marked deleted
	max       int
}

func newHistory(dir string) *history {
	h := &history{
		path:      filepath.Join(dir, "history.jsonl"),
		active:    map[string]string{},
		lastSeen:  map[string]time.Time{},
		goneGrace: 60 * time.Second,
		max:       2000,
	}
	h.load()
	return h
}

func (h *history) load() {
	f, err := os.Open(h.path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e historyEvent
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			h.events = append(h.events, e)
			h.trackActive(&e)
		}
	}
}

// trackActive keeps active in sync with the event stream so sessions are
// correctly attributed across app restarts.
func (h *history) trackActive(e *historyEvent) {
	switch e.Kind {
	case "arrived":
		h.active[e.Name] = e.ID
	case "saved", "deleted":
		delete(h.active, e.Name)
	}
}

// recordArrivals opens a session for every newly seen waiting file and
// closes sessions for files that vanished without our save/delete (consumed
// by the CLI or daemon cleanup). Files are only marked deleted once they've
// been absent for goneGrace — a save/delete in flight must not race with
// this (that race previously recorded "deleted" for a file that was saved).
func (h *history) recordArrivals(files []waitingFile) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	changed := false
	for _, f := range files {
		h.lastSeen[f.Name] = now
		if _, ok := h.active[f.Name]; ok {
			continue
		}
		e := historyEvent{ID: newID(), Ts: now, Kind: "arrived", Name: f.Name, Size: f.Size, Source: f.Source}
		h.events = append(h.events, e)
		h.active[f.Name] = e.ID
		changed = true
	}
	for name, id := range h.active {
		if ls, ok := h.lastSeen[name]; ok && now.Sub(ls) < h.goneGrace {
			continue // still within the grace period
		}
		if !containsName(files, name) {
			h.events = append(h.events, historyEvent{ID: id, Ts: now, Kind: "deleted", Name: name})
			delete(h.active, name)
			changed = true
		}
	}
	if changed {
		h.prune()
		h.persist()
	}
}

func containsName(files []waitingFile, name string) bool {
	for _, f := range files {
		if f.Name == name {
			return true
		}
	}
	return false
}

// sessionIDFor resolves the session that should carry a save/delete for name:
// the open session if there is one, otherwise the most recent recorded
// arrival for that name (covers app restarts between arrival and save).
func (h *history) sessionIDFor(name string) string {
	if id, ok := h.active[name]; ok {
		return id
	}
	for i := len(h.events) - 1; i >= 0; i-- {
		if h.events[i].Kind == "arrived" && h.events[i].Name == name {
			return h.events[i].ID
		}
	}
	return ""
}

func (h *history) recordSaved(name, path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.sessionIDFor(name)
	if id == "" {
		id = newID()
	}
	h.events = append(h.events, historyEvent{ID: id, Ts: time.Now(), Kind: "saved", Name: name, Path: path})
	delete(h.active, name)
	h.prune()
	h.persist()
}

func (h *history) recordDeleted(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.sessionIDFor(name)
	if id == "" {
		id = newID()
	}
	h.events = append(h.events, historyEvent{ID: id, Ts: time.Now(), Kind: "deleted", Name: name})
	delete(h.active, name)
	h.prune()
	h.persist()
}

func (h *history) recordSend(peer, name string, size int64, err error) {
	kind := "sent"
	if err != nil {
		kind = "send_failed"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, historyEvent{ID: newID(), Ts: time.Now(), Kind: kind, Name: name, Size: size, Peer: peer})
	h.prune()
	h.persist()
}

func (h *history) clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = nil
	h.active = map[string]string{}
	os.Remove(h.path)
	os.Remove(h.path + ".tmp")
}

func (h *history) prune() {
	if len(h.events) > h.max {
		h.events = h.events[len(h.events)-h.max:]
	}
}

func (h *history) persist() {
	tmp := h.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	enc := json.NewEncoder(f)
	for _, e := range h.events {
		enc.Encode(e)
	}
	f.Close()
	os.Rename(tmp, h.path)
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
