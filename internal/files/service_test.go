package files

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/vault"
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

func TestServiceStopsOnAuthFatal(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	client, vk := newTestClientReturning401(t)
	store := NewStore(client, vk)
	_ = store.Load(context.Background())
	local := t.TempDir()
	os.WriteFile(filepath.Join(local, "a.txt"), []byte("x"), 0o600)

	svc := &Service{Client: client, Store: store, VK: vk, Log: func(string) {}}
	err := svc.runPass(context.Background(), ServiceMapping{Local: local,
		Opts: SyncOptions{Conflict: ConflictNewest, Delete: DeleteBoth}})
	if err == nil {
		t.Fatal("want fatal auth error to stop the service, got nil")
	}
}

func TestServiceStopsOnAuthFatalDuringBlobUpload(t *testing.T) {
	// The common auth-expiry case: the token dies while blob uploads are in
	// flight, so the 401 lands on /files/upload, not the manifest store. The
	// syncer must surface it (via Syncer.authErr) so runPass returns fatal.
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	mk := newMock(t, "pass")
	client := mk.client(t)
	vk, err := vault.Unlock(context.Background(), client, "pass")
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(client, vk)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	os.WriteFile(filepath.Join(local, "a.txt"), []byte("x"), 0o600)

	// Manifest store is healthy; only blob uploads 401.
	mk.mu.Lock()
	mk.failUpload = http.StatusUnauthorized
	mk.mu.Unlock()

	svc := &Service{Client: client, Store: store, VK: vk, Log: func(string) {}}
	err = svc.runPass(context.Background(), ServiceMapping{Local: local,
		Opts: SyncOptions{Conflict: ConflictNewest, Delete: DeleteBoth}})
	if err == nil {
		t.Fatal("want fatal auth error when a blob upload 401s, got nil")
	}
}

func TestServiceTransientErrorDoesNotStopService(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	mk := newMock(t, "pass")
	client := mk.client(t)
	vk, err := vault.Unlock(context.Background(), client, "pass")
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(client, vk)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	os.WriteFile(filepath.Join(local, "a.txt"), []byte("x"), 0o600)

	m := ServiceMapping{Local: local, Remote: "",
		Opts: SyncOptions{Conflict: ConflictNewest, Delete: DeleteBoth}}
	svc := &Service{Client: client, Store: store, VK: vk, Log: func(string) {}}

	// First pass succeeds and uploads a.txt, establishing sync state.
	if fatal := svc.runPass(context.Background(), m); fatal != nil {
		t.Fatalf("pass1: %v", fatal)
	}

	// Simulate a transient server failure on the next store round-trip.
	mk.mu.Lock()
	mk.failStorePut = http.StatusInternalServerError
	mk.mu.Unlock()
	os.WriteFile(filepath.Join(local, "b.txt"), []byte("y"), 0o600)
	if fatal := svc.runPass(context.Background(), m); fatal != nil {
		t.Fatalf("transient error must not be fatal, got %v", fatal)
	}
	if svc.paused[local] {
		t.Fatal("transient error must not pause the mapping")
	}
}

func TestServicePausesWhenLocalRootVanishes(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	client, vk := newTestClient(t)
	store := NewStore(client, vk)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := ServiceMapping{Local: local, Remote: "",
		Opts: SyncOptions{Conflict: ConflictNewest, Delete: DeleteBoth}}
	svc := &Service{Client: client, Store: store, VK: vk, Log: func(string) {}}

	// First pass uploads a.txt and records state.
	if fatal := svc.runPass(context.Background(), m); fatal != nil {
		t.Fatalf("pass1: %v", fatal)
	}
	if !remoteHasFile(t, client, vk, "a.txt") {
		t.Fatal("a.txt not uploaded on first pass")
	}

	// Local root emptied (simulates an unmounted drive). Guard must NOT trash remote.
	os.RemoveAll(local)
	if fatal := svc.runPass(context.Background(), m); fatal != nil {
		t.Fatalf("pass2: %v", fatal)
	}
	if !remoteHasFile(t, client, vk, "a.txt") {
		t.Fatal("guard failed: remote a.txt was deleted after local root vanished")
	}
	if !svc.paused[local] {
		t.Fatal("mapping should be marked paused")
	}
}

// TestServicePausesWhenLocalRootVanishes_NormalizedRemote mirrors
// TestServicePausesWhenLocalRootVanishes but uses a remote mapping that needs
// normalization (a leading slash). NewSyncer normalizes remoteBase before
// deriving the sync-state key; hadState must normalize identically or it
// reads a state file the syncer never wrote — silently disabling the
// vanished-local-root guard for any mapping remote that isn't already
// slash-trimmed. Regression test for that mismatch (normalizeRemote).
func TestServicePausesWhenLocalRootVanishes_NormalizedRemote(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	client, vk := newTestClient(t)
	store := NewStore(client, vk)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// "/Sub" needs normalizeRemote to strip the leading slash before it
	// matches the key the syncer used to save state.
	m := ServiceMapping{Local: local, Remote: "/Sub",
		Opts: SyncOptions{Conflict: ConflictNewest, Delete: DeleteBoth}}
	svc := &Service{Client: client, Store: store, VK: vk, Log: func(string) {}}

	// First pass uploads a.txt (under remote "Sub") and records state.
	if fatal := svc.runPass(context.Background(), m); fatal != nil {
		t.Fatalf("pass1: %v", fatal)
	}
	if !remoteHasFile(t, client, vk, "a.txt") {
		t.Fatal("a.txt not uploaded on first pass")
	}

	// Local root emptied (simulates an unmounted drive). Guard must NOT trash remote.
	os.RemoveAll(local)
	if fatal := svc.runPass(context.Background(), m); fatal != nil {
		t.Fatalf("pass2: %v", fatal)
	}
	if !remoteHasFile(t, client, vk, "a.txt") {
		t.Fatal("guard failed: remote a.txt was deleted after local root vanished (normalized-remote state key mismatch)")
	}
	if !svc.paused[local] {
		t.Fatal("mapping should be marked paused")
	}
}
