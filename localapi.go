package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

// tsClient talks to the local tailscaled daemon over its LocalAPI.
// The zero value is valid: it discovers the daemon socket/named pipe
// automatically on every platform (Linux, macOS, Windows). The socket
// path is overridable via OWLDROP_TS_SOCKET (testing, unusual setups).
var tsClient = newTSClient()

func newTSClient() *local.Client {
	c := &local.Client{}
	if s := os.Getenv("OWLDROP_TS_SOCKET"); s != "" {
		c.Socket = s
	}
	return c
}

// waitingFile is one file sitting in the Taildrop inbox on this machine.
type waitingFile struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Arrived   time.Time `json:"arrived"`             // when we first noticed it (daemon exposes no timestamp)
	Source    string    `json:"source,omitempty"`    // "" = taildrop, "link" = drop link
	Sender    string    `json:"sender,omitempty"`    // drop link label, for link drops
	LinkToken string    `json:"linkToken,omitempty"` // drop link token, for link drops (rule matching)
}

var (
	arrivedMu sync.Mutex
	arrivedAt = map[string]time.Time{}
)

func noteArrival(name string) time.Time {
	arrivedMu.Lock()
	defer arrivedMu.Unlock()
	if t, ok := arrivedAt[name]; ok {
		return t
	}
	t := time.Now()
	arrivedAt[name] = t
	return t
}

// pruneArrivals forgets arrival times for files no longer in the inbox, so a
// file that comes back later counts as a fresh arrival.
func pruneArrivals(keep map[string]bool) {
	arrivedMu.Lock()
	defer arrivedMu.Unlock()
	for name := range arrivedAt {
		if !keep[name] {
			delete(arrivedAt, name)
		}
	}
}

func mapWaitingFiles(wfs []apitype.WaitingFile) []waitingFile {
	seen := make(map[string]bool, len(wfs))
	out := make([]waitingFile, 0, len(wfs))
	for _, wf := range wfs {
		seen[wf.Name] = true
		out = append(out, waitingFile{Name: wf.Name, Size: wf.Size, Arrived: noteArrival(wf.Name)})
	}
	pruneArrivals(seen)
	return out
}

// tsInbox lists the files currently waiting in the Taildrop inbox.
func tsInbox(ctx context.Context) ([]waitingFile, error) {
	wfs, err := tsClient.WaitingFiles(ctx)
	if err != nil {
		return nil, err
	}
	return mapWaitingFiles(wfs), nil
}

// tsInboxWait blocks up to d waiting for inbox changes, then returns the list.
func tsInboxWait(ctx context.Context, d time.Duration) ([]waitingFile, error) {
	wfs, err := tsClient.AwaitWaitingFiles(ctx, d)
	if err != nil {
		return nil, err
	}
	return mapWaitingFiles(wfs), nil
}

// tsSaveFile streams one inbox file out of the daemon into dir, handling
// name conflicts the way Chrome downloads do ("report (1).pdf"). The file
// stays in the inbox until the save fully succeeds.
func tsSaveFile(ctx context.Context, name, dir string, onProgress func(written, size int64)) (path string, size int64, err error) {
	rc, size, err := tsClient.GetWaitingFile(ctx, name)
	if err != nil {
		return "", 0, fmt.Errorf("opening inbox file: %w", err)
	}
	defer rc.Close()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}

	target := conflictFreeName(dir, name)
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	var written int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := rc.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return "", 0, werr
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(written, size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", 0, rerr
		}
	}
	if written != size {
		return "", 0, fmt.Errorf("short read: got %d of %d bytes", written, size)
	}
	if err := tsClient.DeleteWaitingFile(ctx, name); err != nil {
		return "", 0, fmt.Errorf("deleting inbox file: %w", err)
	}
	return target, size, nil
}

// conflictFreeName returns a path in dir that doesn't exist yet, appending
// " (1)", " (2)", ... before the extension as needed.
func conflictFreeName(dir, name string) string {
	target := filepath.Join(dir, filepath.Base(name))
	ext := filepath.Ext(target)
	base := strings.TrimSuffix(target, ext)
	for i := 1; ; i++ {
		if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
			return target
		}
		target = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
}

// tsDeleteFile discards an inbox file without saving it.
func tsDeleteFile(ctx context.Context, name string) error {
	return tsClient.DeleteWaitingFile(ctx, name)
}

