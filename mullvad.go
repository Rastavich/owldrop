package main

// mullvad.go — Mullvad VPN connection management. Mullvad servers show up in
// the tailnet as exit-node peers carrying location metadata, so the LocalAPI
// Status response is the whole data source. Selecting one goes through
// EditPrefs — the same path `tailscale set --exit-node` uses — so it works
// against a real tailscaled and a tsnet-embedded LocalClient alike.

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
)

// mullvadNode is one Mullvad VPN server offered by the tailnet.
type mullvadNode struct {
	ID          string `json:"id"`
	IP          string `json:"ip"`
	Host        string `json:"host"` // short server name, e.g. "au-syd-wg-001"
	Country     string `json:"country"`
	CountryCode string `json:"countryCode"`
	City        string `json:"city"`
	CityCode    string `json:"cityCode"`
	Online      bool   `json:"online"`
	Current     bool   `json:"current"`
}

type mullvadState struct {
	Reachable  bool          `json:"reachable"`
	Hint       string        `json:"hint,omitempty"`
	Connected  bool          `json:"connected"`  // an exit node is selected
	ExitOnline bool          `json:"exitOnline"` // the selected exit node is alive
	AllowLAN   bool          `json:"allowLan"`
	Current    *mullvadNode  `json:"current,omitempty"` // set when the selected node is a Mullvad one
	Nodes      []mullvadNode `json:"nodes"`
}

// mullvadInfo maps a LocalAPI Status + Prefs pair onto the UI state. Peers
// with location metadata are Mullvad servers; exit-node peers without one are
// self-hosted and stay off this list. An unreachable daemon yields the
// hint-carrying state the connect banner pattern uses elsewhere.

// mullvadServerName shortens a peer's DNS name to the server code Mullvad
// nodes carry ("au-syd-wg-001.mullvad.ts.net." → "au-syd-wg-001"), the only
// label that distinguishes the many servers sharing one city. Falls back to
// the bare hostname when the DNS name doesn't match that shape.
func mullvadServerName(p *ipnstate.PeerStatus) string {
	h := strings.TrimSuffix(p.DNSName, ".")
	if rest, ok := strings.CutSuffix(h, ".mullvad.ts.net"); ok {
		h = rest
	} else {
		h = p.HostName
	}
	return h
}

func mullvadInfo(st *ipnstate.Status, prefs *ipn.Prefs) mullvadState {
	if st == nil {
		return mullvadState{Hint: tailscaledHint()}
	}
	s := mullvadState{Reachable: true, Nodes: []mullvadNode{}}
	if prefs != nil {
		s.AllowLAN = prefs.ExitNodeAllowLANAccess
	}
	if es := st.ExitNodeStatus; es != nil {
		s.Connected = true
		s.ExitOnline = es.Online
	}
	for _, p := range st.Peer {
		if !p.ExitNodeOption || p.Location == nil {
			continue
		}
		n := mullvadNode{
			ID:          string(p.ID),
			Host:        mullvadServerName(p),
			Country:     p.Location.Country,
			CountryCode: p.Location.CountryCode,
			City:        p.Location.City,
			CityCode:    p.Location.CityCode,
			Online:      p.Online,
		}
		if len(p.TailscaleIPs) > 0 {
			n.IP = p.TailscaleIPs[0].String()
		}
		if es := st.ExitNodeStatus; es != nil && es.ID == p.ID {
			n.Current = true
			cur := n
			s.Current = &cur
		}
		s.Nodes = append(s.Nodes, n)
	}
	return s
}

// fetchMullvad is the read side both the handler and mutations use to report
// fresh state. Prefs failure only hides the LAN flag — connection management
// still works.
func fetchMullvad(ctx context.Context) mullvadState {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := tsClient.Status(ctx)
	if err != nil || st == nil {
		return mullvadState{Hint: tailscaledHint()}
	}
	prefs, err := tsClient.GetPrefs(ctx)
	if err != nil {
		prefs = nil
	}
	return mullvadInfo(st, prefs)
}

// handleMullvad serves the current Mullvad state.
func (s *server) handleMullvad(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, fetchMullvad(r.Context()))
}

// handleMullvadConnect selects a Mullvad exit node by peer ID. The ID must
// name a peer the tailnet currently offers as an exit node — nothing
// user-controlled reaches EditPrefs unvalidated.
func (s *server) handleMullvadConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	st, err := tsClient.Status(ctx)
	if err != nil || st == nil {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": tailscaledHint()})
		return
	}
	var peer *ipnstate.PeerStatus
	for _, p := range st.Peer {
		if string(p.ID) == req.ID && p.ExitNodeOption {
			peer = p
			break
		}
	}
	if peer == nil {
		http.Error(w, "unknown exit node", http.StatusBadRequest)
		return
	}
	mp := &ipn.MaskedPrefs{
		Prefs: ipn.Prefs{
			ExitNodeID: peer.ID,
		},
		ExitNodeIDSet: true,
	}
	if _, err := tsClient.EditPrefs(ctx, mp); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleMullvadDisconnect clears the exit node selection, mirroring
// `tailscale set --exit-node=`: both ID and IP are cleared so a stale IP from
// an earlier selection can't resurrect.
func (s *server) handleMullvadDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	mp := &ipn.MaskedPrefs{
		Prefs: ipn.Prefs{
			ExitNodeID: "",
			ExitNodeIP: netip.Addr{},
		},
		ExitNodeIDSet: true,
		ExitNodeIPSet: true,
	}
	if _, err := tsClient.EditPrefs(ctx, mp); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleMullvadLan toggles the "allow LAN access while using an exit node"
// pref, which defaults to off (exit-node use normally blocks local network).
func (s *server) handleMullvadLan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Allow bool `json:"allow"`
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	mp := &ipn.MaskedPrefs{
		Prefs: ipn.Prefs{
			ExitNodeAllowLANAccess: req.Allow,
		},
		ExitNodeAllowLANAccessSet: true,
	}
	if _, err := tsClient.EditPrefs(ctx, mp); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
