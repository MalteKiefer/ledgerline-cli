package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeWatcher struct {
	events chan string
	errs   chan error
}

func (f *fakeWatcher) Events() <-chan string { return f.events }
func (f *fakeWatcher) Errors() <-chan error  { return f.errs }
func (f *fakeWatcher) Close() error          { return nil }

func TestServiceRunsPassOnEvent(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	client, vk := newTestClient(t)
	store := NewStore(client, vk)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	local := t.TempDir()
	if err := os.WriteFile(filepath.Join(local, "note.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	fw := &fakeWatcher{events: make(chan string, 4), errs: make(chan error, 1)}
	svc := &Service{
		Client: client, Store: store, VK: vk,
		Debounce: 5 * time.Millisecond,
		Log:      func(string) {},
		Mappings: []ServiceMapping{{
			Local: local, Remote: "",
			Opts:     SyncOptions{Conflict: ConflictNewest, Delete: DeleteBoth},
			Interval: time.Hour, // don't rely on the ticker in this test
		}},
		NewWatcher: func(string, *Matcher, bool) (watcher, error) { return fw, nil },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	fw.events <- local // signal a local change

	// Poll the server-side store for the uploaded file (bounded).
	deadline := time.Now().Add(3 * time.Second)
	for {
		if remoteHasFile(t, client, vk, "note.txt") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("service did not upload note.txt")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil && err != context.Canceled {
		t.Fatalf("Run returned %v", err)
	}
}
