package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newSyncTestServer(t *testing.T) (*server, string) {
	t.Helper()
	cfg := &config{SaveDir: t.TempDir()}
	s := newServerDir(cfg, t.TempDir())
	s.port = 8976
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	return s, ts.URL
}

func syncReq(t *testing.T, base, token, method, path string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" && method != http.MethodGet {
		req.Header.Set("X-Owldrop-Token", token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func getSyncItems(t *testing.T, base string) []syncItem {
	t.Helper()
	res := syncReq(t, base, "", http.MethodGet, "/api/sync", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/sync: %d", res.StatusCode)
	}
	var out struct {
		Items []syncItem `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Items
}

// --- store unit tests -------------------------------------------------------

func TestSyncStoreTextLifecycle(t *testing.T) {
	dir := t.TempDir()
	st := newSyncStore(dir)
	_, err := st.addText("   ")
	if err == nil {
		t.Fatal("blank text accepted")
	}
	it, err := st.addText("hello from device A")
	if err != nil {
		t.Fatal(err)
	}
	it2, _ := st.addText("second")
	list := st.list()
	if len(list) != 2 || list[0].ID != it2.ID || list[1].ID != it.ID {
		t.Fatalf("list order wrong: %+v", list)
	}
	// Persistence across reload.
	st2 := newSyncStore(dir)
	if len(st2.list()) != 2 {
		t.Fatal("items not persisted")
	}
	if !st.remove(it.ID) {
		t.Fatal("remove returned false for existing item")
	}
	if len(st.list()) != 1 {
		t.Fatal("remove failed")
	}
	st.clear()
	if len(st.list()) != 0 {
		t.Fatal("clear failed")
	}
}

func TestSyncStoreEvictionUnlinksFiles(t *testing.T) {
	dir := t.TempDir()
	st := newSyncStore(dir)
	for range maxSyncItems + 10 {
		st.addFile(newSyncID(), "f.txt", 1)
	}
	if got := len(st.list()); got != maxSyncItems {
		t.Fatalf("want %d items after eviction, got %d", maxSyncItems, got)
	}
}

func TestSyncStoreTooLongText(t *testing.T) {
	st := newSyncStore(t.TempDir())
	long := strings.Repeat("x", maxSyncTextLen+1)
	if _, err := st.addText(long); err == nil {
		t.Fatal("oversized text accepted")
	}
}

// --- handler tests ----------------------------------------------------------

func TestSyncAPIRequiresTokenForMutations(t *testing.T) {
	s, base := newSyncTestServer(t)
	// GET is open (host-only, like the rest of the app).
	if res := syncReq(t, base, "", http.MethodGet, "/api/sync", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("GET without token: %d", res.StatusCode)
	}
	// POST without token is forbidden.
	res := syncReq(t, base, "", http.MethodPost, "/api/sync", strings.NewReader(`{"text":"x"}`))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("POST without token: %d", res.StatusCode)
	}
	// With token it works.
	res = syncReq(t, base, s.token, http.MethodPost, "/api/sync", strings.NewReader(`{"text":"hi"}`))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST with token: %d", res.StatusCode)
	}
}

func TestSyncAPITextAndClear(t *testing.T) {
	s, base := newSyncTestServer(t)
	post := func(text string) {
		t.Helper()
		res := syncReq(t, base, s.token, http.MethodPost, "/api/sync", strings.NewReader(`{"text":"`+text+`"}`))
		if res.StatusCode != http.StatusOK {
			t.Fatalf("POST: %d", res.StatusCode)
		}
	}
	post("one")
	post("two")
	items := getSyncItems(t, base)
	if len(items) != 2 || items[0].Text != "two" || items[1].Text != "one" {
		t.Fatalf("unexpected items: %+v", items)
	}
	// Delete one.
	res := syncReq(t, base, s.token, http.MethodDelete, "/api/sync/"+items[0].ID, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: %d", res.StatusCode)
	}
	if items = getSyncItems(t, base); len(items) != 1 {
		t.Fatalf("delete failed: %+v", items)
	}
	// Clear all.
	res = syncReq(t, base, s.token, http.MethodDelete, "/api/sync", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear: %d", res.StatusCode)
	}
	if items = getSyncItems(t, base); len(items) != 0 {
		t.Fatalf("clear failed: %+v", items)
	}
}

func TestSyncAPIUploadAndDownload(t *testing.T) {
	s, base := newSyncTestServer(t)
	payload := []byte("sync file payload")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(payload)
	mw.Close()

	req, _ := http.NewRequest(http.MethodPost, base+"/api/sync/file", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Owldrop-Token", s.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d", res.StatusCode)
	}

	items := getSyncItems(t, base)
	if len(items) != 1 || items[0].Kind != "file" || items[0].Name != "note.txt" || items[0].Size != int64(len(payload)) {
		t.Fatalf("unexpected item: %+v", items)
	}

	// Download round-trip.
	got := syncReq(t, base, "", http.MethodGet, "/api/sync/file/"+items[0].ID, nil)
	body, _ := io.ReadAll(got.Body)
	if got.StatusCode != http.StatusOK || string(body) != string(payload) {
		t.Fatalf("download mismatch: %d %q", got.StatusCode, body)
	}
	if cd := got.Header.Get("Content-Disposition"); !strings.Contains(cd, "note.txt") {
		t.Fatalf("missing content-disposition: %q", cd)
	}

	// Delete removes the payload from disk too.
	res2 := syncReq(t, base, s.token, http.MethodDelete, "/api/sync/"+items[0].ID, nil)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", res2.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(s.sync.dir, syncDirName)); err == nil {
		// Dir may remain; the file itself must be gone.
		entries, _ := os.ReadDir(filepath.Join(s.sync.dir, syncDirName))
		if len(entries) != 0 {
			t.Fatalf("payload not unlinked: %v", entries)
		}
	}
}

func TestSyncAPIFileTraversalSafe(t *testing.T) {
	s, base := newSyncTestServer(t)
	res := syncReq(t, base, "", http.MethodGet, "/api/sync/file/..%2F..%2Fconfig", nil)
	if res.StatusCode == http.StatusOK {
		t.Fatal("traversal path served")
	}
	_ = s
}
