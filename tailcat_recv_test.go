package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

const fakeAddr = "tcFAKEaddrFAKEaddrFAKEaddrFAKEaddrFAKEaddrFAKEaddr"

// TestMain lets the test binary double as the client half of a fake tailcat:
// `tailcat <addr> 22` becomes an SSH server on stdin/stdout that serves
// OWLDROP_FAKE_SHARE over SFTP, just like a real tailcat file share.
func TestMain(m *testing.M) {
	if dir := os.Getenv("OWLDROP_FAKE_SHARE"); dir != "" {
		serveFakeShare(dir)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func serveFakeShare(dir string) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := gossh.NewSignerFromKey(priv)
	cfg := &gossh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	_, chans, reqs, err := gossh.NewServerConn(&pipeConn{r: os.Stdin, w: os.Stdout}, cfg)
	if err != nil {
		return
	}
	go gossh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, _ := nc.Accept()
		go func() {
			for req := range creqs {
				ok := req.Type == "subsystem" && string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
				if ok {
					srv, _ := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(dir), sftp.ReadOnly())
					srv.Serve()
					ch.Close()
				}
			}
		}()
	}
}

// fakeTailcatClient writes a "tailcat" script that runs this test binary as
// the share server for dir.
func fakeTailcatClient(t *testing.T, dir string) string {
	t.Helper()
	if os.PathSeparator != '/' {
		t.Skip("fake tailcat is a shell script")
	}
	exe, _ := os.Executable()
	bin := filepath.Join(t.TempDir(), "tailcat")
	script := "#!/bin/sh\nOWLDROP_FAKE_SHARE='" + dir + "' exec '" + exe + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func waitReceive(t *testing.T, s *server, id string) tailcatReceive {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range s.tailcat.receiveList() {
			if r.ID == id && r.Done {
				return r
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("receive didn't finish")
	return tailcatReceive{}
}

func TestParseTailcatInput(t *testing.T) {
	a := fakeAddr
	for in, want := range map[string]string{
		a:                                       a,
		"  " + a + "\n":                         a,
		"tailcat cp '" + a + ":my file.pdf' .":  a,
		"tailcat cp -r " + a + ":owldrop-x .":   a,
		"https://owldrop.app/r#" + a + ":x.pdf": a,
		"owldrop://tailcat/" + a:                a,
		"tcp://example.com":                     "",
		"hello":                                 "",
	} {
		got, ok := parseTailcatInput(in)
		if got != want || ok != (want != "") {
			t.Errorf("parseTailcatInput(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestTailcatReceiveDownloadsShare(t *testing.T) {
	share := t.TempDir()
	writeTree(t, share, map[string]string{
		"owldrop-1/a.txt":      "alpha",
		"owldrop-1/sub/b.bin":  strings.Repeat("b", 300_000),
		"owldrop-1/sub/deep/c": "c",
	})
	os.Symlink("/etc/passwd", filepath.Join(share, "owldrop-1", "link")) // must be skipped

	s, _ := newSyncTestServer(t)
	s.tailcat.findBin = func() string { return fakeTailcatClient(t, share) }
	dest := t.TempDir()
	// An existing folder with the same name must not be touched.
	writeTree(t, dest, map[string]string{"owldrop-1/a.txt": "mine"})

	r, err := s.startReceive(fakeAddr, dest, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := waitReceive(t, s, r.ID)
	if got.Err != "" {
		t.Fatalf("receive failed: %s", got.Err)
	}
	if got.Files != 3 || got.Received != got.Total || got.Total != 300_006 {
		t.Fatalf("progress = %+v", got)
	}
	if mine, _ := os.ReadFile(filepath.Join(dest, "owldrop-1", "a.txt")); string(mine) != "mine" {
		t.Fatal("existing file was overwritten")
	}
	want := filepath.Join(dest, "owldrop-1 (1)")
	if len(got.Saved) != 1 || got.Saved[0] != want {
		t.Fatalf("saved = %v, want [%s]", got.Saved, want)
	}
	for p, c := range map[string]string{"a.txt": "alpha", "sub/deep/c": "c"} {
		if b, _ := os.ReadFile(filepath.Join(want, filepath.FromSlash(p))); string(b) != c {
			t.Fatalf("%s = %q", p, b)
		}
	}
	if _, err := os.Lstat(filepath.Join(want, "link")); !os.IsNotExist(err) {
		t.Fatal("symlink was downloaded")
	}
	matches, _ := filepath.Glob(filepath.Join(want, "*"+tailcatPartSuffix))
	if len(matches) != 0 {
		t.Fatalf("leftover temp files: %v", matches)
	}

	// History shows each file as received + saved via tailcat.
	s.history.mu.Lock()
	var tc int
	for _, e := range s.history.events {
		if e.Source == "tailcat" && e.Kind == "saved" {
			tc++
		}
	}
	s.history.mu.Unlock()
	if tc != 3 {
		t.Fatalf("%d tailcat saves in history, want 3", tc)
	}
}

func TestTailcatInspectAPI(t *testing.T) {
	share := t.TempDir()
	writeTree(t, share, map[string]string{"report.pdf": "pdf!"})
	s, base := newSyncTestServer(t)
	s.tailcat.findBin = func() string { return fakeTailcatClient(t, share) }

	body := strings.NewReader(`{"input":"tailcat cp '` + fakeAddr + `:report.pdf' ."}`)
	res := syncReq(t, base, s.token, http.MethodPost, "/api/tailcat/inspect", body)
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("inspect: %d %s", res.StatusCode, b)
	}
	var out struct {
		Addr  string              `json:"addr"`
		Files []tailcatRemoteFile `json:"files"`
		Total int64               `json:"total"`
	}
	json.NewDecoder(res.Body).Decode(&out)
	if out.Addr != fakeAddr || len(out.Files) != 1 || out.Files[0].Path != "report.pdf" || out.Total != 4 {
		t.Fatalf("inspect = %+v", out)
	}
}

func TestTailcatReceiveUnreachable(t *testing.T) {
	s, _ := newSyncTestServer(t)
	bin := filepath.Join(t.TempDir(), "tailcat")
	os.WriteFile(bin, []byte("#!/bin/sh\necho 'connection refused by relay' >&2\nexit 1\n"), 0o755)
	s.tailcat.findBin = func() string { return bin }
	r, err := s.startReceive(fakeAddr, t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	got := waitReceive(t, s, r.ID)
	if !strings.Contains(got.Err, "connection refused by relay") {
		t.Fatalf("err = %q", got.Err)
	}
}

func TestHandleOpenURLs(t *testing.T) {
	s, _ := newSyncTestServer(t)
	if s.handleOpenURLs([]string{"/home/me/file.txt", "https://example.com/" + fakeAddr}) {
		t.Fatal("non-owldrop args handled")
	}
	if !s.handleOpenURLs([]string{"owldrop://tailcat/" + fakeAddr}) {
		t.Fatal("owldrop:// link not handled")
	}
	if got := s.tailcat.incomingAddr(); got != fakeAddr {
		t.Fatalf("incoming = %q", got)
	}
}

func TestTailcatReceiveRefusesGrownShare(t *testing.T) {
	share := t.TempDir()
	writeTree(t, share, map[string]string{"a.bin": strings.Repeat("x", 1000)})
	s, _ := newSyncTestServer(t)
	s.tailcat.findBin = func() string { return fakeTailcatClient(t, share) }
	dest := t.TempDir()
	r, err := s.startReceive(fakeAddr, dest, 10) // the preview said 10 bytes
	if err != nil {
		t.Fatal(err)
	}
	if got := waitReceive(t, s, r.ID); !strings.Contains(got.Err, "changed since you looked it up") {
		t.Fatalf("err = %q", got.Err)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatalf("wrote %v", entries)
	}
}

func TestCountingWriterLimit(t *testing.T) {
	var sink strings.Builder
	cw := &countingWriter{w: &sink, fn: func(int64) {}, limit: 5}
	if _, err := cw.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	if _, err := cw.Write([]byte("6")); err != errTailcatOversize {
		t.Fatalf("err = %v, want errTailcatOversize", err)
	}
	if sink.String() != "12345" {
		t.Fatalf("wrote %q", sink.String())
	}
}
