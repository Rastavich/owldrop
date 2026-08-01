package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Drop links are short-lived, capability-URL invitations to upload files
// into this machine's inbox. The random token in the URL is the only
// authentication; links expire by time or by use count and can be revoked.
// Uploaded files are quarantined on disk and appear in the inbox like any
// other incoming file (with "via drop link" attribution), subject to the
// same save/delete and risky-open handling.

const maxDropUploadSize = 4 << 30 // 4 GiB, matching Taildrop's cap

type dropLink struct {
	Token   string    `json:"token"`
	Name    string    `json:"name"` // optional sender label
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	MaxUses int       `json:"maxUses"` // 0 = unlimited
	Uses    int       `json:"uses"`
	Revoked bool      `json:"revoked"`
}

// linkFile is one uploaded file sitting in the quarantine area, exposed to
// the UI as an inbox item.
type linkFile struct {
	Token   string    `json:"-"`
	Name    string    `json:"name"`
	Path    string    `json:"-"`
	Size    int64     `json:"size"`
	Arrived time.Time `json:"arrived"`
	Sender  string    `json:"sender,omitempty"`
}

type dropManager struct {
	mu    sync.Mutex
	path  string // config file (droplinks.json)
	dir   string // quarantine dir (drops/)
	links map[string]*dropLink
	files map[string]*linkFile // inbox basename → uploaded file
}

func newDropManager(cfgDir string) *dropManager {
	m := &dropManager{
		path:  filepath.Join(cfgDir, "droplinks.json"),
		dir:   filepath.Join(cfgDir, "drops"),
		links: map[string]*dropLink{},
		files: map[string]*linkFile{},
	}
	m.load()
	return m
}

func (m *dropManager) load() {
	if b, err := os.ReadFile(m.path); err == nil {
		var links []*dropLink
		if json.Unmarshal(b, &links) == nil {
			for _, l := range links {
				m.links[l.Token] = l
			}
		}
	}
	// Rebuild the file registry from quarantine dirs so inbox items survive
	// restarts. Links are only stored if their token is still known; orphaned
	// files (e.g. link revoked and cleaned) are left for the OS to reap.
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		link := m.links[ent.Name()]
		if link == nil {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(m.dir, ent.Name()))
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			fi, err := f.Info()
			if err != nil {
				continue
			}
			p := filepath.Join(m.dir, ent.Name(), f.Name())
			m.files[f.Name()] = &linkFile{
				Token:   ent.Name(),
				Name:    f.Name(),
				Path:    p,
				Size:    fi.Size(),
				Arrived: fi.ModTime(),
				Sender:  link.Name,
			}
		}
	}
}

func (m *dropManager) persist() {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.MarshalIndent(m.linkListLocked(), "", "  ")
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	os.Rename(tmp, m.path)
}

func (m *dropManager) linkListLocked() []*dropLink {
	out := make([]*dropLink, 0, len(m.links))
	for _, l := range m.links {
		out = append(out, l)
	}
	return out
}

// create makes a new link; ttl is a duration, maxUses 0 = unlimited.
func (m *dropManager) create(name string, ttl time.Duration, maxUses int) *dropLink {
	tok := make([]byte, 16)
	rand.Read(tok)
	l := &dropLink{
		Token:   hex.EncodeToString(tok),
		Name:    name,
		Created: time.Now(),
		Expires: time.Now().Add(ttl),
		MaxUses: maxUses,
	}
	m.mu.Lock()
	m.links[l.Token] = l
	m.mu.Unlock()
	m.persist()
	return l
}

// usable reports whether the link currently accepts uploads.
func (m *dropManager) usable(token string) (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.links[token]
	if l == nil {
		return false, "This drop link doesn't exist."
	}
	if l.Revoked {
		return false, "This drop link has been revoked."
	}
	if time.Now().After(l.Expires) {
		return false, "This drop link has expired."
	}
	if l.MaxUses > 0 && l.Uses >= l.MaxUses {
		return false, "This drop link has already been used."
	}
	return true, ""
}

func (m *dropManager) get(token string) *dropLink {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.links[token]
}

func (m *dropManager) revoke(token string) {
	m.mu.Lock()
	if l := m.links[token]; l != nil {
		l.Revoked = true
	}
	m.mu.Unlock()
	m.persist()
}

