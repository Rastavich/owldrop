package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The poll state is what every build reports before its own updater has looked
// at the feed, so it has to answer in the UI's shape — and stay "disabled" on
// builds with no release version to compare against.
func TestVersionSnapshot(t *testing.T) {
	var nilCheck *versionCheck
	if st := nilCheck.snapshot(); st.State != "disabled" {
		t.Errorf("nil versionCheck state = %q, want disabled", st.State)
	}
	if st := (&versionCheck{current: "dev"}).snapshot(); st.State != "disabled" {
		t.Errorf("dev build state = %q, want disabled", st.State)
	}
	if st := (&versionCheck{current: "0.10.3", enabled: true}).snapshot(); st.State != "idle" || st.Available {
		t.Errorf("up-to-date poll = %+v, want idle and not available", st)
	}
	st := (&versionCheck{current: "0.10.3", latest: "0.10.4", available: true, enabled: true}).snapshot()
	if st.State != "available" || !st.Available || st.Latest != "0.10.4" {
		t.Errorf("polled release = %+v, want available 0.10.4", st)
	}
}

// The poll fills in availability only until the desktop updater has checked for
// itself: past that point the updater's answer is the one an install can act on.
func TestWithPolledAvailability(t *testing.T) {
	idle := updateState{Current: "0.10.3", State: "idle"}
	polled := updateState{Current: "0.10.3", Latest: "0.10.4", Available: true, State: "available"}

	if got := withPolledAvailability(idle, polled, false); !got.Available || got.Latest != "0.10.4" || got.State != "available" {
		t.Errorf("unchecked updater should adopt the polled release, got %+v", got)
	}
	if got := withPolledAvailability(idle, polled, true); got.Available {
		t.Errorf("a checked updater's up-to-date answer must win, got %+v", got)
	}
	updater := updateState{Current: "0.10.3", Latest: "0.11.0", Available: true, State: "available"}
	if got := withPolledAvailability(updater, polled, false); got.Latest != "0.11.0" {
		t.Errorf("the updater's own release must not be replaced by the poll, got %+v", got)
	}
	if got := withPolledAvailability(idle, updateState{State: "disabled"}, false); got.Available {
		t.Errorf("a disabled poll must leave the state alone, got %+v", got)
	}
}

// /api/update on a build with no self-updater (server/Docker/Nix) reports the
// poll — the state the update popup keys on.
func TestHandleUpdateReportsPolledRelease(t *testing.T) {
	s := newServerDir(&config{}, t.TempDir())
	s.version = &versionCheck{current: "0.10.3", latest: "0.10.4", available: true, enabled: true}

	w := httptest.NewRecorder()
	s.handleUpdate(w, httptest.NewRequest(http.MethodGet, "/api/update", nil))

	var st updateState
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("decoding /api/update: %v", err)
	}
	if !st.Available || st.Latest != "0.10.4" || st.State != "available" {
		t.Errorf("/api/update = %+v, want available 0.10.4", st)
	}
}
