# Files Sync Service (`files sync --service`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `files sync --service` mode that continuously keeps configured local folders in sync with the encrypted files store, driven by filesystem watching plus interval polling.

**Architecture:** A foreground supervisor (`files.Service`) unlocks the VK once, then loops: `fsnotify` watchers (one per mapping, debounced) and an interval ticker feed a per-mapping coalescing work queue; a single worker runs the existing `files.NewSyncer(...).Run` sequentially per mapping. A sanity-guard pauses a mapping instead of mass-deleting when its local root vanishes. Config lives in the existing `settings.json`.

**Tech Stack:** Go 1.25, cobra, `github.com/fsnotify/fsnotify` (new), existing `internal/files` sync engine, `internal/settings`, `internal/audit`, `internal/config`.

## Global Constraints

- **Standard library first** (§5). The only new third-party module is `github.com/fsnotify/fsnotify`, pinned in `go.mod`/`go.sum`; record it in `CLAUDE.md` §5 with justification.
- **VK is process-memory only** — never written to disk (§6). Prompt for the passphrase once, before the loop.
- **No wire-contract change** (§2). Reuse existing `/files/store` + upload/raw endpoints via the current sync engine.
- **Audit trail carries metadata only** — never a key/token/passphrase/content, and never a folder name to the *server* heartbeat (local audit `Target` may hold a coarse mapping tag) (§7/§18).
- **Config files are 0600 in a 0700 dir** (§7).
- **Commits: no AI attribution** (repo hook-enforced) — do not add Co-Authored-By/assistant trailers.
- `time.ParseDuration` for all interval/debounce parsing; defaults interval `60s`, debounce `2s`.
- Conflict/delete command defaults: `ConflictNewest`, `DeleteBoth`.

---

## File Structure

- `internal/settings/settings.go` (modify) — enrich `Mapping`, add `Service` struct, add `Upsert`.
- `internal/files/watch.go` (create) — `watcher` interface, `debouncer`, real `fsnotify` adapter.
- `internal/files/service.go` (create) — `Service` supervisor, `ServiceMapping`, work queue, sanity-guard, resilience.
- `internal/files/service_test.go` (create) — supervisor/guard/resilience tests with a fake watcher + mock server.
- `internal/files/watch_test.go` (create) — debouncer unit tests + one real-fsnotify integration test.
- `internal/settings/settings_test.go` (create) — parse/back-compat/upsert tests.
- `cmd/files_sync.go` (modify) — `--service` flag, `--map` upsert+start, lockfile, signal handling, banner.
- `cmd/lock.go` (create) — single-instance lockfile helper.
- `CLAUDE.md` (modify) — §4 module map, §5 dependency, §15 changelog.

---

## Task 1: Settings schema — per-mapping policy, service block, Upsert

**Files:**
- Modify: `internal/settings/settings.go`
- Test: `internal/settings/settings_test.go` (create)

**Interfaces:**
- Consumes: existing `Settings{Hidden,Ignore,Sync}`, `Mapping{Remote,Local}`, `Load()`, `Save()`, `Path()`.
- Produces:
  - `Mapping{Remote, Local string; Conflict, Delete string; Interval string; Hidden *bool; Ignore []string}` (new fields `omitempty`).
  - `Service{Interval, Debounce string}` (new).
  - `Settings.Service Service` field (new, `omitempty`).
  - `func (s *Settings) Upsert(m Mapping)` — idempotent by absolute `Local`.
  - `func ParseDurationOr(s string, def time.Duration) time.Duration`.

- [ ] **Step 1: Write failing tests**

