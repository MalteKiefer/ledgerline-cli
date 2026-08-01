package files

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDebouncerCoalescesBurst(t *testing.T) {
	d := newDebouncer(50 * time.Millisecond)
	defer d.stop()
	for i := 0; i < 5; i++ {
		d.trigger("/m/a")
	}
	d.trigger("/m/b")
	// Force the window to elapse deterministically.
	d.flushNow()

	got := drain(d.C())
	if len(got) != 2 {
		t.Fatalf("want 2 coalesced keys, got %d: %v", len(got), got)
	}
	if !got["/m/a"] || !got["/m/b"] {
		t.Fatalf("missing keys: %v", got)
	}
}

func TestFSWatcherEmitsOnLocalWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := newFSWatcher(dir, NewMatcher(nil), false)
	if err != nil {
		t.Fatalf("newFSWatcher: %v", err)
	}
	defer w.Close()

	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-w.Events():
		if got != dir {
			t.Fatalf("want %q, got %q", dir, got)
		}
	case err := <-w.Errors():
		t.Fatalf("watcher error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("no event within 2s")
	}
}

func TestFSWatcherSkipsIgnoredNestedDir(t *testing.T) {
	dir := t.TempDir()
	skip := filepath.Join(dir, "skip")
	if err := os.MkdirAll(skip, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := newFSWatcher(dir, NewMatcher([]string{"skip/"}), false)
	if err != nil {
		t.Fatalf("newFSWatcher: %v", err)
	}
	defer w.Close()

	if err := os.WriteFile(filepath.Join(skip, "f.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-w.Events():
		t.Fatalf("unexpected event for ignored dir: %q", got)
	case err := <-w.Errors():
		t.Fatalf("watcher error: %v", err)
	case <-time.After(500 * time.Millisecond):
		// No event — correct.
	}
}

func TestFSWatcherSkipsHiddenNestedDir(t *testing.T) {
	dir := t.TempDir()
	hiddenDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(hiddenDir, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := newFSWatcher(dir, NewMatcher(nil), false)
	if err != nil {
		t.Fatalf("newFSWatcher: %v", err)
	}
	defer w.Close()

	if err := os.WriteFile(filepath.Join(hiddenDir, "HEAD"), []byte("ref"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-w.Events():
		t.Fatalf("unexpected event for hidden dir: %q", got)
	case err := <-w.Errors():
		t.Fatalf("watcher error: %v", err)
	case <-time.After(500 * time.Millisecond):
		// No event — correct.
	}
}

func drain(ch <-chan string) map[string]bool {
	out := map[string]bool{}
	for {
		select {
		case k := <-ch:
			out[k] = true
		case <-time.After(20 * time.Millisecond):
			return out
		}
	}
}
