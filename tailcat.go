package main

// "Send to anyone" via tailcat (https://github.com/tailscale/tailcat).
//
// Taildrop only reaches devices on your tailnet. tailcat runs Tailscale's
// data plane (WireGuard + DERP + NAT traversal) without the control plane:
// a server prints a one-time address, and anyone with the tailcat CLI can
// connect to it, end-to-end encrypted and usually peer-to-peer.
//
// Each share stages the chosen files in a private directory and runs
//
//	tailcat --key=new --json serve --full-address --files=<dir>:ro files
//
// as a child process. That is a read-only SFTP server confined to the staging
// directory (tailcat uses os.Root, so neither ".." nor symlinks escape it),
// under a fresh ephemeral key with a WireGuard PSK. The address is the only
// credential, so stopping or expiring a share kills the process and deletes
// the staging copy, and the address stops working for good.
//
// We run the external binary instead of importing the library because the
// library pins a pre-release tailscale.com that would drag the whole app
// onto it. Owldrop just looks for `tailcat` on the PATH and in well-known
// install locations.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	tailcatDefaultTTL   = time.Hour
	tailcatMaxTTL       = 7 * 24 * time.Hour
	tailcatMaxShares    = 10
	tailcatStartTimeout = 60 * time.Second
)

// tailcatFile is one file offered by a share.
type tailcatFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// tailcatShare is one running `tailcat serve` process.
type tailcatShare struct {
	ID        string        `json:"id"`
	Addr      string        `json:"addr"`
	Files     []tailcatFile `json:"files"`
	Folder    string        `json:"folder,omitempty"` // set when several files are shared
	Command   string        `json:"command"`          // what the recipient runs
	CreatedAt time.Time     `json:"createdAt"`
	ExpiresAt time.Time     `json:"expiresAt"`

	dir      string // staging dir (served root)
	cmd      *exec.Cmd
	timer    *time.Timer
	stopping bool          // we killed it (stop/expiry), so exit errors are expected; guarded by tailcatManager.mu
	done     chan struct{} // closed once the process has exited and been cleaned up
}

type tailcatManager struct {
	root     string        // parent of all staging dirs
	findBin  func() string // "" when tailcat isn't installed
	onChange func()        // broadcast to open pages
	mu       sync.Mutex
	shares   map[string]*tailcatShare
	receives map[string]*tailcatReceive // downloads (tailcat_recv.go)
	incoming string                     // address from an owldrop:// link, awaiting the user
}

func newTailcatManager(root string) *tailcatManager {
	// Staging dirs from a previous run (crash, kill -9) are orphans: their
	// processes are gone, so the copies serve nobody.
	os.RemoveAll(root)
	return &tailcatManager{
		root:     root,
		findBin:  findTailcat,
		onChange: func() {},
		shares:   map[string]*tailcatShare{},
		receives: map[string]*tailcatReceive{},
	}
}

// findTailcat locates the tailcat CLI. OWLDROP_TAILCAT overrides; then the
// copy bundled with Owldrop (a known-good version, see tools/buildtailcat);
// then PATH; then well-known install locations, because GUI apps often start
// with a sparse PATH (no ~/go/bin, no Homebrew, no nix profile).
func findTailcat() string {
	if p := os.Getenv("OWLDROP_TAILCAT"); p != "" {
		if isExecutableFile(p) {
			return p
		}
		return ""
	}
	bin := "tailcat"
	if runtime.GOOS == "windows" {
		bin = "tailcat.exe"
	}
	for _, p := range bundledTailcatPaths(bin) {
		if isExecutableFile(p) {
			return p
		}
	}
	if p, err := exec.LookPath(bin); err == nil {
		return p
	}
	for _, dir := range tailcatSearchDirs() {
		p := filepath.Join(dir, bin)
		if isExecutableFile(p) {
			return p
		}
	}
	return ""
}

// bundledTailcatPaths is where Owldrop's packages put tailcat, relative to
// the running executable: alongside it (Windows installer, macOS
// Contents/MacOS, AppImage) or in ../lib/owldrop (deb/rpm install
// /usr/local/bin/owldrop + /usr/local/lib/owldrop/tailcat).
func bundledTailcatPaths(bin string) []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return bundledTailcatPathsFor(exe, bin)
}

func bundledTailcatPathsFor(exe, bin string) []string {
	dir := filepath.Dir(exe)
	return []string{
		filepath.Join(dir, bin),
		filepath.Join(dir, "..", "lib", "owldrop", bin),
	}
}