```go
package settings

import (
	"testing"
	"time"
)

func TestUpsertIsIdempotentByLocal(t *testing.T) {
	var s Settings
	s.Upsert(Mapping{Remote: "Aleph Alpha", Local: "/backup/aa"})
	s.Upsert(Mapping{Remote: "Renamed", Local: "/backup/aa"}) // same local → update
	if len(s.Sync) != 1 {
		t.Fatalf("want 1 mapping, got %d", len(s.Sync))
	}
	if s.Sync[0].Remote != "Renamed" {
		t.Fatalf("want updated remote, got %q", s.Sync[0].Remote)
	}
	s.Upsert(Mapping{Remote: "Other", Local: "/backup/bb"})
	if len(s.Sync) != 2 {
		t.Fatalf("want 2 mappings, got %d", len(s.Sync))
	}
}

func TestParseDurationOrFallsBack(t *testing.T) {
	if got := ParseDurationOr("30s", time.Minute); got != 30*time.Second {
		t.Fatalf("want 30s, got %v", got)
	}
	if got := ParseDurationOr("", time.Minute); got != time.Minute {
		t.Fatalf("want default 1m, got %v", got)
	}
	if got := ParseDurationOr("garbage", time.Minute); got != time.Minute {
		t.Fatalf("want default on bad input, got %v", got)
	}
}

func TestLegacyMappingStillDecodes(t *testing.T) {
	// A pre-existing settings.json with only remote/local must still load.
	const legacy = `{"sync":[{"remote":"Docs","local":"/home/me/docs"}]}`
	var s Settings
	if err := s.unmarshal([]byte(legacy)); err != nil {
		t.Fatalf("legacy decode: %v", err)
	}
	if len(s.Sync) != 1 || s.Sync[0].Local != "/home/me/docs" {
		t.Fatalf("legacy mapping lost: %+v", s.Sync)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/settings/ -run 'Upsert|ParseDuration|Legacy' -v`
Expected: FAIL (undefined `Upsert`, `ParseDurationOr`, `unmarshal`).

- [ ] **Step 3: Implement**

In `internal/settings/settings.go` add imports `"path/filepath"`, `"time"`. Extend the types and add helpers:

```go
// Mapping is one sync pairing plus optional per-mapping policy overrides. Zero
// values fall back to the service defaults, then the command defaults.
type Mapping struct {
	Remote   string   `json:"remote"`
	Local    string   `json:"local"`
	Conflict string   `json:"conflict,omitempty"`
	Delete   string   `json:"delete,omitempty"`
	Interval string   `json:"interval,omitempty"`
	Hidden   *bool    `json:"hidden,omitempty"`
	Ignore   []string `json:"ignore,omitempty"`
}

// Service holds the continuous-service defaults.
type Service struct {
	Interval string `json:"interval,omitempty"` // server-poll cadence, default 60s
	Debounce string `json:"debounce,omitempty"` // coalesce local FS events, default 2s
}

// Settings is the persisted CLI configuration users may edit by hand.
type Settings struct {
	Hidden  bool      `json:"hidden"`
	Ignore  []string  `json:"ignore"`
	Sync    []Mapping `json:"sync"`
	Service Service   `json:"service,omitempty"`
}

// Upsert adds or replaces a mapping, keyed by its cleaned absolute local path.
func (s *Settings) Upsert(m Mapping) {
	key := filepath.Clean(m.Local)
	for i := range s.Sync {
		if filepath.Clean(s.Sync[i].Local) == key {
			s.Sync[i] = m
			return
		}
	}
	s.Sync = append(s.Sync, m)
}

// unmarshal is a thin seam so tests can decode without touching disk.
func (s *Settings) unmarshal(b []byte) error { return json.Unmarshal(b, s) }

// ParseDurationOr parses a duration string, returning def on empty/invalid input.
func ParseDurationOr(v string, def time.Duration) time.Duration {
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/settings/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/settings/settings.go internal/settings/settings_test.go
git commit -m "feat(settings): per-mapping sync policy + service block + Upsert"
```

---

## Task 2: Watcher interface + debouncer

**Files:**
- Create: `internal/files/watch.go`
- Test: `internal/files/watch_test.go`

**Interfaces:**
- Produces:
  - `type watcher interface { Events() <-chan string; Errors() <-chan error; Close() error }` — `Events()` emits the mapping key (`local` path) on any relevant change.
  - `type debouncer struct { ... }`, `func newDebouncer(window time.Duration) *debouncer`, `func (d *debouncer) trigger(key string)`, `func (d *debouncer) C() <-chan string`, `func (d *debouncer) stop()`.
