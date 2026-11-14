package main

// Receiving a tailcat share inside Owldrop, so recipients never touch a
// terminal (or need scp).
//
// A tailcat file share is an SSH server on port 22 inside the tunnel that
// offers only SFTP, with no client authentication (the address is the
// credential). `tailcat <addr> 22` pipes that port to stdin/stdout, the same
// trick `tailcat cp` uses as its ssh ProxyCommand. We speak SSH + SFTP over
// that pipe with Go's own libraries, list the share, and download it into a
// local folder through an os.Root, so a hostile server can't write outside
// the destination no matter what names it serves.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

const (
	tailcatDialTimeout = 60 * time.Second
	tailcatMaxEntries  = 10000
	tailcatMaxDepth    = 32
	tailcatPartSuffix  = ".owldrop-part"
)

// tailcatAddrRe matches a tailcat address: "tc" + URL-safe base64. Real
// addresses are 50+ characters; the floor keeps words like "tcp" out.
var tailcatAddrRe = regexp.MustCompile(`tc[A-Za-z0-9_-]{40,}`)

// parseTailcatInput pulls the address out of whatever the user pasted: a
// bare address, the `tailcat cp 'tc…:file' .` command, an owldrop.app/r#…
// link, or an owldrop://tailcat/… deep link. Anything after the address
// (a path inside the share) is ignored: Owldrop downloads the whole share,
// which is exactly what a sender's Owldrop puts in it.
func parseTailcatInput(s string) (string, bool) {
	if u, err := url.Parse(strings.TrimSpace(s)); err == nil && u.Fragment != "" {
		s += " " + u.Fragment // links carry the address in the #fragment
	}
	a := tailcatAddrRe.FindString(s)
	return a, a != ""
}

// tailcatRemoteFile is one regular file in a share, by slash-separated path
// relative to the share root.
type tailcatRemoteFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// tailcatSession is an SFTP session to a share over a `tailcat <addr> 22`
// pipe. Close kills the tailcat process.
type tailcatSession struct {
	sf    *sftp.Client
	ssh   *gossh.Client
	cmd   *exec.Cmd
	close sync.Once
}

func (t *tailcatSession) Close() { t.close.Do(t.doClose) }

func (t *tailcatSession) doClose() {
	if t.sf != nil {
		t.sf.Close()
	}
	if t.ssh != nil {
		t.ssh.Close()
	}
	if t.cmd.Process != nil {
		t.cmd.Process.Kill()
	}
	t.cmd.Wait()
}

// dialTailcat opens an SFTP session to addr. ctx bounds the dial and also
// the session's lifetime: cancelling it kills the pipe.
func dialTailcat(ctx context.Context, bin, addr string) (*tailcatSession, error) {
	if !tailcatAddrRe.MatchString(addr) || tailcatAddrRe.FindString(addr) != addr {
		return nil, errors.New("not a tailcat address")
	}
	cmd := exec.CommandContext(ctx, bin, addr, "22")
	setTailcatSysProcAttr(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &tailBuffer{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting tailcat: %w", err)
	}
	sess := &tailcatSession{cmd: cmd}

	type result struct {
		c   *gossh.Client
		err error
	}
	done := make(chan result, 1)
	go func() {
		conn := &pipeConn{r: stdout, w: stdin}
		// The WireGuard tunnel already authenticated the server by the node
		// key in its address, so the SSH host key adds nothing (tailcat's
		// own `ls` makes the same call).
		c, chans, reqs, err := gossh.NewClientConn(conn, "tailcat", &gossh.ClientConfig{
			User:            "owldrop",
			HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		})
		if err != nil {
			done <- result{err: err}
			return
		}
		done <- result{c: gossh.NewClient(c, chans, reqs)}
	}()
	dialErr := func(err error) error {
		if msg := lastLines(strings.TrimSpace(stderr.String()), 2); msg != "" {
			return fmt.Errorf("couldn't reach the share: %s", msg)
		}
		return fmt.Errorf("couldn't reach the share: %w", err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			sess.Close()
			return nil, dialErr(r.err)
		}
		sess.ssh = r.c
	case <-time.After(tailcatDialTimeout):
		sess.Close()
		return nil, errors.New("couldn't reach the share (timed out). It may have expired or been stopped")
	case <-ctx.Done():
		sess.Close()
		return nil, ctx.Err()
	}
	sf, err := sftp.NewClient(sess.ssh, sftp.UseConcurrentReads(true))
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("opening the share: %w", err)
	}
	sess.sf = sf
	return sess, nil
}