func (m *dropManager) list() []*dropLink {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.linkListLocked()
}

func (m *dropManager) file(name string) *linkFile {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.files[name]
}

func (m *dropManager) removeFile(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, name)
}

// upload accepts one multipart file for the given link. The name is made
// unique against everything already in the combined inbox.
func (m *dropManager) upload(token string, part multipart.File, size int64, baseName string, inboxHas func(string) bool) (*linkFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.links[token]
	if l == nil {
		return nil, errors.New("no such link")
	}
	dir := filepath.Join(m.dir, token)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := uniqueName(baseName, func(n string) bool {
		_, exists := m.files[n]
		return exists || inboxHas(n)
	})
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := io.Copy(f, part); err != nil {
		os.Remove(f.Name())
		return nil, err
	}
	lf := &linkFile{
		Token:   token,
		Name:    name,
		Path:    f.Name(),
		Size:    size,
		Arrived: time.Now(),
		Sender:  l.Name,
	}
	m.files[name] = lf
	l.Uses++
	return lf, nil
}

// linkInbox returns the current uploaded files as waitingFile items.
func (m *dropManager) linkInbox() []waitingFile {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]waitingFile, 0, len(m.files))
	for _, f := range m.files {
		out = append(out, waitingFile{
			Name:    f.Name,
			Size:    f.Size,
			Arrived: f.Arrived,
			Source:  "link",
			Sender:  f.Sender,
		})
	}
	return out
}

// --- HTTP handlers (public: the URL token is the auth) --------------------

func (s *server) handleDropPage(w http.ResponseWriter, r *http.Request) {
	token := dropTokenFromPath(r.URL.Path)
	if ok, msg := s.drops.usable(token); !ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>Drop link</title>"+
			"<body style='font-family:system-ui;background:#0b0e14;color:#e8ebf3;display:grid;place-items:center;height:100vh;margin:0'>"+
			"<p style='font-size:15px'>%s</p></body>", htmlEscape(msg))
		return
	}
	l := s.drops.get(token)
	html := strings.ReplaceAll(dropPageHTML, "__NAME__", htmlEscape(l.Name))
	html = strings.ReplaceAll(html, "__EXPIRES__", l.Expires.Format("Mon 2 Jan 15:04"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.WriteString(w, html)
}

func (s *server) handleDropUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := dropTokenFromPath(r.URL.Path)
	if ok, msg := s.drops.usable(token); !ok {
		writeJSONStatus(w, http.StatusGone, map[string]any{"error": msg})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDropUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "upload too large or malformed"})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "no file part named \"file\""})
		return
	}
	defer file.Close()
	base := filepath.Base(header.Filename)
	if base == "." || base == "" {
		base = "upload.bin"
	}

	// Names must be unique against the whole inbox (daemon + other links).
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	inboxHas := func(n string) bool {
		files, err := tsInbox(ctx)
		if err != nil {
			return false
		}
		for _, f := range files {
			if f.Name == n {
				return true
			}
		}
		return false
	}

	lf, err := s.drops.upload(token, file, header.Size, base, inboxHas)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.history.recordArrivals([]waitingFile{{
		Name: lf.Name, Size: lf.Size, Arrived: lf.Arrived, Source: "link", Sender: lf.Sender,
	}})
	s.broadcastInboxNow()
	writeJSON(w, map[string]any{"ok": true, "name": lf.Name, "size": lf.Size})
}

func dropTokenFromPath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;").Replace(s)
}

// uniqueName returns base possibly suffixed with " (N)" so no collision
// with existing names (per the taken predicate).
func uniqueName(base string, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if !taken(cand) {
			return cand
		}
	}
}

// --- server integration ------------------------------------------------

// combinedInbox is the daemon's waiting files plus drop-link uploads.
func (s *server) combinedInbox(ctx context.Context) ([]waitingFile, error) {
	files, err := tsInbox(ctx)
	if err != nil {
		return nil, err
	}
	files = append(files, s.drops.linkInbox()...)
	return files, nil
}