- Consumes: nothing from other tasks.

Note: the debouncer must be time-injectable. Use a `now func() time.Time` and a manual `flush()` path exercised by tests, so no real sleeps are needed.

- [ ] **Step 1: Write failing tests**

```go
package files

import (
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
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/files/ -run Debouncer -v`
Expected: FAIL (undefined `newDebouncer`).

- [ ] **Step 3: Implement `internal/files/watch.go`**

```go
package files

import (
	"sync"
	"time"
)

// watcher reports "something changed" under a mapping's local root. Events()
// yields the mapping key (its local path); the service re-scans that mapping.
type watcher interface {
	Events() <-chan string
	Errors() <-chan error
	Close() error
}

// debouncer coalesces a burst of triggers per key into one emission per window.
type debouncer struct {
	window  time.Duration
	mu      sync.Mutex
	pending map[string]bool
	out     chan string
	timer   *time.Timer
}

func newDebouncer(window time.Duration) *debouncer {
	return &debouncer{window: window, pending: map[string]bool{}, out: make(chan string, 64)}
}

func (d *debouncer) trigger(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending[key] = true
	if d.timer == nil {
		d.timer = time.AfterFunc(d.window, d.flushNow)
	}
}

// flushNow emits every pending key immediately and clears the window.
func (d *debouncer) flushNow() {
	d.mu.Lock()
	keys := make([]string, 0, len(d.pending))
	for k := range d.pending {
		keys = append(keys, k)
	}
	d.pending = map[string]bool{}
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.mu.Unlock()
	for _, k := range keys {
		d.out <- k
	}
}

func (d *debouncer) C() <-chan string { return d.out }

func (d *debouncer) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/files/ -run Debouncer -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/files/watch.go internal/files/watch_test.go
git commit -m "feat(files): debouncer + watcher interface for sync service"
```

---

## Task 3: Real fsnotify adapter + dependency

**Files:**
- Modify: `internal/files/watch.go` (append the fsnotify adapter)
- Modify: `go.mod`, `go.sum`
- Test: `internal/files/watch_test.go` (append integration test)

**Interfaces:**
- Consumes: `watcher` interface (Task 2).
- Produces: `func newFSWatcher(root string, ignore *Matcher, hidden bool) (watcher, error)` — recursively watches `root`, emits `root` on any create/write/remove/rename of a non-ignored path, and adds watches for newly created directories.

- [ ] **Step 1: Add the dependency**

Run:
```bash
go get github.com/fsnotify/fsnotify@v1.9.0
go mod tidy
```
Expected: `go.mod` lists `github.com/fsnotify/fsnotify`.

- [ ] **Step 2: Write failing integration test**

```go
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
```
Add imports `"os"`, `"path/filepath"` to the test file.

- [ ] **Step 3: Run to verify fail**

Run: `go test ./internal/files/ -run FSWatcher -v`
Expected: FAIL (undefined `newFSWatcher`).

- [ ] **Step 4: Implement the adapter (append to `watch.go`)**

```go
import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

type fsWatcher struct {
	root   string
	inner  *fsnotify.Watcher
	events chan string
	errs   chan error
	done   chan struct{}
}

func newFSWatcher(root string, ignore *Matcher, hidden bool) (watcher, error) {
	inner, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &fsWatcher{root: root, inner: inner,
		events: make(chan string, 8), errs: make(chan error, 1), done: make(chan struct{})}
	if err := w.addTree(root); err != nil {
		inner.Close()
		return nil, err
	}
	go w.loop(ignore, hidden)
	return w, nil
}

// addTree walks dir and registers a watch on every subdirectory (fsnotify is
// non-recursive). A missing dir is not fatal — the service's sanity-guard handles
// a vanished root.
func (w *fsWatcher) addTree(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			_ = w.inner.Add(p)
		}
		return nil
	})
}

func (w *fsWatcher) loop(ignore *Matcher, hidden bool) {
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.inner.Events:
			if !ok {
				return
			}
			base := filepath.Base(ev.Name)
			if !hidden && strings.HasPrefix(base, ".") {
				continue
			}
			if ignore != nil && ignore.Match(base) {
				continue
			}
			// A newly created directory needs its own watch.
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = w.addTree(ev.Name)
				}
			}
			select {
			case w.events <- w.root:
			default: // a pending event already covers this root
			}
		case err, ok := <-w.inner.Errors:
			if !ok {
				return
			}
			select {
			case w.errs <- err:
			default:
			}
		}
	}
}

func (w *fsWatcher) Events() <-chan string { return w.events }
func (w *fsWatcher) Errors() <-chan error  { return w.errs }
func (w *fsWatcher) Close() error {
	close(w.done)
	return w.inner.Close()
}
```

