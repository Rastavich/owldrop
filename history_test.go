package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestHistory(t *testing.T) *history {
	t.Helper()
	h := newHistory(t.TempDir())
	h.goneGrace = 10 * time.Millisecond
	return h
}

func TestHistoryArrivalThenSave(t *testing.T) {
	h := newTestHistory(t)
	h.recordArrivals([]waitingFile{{Name: "a.jpg", Size: 100}})
	h.recordSaved("a.jpg", "/tmp/a.jpg")

	if len(h.events) != 2 || h.events[0].Kind != "arrived" || h.events[1].Kind != "saved" {
		t.Fatalf("events = %+v", h.events)
	}
	// Same session: the save must carry the arrival's ID.
	if h.events[0].ID != h.events[1].ID {
		t.Fatalf("save not attached to arrival session: %s vs %s", h.events[0].ID, h.events[1].ID)
	}
	if h.events[1].Path != "/tmp/a.jpg" {
		t.Fatalf("save path = %q", h.events[1].Path)
	}
}

// The regression: a poll that sees the file vanish between the daemon delete
// and recordSaved must NOT record "deleted".
func TestHistoryNoRaceOnSave(t *testing.T) {
	h := newTestHistory(t)
	h.recordArrivals([]waitingFile{{Name: "a.jpg", Size: 100}})

	// The daemon delete has happened; the watcher polls before recordSaved.
	h.recordArrivals(nil) // file gone from the daemon list
	if len(h.events) != 1 {
		t.Fatalf("vanishing file marked deleted within grace: %+v", h.events)
	}
	// Now the save lands — it must still attach to the arrival session.
	h.recordSaved("a.jpg", "/tmp/a.jpg")
	if len(h.events) != 2 || h.events[1].Kind != "saved" {
		t.Fatalf("events = %+v", h.events)
	}
	if h.events[0].ID != h.events[1].ID {
		t.Fatalf("save not attached after poll raced: %s vs %s", h.events[0].ID, h.events[1].ID)
	}
}

func TestHistoryGoneAfterGrace(t *testing.T) {
	h := newTestHistory(t)
	h.recordArrivals([]waitingFile{{Name: "a.jpg", Size: 100}})
	h.recordArrivals(nil)
	if len(h.events) != 1 {
		t.Fatalf("marked deleted too early: %+v", h.events)
	}
	time.Sleep(20 * time.Millisecond) // > goneGrace
	h.recordArrivals(nil)
	if len(h.events) != 2 || h.events[1].Kind != "deleted" {
		t.Fatalf("not marked deleted after grace: %+v", h.events)
	}
}

func TestHistorySaveFallsBackToLastArrival(t *testing.T) {
	h := newTestHistory(t)
	h.recordArrivals([]waitingFile{{Name: "a.jpg", Size: 100}})
	// App restarted: active map is rebuilt from the log, then the file is
	// saved with the map in a weird state — the fallback must still attach.
	h.recordSaved("a.jpg", "/tmp/a.jpg")
	if len(h.events) != 2 || h.events[1].Kind != "saved" {
		t.Fatalf("events = %+v", h.events)
	}
	if h.events[0].ID != h.events[1].ID {
		t.Fatalf("fallback didn't attach: %s vs %s", h.events[0].ID, h.events[1].ID)
	}
}

func TestHistoryPersistence(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(dir)
	h.recordArrivals([]waitingFile{{Name: "a.jpg", Size: 100}})
	h.recordSaved("a.jpg", filepath.Join(dir, "a.jpg"))

	h2 := newHistory(dir) // reload from disk
	if len(h2.events) != 2 {
		t.Fatalf("reloaded %d events, want 2", len(h2.events))
	}
	if h2.events[1].Kind != "saved" || h2.events[1].Path != filepath.Join(dir, "a.jpg") {
		t.Fatalf("reloaded save = %+v", h2.events[1])
	}
	// Active map rebuilt: an open arrival must be found after reload.
	os.Remove(filepath.Join(dir, "history.jsonl"))
	h3 := newHistory(dir)
	h3.recordArrivals([]waitingFile{{Name: "b.txt", Size: 5}})
	h3.recordSaved("b.txt", "/tmp/b.txt")
	if h3.events[1].ID != h3.events[0].ID {
		t.Fatalf("save not attached: %s vs %s", h3.events[0].ID, h3.events[1].ID)
	}
}