// linkSave moves a quarantined upload into dir (conflict-renamed), then
// removes it from the quarantine. Mirrors tsSaveFile for daemon files.
func (s *server) linkSave(name, dir string) (string, error) {
	lf := s.drops.file(name)
	if lf == nil {
		return "", errors.New("no such drop-link file")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	target := conflictFreeName(dir, name)
	src, err := os.Open(lf.Path)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(target)
		return "", err
	}
	if err := dst.Close(); err != nil {
		os.Remove(target)
		return "", err
	}
	os.Remove(lf.Path)
	s.drops.removeFile(name)
	s.history.recordSaved(name, target)
	return target, nil
}

// dropPageHTML is the minimal public upload page served at /drop/<token>.
// The URL token is embedded in the page's own origin; nothing else is.
const dropPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Send a file</title>
<style>
:root{--bg:#0b0e14;--panel:#12161f;--panel2:#181e2c;--border:#232b3d;--text:#e8ebf3;--muted:#8a93a8;--accent:#5f6cff;--green:#34d399;--red:#f87171}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:15px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;min-height:100vh;display:flex;flex-direction:column;align-items:center;justify-content:center;padding:24px}
.card{width:100%;max-width:460px;background:var(--panel);border:1px solid var(--border);border-radius:14px;padding:26px;text-align:center}
h1{margin:0 0 6px;font-size:20px}
.meta{color:var(--muted);font-size:13px;margin-bottom:22px}
.drop{border:1.5px dashed #2c3550;border-radius:12px;padding:36px 18px;cursor:pointer;transition:border-color .15s,background .15s}
.drop.over{border-color:var(--accent);background:rgba(95,108,255,.08)}
.drop p{margin:0;color:var(--muted)}
.drop .t{color:var(--text);font-weight:600;margin-bottom:4px}
#status{margin-top:18px;font-size:13.5px;min-height:22px}
#status.ok{color:var(--green)}
#status.err{color:var(--red)}
.bar{height:6px;border-radius:99px;background:var(--panel2);overflow:hidden;margin-top:10px;display:none}
.bar.on{display:block}
.bar .fill{height:100%;width:0;background:linear-gradient(90deg,var(--accent),#8b5cf6);transition:width .15s}
</style>
</head>
<body>
<div class="card">
  <h1>Send a file</h1>
  <p class="meta" id="meta">to <b>__NAME__</b> · link expires __EXPIRES__</p>
  <div class="drop" id="drop">
    <p class="t">Drop your file here</p>
    <p>or click to choose</p>
  </div>
  <input type="file" id="file" hidden>
  <div class="bar" id="bar"><div class="fill" id="fill"></div></div>
  <div id="status"></div>
</div>
<script>
const token = location.pathname.split('/')[2];
const drop = document.getElementById('drop');
const status = document.getElementById('status');
const bar = document.getElementById('bar');
const fill = document.getElementById('fill');
function setStatus(msg, kind) { status.textContent = msg; status.className = kind || ''; }
drop.addEventListener('click', () => document.getElementById('file').click());
document.getElementById('file').addEventListener('change', e => { if (e.target.files.length) send(e.target.files[0]); e.target.value = ''; });
drop.addEventListener('dragover', e => { e.preventDefault(); drop.classList.add('over'); });
drop.addEventListener('dragleave', () => drop.classList.remove('over'));
drop.addEventListener('drop', e => { e.preventDefault(); drop.classList.remove('over'); if (e.dataTransfer.files.length) send(e.dataTransfer.files[0]); });
async function send(file) {
  const fd = new FormData();
  fd.append('file', file);
  setStatus('Uploading ' + file.name + '…');
  bar.classList.add('on');
  fill.style.width = '0';
  try {
    await new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/drop/' + token + '/upload');
      xhr.upload.onprogress = e => { if (e.lengthComputable) fill.style.width = Math.round(e.loaded * 100 / e.total) + '%'; };
      xhr.onload = () => { if (xhr.status === 200) resolve(); else { try { reject(new Error(JSON.parse(xhr.responseText).error || 'upload failed')); } catch { reject(new Error('upload failed (' + xhr.status + ')')); } } };
      xhr.onerror = () => reject(new Error('network error'));
      xhr.send(fd);
    });
    bar.classList.remove('on');
    setStatus('Sent! ' + file.name + ' arrived. You can close this page.', 'ok');
  } catch (e) {
    bar.classList.remove('on');
    setStatus(e.message, 'err');
  }
}
</script>
</body>
</html>`
