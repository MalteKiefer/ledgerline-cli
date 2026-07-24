package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func readLines(t *testing.T, path string) []Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line not valid JSON: %v", err)
		}
		out = append(out, e)
	}
	return out
}

func TestLogAppendsJSONL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	l := New(dir)
	if l == nil {
		t.Fatal("New returned nil for a writable dir")
	}
	l.Log(Event{Event: "cmd:auth login", Outcome: OutcomeOK, Target: "example.com"})
	l.Log(Event{Event: "gallery.upload", Outcome: OutcomeOK, Count: 3, Duration: 42})

	events := readLines(t, filepath.Join(dir, "audit.log"))
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	if events[0].Event != "cmd:auth login" || events[0].Outcome != "ok" || events[0].Target != "example.com" {
		t.Fatalf("bad first event: %+v", events[0])
	}
	if events[1].Count != 3 || events[1].Duration != 42 {
		t.Fatalf("bad second event: %+v", events[1])
	}
	if events[0].Time == "" || events[0].PID == 0 {
		t.Fatalf("event missing ts/pid: %+v", events[0])
	}
}

func TestNilLoggerDiscards(t *testing.T) {
	var l *Logger
	l.Log(Event{Event: "x"}) // must not panic
	if New("") != nil {
		t.Fatal("New(\"\") should return a nil logger")
	}
}

func TestRotation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	l := New(dir)
	// Write one over-size line to force a rotation on the next write.
	big := strings.Repeat("x", maxSize)
	l.Log(Event{Event: "big", Detail: big})
	l.Log(Event{Event: "after-rotate", Outcome: OutcomeOK})

	if _, err := os.Stat(filepath.Join(dir, "audit.log.1")); err != nil {
		t.Fatalf("expected rotated backup audit.log.1: %v", err)
	}
	events := readLines(t, filepath.Join(dir, "audit.log"))
	if len(events) != 1 || events[0].Event != "after-rotate" {
		t.Fatalf("fresh log should hold only the post-rotate event: %+v", events)
	}
}

func TestPurge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	l := New(dir)
	l.Log(Event{Event: "a"})
	_ = os.WriteFile(filepath.Join(dir, "audit.log.1"), []byte("{}\n"), 0o600)
	if err := Purge(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.log")); !os.IsNotExist(err) {
		t.Fatal("audit.log should be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.log.1")); !os.IsNotExist(err) {
		t.Fatal("audit.log.1 should be gone")
	}
	if err := Purge(dir); err != nil {
		t.Fatalf("purge of an already-clean dir must be a no-op: %v", err)
	}
}

func TestFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX bits n/a")
	}
	dir := filepath.Join(t.TempDir(), "state")
	l := New(dir)
	l.Log(Event{Event: "a"})

	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm = %o want 0700", di.Mode().Perm())
	}
	fi, _ := os.Stat(filepath.Join(dir, "audit.log"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("log perm = %o want 0600", fi.Mode().Perm())
	}
}

func TestClockInjectable(t *testing.T) {
	orig := nowUTC
	t.Cleanup(func() { nowUTC = orig })
	nowUTC = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

	dir := filepath.Join(t.TempDir(), "state")
	l := New(dir)
	l.Log(Event{Event: "x", Outcome: OutcomeOK})
	events := readLines(t, filepath.Join(dir, "audit.log"))
	if events[0].Time != "2026-01-02T03:04:05Z" {
		t.Fatalf("ts = %q", events[0].Time)
	}
}