func tailcatSearchDirs() []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	if gp := os.Getenv("GOPATH"); gp != "" {
		dirs = append(dirs, filepath.Join(gp, "bin"))
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "go", "bin"))
	}
	switch runtime.GOOS {
	case "windows":
		if home != "" {
			dirs = append(dirs, filepath.Join(home, "scoop", "shims"))
		}
		dirs = append(dirs, filepath.Join(os.Getenv("ProgramFiles"), "tailcat"))
	case "darwin":
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
		if home != "" {
			dirs = append(dirs, filepath.Join(home, ".nix-profile", "bin"))
		}
		dirs = append(dirs, "/run/current-system/sw/bin")
	default:
		dirs = append(dirs, "/usr/local/bin", "/usr/bin", "/snap/bin")
		if home != "" {
			dirs = append(dirs, filepath.Join(home, ".nix-profile", "bin"), filepath.Join(home, ".local", "bin"))
		}
		if u := os.Getenv("USER"); u != "" {
			dirs = append(dirs, "/etc/profiles/per-user/"+u+"/bin")
		}
		dirs = append(dirs, "/run/current-system/sw/bin")
	}
	return dirs
}

func isExecutableFile(p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || st.Mode()&0o111 != 0
}

// list returns the live shares, newest first.
func (m *tailcatManager) list() []tailcatShare {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]tailcatShare, 0, len(m.shares))
	for _, s := range m.shares {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// newStaging reserves a share ID and its staging dir. Files go in dir/files
// (single file) or dir/<folder>/ (several), and dir is what tailcat serves.
func (m *tailcatManager) newStaging() (id, dir string, err error) {
	m.mu.Lock()
	n := len(m.shares)
	m.mu.Unlock()
	if n >= tailcatMaxShares {
		return "", "", fmt.Errorf("too many active tailcat shares (max %d) — stop one first", tailcatMaxShares)
	}
	id = newToken()[:16]
	dir = filepath.Join(m.root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	return id, dir, nil
}

// start launches tailcat over a staging dir already holding files, waits for
// its address, and registers the share. On error the staging dir is removed.
func (m *tailcatManager) start(id, dir string, files []tailcatFile, folder string, ttl time.Duration) (*tailcatShare, error) {
	fail := func(err error) (*tailcatShare, error) {
		os.RemoveAll(dir)
		return nil, err
	}
	if len(files) == 0 {
		return fail(errors.New("no files to share"))
	}
	bin := m.findBin()
	if bin == "" {
		return fail(errTailcatMissing)
	}
	if ttl <= 0 {
		ttl = tailcatDefaultTTL
	}
	if ttl > tailcatMaxTTL {
		ttl = tailcatMaxTTL
	}

	cmd := exec.Command(bin, tailcatServeArgs(dir)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TAILCAT_ADDR_FILE=") // never publish the address anywhere else
	setTailcatSysProcAttr(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	stderr := &tailBuffer{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fail(fmt.Errorf("starting tailcat: %w", err))
	}

	addrCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sent := false
		for sc.Scan() {
			var v struct {
				ListenAddr string `json:"listenAddr"`
			}
			if !sent && json.Unmarshal(sc.Bytes(), &v) == nil && v.ListenAddr != "" {
				addrCh <- v.ListenAddr
				sent = true
			}
		}
		if !sent {
			close(addrCh)
		}
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	var addr string
	select {
	case a, ok := <-addrCh:
		if !ok {
			err := <-exited
			return fail(tailcatExitError(err, stderr))
		}
		addr = a
	case err := <-exited:
		return fail(tailcatExitError(err, stderr))
	case <-time.After(tailcatStartTimeout):
		cmd.Process.Kill()
		<-exited
		return fail(errors.New("tailcat didn't come up in time (can this machine reach the internet / a DERP relay?)"))
	}

	now := time.Now()
	sh := &tailcatShare{
		ID:        id,
		Addr:      addr,
		Files:     files,
		Folder:    folder,
		Command:   tailcatRecipientCommand(addr, files, folder),
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		dir:       dir,
		cmd:       cmd,
		done:      make(chan struct{}),
	}
	m.mu.Lock()
	m.shares[id] = sh
	m.mu.Unlock()
	sh.timer = time.AfterFunc(ttl, func() { m.stop(id) })

	go func() {
		err := <-exited
		m.mu.Lock()
		expected := sh.stopping
		m.mu.Unlock()
		if !expected {
			log.Printf("tailcat share %s exited unexpectedly: %v: %s", id, err, lastLines(strings.TrimSpace(stderr.String()), 3))
		}
		sh.timer.Stop()
		m.mu.Lock()
		delete(m.shares, id)
		m.mu.Unlock()
		os.RemoveAll(dir)
		close(sh.done)
		m.onChange()
	}()
	m.onChange()
	return sh, nil
}

// stop kills a share's process; cleanup happens in its exit goroutine. It
// waits (briefly) for that cleanup so callers see a consistent list.
func (m *tailcatManager) stop(id string) bool {
	m.mu.Lock()
	sh, ok := m.shares[id]
	if ok {
		sh.stopping = true
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	if sh.cmd.Process != nil {
		sh.cmd.Process.Kill()
	}
	select {
	case <-sh.done:
	case <-time.After(5 * time.Second):
	}
	return true
}

// stopAll ends every share (app shutdown).
func (m *tailcatManager) stopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.shares))
	for id := range m.shares {
		ids = append(ids, id)
	}
	for _, r := range m.receives {
		r.cancel()
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.stop(id)
	}
	os.RemoveAll(m.root)
}

var errTailcatMissing = errors.New("tailcat is not installed — see https://github.com/tailscale/tailcat#install")

// tailcatServeArgs is the read-only file server invocation: an ephemeral key
// (a new address per share, dead once the process exits), the address as
// JSON on stdout, and the DERP info embedded so recipients connect without a
// DERP map fetch. The PSK is on by default (tailcat >= 0.6).
func tailcatServeArgs(dir string) []string {
	return []string{"--key=new", "--json", "serve", "--full-address", "--files=" + dir + ":ro", "files"}
}

// tailcatRecipientCommand is what the recipient pastes into a terminal.
func tailcatRecipientCommand(addr string, files []tailcatFile, folder string) string {
	if folder != "" {
		return "tailcat cp -r " + shellQuote(addr+":"+folder) + " ."
	}
	return "tailcat cp " + shellQuote(addr+":"+files[0].Name) + " ."
}

// shellQuote single-quotes s for POSIX shells when it has anything beyond a
// conservative safe set. tailcat addresses are URL-safe base64, so only file
// names ever trigger quoting.
func shellQuote(s string) string {
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:/+@,=", r)) {
			safe = false
			break
		}
	}
	if safe && s != "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func tailcatExitError(err error, stderr *tailBuffer) error {
	msg := lastLines(strings.TrimSpace(stderr.String()), 3)
	if msg == "" {
		if err == nil {
			return errors.New("tailcat exited without printing an address")
		}
		return fmt.Errorf("tailcat exited: %v", err)
	}
	return fmt.Errorf("tailcat failed: %s", msg)
}