Note: confirm `Matcher.Match` is the correct method name; if the existing matcher exposes a different method (e.g. `Matches`), use that. Check `internal/files/ignore.go`.

- [ ] **Step 5: Run to verify pass + full package**

Run: `go test ./internal/files/ -run 'FSWatcher|Debouncer' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/files/watch.go internal/files/watch_test.go go.mod go.sum
git commit -m "feat(files): recursive fsnotify watcher adapter (+ fsnotify dep)"
```

---

## Task 4: Service supervisor core

**Files:**
- Create: `internal/files/service.go`
- Test: `internal/files/service_test.go`

**Interfaces:**
- Consumes: `NewSyncer`, `SyncOptions`, `NewMatcher`, `*Store`, `*api.Client`, `watcher`, `debouncer`, `*audit.Logger`.
- Produces:
  ```go
  type ServiceMapping struct {
      Remote, Local string
      Opts          SyncOptions
      Interval      time.Duration
  }
  type Service struct {
      Client     *api.Client
      Store      *Store
      VK         []byte
      Mappings   []ServiceMapping
      Debounce   time.Duration
      Log        func(string)
      Audit      *audit.Logger
      Heartbeat  func(state, detail string) (wipe bool) // may be nil
      NewWatcher func(root string, ignore *Matcher, hidden bool) (watcher, error) // nil → newFSWatcher
  }
  func (s *Service) Run(ctx context.Context) error
  ```
- The worker runs passes sequentially. Triggers (debounced FS events + per-mapping interval tickers) enqueue the mapping key; a queue de-dupes so a mapping already queued/running is not queued twice.

- [ ] **Step 1: Write failing test (fake watcher + mock server)**

Reuse the package's existing mock-server + store test helpers (see `internal/files/files_test.go` for `newTestClient`/store setup — mirror that). The fake watcher lets the test push an event and assert a pass ran.

```go
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
	client, vk := newTestClient(t) // helper mirrored from files_test.go
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
```

If `newTestClient`/`remoteHasFile` helpers don't already exist in `files_test.go`, add minimal versions there (a helper that reloads a fresh store and checks a filename exists). Keep them in the test file, not production code.

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/files/ -run ServiceRunsPass -v`
Expected: FAIL (undefined `Service`).

- [ ] **Step 3: Implement `internal/files/service.go`**

```go
package files

import (
	"context"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
)

type ServiceMapping struct {
	Remote, Local string
	Opts          SyncOptions
	Interval      time.Duration
}

type Service struct {
	Client     *api.Client
	Store      *Store
	VK         []byte
	Mappings   []ServiceMapping
	Debounce   time.Duration
	Log        func(string)
	Audit      *audit.Logger
	Heartbeat  func(state, detail string) (wipe bool)
	NewWatcher func(root string, ignore *Matcher, hidden bool) (watcher, error)
}

func (s *Service) logf(format string, a ...any) {
	if s.Log != nil {
		s.Log(sprintf(format, a...))
	}
}

