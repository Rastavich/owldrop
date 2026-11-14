package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeTailcat writes a shell script standing in for the tailcat CLI. In
// "ok" mode it records its argv, prints the JSON address and blocks; in
// "fail" mode it prints an error to stderr and exits non-zero.
func fakeTailcat(t *testing.T, mode string) (bin, argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tailcat is a shell script")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "tailcat")
	argsFile = filepath.Join(dir, "args")
	var script string
	switch mode {
	case "ok":
		script = "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n" +
			"echo '# 🐈 Server listening with new address: tcFAKE' >&2\n" +
			"echo '{\"listenAddr\":\"tcFAKEaddr\"}'\nexec sleep 600\n"
	case "fail":
		script = "#!/bin/sh\necho '# Selected bootstrap relay region 1' >&2\necho 'Server.Start: boom' >&2\nexit 1\n"
	}
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

func newTailcatTestServer(t *testing.T, bin string) (*server, string) {
	t.Helper()
	s, base := newSyncTestServer(t)
	s.tailcat.findBin = func() string { return bin }
	t.Cleanup(s.tailcat.stopAll)
	return s, base
}

func postTailcatFiles(t *testing.T, s *server, base string, names ...string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, n := range names {
		fw, err := mw.CreateFormFile("file", n)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte("payload of " + n))
	}
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, base+"/api/tailcat/shares?ttl=5", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Owldrop-Token", s.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func decodeBody[T any](t *testing.T, res *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTailcatShareSingleFile(t *testing.T) {
	bin, argsFile := fakeTailcat(t, "ok")
	s, base := newTailcatTestServer(t, bin)

	res := postTailcatFiles(t, s, base, "report final.pdf")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create: %d", res.StatusCode)
	}
	sh := decodeBody[tailcatShare](t, res)
	if sh.Addr != "tcFAKEaddr" || sh.Folder != "" || len(sh.Files) != 1 {
		t.Fatalf("share = %+v", sh)
	}
	if want := "tailcat cp 'tcFAKEaddr:report final.pdf' ."; sh.Command != want {
		t.Fatalf("command = %q, want %q", sh.Command, want)
	}
	if d := time.Until(sh.ExpiresAt); d < 4*time.Minute || d > 5*time.Minute {
		t.Fatalf("expiry in %v, want ~5m", d)
	}

	// The process got a read-only, ephemeral-key invocation over the staging dir.
	args, _ := os.ReadFile(argsFile)
	dir := filepath.Join(s.tailcat.root, sh.ID)
	want := strings.Join(tailcatServeArgs(dir), "\n") + "\n"
	if string(args) != want {
		t.Fatalf("argv:\n%s\nwant:\n%s", args, want)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "report final.pdf")); string(got) != "payload of report final.pdf" {
		t.Fatalf("staged content = %q", got)
	}

	// Stop: process gone, staging gone, list empty.
	req, _ := http.NewRequest(http.MethodDelete, base+"/api/tailcat/shares/"+sh.ID, nil)
	req.Header.Set("X-Owldrop-Token", s.token)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	del.Body.Close()
	if del.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", del.StatusCode)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("staging dir still exists: %v", err)
	}
	if n := len(s.tailcat.list()); n != 0 {
		t.Fatalf("%d shares left", n)
	}
}

