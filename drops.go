package main

import (
	"archive/zip"
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
	mu       sync.Mutex
	path     string // config file (droplinks.json)
	dir      string // quarantine dir (drops/)
	links    map[string]*dropLink
	files    map[string]*linkFile // inbox basename → uploaded file
	uploadMu sync.Map // per-token *sync.Mutex: serializes uploads to one link (kills TOCTOU on maxUses/TTL + the concurrent map race)
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

// uploadLock returns the per-token mutex used to serialize uploads to a
// single link (see handleDropUpload).
func (m *dropManager) uploadLock(token string) *sync.Mutex {
	actual, _ := m.uploadMu.LoadOrStore(token, &sync.Mutex{})
	return actual.(*sync.Mutex)
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
	tele.event("drop_link_created")
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

// storeFile writes one uploaded file into the quarantine area and registers
// it as an inbox item. Does not touch the link's use count (see useOnce).
func (m *dropManager) storeFile(token, baseName string, size int64, content io.Reader, inboxHas func(string) bool) (*linkFile, error) {
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
	if _, err := io.Copy(f, content); err != nil {
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
	return lf, nil
}

// useOnce counts one upload batch against the link's use budget.
func (m *dropManager) useOnce(token string) {
	m.mu.Lock()
	if l := m.links[token]; l != nil {
		l.Uses++
	}
	m.mu.Unlock()
	m.persist()
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
	// Serialize uploads to this link: closes the TOCTOU between usable() and
	// useOnce() (maxUses/TTL bypass via concurrent uploads) and the
	// concurrent map access in storeFolderZip.
	mu := s.drops.uploadLock(token)
	mu.Lock()
	defer mu.Unlock()
	if ok, msg := s.drops.usable(token); !ok {
		writeJSONStatus(w, http.StatusGone, map[string]any{"error": msg})
		return
	}
	// Consume the use up front: a single-use link can't be raced past 1
	// upload, and a failed upload consumes the slot (safer default than
	// letting an attacker retry into a bypass).
	s.drops.useOnce(token)
	r.Body = http.MaxBytesReader(w, r.Body, maxDropUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "upload too large or malformed"})
		return
	}
	defer r.MultipartForm.RemoveAll() // never leak the 4 GiB temp spool
	parts := r.MultipartForm.File["file"]
	if len(parts) == 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "no file part named \"file\""})
		return
	}
	paths := r.MultipartForm.Value["path"]
	// Folder drops arrive with paths containing "/" (sent in the parallel
	// 'path' field because Go's multipart parser strips paths from part
	// filenames); zip those into a single inbox item.
	isFolder := false
	for _, p := range paths {
		if strings.Contains(p, "/") {
			isFolder = true
			break
		}
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

	var lfs []*linkFile
	if isFolder {
		lf, err := s.storeFolderZip(token, parts, paths, inboxHas)
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		lfs = []*linkFile{lf}
	} else {
		for _, p := range parts {
			f, err := p.Open()
			if err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			base := filepath.Base(p.Filename)
			if base == "." || base == "" {
				base = "upload.bin"
			}
			lf, err := s.drops.storeFile(token, base, p.Size, f, inboxHas)
			f.Close()
			if err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			lfs = append(lfs, lf)
		}
	}


	files := make([]waitingFile, 0, len(lfs))
	s.history.recordArrivals(files)
	s.broadcastInboxNow()
	names := make([]string, 0, len(lfs))
	var total int64
	for _, lf := range lfs {
		names = append(names, lf.Name)
		total += lf.Size
	}
	writeJSON(w, map[string]any{"ok": true, "names": names, "size": total})
}

