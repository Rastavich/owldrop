package main

import (
	"errors"
	"testing"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
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

func TestIsInfraPeer(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"funnel-ingress-node", true},                // control-plane injected while Funnel is on
		{"funnel-ingress-node.tailnet.ts.net", true}, // fully-qualified form
		{"laptop", false},
		{"raspberry-pi", false},
		{"funnel", false}, // a real user device named "funnel" stays
	}
	for _, c := range cases {
		if got := isInfraPeer(c.name); got != c.want {
			t.Errorf("isInfraPeer(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDevicesFromStatusSkipsMullvadPeers(t *testing.T) {
	node := func(id, name string) *tailcfg.Node {
		return &tailcfg.Node{StableID: tailcfg.StableNodeID(id), ComputedName: name}
	}
	fts := []apitype.FileTarget{
		{Node: node("n1", "laptop")},
		{Node: node("n2", "de-fra-wg-001")}, // should never happen, but must not leak either
	}
	st := &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{
		key.NewNode().Public(): {ID: "n1", HostName: "laptop", Online: true},
		key.NewNode().Public(): {
			ID: "n2", HostName: "de-fra-wg-001", Online: true,
			ExitNodeOption: true, Location: &tailcfg.Location{Country: "Germany", CountryCode: "DE"},
		},
		key.NewNode().Public(): {
			ID: "n4", HostName: "mullvad-exit-only", Online: true,
			ExitNodeOption: true, Location: &tailcfg.Location{Country: "France"},
		},
		key.NewNode().Public(): {ID: "n3", HostName: "self-hosted-exit", Online: true, ExitNodeOption: true},
	}}

	devs := devicesFromStatus(fts, st)
	got := map[tailcfg.StableNodeID]bool{}
	for _, d := range devs {
		got[d.ID] = true
	}
	if !got["n1"] || !got["n3"] {
		t.Errorf("user and self-hosted exit devices must stay, got %v", got)
	}
	if got["n2"] || got["n4"] {
		t.Errorf("Mullvad exit servers must be absent from the send picker, got %v", got)
	}
}