// lastLines returns the last n non-comment lines of s (tailcat's "# ..."
// status lines are noise in an error message), falling back to the raw tail.
func lastLines(s string, n int) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			keep = append(keep, l)
		}
	}
	if len(keep) == 0 {
		keep = strings.Split(s, "\n")
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, "\n")
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

// tailcatFolderName names the folder several files are shared in, so the
// recipient gets them all with a single `cp -r`.
func tailcatFolderName(now time.Time) string {
	return "owldrop-" + now.Format("20060102-150405")
}

// --- HTTP -----------------------------------------------------------------

func (s *server) broadcastTailcat() {
	s.hub.broadcast(map[string]string{"type": "tailcat"})
}

// handleTailcat: GET → install state + live shares. The response carries the
// share addresses (which are credentials), so the route uses guardToken.
func (s *server) handleTailcat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	bin := s.tailcat.findBin()
	writeJSON(w, map[string]any{
		"installed": bin != "",
		"path":      bin,
		"shares":    s.tailcat.list(),
		"receives":  s.tailcat.receiveList(),
		"incoming":  s.tailcat.incomingAddr(),
	})
}

// handleTailcatShares:
//
//	POST /api/tailcat/shares?ttl=<minutes>   multipart "file" parts → new share
//	POST /api/tailcat/shares?ttl=<minutes>   JSON {"pending": "<id>"} → share an OS-shared file
//	DELETE /api/tailcat/shares/<id>          stop a share
func (s *server) handleTailcatShares(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/api/tailcat/shares/")
		if id == "" || strings.Contains(id, "/") {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "share id required"})
			return
		}
		if !s.tailcat.stop(id) {
			writeJSONStatus(w, http.StatusNotFound, map[string]any{"error": "no such share"})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodPost:
		if r.URL.Path != "/api/tailcat/shares" {
			http.NotFound(w, r)
			return
		}
		s.createTailcatShare(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) createTailcatShare(w http.ResponseWriter, r *http.Request) {
	if s.tailcat.findBin() == "" {
		writeJSONStatus(w, http.StatusPreconditionFailed, map[string]any{"error": errTailcatMissing.Error()})
		return
	}
	ttl := tailcatDefaultTTL
	if v := r.URL.Query().Get("ttl"); v != "" {
		mins, err := strconv.Atoi(v)
		if err != nil || mins <= 0 {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "ttl must be a positive number of minutes"})
			return
		}
		ttl = time.Duration(mins) * time.Minute
	}

	id, dir, err := s.tailcat.newStaging()
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}

	var files []tailcatFile
	var folder string
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "multipart/form-data" {
		files, folder, err = stageTailcatUpload(r, dir)
	} else {
		files, folder, err = s.stageTailcatPending(r, dir)
	}
	if err != nil {
		os.RemoveAll(dir)
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	sh, err := s.tailcat.start(id, dir, files, folder, ttl)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errTailcatMissing) {
			status = http.StatusPreconditionFailed
		}
		writeJSONStatus(w, status, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, sh)
}