// Run starts watchers + tickers and processes a coalescing per-mapping queue
// until ctx is cancelled. Passes run sequentially (the store is single-writer).
func (s *Service) Run(ctx context.Context) error {
	if s.NewWatcher == nil {
		s.NewWatcher = newFSWatcher
	}
	deb := newDebouncer(s.Debounce)
	defer deb.stop()

	byKey := map[string]ServiceMapping{}
	for _, m := range s.Mappings {
		byKey[m.Local] = m
		if w, err := s.NewWatcher(m.Local, m.Opts.Ignore, m.Opts.Hidden); err != nil {
			s.logf("watch %s: %v (interval-only for this mapping)", m.Local, err)
		} else {
			go s.pump(ctx, w, m.Local, deb)
		}
		go s.tick(ctx, m, deb)
	}

	queued := map[string]bool{}
	pending := []string{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case key := <-deb.C():
			if !queued[key] {
				queued[key] = true
				pending = append(pending, key)
			}
		}
		for len(pending) > 0 {
			if ctx.Err() != nil {
				return nil
			}
			key := pending[0]
			pending = pending[1:]
			delete(queued, key)
			m, ok := byKey[key]
			if !ok {
				continue
			}
			if fatal := s.runPass(ctx, m); fatal != nil {
				return fatal
			}
			// Drain any triggers that arrived during the pass without blocking.
			for {
				select {
				case k := <-deb.C():
					if !queued[k] {
						queued[k] = true
						pending = append(pending, k)
					}
					continue
				default:
				}
				break
			}
		}
	}
}

// pump forwards a watcher's events into the debouncer, keyed by the mapping.
func (s *Service) pump(ctx context.Context, w watcher, key string, deb *debouncer) {
	defer w.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-w.Events():
			if !ok {
				return
			}
			deb.trigger(key)
		case err, ok := <-w.Errors():
			if !ok {
				return
			}
			s.logf("watch %s error: %v", key, err)
		}
	}
}