func TestTailcatShareSeveralFilesFolded(t *testing.T) {
	bin, _ := fakeTailcat(t, "ok")
	s, base := newTailcatTestServer(t, bin)

	res := postTailcatFiles(t, s, base, "a.txt", "a.txt", "b.png")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create: %d", res.StatusCode)
	}
	sh := decodeBody[tailcatShare](t, res)
	if sh.Folder == "" || len(sh.Files) != 3 || sh.Files[1].Name != "a (1).txt" {
		t.Fatalf("share = %+v", sh)
	}
	if want := "tailcat cp -r tcFAKEaddr:" + sh.Folder + " ."; sh.Command != want {
		t.Fatalf("command = %q, want %q", sh.Command, want)
	}
	for _, f := range sh.Files {
		if _, err := os.Stat(filepath.Join(s.tailcat.root, sh.ID, sh.Folder, f.Name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTailcatListNeedsToken(t *testing.T) {
	bin, _ := fakeTailcat(t, "ok")
	s, base := newTailcatTestServer(t, bin)
	postTailcatFiles(t, s, base, "x.txt")

	// The list carries share addresses (credentials): no token, no list.
	res := syncReq(t, base, "", http.MethodGet, "/api/tailcat", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("GET without token: %d", res.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/api/tailcat", nil)
	req.Header.Set("X-Owldrop-Token", s.token)
	ok, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer ok.Body.Close()
	st := decodeBody[struct {
		Installed bool           `json:"installed"`
		Shares    []tailcatShare `json:"shares"`
	}](t, ok)
	if !st.Installed || len(st.Shares) != 1 || st.Shares[0].Addr != "tcFAKEaddr" {
		t.Fatalf("state = %+v", st)
	}
}

func TestTailcatStartFailureCleansUp(t *testing.T) {
	bin, _ := fakeTailcat(t, "fail")
	s, base := newTailcatTestServer(t, bin)

	res := postTailcatFiles(t, s, base, "x.txt")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", res.StatusCode)
	}
	body := decodeBody[map[string]string](t, res)
	if !strings.Contains(body["error"], "boom") || strings.Contains(body["error"], "bootstrap") {
		t.Fatalf("error = %q", body["error"])
	}
	entries, _ := os.ReadDir(s.tailcat.root)
	if len(entries) != 0 {
		t.Fatalf("leftover staging: %v", entries)
	}
}

func TestTailcatNotInstalled(t *testing.T) {
	s, base := newTailcatTestServer(t, "")
	res := postTailcatFiles(t, s, base, "x.txt")
	if res.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestTailcatRejectsBadNames(t *testing.T) {
	bin, _ := fakeTailcat(t, "ok")
	s, base := newTailcatTestServer(t, bin)
	for _, n := range []string{"..", ".hidden"} {
		if res := postTailcatFiles(t, s, base, n); res.StatusCode != http.StatusBadRequest {
			t.Fatalf("%q: status = %d", n, res.StatusCode)
		}
	}
	// Path components are stripped, not trusted.
	res := postTailcatFiles(t, s, base, "../../etc/passwd")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if sh := decodeBody[tailcatShare](t, res); sh.Files[0].Name != "passwd" {
		t.Fatalf("name = %q", sh.Files[0].Name)
	}
}

func TestTailcatSharePendingFile(t *testing.T) {
	bin, _ := fakeTailcat(t, "ok")
	s, base := newTailcatTestServer(t, bin)
	src := filepath.Join(t.TempDir(), "photo.jpg")
	os.WriteFile(src, []byte("jpeg"), 0o644)
	s.enqueueShare([]string{src})
	pending := s.pendingShares()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v", pending)
	}

	req, _ := http.NewRequest(http.MethodPost, base+"/api/tailcat/shares", strings.NewReader(`{"pending":"`+pending[0].ID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Owldrop-Token", s.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	sh := decodeBody[tailcatShare](t, res)
	if sh.Files[0].Name != "photo.jpg" || sh.Files[0].Size != 4 {
		t.Fatalf("share = %+v", sh)
	}
	if len(s.pendingShares()) != 0 {
		t.Fatal("pending file not consumed")
	}
	// The original survives the share being stopped.
	s.tailcat.stop(sh.ID)
	if _, err := os.Stat(src); err != nil {
		t.Fatal(err)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"tcABC_-x:file.pdf": "tcABC_-x:file.pdf",
		"tc:my file.pdf":    "'tc:my file.pdf'",
		"tc:it's.txt":       `'tc:it'\''s.txt'`,
		"tc:$(rm -rf).txt":  "'tc:$(rm -rf).txt'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTailcatRealE2E shares a file through the real tailcat CLI and fetches
// it back with `tailcat cp` over the internet (DERP). Opt-in: needs tailcat,
// scp and network access. OWLDROP_TAILCAT_E2E=1 go test -run RealE2E .
func TestTailcatRealE2E(t *testing.T) {
	if os.Getenv("OWLDROP_TAILCAT_E2E") == "" {
		t.Skip("set OWLDROP_TAILCAT_E2E=1")
	}
	bin := findTailcat()
	if bin == "" {
		t.Skip("tailcat not installed")
	}
	s, base := newTailcatTestServer(t, bin)
	res := postTailcatFiles(t, s, base, "hello world.txt", "second.txt")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create: %d %v", res.StatusCode, decodeBody[map[string]string](t, res))
	}
	sh := decodeBody[tailcatShare](t, res)
	t.Logf("recipient command: %s", sh.Command)

	out := t.TempDir()
	cmd := exec.Command("sh", "-c", sh.Command)
	cmd.Dir = out
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", sh.Command, err, b)
	}
	got, err := os.ReadFile(filepath.Join(out, sh.Folder, "hello world.txt"))
	if err != nil || string(got) != "payload of hello world.txt" {
		t.Fatalf("fetched %q, %v", got, err)
	}

	// Owldrop-to-Owldrop: the in-app receiver fetches the same share.
	dest := t.TempDir()
	rcv, err := s.startReceive(sh.Addr, dest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitReceive(t, s, rcv.ID); got.Err != "" || got.Files != 2 {
		t.Fatalf("in-app receive: %+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, sh.Folder, "second.txt")); string(b) != "payload of second.txt" {
		t.Fatalf("in-app receive content = %q", b)
	}

	// Read-only: uploading into the share must fail.
	up := filepath.Join(out, "evil.txt")
	os.WriteFile(up, []byte("x"), 0o644)
	if b, err := exec.Command(bin, "cp", up, sh.Addr+":"+sh.Folder+"/").CombinedOutput(); err == nil {
		t.Fatalf("upload into read-only share succeeded:\n%s", b)
	}

	// Stopped share: the address is dead.
	s.tailcat.stop(sh.ID)
	ctxCmd := exec.Command(bin, "ls", sh.Addr)
	done := make(chan error, 1)
	ctxCmd.Start()
	go func() { done <- ctxCmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ls on a stopped share succeeded")
		}
	case <-time.After(20 * time.Second):
		ctxCmd.Process.Kill() // hanging = unreachable, which is also fine
	}
}

func TestBundledTailcatPaths(t *testing.T) {
	has := func(paths []string, want string) bool {
		for _, p := range paths {
			if filepath.Clean(p) == filepath.Clean(want) {
				return true
			}
		}
		return false
	}
	for exe, want := range map[string]string{
		"/usr/local/bin/owldrop":                           "/usr/local/lib/owldrop/tailcat", // deb/rpm
		"/Applications/Owldrop.app/Contents/MacOS/owldrop": "/Applications/Owldrop.app/Contents/MacOS/tailcat",
		"/tmp/.mount_x/usr/bin/owldrop":                    "/tmp/.mount_x/usr/bin/tailcat", // AppImage
	} {
		if got := bundledTailcatPathsFor(exe, "tailcat"); !has(got, want) {
			t.Errorf("%s: %v lacks %s", exe, got, want)
		}
	}
}
