package main

import (
	"os"
	"path/filepath"
	"strings"
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

// TestHistoryWiring guards against lost history hooks — a mutation path that
// stops recording its event (as happened once with an aborted edit batch)
// silently breaks the History tab. Source-level check; mocking the daemon
// client for a real integration test isn't worth the plumbing.
func TestHistoryWiring(t *testing.T) {
	b, err := os.ReadFile("ops.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"s.history.recordSaved(name, path)",
		"s.history.recordDeleted(name)",
		"s.history.recordSend(",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("ops.go is missing %q — history event not recorded", want)
		}
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
