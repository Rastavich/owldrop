// JSON-file persistence for the relay: devices, drop links, and queued
// deliveries. Small, boring, crash-safe (atomic rename), and dependency-free
// — the same pattern the desktop app uses. State lives in DATA_DIR; upload
// bytes live under DATA_DIR/uploads.
package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var errNotFound = errors.New("not found")

type device struct {
	ID       string    `json:"id"`
	Key      string    `json:"key"`
	Customer string    `json:"customer,omitempty"` // linked Stripe customer
	Created  time.Time `json:"created"`
}

type link struct {
	Token    string    `json:"token"`
	DeviceID string    `json:"device_id"`
	Name     string    `json:"name"`
	Expires  time.Time `json:"expires"`
	MaxUses  int       `json:"max_uses"` // 0 = unlimited
	Uses     int       `json:"uses"`
	Revoked  bool      `json:"revoked"`
	Created  time.Time `json:"created"`
}

// delivery is one queued upload waiting for the owning device to poll.
// The bytes live at uploads/<deviceID>/<ID>_<name>; the manifest entry is
// removed once the device has downloaded the file.
type delivery struct {
	ID       string    `json:"id"`
	DeviceID string    `json:"device_id"`
	Token    string    `json:"token"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Queued   time.Time `json:"queued"`
	Taken    bool      `json:"taken"` // manifest handed to the device
}

type store struct {
	mu     sync.Mutex
	dir    string
	devs   map[string]*device // by ID
	keys   map[string]string  // apiKey -> device ID
	links  map[string]*link   // by token
	queue  map[string][]*delivery
	wake   chan struct{} // broadcast on queue change
}

func newStore(dir string) (*store, error) {
	s := &store{
		dir:   dir,
		devs:  map[string]*device{},
		keys:  map[string]string{},
		links: map[string]*link{},
		queue: map[string][]*delivery{},
		wake:  make(chan struct{}, 1),
	}
	for _, dir := range []string{s.dir, filepath.Join(s.dir, "uploads")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := s.loadDevices(); err != nil {
		return nil, err
	}
	if err := s.loadLinks(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *store) loadDevices() error {
	b, err := os.ReadFile(filepath.Join(s.dir, "devices.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*device
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	for _, d := range list {
		s.devs[d.ID] = d
		s.keys[d.Key] = d.ID
	}
	return nil
}

func (s *store) loadLinks() error {
	b, err := os.ReadFile(filepath.Join(s.dir, "links.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*link
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	for _, l := range list {
		s.links[l.Token] = l
	}
	return nil
}

// persistJSON atomically writes v to name in DATA_DIR.
func (s *store) persistJSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- devices --------------------------------------------------------------

func (s *store) addDevice(d *device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devs[d.ID] = d
	s.keys[d.Key] = d.ID
	return s.persistJSON("devices.json", s.deviceListLocked())
}

func (s *store) deviceByKey(key string) *device {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.keys[key]
	if !ok {
		return nil
	}
	return s.devs[id]
}

func (s *store) deviceByID(id string) *device {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devs[id]
}

func (s *store) setCustomer(id, customer string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devs[id]
	if !ok {
		return errNotFound
	}
	d.Customer = customer
	return s.persistJSON("devices.json", s.deviceListLocked())
}

func (s *store) deviceListLocked() []*device {
	out := make([]*device, 0, len(s.devs))
	for _, d := range s.devs {
		out = append(out, d)
	}
	return out
}

// --- links ----------------------------------------------------------------

func (s *store) addLink(l *link) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.links[l.Token] = l
	return s.persistJSON("links.json", s.linkListLocked())
}

func (s *store) linkByToken(token string) *link {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.links[token]
}

func (s *store) deviceLinks(deviceID string) []*link {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*link
	for _, l := range s.links {
		if l.DeviceID == deviceID {
			out = append(out, l)
		}
	}
	return out
}

func (s *store) setRevoked(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.links[token]
	if !ok {
		return errNotFound
	}
	l.Revoked = true
	return s.persistJSON("links.json", s.linkListLocked())
}

func (s *store) useOnce(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.links[token]; ok {
		l.Uses++
		s.persistJSON("links.json", s.linkListLocked())
	}
}

func (s *store) linkListLocked() []*link {
	out := make([]*link, 0, len(s.links))
	for _, l := range s.links {
		out = append(out, l)
	}
	return out
}

// usable reports whether the link still accepts uploads.
func (s *store) usable(token string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.links[token]
	if !ok {
		return false, "This drop link doesn't exist."
	}
	if l.Revoked {
		return false, "This drop link was revoked."
	}
	if !l.Expires.IsZero() && time.Now().After(l.Expires) {
		return false, "This drop link has expired."
	}
	if l.MaxUses > 0 && l.Uses >= l.MaxUses {
		return false, "This drop link has already been used."
	}
	return true, ""
}

// --- delivery queue -------------------------------------------------------

func (s *store) enqueue(d *delivery) {
	s.mu.Lock()
	s.queue[d.DeviceID] = append(s.queue[d.DeviceID], d)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// takePending returns untaken deliveries for the device and marks them taken.
func (s *store) takePending(deviceID string) []*delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*delivery
	rest := s.queue[deviceID][:0]
	for _, d := range s.queue[deviceID] {
		if d.Taken {
			rest = append(rest, d)
			continue
		}
		d.Taken = true
		out = append(out, d)
		rest = append(rest, d)
	}
	s.queue[deviceID] = rest
	return out
}

func (s *store) pendingCount(deviceID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, d := range s.queue[deviceID] {
		if !d.Taken {
			n++
		}
	}
	return n
}

// take marks one delivery taken and returns it (for the file download).
func (s *store) take(deviceID, id string) *delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.queue[deviceID] {
		if d.ID == id {
			d.Taken = true
			return d
		}
	}
	return nil
}

// done removes a delivered manifest (called after the device downloaded it).
func (s *store) done(deviceID, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rest := s.queue[deviceID][:0]
	found := false
	for _, d := range s.queue[deviceID] {
		if d.ID == id {
			found = true
			continue
		}
		rest = append(rest, d)
	}
	s.queue[deviceID] = rest
	return found
}

func (s *store) deliveryPath(deviceID, id, name string) string {
	return filepath.Join(s.dir, "uploads", deviceID, id+"_"+name)
}
