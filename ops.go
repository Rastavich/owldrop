package main

import (
	"context"
	"io"
	"time"

	"tailscale.com/tailcfg"
)

// saveOne streams an inbox file out of the daemon into dir, broadcasting
// progress to the UI hub as it goes. The file stays in the daemon inbox
// until the save fully succeeds (see tsSaveFile).
func (s *server) saveOne(ctx context.Context, name, dir string, onProgress func(written, size int64)) (string, error) {
	var lastEmit time.Time
	path, _, err := tsSaveFile(ctx, name, dir, func(w, sz int64) {
		if onProgress != nil {
			onProgress(w, sz)
		}
		if now := time.Now(); now.Sub(lastEmit) >= 150*time.Millisecond {
			lastEmit = now
			s.hub.broadcast(saveEvent{Type: "save", Name: name, Written: w, Size: sz})
		}
	})
	if err != nil {
		s.hub.broadcast(saveEvent{Type: "save", Name: name, Done: true, Err: err.Error()})
		return "", err
	}
	s.hub.broadcast(saveEvent{Type: "save", Name: name, Done: true, Path: path})
	s.refresh()
	return path, nil
}

// sendOne pushes a file to a peer via the local daemon, broadcasting
// progress to the UI hub. size -1 means unknown.
func (s *server) sendOne(ctx context.Context, id string, peer tailcfg.StableNodeID, name string, size int64, body io.Reader, onProgress func(sent int64)) error {
	cr := &countingReader{r: body}
	done := make(chan error, 1)
	go func() {
		done <- tsClient.PushFile(ctx, peer, size, name, cr)
	}()

	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	var last int64
	for {
		select {
		case err := <-done:
			ev := sendEvent{Type: "send", ID: id, Peer: peer, Name: name, Sent: cr.n.Load(), Size: size, Done: true}
			if err != nil {
				ev.Err = err.Error()
			}
			s.hub.broadcast(ev)
			if onProgress != nil {
				onProgress(cr.n.Load())
			}
			return err
		case <-ticker.C:
			if n := cr.n.Load(); n != last {
				last = n
				if onProgress != nil {
					onProgress(n)
				}
				s.hub.broadcast(sendEvent{Type: "send", ID: id, Peer: peer, Name: name, Sent: n, Size: size})
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// deleteInboxFile discards an inbox file and pings the watcher so the UI
// updates immediately.
func (s *server) deleteInboxFile(ctx context.Context, name string) error {
	if err := tsDeleteFile(ctx, name); err != nil {
		return err
	}
	s.refresh()
	return nil
}
