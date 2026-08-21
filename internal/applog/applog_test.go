package applog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritesTimestampedLines(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Printf("synced %d folders", 2)
	l.Printf("failed: %v", os.ErrPermission)
	l.Close()

	raw, err := os.ReadFile(filepath.Join(dir, "ledgerline.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	if !strings.Contains(lines[0], "synced 2 folders") || !strings.HasPrefix(lines[0], "20") {
		t.Fatalf("first line = %q", lines[0])
	}
}

// TestMultilineIsFlattened keeps one event on one line: a multi-line API error
// would otherwise look like several events.
func TestMultilineIsFlattened(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir)
	l.Printf("boom:\nsecond line\nthird")
	l.Close()

	raw, _ := os.ReadFile(filepath.Join(dir, "ledgerline.log"))
	if got := strings.Count(strings.TrimSpace(string(raw)), "\n"); got != 0 {
		t.Fatalf("entry spans %d extra lines: %q", got, raw)
	}
}

// TestNilLoggerIsSafe is the contract that lets callers log unconditionally.
func TestNilLoggerIsSafe(t *testing.T) {
	var l *Logger
	l.Printf("nothing happens")
	l.Close()
	if l.Path() != "" {
		t.Fatal("a nil logger claims a path")
	}

	zero := &Logger{}
	zero.Printf("also nothing")
	zero.Close()
}

func TestOpenFirstWritableFallsBack(t *testing.T) {
	good := t.TempDir()

	// A path whose parent is a FILE cannot be created as a directory on any OS,
	// which is the portable way to simulate "install directory not writable".
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(blocker, "logs")

	l, dir := OpenFirstWritable(bad, good)
	defer l.Close()
	if dir != good {
		t.Fatalf("settled on %q, want the writable directory", dir)
	}
	l.Printf("hello")
	if _, err := os.Stat(filepath.Join(good, "ledgerline.log")); err != nil {
		t.Fatalf("nothing written to the fallback: %v", err)
	}
}

func TestOpenFirstWritableNeverReturnsNil(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, dir := OpenFirstWritable(filepath.Join(blocker, "logs"))
	if l == nil {
		t.Fatal("nil logger")
	}
	if dir != "" {
		t.Fatalf("dir = %q, want empty", dir)
	}
	l.Printf("discarded") // must not panic
}

// TestRotationKeepsABoundedNumberOfFiles: the log lives in a directory the user
// did not ask to grow, so it must cap itself.
func TestRotationKeepsABoundedNumberOfFiles(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	line := strings.Repeat("x", 4096)
	for range (maxSize/4096 + 2) * (keep + 3) {
		l.Printf("%s", line)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rotated int
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "ledgerline-") {
			rotated++
		}
	}
	if rotated > keep {
		t.Fatalf("%d rotated files kept, want at most %d", rotated, keep)
	}
	if _, err := os.Stat(filepath.Join(dir, "ledgerline.log")); err != nil {
		t.Fatalf("current log missing after rotation: %v", err)
	}
}
