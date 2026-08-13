package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileArgsSkipsFlags(t *testing.T) {
	got := fileArgs([]string{"--lan", "/tmp/a.pdf", "-v", ""})
	if len(got) != 1 || got[0] != "/tmp/a.pdf" {
		t.Fatalf("got %v", got)
	}
}

func TestEnqueueShareIgnoresMissing(t *testing.T) {
	s := newServerDir(&config{SaveDir: t.TempDir()}, t.TempDir())
	s.enqueueShare([]string{"/no/such/file.bin", t.TempDir()})
	if n := len(s.pendingShares()); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
	f := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.enqueueShare([]string{f})
	got := s.pendingShares()
	if len(got) != 1 || got[0].Name != "hello.txt" || got[0].Size != 2 {
		t.Fatalf("pending = %+v", got)
	}
	if _, ok := s.takeShare(got[0].ID); !ok {
		t.Fatal("take missed")
	}
	if len(s.pendingShares()) != 0 {
		t.Fatal("queue not empty after take")
	}
}