// isInfraPeer reports whether name belongs to Tailscale infrastructure
// rather than a user device — e.g. the Funnel ingress node, which the
// control plane injects into the netmap (as "funnel-ingress-node") while
// Funnel is enabled. Such peers can never receive Taildrop and are hidden
// from the send picker.
func isInfraPeer(name string) bool {
	return strings.Contains(name, "funnel-ingress")
}

// device is one machine on the tailnet, as shown in the Send tab.
type device struct {
	ID       tailcfg.StableNodeID `json:"id"`
	Name     string               `json:"name"`
	OS       string               `json:"os"`
	Online   bool                 `json:"online"`
	LastSeen *time.Time           `json:"lastSeen,omitempty"`
	Taildrop string               `json:"taildrop"` // "available" or a human reason
	Relay    string               `json:"relay,omitempty"`    // DERP region ("syd") when relayed
	CurAddr  string               `json:"curAddr,omitempty"`  // current direct address when connected directly
}

// peerTransport summarizes how this machine currently reaches the peer:
// "direct" (CurAddr set), the relay region code when relayed, or "" when
// nothing is known. Derived from the daemon's status fields (verified on
// v1.102: direct peers carry CurAddr, relayed peers carry Relay with an
// empty CurAddr).
func peerTransport(curAddr, relay string) string {
	if curAddr != "" {
		return "direct"
	}
	return relay
}

// tsDevices lists all tailnet devices, marking which can receive Taildrop and
// why the others can't.
func tsDevices(ctx context.Context) ([]device, error) {
	fts, err := tsClient.FileTargets(ctx)
	if err != nil {
		return nil, err
	}
	st, err := tsClient.Status(ctx)
	if err != nil {
		return nil, err
	}

	byID := make(map[tailcfg.StableNodeID]device, len(fts)+len(st.Peer))
	peerByID := make(map[tailcfg.StableNodeID]*ipnstate.PeerStatus, len(st.Peer))
	for _, ps := range st.Peer {
		peerByID[ps.ID] = ps
	}
	for _, ft := range fts {
		n := ft.Node
		d := device{ID: n.StableID, Name: n.ComputedName, Taildrop: "available"}
		if ps, ok := peerByID[n.StableID]; ok {
			d.OS = ps.OS
			d.Online = ps.Online
			d.Relay = ps.Relay
			d.CurAddr = ps.CurAddr
			if !ps.LastSeen.IsZero() {
				t := ps.LastSeen
				d.LastSeen = &t
			}
		} else if n.Online != nil {
			d.Online = *n.Online
			d.LastSeen = n.LastSeen
		}
		byID[n.StableID] = d
	}
	// Peers that can't receive Taildrop at all (show why).
	for _, ps := range st.Peer {
		id := ps.ID
		if _, ok := byID[id]; ok {
			continue
		}
		d := device{ID: id, Name: ps.HostName, OS: ps.OS, Online: ps.Online, Relay: ps.Relay, CurAddr: ps.CurAddr}
		if !ps.LastSeen.IsZero() {
			t := ps.LastSeen
			d.LastSeen = &t
		}
		if ps.NoFileSharingReason != "" {
			d.Taildrop = ps.NoFileSharingReason
		} else {
			d.Taildrop = taildropReason(ps.TaildropTarget)
		}
		byID[id] = d
	}

	out := make([]device, 0, len(byID))
	for _, d := range byID {
		// The Funnel ingress node (funnel-ingress-node) is ts.net
		// infrastructure, not a user device: it can never receive files
		// and would only clutter the send picker.
		if isInfraPeer(d.Name) {
			continue
		}
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b device) int {
		if a.Online != b.Online {
			if a.Online {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, nil
}

func taildropReason(t ipnstate.TaildropTargetStatus) string {
	switch t {
	case ipnstate.TaildropTargetAvailable:
		return "available"
	case ipnstate.TaildropTargetNoNetmapAvailable:
		return "no netmap available"
	case ipnstate.TaildropTargetIpnStateNotRunning:
		return "not connected to tailnet"
	case ipnstate.TaildropTargetMissingCap:
		return "missing taildrop capability"
	case ipnstate.TaildropTargetOffline:
		return "offline"
	case ipnstate.TaildropTargetNoPeerInfo:
		return "no peer info"
	case ipnstate.TaildropTargetUnsupportedOS:
		return "os does not support taildrop"
	case ipnstate.TaildropTargetNoPeerAPI:
		return "not advertising a file API"
	case ipnstate.TaildropTargetOwnedByOtherUser:
		return "owned by another user"
	default:
		return "unknown"
	}
}

// countingReader reports how many bytes have been read from r.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}