// storeFolderZip zips a dropped folder into one quarantined inbox item named
// after the top-level folder. paths[i] is the relative path for parts[i]
// (already sanitized; empty falls back to the part's basename).
func (s *server) storeFolderZip(token string, parts []*multipart.FileHeader, paths []string, inboxHas func(string) bool) (*linkFile, error) {
	zipName := "folder.zip"
	for _, p := range paths {
		if top := strings.SplitN(sanitizeZipPath(p), "/", 2)[0]; top != "" {
			zipName = top
			break
		}
	}
	zipName += ".zip"

	dir := filepath.Join(s.drops.dir, token)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	finalName := uniqueName(zipName, func(n string) bool {
		s.drops.mu.Lock()
		_, exists := s.drops.files[n]
		s.drops.mu.Unlock()
		return exists || inboxHas(n)
	})
	out, err := os.OpenFile(filepath.Join(dir, finalName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	zw := zip.NewWriter(out)
	var total int64
	for i, p := range parts {
		entry := ""
		if i < len(paths) {
			entry = sanitizeZipPath(paths[i])
		}
		if entry == "" {
			entry = filepath.Base(p.Filename)
		}
		w, err := zw.Create(entry)
		if err != nil {
			out.Close()
			os.Remove(out.Name())
			return nil, err
		}
		f, err := p.Open()
		if err != nil {
			out.Close()
			os.Remove(out.Name())
			return nil, err
		}
		n, err := io.Copy(w, f)
		f.Close()
		if err != nil {
			out.Close()
			os.Remove(out.Name())
			return nil, err
		}
		total += n
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(out.Name())
		return nil, err
	}
	if err := out.Close(); err != nil {
		os.Remove(out.Name())
		return nil, err
	}
	lf := &linkFile{
		Token:   token,
		Name:    finalName,
		Path:    out.Name(),
		Size:    total,
		Arrived: time.Now(),
		Sender:  s.drops.get(token).Name,
	}
	s.drops.mu.Lock()
	s.drops.files[finalName] = lf
	s.drops.mu.Unlock()
	return lf, nil
}

// sanitizeZipPath makes a dropped relative path safe as a zip entry name:
// no leading slashes, no ".." segments.
func sanitizeZipPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
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
    <p class="t">Drop files or a folder here</p>
    <p>or click to choose</p>
  </div>
  <input type="file" id="file" multiple hidden>
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
document.getElementById('file').addEventListener('change', e => { if (e.target.files.length) send(Array.from(e.target.files)); e.target.value = ''; });
drop.addEventListener('dragover', e => { e.preventDefault(); drop.classList.add('over'); });
drop.addEventListener('dragleave', () => drop.classList.remove('over'));
drop.addEventListener('drop', e => { e.preventDefault(); drop.classList.remove('over'); if (e.dataTransfer.files.length) send(Array.from(e.dataTransfer.files)); });
async function send(files) {
  if (!files.length) return;
  const fd = new FormData();
  for (const file of files) {
    // Go's multipart parser strips paths from part filenames, so the folder
    // structure travels in a parallel 'path' field; the server re-zips it.
    fd.append('file', file, file.name);
    fd.append('path', file.webkitRelativePath || file.name);
  }
  const first = files[0];
  const what = files.length > 1 ? files.length + ' files'
    : (first.webkitRelativePath ? first.webkitRelativePath.split('/')[0] + ' folder' : first.name);
  setStatus('Uploading ' + what + '…');
  bar.classList.add('on');
  fill.style.width = '0';
  let data = null;
  try {
    await new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/drop/' + token + '/upload');
      xhr.upload.onprogress = e => { if (e.lengthComputable) fill.style.width = Math.round(e.loaded * 100 / e.total) + '%'; };
      xhr.onload = () => { if (xhr.status === 200) { try { data = JSON.parse(xhr.responseText); } catch {} resolve(); } else { try { reject(new Error(JSON.parse(xhr.responseText).error || 'upload failed')); } catch { reject(new Error('upload failed (' + xhr.status + ')')); } } };
      xhr.onerror = () => reject(new Error('network error'));
      xhr.send(fd);
    });
    bar.classList.remove('on');
    const n = data && data.names ? data.names.length : 1;
    setStatus('Sent! ' + (n > 1 ? n + ' files arrived' : what + ' arrived') + '. You can close this page.', 'ok');
  } catch (e) {
    bar.classList.remove('on');
    setStatus(e.message, 'err');
  }
}
</script>
</body>
</html>`
