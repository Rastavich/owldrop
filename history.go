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
	ID   string    `json:"id"`
	Ts   time.Time `json:"ts"`
	Kind string    `json:"kind"` // arrived | saved | deleted | sent | send_failed
	Name string    `json:"name"`
	Size int64     `json:"size"`
	Path string    `json:"path,omitempty"`
	Peer string    `json:"peer,omitempty"`
}

// history is a small append-only log of everything that happened to files.
// Persisted as JSONL next to the config; rebuilt in memory at startup.
type history struct {
	mu     sync.Mutex
	path   string
	events []historyEvent
	active map[string]string // waiting filename → open session ID (awaiting save/delete)
	max    int
}

func newHistory(dir string) *history {
	h := &history{
		path:   filepath.Join(dir, "history.jsonl"),
		active: map[string]string{},
		max:    2000,
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
// by the CLI or daemon cleanup).
func (h *history) recordArrivals(files []waitingFile) {
	h.mu.Lock()
	defer h.mu.Unlock()
	changed := false
	for _, f := range files {
		if _, ok := h.active[f.Name]; ok {
			continue
		}
		e := historyEvent{ID: newID(), Ts: time.Now(), Kind: "arrived", Name: f.Name, Size: f.Size}
		h.events = append(h.events, e)
		h.active[f.Name] = e.ID
		changed = true
	}
	for name, id := range h.active {
		found := false
		for _, f := range files {
			if f.Name == name {
				found = true
				break
			}
		}
		if !found {
			h.events = append(h.events, historyEvent{ID: id, Ts: time.Now(), Kind: "deleted", Name: name})
			delete(h.active, name)
			changed = true
		}
	}
	if changed {
		h.prune()
		h.persist()
	}
}

func (h *history) recordSaved(name, path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id, ok := h.active[name]
	if !ok {
		id = newID() // e.g. the arrival happened before this run
	}
	h.events = append(h.events, historyEvent{ID: id, Ts: time.Now(), Kind: "saved", Name: name, Path: path})
	delete(h.active, name)
	h.prune()
	h.persist()
}

func (h *history) recordDeleted(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id, ok := h.active[name]
	if !ok {
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
