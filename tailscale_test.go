package main

import (
	"errors"
	"testing"

	"tailscale.com/ipn/ipnstate"
)

var errTailscaleUnreachable = errors.New("dial tailscaled: connection refused")

func TestTailscaleStatusInfo(t *testing.T) {
	cases := []struct {
		backend   string
		wantConn  bool
		wantLogin bool
		wantHint  bool
	}{
		{"Running", true, true, false},
		{"NeedsLogin", false, false, true},
		{"NoState", false, false, true},
		{"NeedsMachineAuth", false, false, true},
		{"Stopped", false, false, true},
		{"Starting", false, false, true},
		{"WeirdState", false, false, true},
	}
	for _, c := range cases {
		st := tailscaleStatusInfo(&ipnstate.Status{BackendState: c.backend}, nil)
		if !st.Reachable || st.Connected != c.wantConn || st.LoggedIn != c.wantLogin {
			t.Errorf("backend=%q: reachable=%v connected=%v loggedIn=%v (want conn=%v login=%v)",
				c.backend, st.Reachable, st.Connected, st.LoggedIn, c.wantConn, c.wantLogin)
		}
		if (st.Hint != "") != c.wantHint {
			t.Errorf("backend=%q: hint=%q, wantHint=%v", c.backend, st.Hint, c.wantHint)
		}
		if st.BackendState != c.backend {
			t.Errorf("backend=%q: BackendState passthrough = %q", c.backend, st.BackendState)
		}
	}

	// Unreachable daemon: not connected, no backend state, hint present.
	un := tailscaleStatusInfo(nil, errTailscaleUnreachable)
	if un.Reachable || un.Connected || un.LoggedIn || un.BackendState != "" || un.Hint == "" {
		t.Errorf("unreachable: %+v", un)
	}

	// Nil status without error is treated as unreachable too.
	if nilSt := tailscaleStatusInfo(nil, nil); nilSt.Reachable {
		t.Error("nil status without error should not be reachable")
	}
}

func TestTailscaledHintPlatform(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		if h := tailscaledHintFor(goos); h == "" {
			t.Errorf("tailscaledHintFor(%q) is empty", goos)
		}
	}
}
