package syncrunner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/files"
	"github.com/MalteKiefer/ledgerline-cli/internal/syncconfig"
)

// recorder counts the pairs a run touched, so a test can wait for one.
type recorder struct {
	mu    sync.Mutex
	runs  []string
	fired chan string
}

func newRecorder() *recorder { return &recorder{fired: make(chan string, 16)} }

func (r *recorder) sync(context.Context, *api.Client, syncconfig.Pair) (files.SyncResult, error) {
	return files.SyncResult{Pushed: 1}, nil
}

func (r *recorder) record(p syncconfig.Pair, _ files.SyncResult, _ error) {
	r.mu.Lock()
	r.runs = append(r.runs, p.ID)
	r.mu.Unlock()
	select {
	case r.fired <- p.ID:
	default:
	}
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}

// waitFor blocks until a run happens or the deadline passes.
func (r *recorder) waitFor(t *testing.T, within time.Duration, why string) {
	t.Helper()
	select {
	case <-r.fired:
	case <-time.After(within):
		t.Fatalf("no sync within %s: %s", within, why)
	}
}

func isolate(t *testing.T) { t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir()) }

// fakeClient stands in for an authenticated client; the injected sync never
// touches it.
func fakeClient() (*api.Client, error) { return nil, nil }

// TestChangeInAWatchedFolderTriggersASync is the behaviour an interval cannot
// give you: an edit is picked up in seconds, not at the next tick.
func TestChangeInAWatchedFolderTriggersASync(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	if _, err := syncconfig.Add(syncconfig.Pair{
		Local: dir, Remote: "Docs", Enabled: true, Watch: true,
		IntervalMinutes: 0, // no schedule at all: only the watcher can fire this
	}); err != nil {
		t.Fatal(err)
	}

	rec := newRecorder()
	r := New(fakeClient, Options{
		Debounce: 80 * time.Millisecond,
		Tick:     time.Hour, // never
		Reload:   time.Hour, // never
		Sync:     rec.sync,
		OnResult: rec.record,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	time.Sleep(150 * time.Millisecond) // let the watcher arm

	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, 3*time.Second, "a file was created in a watched folder")
}

// TestBurstOfChangesCollapsesIntoOneSync: copying a directory in produces one
// event per file, and syncing per event would hammer the server.
func TestBurstOfChangesCollapsesIntoOneSync(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	if _, err := syncconfig.Add(syncconfig.Pair{Local: dir, Enabled: true, Watch: true}); err != nil {
		t.Fatal(err)
	}

	rec := newRecorder()
	r := New(fakeClient, Options{
		Debounce: 250 * time.Millisecond,
		Tick:     time.Hour,
		Reload:   time.Hour,
		Sync:     rec.sync,
		OnResult: rec.record,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	time.Sleep(150 * time.Millisecond)

	for i := range 20 {
		name := filepath.Join(dir, string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec.waitFor(t, 3*time.Second, "the burst should have produced a sync")
	time.Sleep(400 * time.Millisecond)

	if got := rec.count(); got > 2 {
		t.Fatalf("%d syncs for one burst of 20 writes", got)
	}
}

// TestAPausedPairIsNotWatched: pausing has to stop the automatic runs too, not
// just the scheduled ones.
func TestAPausedPairIsNotWatched(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	if _, err := syncconfig.Add(syncconfig.Pair{
		Local: dir, Enabled: false, Watch: true,
	}); err != nil {
		t.Fatal(err)
	}

	rec := newRecorder()
	r := New(fakeClient, Options{
		Debounce: 80 * time.Millisecond,
		Tick:     time.Hour,
		Reload:   time.Hour,
		Sync:     rec.sync,
		OnResult: rec.record,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	time.Sleep(150 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if got := rec.count(); got != 0 {
		t.Fatalf("a paused pair ran %d times", got)
	}
}

// TestDuePairRunsOnTheTick covers the schedule half.
func TestDuePairRunsOnTheTick(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	if _, err := syncconfig.Add(syncconfig.Pair{
		Local: dir, Enabled: true, Watch: false, IntervalMinutes: 1,
	}); err != nil {
		t.Fatal(err)
	}

	rec := newRecorder()
	r := New(fakeClient, Options{
		Debounce: time.Hour,
		Tick:     50 * time.Millisecond,
		Reload:   time.Hour,
		Sync:     rec.sync,
		OnResult: rec.record,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	// A pair that has never run is due immediately; the recorded run then
	// pushes it out by its interval.
	rec.waitFor(t, 2*time.Second, "a never-run pair is due")
	time.Sleep(300 * time.Millisecond)
	if got := rec.count(); got > 1 {
		t.Fatalf("ran %d times inside one interval", got)
	}
}

// TestSignedOutRunnerDoesNothing: the tray keeps running while signed out, and
// must not spin on a client it cannot get.
func TestSignedOutRunnerDoesNothing(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	if _, err := syncconfig.Add(syncconfig.Pair{Local: dir, Enabled: true, Watch: true}); err != nil {
		t.Fatal(err)
	}

	rec := newRecorder()
	r := New(
		func() (*api.Client, error) { return nil, errors.New("not signed in") },
		Options{
			Debounce: 60 * time.Millisecond,
			Tick:     time.Hour,
			Reload:   time.Hour,
			Sync:     rec.sync,
			OnResult: rec.record,
		})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	time.Sleep(150 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if got := rec.count(); got != 0 {
		t.Fatalf("signed out, yet %d syncs ran", got)
	}
}

// TestChangesOutsideAnyPairAreIgnored guards the path→pair mapping.
func TestChangesOutsideAnyPairAreIgnored(t *testing.T) {
	isolate(t)
	watched, other := t.TempDir(), t.TempDir()
	if _, err := syncconfig.Add(syncconfig.Pair{Local: watched, Enabled: true, Watch: true}); err != nil {
		t.Fatal(err)
	}

	rec := newRecorder()
	r := New(fakeClient, Options{
		Debounce: 60 * time.Millisecond,
		Tick:     time.Hour,
		Reload:   time.Hour,
		Sync:     rec.sync,
		OnResult: rec.record,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	time.Sleep(150 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(other, "x.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if got := rec.count(); got != 0 {
		t.Fatalf("a change outside every pair caused %d syncs", got)
	}
}

func TestUnderRoot(t *testing.T) {
	root := filepath.Join("C:", "Users", "ada", "docs")
	cases := map[string]bool{
		root:                                   true,
		filepath.Join(root, "a.txt"):           true,
		filepath.Join(root, "sub", "b.txt"):    true,
		filepath.Join("C:", "Users", "ada"):    false,
		filepath.Join("C:", "Users", "adam"):   false,
		filepath.Join("C:", "Users", "ada2"):   false,
		filepath.Join(root+"-backup", "c.txt"): false,
	}
	for path, want := range cases {
		if got := underRoot(path, root); got != want {
			t.Fatalf("underRoot(%q) = %v, want %v", path, got, want)
		}
	}
}