// stageTailcatUpload streams every "file" part to disk. With more than one
// file they're moved into a timestamped folder afterwards, so the recipient
// fetches them all with one `cp -r`.
func stageTailcatUpload(r *http.Request, dir string) ([]tailcatFile, string, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", err
	}
	taken := map[string]bool{}
	var files []tailcatFile
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		if p.FormName() != "file" {
			p.Close()
			continue
		}
		name := filepath.Base(strings.ReplaceAll(p.FileName(), `\`, "/"))
		if !validBaseName(name) || strings.HasPrefix(name, ".") {
			p.Close()
			return nil, "", fmt.Errorf("bad file name %q", p.FileName())
		}
		name = uniqueName(name, func(n string) bool { return taken[n] })
		taken[name] = true
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			p.Close()
			return nil, "", err
		}
		n, err := io.Copy(f, p)
		p.Close()
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, "", fmt.Errorf("receiving %s: %w", name, err)
		}
		files = append(files, tailcatFile{Name: name, Size: n})
	}
	folder, err := foldTailcatFiles(dir, files)
	return files, folder, err
}

// stageTailcatPending copies (or hard-links) a file the OS share sheet handed
// us into the staging dir. The file stays queued for Taildrop too only if
// staging fails — a successful tailcat share consumes it, like a send.
func (s *server) stageTailcatPending(r *http.Request, dir string) ([]tailcatFile, string, error) {
	var req struct {
		Pending string `json:"pending"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Pending == "" {
		return nil, "", errors.New("multipart files or {\"pending\": id} required")
	}
	it, ok := s.takeShare(req.Pending)
	if !ok {
		return nil, "", errors.New("no such shared file")
	}
	requeue := func() {
		s.shareMu.Lock()
		s.shares = append(s.shares, it)
		s.shareMu.Unlock()
	}
	dst := filepath.Join(dir, it.Name)
	if err := linkOrCopy(it.path, dst); err != nil {
		requeue()
		return nil, "", err
	}
	st, err := os.Stat(dst)
	if err != nil {
		requeue()
		return nil, "", err
	}
	s.hub.broadcast(map[string]any{"type": "share", "n": len(s.pendingShares())})
	return []tailcatFile{{Name: it.Name, Size: st.Size()}}, "", nil
}

// foldTailcatFiles moves several staged files into one folder; a single file
// stays at the root.
func foldTailcatFiles(dir string, files []tailcatFile) (string, error) {
	if len(files) <= 1 {
		return "", nil
	}
	folder := tailcatFolderName(time.Now())
	if err := os.Mkdir(filepath.Join(dir, folder), 0o700); err != nil {
		return "", err
	}
	for _, f := range files {
		if err := os.Rename(filepath.Join(dir, f.Name), filepath.Join(dir, folder, f.Name)); err != nil {
			return "", err
		}
	}
	return folder, nil
}

// linkOrCopy hard-links src to dst (instant, no extra space) and falls back
// to a copy across filesystems. A hard link is safe here: tailcat serves it
// read-only and we only ever delete the link.
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// tailcatShutdown is called on app exit so no tailcat process outlives us.
func (s *server) tailcatShutdown(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.tailcat.stopAll(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