// list walks the share and returns its regular files, sorted. Symlinks and
// special files are skipped; paths are validated (no "..", no absolute).
func (t *tailcatSession) list() ([]tailcatRemoteFile, error) {
	var out []tailcatRemoteFile
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if depth > tailcatMaxDepth {
			return errors.New("share is nested too deeply")
		}
		fis, err := t.sf.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, fi := range fis {
			name := fi.Name()
			if !validBaseName(name) || strings.ContainsAny(name, `/\`) {
				continue
			}
			p := name
			if dir != "." {
				p = dir + "/" + name
			}
			switch {
			case fi.Mode().IsDir():
				if err := walk(p, depth+1); err != nil {
					return err
				}
			case fi.Mode().IsRegular():
				if len(out) >= tailcatMaxEntries {
					return fmt.Errorf("share has more than %d files", tailcatMaxEntries)
				}
				out = append(out, tailcatRemoteFile{Path: p, Size: fi.Size()})
			}
		}
		return nil
	}
	if err := walk(".", 0); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// pipeConn adapts a child's stdout/stdin to the net.Conn the SSH client
// wants. Deadlines are unsupported (the session is bounded by its context).
type pipeConn struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (p *pipeConn) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeConn) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeConn) Close() error {
	p.w.Close()
	return p.r.Close()
}
func (p *pipeConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (p *pipeConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (p *pipeConn) SetDeadline(time.Time) error      { return nil }
func (p *pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (p *pipeConn) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "tailcat" }
func (pipeAddr) String() string  { return "tailcat" }

// --- receive jobs ----------------------------------------------------------

// tailcatReceive is one download of a share into a local folder.
type tailcatReceive struct {
	ID       string    `json:"id"`
	Dir      string    `json:"dir"`
	Files    int       `json:"files"`
	Total    int64     `json:"total"`
	Received int64     `json:"received"`
	Current  string    `json:"current,omitempty"`
	Saved    []string  `json:"saved,omitempty"` // top-level paths created in Dir
	Done     bool      `json:"done"`
	Err      string    `json:"err,omitempty"`
	Started  time.Time `json:"started"`

	cancel context.CancelFunc
}

type tailcatReceiveEvent struct {
	Type string `json:"type"` // "tcrecv"
	tailcatReceive
}

// receives is guarded by tailcatManager.mu; the manager owns both directions.
func (m *tailcatManager) receiveList() []tailcatReceive {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]tailcatReceive, 0, len(m.receives))
	for _, r := range m.receives {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

func (m *tailcatManager) snapshot(r *tailcatReceive) tailcatReceive {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *r
}

func (m *tailcatManager) update(r *tailcatReceive, fn func(*tailcatReceive)) {
	m.mu.Lock()
	fn(r)
	m.mu.Unlock()
}

// setIncoming records an address handed to the app by an owldrop:// link so
// every open page can offer to receive it.
func (m *tailcatManager) setIncoming(addr string) {
	m.mu.Lock()
	m.incoming = addr
	m.mu.Unlock()
}

func (m *tailcatManager) incomingAddr() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.incoming
}

func (m *tailcatManager) takeIncoming() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.incoming
	m.incoming = ""
	return a
}

// handleOpenURLs takes the OS's launch arguments / URL-open events and acts
// on owldrop:// links. Returns true if any was handled (the caller shows the
// window).
func (s *server) handleOpenURLs(args []string) bool {
	handled := false
	for _, a := range args {
		if !strings.HasPrefix(strings.ToLower(a), "owldrop:") {
			continue
		}
		if addr, ok := parseTailcatInput(a); ok {
			s.tailcat.setIncoming(addr)
			s.hub.broadcast(map[string]string{"type": "tcincoming", "addr": addr})
			handled = true
		}
	}
	return handled
}

// startReceive downloads every file in the share at addr into dir.
// maxTotal, when > 0, is the size the user saw in the preview: a share that
// grew since then (a sender swapping in more data) is refused.
func (s *server) startReceive(addr, dir string, maxTotal int64) (*tailcatReceive, error) {
	bin := s.tailcat.findBin()
	if bin == "" {
		return nil, errTailcatMissing
	}
	if !filepath.IsAbs(dir) {
		return nil, errors.New("destination must be an absolute path")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &tailcatReceive{ID: newToken()[:16], Dir: dir, Started: time.Now(), cancel: cancel}
	s.tailcat.mu.Lock()
	s.tailcat.receives[r.ID] = r
	s.tailcat.mu.Unlock()
	go s.runReceive(ctx, bin, addr, maxTotal, r)
	return r, nil
}

func (s *server) runReceive(ctx context.Context, bin, addr string, maxTotal int64, r *tailcatReceive) {
	m := s.tailcat
	var lastEmit time.Time
	emit := func(force bool) {
		if !force && time.Since(lastEmit) < 250*time.Millisecond {
			return
		}
		lastEmit = time.Now()
		s.hub.broadcast(tailcatReceiveEvent{Type: "tcrecv", tailcatReceive: m.snapshot(r)})
	}
	fail := func(err error) {
		msg := err.Error()
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			msg = "cancelled"
		}
		m.update(r, func(r *tailcatReceive) { r.Done, r.Err, r.Current = true, msg, "" })
		emit(true)
	}
	defer r.cancel()

	sess, err := dialTailcat(ctx, bin, addr)
	if err != nil {
		fail(err)
		return
	}
	defer sess.Close()
	go func() { <-ctx.Done(); sess.Close() }() // cancel unblocks transfers

	files, err := sess.list()
	if err != nil {
		fail(err)
		return
	}
	if len(files) == 0 {
		fail(errors.New("the share is empty"))
		return
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	if maxTotal > 0 && total > maxTotal {
		fail(fmt.Errorf("the share changed since you looked it up (now %d bytes, was %d) — look it up again", total, maxTotal))
		return
	}
	m.update(r, func(r *tailcatReceive) { r.Files, r.Total = len(files), total })
	emit(true)

	root, err := os.OpenRoot(r.Dir)
	if err != nil {
		fail(err)
		return
	}
	defer root.Close()

	// Never overwrite: a top-level name that already exists locally gets a
	// " (N)" suffix, and everything under it follows.
	renamed := map[string]string{}
	topOf := func(p string) string { top, _, _ := strings.Cut(p, "/"); return top }
	for _, f := range files {
		top := topOf(f.Path)
		if _, ok := renamed[top]; ok {
			continue
		}
		renamed[top] = uniqueName(top, func(n string) bool {
			if _, err := root.Lstat(n); err == nil {
				return true
			}
			_, err := root.Lstat(n + tailcatPartSuffix)
			return err == nil
		})
	}

	var done int64
	var savedTops []string
	seenTop := map[string]bool{}
	for _, f := range files {
		top := topOf(f.Path)
		local := renamed[top] + strings.TrimPrefix(f.Path, top)
		m.update(r, func(r *tailcatReceive) { r.Current = path.Base(f.Path) })
		emit(true)
		n, err := s.receiveFile(sess, root, f.Path, local, f.Size, func(n int64) {
			m.update(r, func(r *tailcatReceive) { r.Received = done + n })
			emit(false)
		})
		if err != nil {
			fail(fmt.Errorf("%s: %w", f.Path, err))
			return
		}
		done += n
		full := filepath.Join(r.Dir, filepath.FromSlash(local))
		s.history.recordReceived(path.Base(f.Path), n, full, "tailcat")
		if !seenTop[top] {
			seenTop[top] = true
			savedTops = append(savedTops, filepath.Join(r.Dir, renamed[top]))
		}
		m.update(r, func(r *tailcatReceive) { r.Received, r.Saved = done, append([]string(nil), savedTops...) })
	}
	m.update(r, func(r *tailcatReceive) { r.Done, r.Current = true, "" })
	emit(true)
	tele.event("tailcat_received")
}

// receiveFile copies one remote file to local (slash path under root) via a
// temp name, renaming into place only once complete.
// The server can't stream more than the size it listed: a share that lies
// about sizes can't fill the disk behind the preview's back.
func (s *server) receiveFile(sess *tailcatSession, root *os.Root, remote, local string, size int64, progress func(int64)) (int64, error) {
	if dir := path.Dir(local); dir != "." {
		if err := root.MkdirAll(filepath.FromSlash(dir), 0o755); err != nil {
			return 0, err
		}
	}
	rf, err := sess.sf.Open(remote)
	if err != nil {
		return 0, err
	}
	defer rf.Close()
	tmp := filepath.FromSlash(local) + tailcatPartSuffix
	lf, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	cw := &countingWriter{w: lf, fn: progress, limit: size}
	_, err = rf.WriteTo(cw)
	if cerr := lf.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		root.Remove(tmp)
		return 0, err
	}
	if err := root.Rename(tmp, filepath.FromSlash(local)); err != nil {
		root.Remove(tmp)
		return 0, err
	}
	return cw.n, nil
}

type countingWriter struct {
	w     io.Writer
	n     int64
	fn    func(int64)
	limit int64 // max bytes accepted
}

var errTailcatOversize = errors.New("the share sent more data than it listed for this file")

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.n+int64(len(p)) > c.limit {
		return 0, errTailcatOversize
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.fn(c.n)
	return n, err
}

// --- HTTP -----------------------------------------------------------------

// handleTailcatInspect lists a share before the user commits to it.
//
//	POST /api/tailcat/inspect {"input": "<address, command or link>"}
func (s *server) handleTailcatInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Input string `json:"input"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	addr, ok := parseTailcatInput(req.Input)
	if !ok {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "no tailcat address found (it starts with \"tc\")"})
		return
	}
	bin := s.tailcat.findBin()
	if bin == "" {
		writeJSONStatus(w, http.StatusPreconditionFailed, map[string]any{"error": errTailcatMissing.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), tailcatDialTimeout+30*time.Second)
	defer cancel()
	sess, err := dialTailcat(ctx, bin, addr)
	if err != nil {
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer sess.Close()
	files, err := sess.list()
	if err != nil {
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	writeJSON(w, map[string]any{"addr": addr, "files": files, "total": total})
}

// handleTailcatReceive:
//
//	POST   /api/tailcat/receive       {"addr", "dir"} → start a download job
//	DELETE /api/tailcat/receive/<id>  cancel a running job / dismiss a finished one
//	DELETE /api/tailcat/incoming      dismiss a pending owldrop:// link
func (s *server) handleTailcatReceive(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/tailcat/incoming" && r.Method == http.MethodDelete:
		s.tailcat.takeIncoming()
		writeJSON(w, map[string]any{"ok": true})
	case r.URL.Path == "/api/tailcat/receive" && r.Method == http.MethodPost:
		var req struct {
			Addr  string `json:"addr"`
			Dir   string `json:"dir"`
			Total int64  `json:"total"` // from the inspect preview; the download refuses a bigger share
		}
		if err := decodeJSON(r, &req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		addr, ok := parseTailcatInput(req.Addr)
		if !ok {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "no tailcat address found"})
			return
		}
		if req.Dir == "" {
			req.Dir = s.saveDir()
		}
		rcv, err := s.startReceive(addr, req.Dir, req.Total)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errTailcatMissing) {
				status = http.StatusPreconditionFailed
			}
			writeJSONStatus(w, status, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, s.tailcat.snapshot(rcv))
	case strings.HasPrefix(r.URL.Path, "/api/tailcat/receive/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/api/tailcat/receive/")
		m := s.tailcat
		m.mu.Lock()
		rcv, ok := m.receives[id]
		if ok && rcv.Done {
			delete(m.receives, id)
		}
		m.mu.Unlock()
		if !ok {
			writeJSONStatus(w, http.StatusNotFound, map[string]any{"error": "no such download"})
			return
		}
		rcv.cancel()
		s.broadcastTailcat()
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
