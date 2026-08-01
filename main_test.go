package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConflictFreeName(t *testing.T) {
	dir := t.TempDir()

	// Fresh name: no suffix.
	got := conflictFreeName(dir, "report.pdf")
	if want := filepath.Join(dir, "report.pdf"); got != want {
		t.Fatalf("fresh name: got %q, want %q", got, want)
	}
	// Create it and re-ask: should become "report (1).pdf".
	os.WriteFile(got, []byte("x"), 0o644)
	got = conflictFreeName(dir, "report.pdf")
	if want := filepath.Join(dir, "report (1).pdf"); got != want {
		t.Fatalf("first conflict: got %q, want %q", got, want)
	}
	// Create that too: "report (2).pdf".
	os.WriteFile(got, []byte("x"), 0o644)
	got = conflictFreeName(dir, "report.pdf")
	if want := filepath.Join(dir, "report (2).pdf"); got != want {
		t.Fatalf("second conflict: got %q, want %q", got, want)
	}
	// Extensionless files.
	os.WriteFile(filepath.Join(dir, "Makefile"), []byte("x"), 0o644)
	got = conflictFreeName(dir, "Makefile")
	if want := filepath.Join(dir, "Makefile (1)"); got != want {
		t.Fatalf("extensionless: got %q, want %q", got, want)
	}
	// The daemon already sent a deduplicated name like "foo (1).txt".
	os.WriteFile(filepath.Join(dir, "foo (1).txt"), []byte("x"), 0o644)
	got = conflictFreeName(dir, "foo (1).txt")
	if want := filepath.Join(dir, "foo (1) (1).txt"); got != want {
		t.Fatalf("daemon-deduplicated name: got %q, want %q", got, want)
	}
}

func TestValidBaseName(t *testing.T) {
	sep := string(filepath.Separator)
	for _, bad := range []string{"", ".", "..", "a" + sep + "b", sep + "etc" + sep + "passwd", ".." + sep + "evil"} {
		if validBaseName(bad) {
			t.Errorf("validBaseName(%q) = true, want false", bad)
		}
	}
	for _, good := range []string{"a.txt", "report (1).pdf", "ünïcode 🚀.bin", "-"} {
		if !validBaseName(good) {
			t.Errorf("validBaseName(%q) = false, want true", good)
		}
	}
}
