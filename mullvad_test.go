package main

import (
	"net/netip"
	"testing"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func mullvadTestPeers() map[key.NodePublic]*ipnstate.PeerStatus {
	k1, k2, k3, k4, k5 := key.NewNode().Public(), key.NewNode().Public(), key.NewNode().Public(), key.NewNode().Public(), key.NewNode().Public()
	loc := func(country, cc, city, cityCode string) *tailcfg.Location {
		return &tailcfg.Location{Country: country, CountryCode: cc, City: city, CityCode: cityCode}
	}
	return map[key.NodePublic]*ipnstate.PeerStatus{
		k1: {ID: "n1", ExitNodeOption: true, Online: true, DNSName: "de-fra-wg-001.mullvad.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}, Location: loc("Germany", "DE", "Frankfurt", "FRA")},
		k2: {ID: "n2", ExitNodeOption: true, HostName: "fallback-host", Location: loc("United States", "US", "Ashburn", "IAD")},
		k3: {ID: "n3", ExitNodeOption: true},                          // self-hosted exit node: no location
		k4: {ID: "n4", Location: loc("France", "FR", "Paris", "PAR")}, // location but not an exit node
		k5: {ID: "n5"},                                                // plain peer
	}
}

func TestMullvadInfoListsOnlyMullvadExitNodes(t *testing.T) {
	st := &ipnstate.Status{Peer: mullvadTestPeers()}
	got := mullvadInfo(st, &ipn.Prefs{ExitNodeAllowLANAccess: true})
	if !got.Reachable {
		t.Fatalf("want reachable, got %+v", got)
	}
	if !got.AllowLAN {
		t.Error("AllowLAN should pass through from prefs")
	}
	if len(got.Nodes) != 2 {
		t.Fatalf("want 2 mullvad nodes (exit option + location), got %d: %+v", len(got.Nodes), got.Nodes)
	}
	if got.Nodes[0].ID != "n1" || got.Nodes[0].Country != "Germany" || got.Nodes[0].IP != "100.64.0.1" || got.Nodes[0].Host != "de-fra-wg-001" {
		t.Errorf("n1 mapping wrong: %+v", got.Nodes[0])
	}
	if got.Nodes[1].Host != "fallback-host" {
		t.Errorf("n2 host = %q, want fallback-host (HostName fallback)", got.Nodes[1].Host)
	}
	if got.Connected || got.Current != nil {
		t.Errorf("no exit node selected: connected=%v current=%v", got.Connected, got.Current)
	}
}

func TestMullvadInfoMarksCurrentNode(t *testing.T) {
	st := &ipnstate.Status{
		Peer:           mullvadTestPeers(),
		ExitNodeStatus: &ipnstate.ExitNodeStatus{ID: "n2", Online: true},
	}
	got := mullvadInfo(st, nil)
	if !got.Connected || !got.ExitOnline {
		t.Errorf("want connected+online, got %+v", got)
	}
	if got.Current == nil || got.Current.ID != "n2" || !got.Current.Current {
		t.Fatalf("current should be n2, got %+v", got.Current)
	}
	for i, n := range got.Nodes {
		if n.ID == "n2" && !n.Current {
			t.Errorf("nodes[%d] (%s) should be flagged current", i, n.ID)
		}
	}
}

func TestMullvadInfoCurrentNonMullvadExitNode(t *testing.T) {
	// A self-hosted exit node is selected: connected, but no Mullvad node
	// gets the current flag.
	st := &ipnstate.Status{
		Peer:           mullvadTestPeers(),
		ExitNodeStatus: &ipnstate.ExitNodeStatus{ID: "n3"},
	}
	got := mullvadInfo(st, nil)
	if !got.Connected || got.Current != nil {
		t.Errorf("want connected with nil current, got connected=%v current=%+v", got.Connected, got.Current)
	}
}

func TestMullvadInfoUnreachable(t *testing.T) {
	got := mullvadInfo(nil, nil)
	if got.Reachable || got.Connected || got.Hint == "" || got.Nodes != nil {
		t.Errorf("unreachable state wrong: %+v", got)
	}
}
