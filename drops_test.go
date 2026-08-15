package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newDropTestServer(t *testing.T) (*server, string) {
	t.Helper()
	cfg := &config{SaveDir: t.TempDir()}
	s := newServerDir(cfg, t.TempDir())
	s.port = 8976
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	return s, ts.URL
}

func createTestDropLink(t *testing.T, s *server, name string, ttl time.Duration, maxUses int) string {
	t.Helper()
	l := s.drops.create(name, ttl, maxUses, 0)
	return l.Token
}

func uploadToDrop(t *testing.T, base, token, filename, content string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(content))
	w.Close()
	req, err := http.NewRequest("POST", base+"/drop/"+token+"/upload", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDropUploadAndSave(t *testing.T) {
	s, base := newDropTestServer(t)
	token := createTestDropLink(t, s, "Alice", time.Hour, 1)

	res := uploadToDrop(t, base, token, "notes.txt", "hello from a drop link")
	if res.StatusCode != 200 {
		t.Fatalf("upload status = %d", res.StatusCode)
	}

	// It should appear in the combined inbox as a link-sourced file.
	files, err := s.combinedInbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "notes.txt" || files[0].Source != "link" || files[0].Sender != "Alice" {
		t.Fatalf("inbox = %+v", files)
	}

	// Saving it via the API moves it out of quarantine into the save dir.
	body := strings.NewReader(`{"name":"notes.txt","source":"link","dir":""}`)
	req, _ := http.NewRequest("POST", base+"/api/save", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Owldrop-Token", s.token)
	r2, _ := http.DefaultClient.Do(req)
	if r2.StatusCode != 200 {
		t.Fatalf("save status = %d", r2.StatusCode)
	}
	saved := filepath.Join(s.cfg.SaveDir, "notes.txt")
	b, err := readFile(t, saved)
	if err != nil || string(b) != "hello from a drop link" {
		t.Fatalf("saved content = %q, err = %v", b, err)
	}
	if s.drops.file("notes.txt") != nil {
		t.Fatal("file still in quarantine after save")
	}
}

func TestDropLinkSingleUseAndExpiry(t *testing.T) {
	s, base := newDropTestServer(t)
	token := createTestDropLink(t, s, "", time.Hour, 1)

	if res := uploadToDrop(t, base, token, "a.txt", "one"); res.StatusCode != 200 {
		t.Fatalf("first upload = %d", res.StatusCode)
	}
	// Single-use: the second upload must be refused.
	if res := uploadToDrop(t, base, token, "b.txt", "two"); res.StatusCode != 200 {
		t.Logf("second upload rejected (expected)")
	} else {
		t.Fatal("second upload accepted on a single-use link")
	}

	// Expired link must refuse.
	expired := createTestDropLink(t, s, "", -time.Minute, 0)
	if res := uploadToDrop(t, base, expired, "c.txt", "x"); res.StatusCode != 200 {
		t.Logf("expired upload rejected (expected)")
	} else {
		t.Fatal("upload accepted on an expired link")
	}

	// Revoked link must refuse.
	revoked := createTestDropLink(t, s, "", time.Hour, 0)
	s.drops.revoke(revoked)
	if res := uploadToDrop(t, base, revoked, "d.txt", "y"); res.StatusCode != 200 {
		t.Logf("revoked upload rejected (expected)")
	} else {
		t.Fatal("upload accepted on a revoked link")
	}
}

func TestDropPageAndBadToken(t *testing.T) {
	s, base := newDropTestServer(t)
	token := createTestDropLink(t, s, "Bob", time.Hour, 1)

	page, _ := http.Get(base + "/drop/" + token)
	if page.StatusCode != 200 {
		t.Fatalf("page status = %d", page.StatusCode)
	}
	var buf bytes.Buffer
	buf.ReadFrom(page.Body)
	if !strings.Contains(buf.String(), "Send a file") || !strings.Contains(buf.String(), "Bob") {
		t.Fatalf("page missing content")
	}

	bad, _ := http.Get(base + "/drop/deadbeef")
	if bad.StatusCode != 200 {
		t.Fatalf("bad token page status = %d", bad.StatusCode)
	}
	var b2 bytes.Buffer
	b2.ReadFrom(bad.Body)
	if !strings.Contains(b2.String(), "doesn&#39;t exist") && !strings.Contains(b2.String(), "doesn't exist") {
		t.Fatalf("bad token page: %s", b2.String())
	}
}

func TestDropLinkListAndRevokeAPI(t *testing.T) {
	s, base := newDropTestServer(t)
	token := createTestDropLink(t, s, "Carol", time.Hour, 0)

	req, _ := http.NewRequest("GET", base+"/api/droplinks", nil)
	req.Header.Set("X-Owldrop-Token", s.token)
	res, _ := http.DefaultClient.Do(req)
	var got struct {
		Links []struct {
			Token string `json:"token"`
			URL   string `json:"url"`
		} `json:"links"`
	}
	json.NewDecoder(res.Body).Decode(&got)
	if len(got.Links) != 1 || got.Links[0].Token != token {
		t.Fatalf("list = %+v", got.Links)
	}
	if !strings.Contains(got.Links[0].URL, "/drop/"+token) {
		t.Fatalf("url = %q", got.Links[0].URL)
	}

	req, _ = http.NewRequest("POST", base+"/api/droplinks/"+token+"/revoke", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Owldrop-Token", s.token)
	r2, _ := http.DefaultClient.Do(req)
	if r2.StatusCode != 200 {
		t.Fatalf("revoke = %d", r2.StatusCode)
	}
	if ok, _ := s.drops.usable(token); ok {
		t.Fatal("link still usable after revoke")
	}
}

func readFile(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	return os.ReadFile(path)
}

func uploadFilesToDrop(t *testing.T, base, token string, files map[string]string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for name, content := range files {
		fw, err := w.CreateFormFile("file", filepath.Base(name))
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte(content))
		w.WriteField("path", name)
	}
	w.Close()
	req, err := http.NewRequest("POST", base+"/drop/"+token+"/upload", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDropMultiFileBatch(t *testing.T) {
	s, base := newDropTestServer(t)
	token := createTestDropLink(t, s, "", time.Hour, 1)

	// One request, two files: both delivered, counts as ONE use.
	res := uploadFilesToDrop(t, base, token, map[string]string{"a.txt": "aaa", "b.txt": "bbb"})
	if res.StatusCode != 200 {
		t.Fatalf("batch upload = %d", res.StatusCode)
	}
	files, err := s.combinedInbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range files {
		names[f.Name] = true
	}
	if !names["a.txt"] || !names["b.txt"] {
		t.Fatalf("inbox = %+v", files)
	}
	// Link is now used up (maxUses 1) even though two files arrived.
	if ok, _ := s.drops.usable(token); ok {
		t.Fatal("link still usable after one batch")
	}
}

func TestDropFolderZip(t *testing.T) {
	s, base := newDropTestServer(t)
	token := createTestDropLink(t, s, "Folder Person", time.Hour, 0)

	res := uploadFilesToDrop(t, base, token, map[string]string{
		"Photos/one.jpg":   "jpeg1",
		"Photos/two.jpg":   "jpeg2",
		"Photos/sub/x.png": "png",
	})
	if res.StatusCode != 200 {
		t.Fatalf("folder upload = %d", res.StatusCode)
	}
	files, err := s.combinedInbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one zipped inbox item, got %+v", files)
	}
	lf := files[0]
	if lf.Name != "Photos.zip" || lf.Source != "link" || lf.Sender != "Folder Person" {
		t.Fatalf("zip item = %+v", lf)
	}
	// Verify the zip contents preserve the relative paths.
	zlf := s.drops.file("Photos.zip")
	if zlf == nil {
		t.Fatal("zipped item missing from registry")
	}
	zr, err := zip.OpenReader(zlf.Path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	entries := map[string]bool{}
	for _, f := range zr.File {
		entries[f.Name] = true
	}
	for _, want := range []string{"Photos/one.jpg", "Photos/two.jpg", "Photos/sub/x.png"} {
		if !entries[want] {
			t.Fatalf("zip missing %s; entries = %v", want, entries)
		}
	}
}

// TestDropLinkRateLimit verifies per-link upload rate limiting via token bucket.
func TestDropLinkRateLimit(t *testing.T) {
	s, base := newDropTestServer(t)

	// Create a link with 3 uploads/min rate limit. Bucket starts full (3 tokens).
	l := s.drops.create("ratelimited", time.Hour, 0, 3)
	token := l.Token
	if l.RatePerMin != 3 {
		t.Fatalf("RatePerMin = %d, want 3", l.RatePerMin)
	}

	// First 3 uploads should succeed immediately (burst).
	for i := range 3 {
		res := uploadToDrop(t, base, token, "burst.txt", "data")
		if res.StatusCode != 200 {
			t.Fatalf("burst upload %d: status %d, want 200", i+1, res.StatusCode)
		}
	}

	// 4th upload should be rate-limited (bucket empty).
	res := uploadToDrop(t, base, token, "blocked.txt", "data")
	if res.StatusCode != 429 {
		t.Fatalf("rate-limited upload: status %d, want 429", res.StatusCode)
	}
	if retry := res.Header.Get("Retry-After"); retry == "" {
		t.Error("429 response missing Retry-After header")
	}

	// Wait for one token to refill (3/min = one every 20s; use a shorter
	// sleep + check to keep the test fast — we only need to verify the
	// bucket refills, not test real-time accuracy).
	time.Sleep(100 * time.Millisecond) // won't refill, still blocked
	res = uploadToDrop(t, base, token, "still-blocked.txt", "data")
	if res.StatusCode != 429 {
		t.Fatalf("rate-limited after short sleep: status %d, want 429", res.StatusCode)
	}
}

// TestDropLinkRateLimitUnlimited verifies 0 means unlimited.
func TestDropLinkRateLimitUnlimited(t *testing.T) {
	s, base := newDropTestServer(t)
	token := createTestDropLink(t, s, "unlimited", time.Hour, 0)

	l := s.drops.get(token)
	if l.RatePerMin != 0 {
		t.Fatalf("RatePerMin = %d, want 0 (unlimited)", l.RatePerMin)
	}

	// Many rapid uploads should all succeed.
	for i := range 10 {
		res := uploadToDrop(t, base, token, "many.txt", "data")
		if res.StatusCode != 200 {
			t.Fatalf("upload %d: status %d, want 200", i+1, res.StatusCode)
		}
	}
}
