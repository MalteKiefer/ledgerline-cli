package syncconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", dir)
	return dir
}

func mkdir(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAddListRemoveRoundTrip(t *testing.T) {
	cfgDir := isolate(t)
	docs, photos := mkdir(t, "docs"), mkdir(t, "photos")

	first, err := Add(Pair{Local: docs, Remote: "/Documents/Work/", IntervalMinutes: 30, Enabled: true})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if first.ID != "1" || first.Remote != "Documents/Work" {
		t.Fatalf("pair = %+v", first)
	}
	// Defaults are filled in rather than left empty for the runner to guess.
	if first.Direction != DirectionBoth || first.Conflict != ConflictNewest {
		t.Fatalf("defaults not applied: %+v", first)
	}

	second, err := Add(Pair{Local: photos, Remote: "Photos", Direction: DirectionPush, Enabled: true})
	if err != nil {
		t.Fatalf("add second: %v", err)
	}
	if second.ID != "2" {
		t.Fatalf("id = %q, want 2", second.ID)
	}

	f, err := Load()
	if err != nil || len(f.Pairs) != 2 {
		t.Fatalf("load = %+v (%v)", f, err)
	}

	// The file is the shared state between the terminal and the tray, so it
	// has to survive a round trip on disk, not just in memory.
	if _, err := os.Stat(filepath.Join(cfgDir, fileName)); err != nil {
		t.Fatalf("sync.json not written: %v", err)
	}

	if err := Remove("1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if f, _ := Load(); len(f.Pairs) != 1 || f.Pairs[0].ID != "2" {
		t.Fatalf("after remove: %+v", f)
	}
	if err := Remove("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove unknown = %v", err)
	}
}

// TestDuplicatePairIsRefused matters because two jobs over the same directory
// would race: each would see the other's writes as a remote change.
func TestDuplicatePairIsRefused(t *testing.T) {
	isolate(t)
	docs := mkdir(t, "docs")

	if _, err := Add(Pair{Local: docs, Remote: "Work", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, err := Add(Pair{Local: docs, Remote: "Work", Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "already synced") {
		t.Fatalf("err = %v", err)
	}
	// The same directory against a different remote folder is a real use case.
	if _, err := Add(Pair{Local: docs, Remote: "Archive", Enabled: true}); err != nil {
		t.Fatalf("second remote refused: %v", err)
	}
}

func TestInvalidInputIsRejected(t *testing.T) {
	isolate(t)
	docs := mkdir(t, "docs")
	file := filepath.Join(docs, "note.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := map[string]Pair{
		"no local":          {},
		"missing directory": {Local: filepath.Join(docs, "does-not-exist")},
		"a file":            {Local: file},
		"escaping remote":   {Local: docs, Remote: "../etc"},
		"bad direction":     {Local: docs, Direction: "sideways"},
		"bad conflict":      {Local: docs, Conflict: "coin-toss"},
		"negative interval": {Local: docs, IntervalMinutes: -1},
	}
	for name, p := range cases {
		if _, err := Add(p); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestDueRespectsScheduleAndState(t *testing.T) {
	now := time.Now()
	every := Pair{Enabled: true, IntervalMinutes: 15}

	if !every.Due(now) {
		t.Fatal("a pair that has never run is not due")
	}

	ran := every
	ran.LastRun = now.Add(-5 * time.Minute)
	if ran.Due(now) {
		t.Fatal("due again after 5 of 15 minutes")
	}
	ran.LastRun = now.Add(-15 * time.Minute)
	if !ran.Due(now) {
		t.Fatal("not due after the interval elapsed")
	}

	off := ran
	off.Enabled = false
	if off.Due(now) {
		t.Fatal("a disabled pair ran")
	}

	manual := Pair{Enabled: true, IntervalMinutes: 0}
	if manual.Due(now) {
		t.Fatal("a manual pair ran on a schedule")
	}
}

func TestRecordRunKeepsTheLastFailureVisible(t *testing.T) {
	isolate(t)
	docs := mkdir(t, "docs")
	p, err := Add(Pair{Local: docs, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	when := time.Now()
	if err := RecordRun(p.ID, when, "0 pushed, 0 pulled", errors.New("server unreachable")); err != nil {
		t.Fatal(err)
	}
	got, err := Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastFailure != "server unreachable" || got.LastResult != "0 pushed, 0 pulled" {
		t.Fatalf("pair = %+v", got)
	}

	// A later success must clear the failure, or the list would keep showing a
	// problem that is over.
	if err := RecordRun(p.ID, time.Now(), "3 pushed, 1 pulled", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := Get(p.ID); got.LastFailure != "" {
		t.Fatalf("stale failure kept: %+v", got)
	}
}

func TestUpdateChangesOnePair(t *testing.T) {
	isolate(t)
	a, b := mkdir(t, "a"), mkdir(t, "b")
	first, _ := Add(Pair{Local: a, Enabled: true, IntervalMinutes: 15})
	second, _ := Add(Pair{Local: b, Enabled: true, IntervalMinutes: 15})

	if _, err := Update(first.ID, func(p *Pair) { p.Enabled = false }); err != nil {
		t.Fatal(err)
	}
	got, _ := Get(second.ID)
	if !got.Enabled {
		t.Fatal("updating one pair changed another")
	}
	if _, err := Update("nope", func(*Pair) {}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update unknown = %v", err)
	}
}

func TestConfigFileIsOwnerOnly(t *testing.T) {
	cfgDir := isolate(t)
	if _, err := Add(Pair{Local: mkdir(t, "docs"), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(cfgDir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Windows reports 0666 for any writable file; confidentiality there
		// comes from the NTFS ACL on the profile directory, not POSIX bits.
		return
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permissions = %o, want 600", perm)
	}
}