// tick enqueues a pass for one mapping on its interval (server-change poll).
func (s *Service) tick(ctx context.Context, m ServiceMapping, deb *debouncer) {
	iv := m.Interval
	if iv <= 0 {
		iv = time.Minute
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	deb.trigger(m.Local) // an initial pass at startup
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			deb.trigger(m.Local)
		}
	}
}
```

Add a tiny local `sprintf` wrapper or just import `fmt` and use `fmt.Sprintf` in `logf` (prefer `fmt`). `runPass` is defined in Task 5 — for this task, add a minimal version that runs the syncer and returns nil, to be extended:

```go
func (s *Service) runPass(ctx context.Context, m ServiceMapping) (fatal error) {
	res, err := NewSyncer(s.Client, s.Store, s.VK, m.Local, m.Remote, m.Opts).Run(ctx)
	if err != nil {
		s.logf("sync %s: %v", m.Local, err)
		return nil
	}
	s.logf("sync %s: ↑%d ↓%d ✗%d !%d", m.Local,
		res.Uploaded, res.Downloaded, res.TrashedRemote+res.DeletedLocal, res.Conflicts)
	return nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/files/ -run ServiceRunsPass -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/files/service.go internal/files/service_test.go internal/files/files_test.go
git commit -m "feat(files): sync service supervisor (watch + interval + coalescing queue)"
```

---

## Task 5: Sanity-guard (pause instead of mass-delete)

**Files:**
- Modify: `internal/files/service.go` (extend `runPass`)
- Test: `internal/files/service_test.go`

**Interfaces:**
- Consumes: `loadSyncState(localDir, remoteBase) (syncState, error)` (unexported, same package), `os.Stat`.
- Produces: guard behaviour inside `runPass`; a `paused map[string]bool` field on `Service` guarding log spam.

Guard rule: before a pass, if the local root does not exist, OR it exists but contains no non-ignored files while `loadSyncState` reports a non-empty prior state, skip the pass, emit `files.sync_paused` once, and log a warning. Resume (and log resume) when the root reappears with content.

- [ ] **Step 1: Write failing test**

```go
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
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/files/ -run PausesWhenLocalRoot -v`
Expected: FAIL (guard not implemented / `paused` nil-map panic or remote deleted).

- [ ] **Step 3: Implement the guard**

Add `paused map[string]bool` to `Service`, lazily initialised. Replace `runPass`:

```go
func (s *Service) runPass(ctx context.Context, m ServiceMapping) (fatal error) {
	if s.paused == nil {
		s.paused = map[string]bool{}
	}
	if s.rootUnsafe(m) {
		if !s.paused[m.Local] {
			s.paused[m.Local] = true
			s.logf("PAUSED %s: local folder missing or empty but state has files — refusing to delete remote", m.Local)
			s.Audit.Log(audit.Event{Event: "files.sync_paused", Outcome: audit.OutcomeError, Target: "files"})
		}
		return nil
	}
	if s.paused[m.Local] {
		delete(s.paused, m.Local)
		s.logf("RESUMED %s", m.Local)
	}
	res, err := NewSyncer(s.Client, s.Store, s.VK, m.Local, m.Remote, m.Opts).Run(ctx)
	if err != nil {
		s.logf("sync %s: %v", m.Local, err)
		return nil
	}
	s.logf("sync %s: ↑%d ↓%d ✗%d !%d", m.Local,
		res.Uploaded, res.Downloaded, res.TrashedRemote+res.DeletedLocal, res.Conflicts)
	return nil
}

// rootUnsafe reports whether a pass would be a mass-delete: the local root is
// gone, or empty of non-ignored files while the sync state records prior files.
func (s *Service) rootUnsafe(m ServiceMapping) bool {
	info, err := os.Stat(m.Local)
	if err != nil || !info.IsDir() {
		return s.hadState(m)
	}
	entries, err := os.ReadDir(m.Local)
	if err != nil {
		return s.hadState(m)
	}
	for _, e := range entries {
		name := e.Name()
		if !m.Opts.Hidden && strings.HasPrefix(name, ".") {
			continue
		}
		if m.Opts.Ignore != nil && m.Opts.Ignore.Match(name) {
			continue
		}
		return false // has at least one relevant entry → safe
	}
	return s.hadState(m)
}

func (s *Service) hadState(m ServiceMapping) bool {
	st, err := loadSyncState(m.Local, m.Remote)
	return err == nil && len(st) > 0
}
```

Add imports `"os"`, `"strings"`, and (confirm) `audit` already imported. Note `s.Audit` is a `*audit.Logger`; a nil logger is safe (its `Log` is a no-op).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/files/ -run 'Pauses|ServiceRunsPass' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/files/service.go internal/files/service_test.go
git commit -m "feat(files): sanity-guard pauses a mapping instead of mass-deleting"
```

---

## Task 6: Resilience — auth-stop, degraded-pause, heartbeat, audit

**Files:**
- Modify: `internal/files/service.go`
- Test: `internal/files/service_test.go`

**Interfaces:**
- Consumes: `api.ErrVersionConflict`/`ErrMissingShard` are handled inside the syncer already; here handle `ErrDegraded` (this package), and an auth-fatal signal. Since the syncer returns a plain error, detect auth-fatal via `errors.Is(err, api.ErrUnauthorized)` if that sentinel exists; otherwise via `api.Status(err) == http.StatusUnauthorized`. Check `internal/api` for the exact sentinel/helper and use it.
- Produces: `runPass` returns a non-nil `fatal` error only on auth-fatal; emits start/stop/pass audit events; drives the heartbeat.

- [ ] **Step 1: Write failing test (auth-fatal stops the service)**

```go
func TestServiceStopsOnAuthFatal(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	client, vk := newTestClientReturning401(t) // mock server replies 401 on store PUT
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
```

Add a `newTestClientReturning401` helper in `files_test.go` (a mock server whose `/files/store` PUT returns 401) — mirror the existing mock-server setup.

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/files/ -run StopsOnAuthFatal -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

In `runPass`, after the syncer returns `err`, classify before the generic log:

```go
	if err != nil {
		if isAuthFatal(err) {
			return err // supervisor exits; caller tells the user to log in again
		}
		if errors.Is(err, ErrDegraded) {
			if !s.paused[m.Local] {
				s.paused[m.Local] = true
				s.logf("PAUSED %s: store degraded (missing shard) — read-only", m.Local)
			}
			return nil
		}
		s.logf("sync %s: %v (will retry)", m.Local, err)
		s.Audit.Log(audit.Event{Event: "files.sync_pass", Outcome: audit.OutcomeError, Target: "files"})
		return nil
	}
	s.Audit.Log(audit.Event{Event: "files.sync_pass", Outcome: audit.OutcomeOK, Target: "files",
		Count: res.Uploaded + res.Downloaded, Duration: 0})
```

Add `isAuthFatal` using the confirmed api sentinel/helper, e.g.:

```go
func isAuthFatal(err error) bool {
	return api.Status(err) == http.StatusUnauthorized
}
```

In `Run`, wrap the loop with start/stop audit + heartbeat:

```go
	s.Audit.Log(audit.Event{Event: "files.service_start", Outcome: audit.OutcomeStart, Target: "files", Count: len(s.Mappings)})
	defer s.Audit.Log(audit.Event{Event: "files.service_stop", Outcome: audit.OutcomeOK, Target: "files"})
	if s.Heartbeat != nil {
		if wipe := s.Heartbeat("syncing", "files"); wipe {
			return errWiped
		}
		defer s.Heartbeat("idle", "")
	}
```

Add `var errWiped = errors.New("client wiped remotely")` and imports `"errors"`, `"net/http"`.

- [ ] **Step 4: Run to verify pass + whole package**

Run: `go test ./internal/files/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/files/service.go internal/files/service_test.go internal/files/files_test.go
git commit -m "feat(files): service resilience — auth-stop, degraded-pause, audit + heartbeat"
```

---

## Task 7: CLI wiring — `--service` flag, `--map` upsert, lockfile, signals

**Files:**
- Modify: `cmd/files_sync.go`
- Create: `cmd/lock.go`
- Test: `cmd/lock_test.go`

**Interfaces:**
- Consumes: `settings.Load/Save/Upsert`, `files.Service`, `config.Dir`, `authedClient`, `unlockVaultPrompt` (always prompt — the service must not silently rely on a cache that can't be refreshed), `reportSync`, `shardCache`, `warnIfDegraded`, `auditLog`.
- Produces: `func acquireLock(name string) (release func(), err error)` in `cmd/lock.go`.

- [ ] **Step 1: Write failing lock test**

```go
package cmd

import "testing"

func TestAcquireLockIsExclusive(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	rel, err := acquireLock("service")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := acquireLock("service"); err == nil {
		t.Fatal("second acquire should fail while held")
	}
	rel()
	rel2, err := acquireLock("service")
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	rel2()
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./cmd/ -run AcquireLock -v`
Expected: FAIL (undefined `acquireLock`).

- [ ] **Step 3: Implement `cmd/lock.go`**

```go
package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// acquireLock creates an exclusive <config>/<name>.lock holding the pid. A stale
// lock (pid no longer running) is reclaimed. release() removes it.
func acquireLock(name string) (func(), error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) && staleLock(path) {
			_ = os.Remove(path)
			return acquireLock(name)
		}
		return nil, fmt.Errorf("another instance holds %s (%s)", name, path)
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	return func() { _ = os.Remove(path) }, nil
}

// staleLock reports whether the pid in path is no longer alive.
func staleLock(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(string(trimNL(b)))
	if err != nil {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	return p.Signal(os.Signal(nil)) != nil // signal 0 probes liveness on unix
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
```

Note: `p.Signal(syscall.Signal(0))` is the portable liveness probe on unix; on Windows `FindProcess` already fails for a dead pid. If the build targets Windows, guard with a build tag or accept the simpler "FindProcess err → stale" path. Keep unix-first (the service is a unix/tmux use case).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./cmd/ -run AcquireLock -v`
Expected: PASS.

- [ ] **Step 5: Add `--service` flag + wiring to `cmd/files_sync.go`**

- Add `service bool` to the flags and `syncFlags`; register `f.BoolVar(&service, "service", false, "run continuously: watch + interval sync until interrupted")`.
- In `runFilesSync`, branch at the top:
  - If `fl.service`:
    1. If `fl.maps` present: load settings, for each parsed mapping build a `settings.Mapping` carrying the current policy flags (conflict/delete/hidden/ignore) and `Upsert` it; `settings.Save`.
    2. Resolve all mappings from settings (`resolveMappings(nil, cfg)`).
    3. `acquireLock("service")`; `defer release()`.
    4. `authedClient`; `unlockVaultPrompt` (always prompt).
    5. Build the store (as today), `store.Load`, `warnIfDegraded`.
    6. Build `[]files.ServiceMapping` resolving per-mapping `Opts` + `Interval` (use `settings.ParseDurationOr(m.Interval, globalInterval)`, `globalInterval = ParseDurationOr(cfg.Service.Interval, 60s)`, `debounce = ParseDurationOr(cfg.Service.Debounce, 2s)`).
    7. Create a `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` and run:
       ```go
       svc := &files.Service{
           Client: client, Store: store, VK: vk,
           Mappings: sms, Debounce: debounce,
           Log: func(s string) { fmt.Fprintln(w, s) },
           Audit: auditLog(),
           Heartbeat: func(state, detail string) bool { return reportSync(ctx, client, state, detail) },
       }
       fmt.Fprintf(w, "Service running (%d mappings). Ctrl-C to stop.\n", len(sms))
       return svc.Run(sigCtx)
       ```
  - Else: the existing one-shot path, unchanged, plus: if `acquireLock` would fail (service running), print a warning via `warnServiceRunning()` (best-effort: check for the lock file's existence and print a note) but proceed.

- [ ] **Step 6: Build + manual smoke**

Run:
```bash
go build ./...
go vet ./...
```
Expected: clean build.

Manual (documented for the executor; requires a logged-in client):
```bash
mkdir -p /tmp/ll-sync-demo && echo hi > /tmp/ll-sync-demo/a.txt
./ledgerline-cli files sync --map "Demo":/tmp/ll-sync-demo --service
# observe: banner, initial pass uploads a.txt; edit the file → a new pass runs;
# Ctrl-C → "idle" heartbeat + clean exit; settings.json now has the Demo mapping.
```

- [ ] **Step 7: Commit**

```bash
git add cmd/files_sync.go cmd/lock.go cmd/lock_test.go
git commit -m "feat(files): files sync --service (persist mapping, lockfile, signals)"
```

---

## Task 8: Docs — CLAUDE.md + changelog

**Files:**
- Modify: `CLAUDE.md` (§4 module map note, §5 dependency inventory, §15 changelog)

- [ ] **Step 1: Update §5** — add under Direct dependencies:
  `github.com/fsnotify/fsnotify vX.Y.Z` — **justified**: no stdlib OS file-event API; cross-platform (inotify/kqueue/ReadDirectoryChangesW) recursive watch for `files sync --service`. Pinned + checksum-verified.

- [ ] **Step 2: Update §4** — note `internal/files` now also holds the continuous sync service (`service.go`, `watch.go`).

- [ ] **Step 3: Add §15 changelog entry** (date 2026-08-01) — `feat(files): files sync --service — continuous watch+interval sync client (fsnotify), per-mapping policy in settings.json, unattended sanity-guard, single-instance lock, graceful shutdown`.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "docs(claude): record files sync --service + fsnotify dependency"
```

---

## Self-Review

**Spec coverage:** config schema → T1; watcher+interval → T2/T3/T4; `--map` upsert + `--service` start → T1/T7; sanity-guard → T5; resilience (auth-stop/degraded/heartbeat/audit) → T6; single-instance lock + signals → T7; dependency + docs → T3/T8; testing → each task. All spec sections covered.

**Placeholder scan:** every code step carries real code. Two explicit "confirm the exact name in the existing package" notes remain (`Matcher.Match`, the api unauthorized sentinel/helper) — these are verification instructions with a concrete fallback, not placeholders.

**Type consistency:** `Service`, `ServiceMapping`, `watcher`, `debouncer`, `acquireLock`, `settings.Mapping`/`Service`/`Upsert`/`ParseDurationOr` names are used consistently across tasks. `runPass` is introduced minimally in T4 and extended in T5/T6 (same signature `(ctx, ServiceMapping) (fatal error)`).

## Execution Handoff

Two execution options — see below.
