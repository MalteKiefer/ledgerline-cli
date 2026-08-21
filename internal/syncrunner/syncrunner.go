// Package syncrunner keeps the configured folder pairs up to date: on a
// schedule, and — the part an interval alone cannot give you — as soon as
// something in a watched directory changes.
//
// It exists as its own package because both front ends need exactly this loop:
// `ledgerline-cli sync service` on a machine with no desktop, and the tray while
// it is running. Two copies would drift, and the interesting behaviour (when to
// run, when not to, what to do when the pair list changes underneath) is worth
// testing without a server or a tray.
package syncrunner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/files"
	"github.com/MalteKiefer/ledgerline-cli/internal/syncconfig"
)

// Defaults. Debounce is the one that matters: an editor saving a file produces
// a burst of events, and a copy of a directory produces one per file, so the
// runner waits for the noise to stop before syncing that pair.
const (
	DefaultDebounce = 5 * time.Second
	DefaultTick     = time.Minute
	DefaultReload   = 30 * time.Second
)

// ClientFunc returns an authenticated client, or an error when the user is
// signed out. It is called per run rather than once, so the runner picks up a
// sign-in without being restarted.
type ClientFunc func() (*api.Client, error)

// SyncFunc performs one pair's sync. Injectable so the loop can be tested
// without a server.
type SyncFunc func(ctx context.Context, c *api.Client, p syncconfig.Pair) (files.SyncResult, error)

// Options tunes the loop; the zero value uses the defaults above.
type Options struct {
	Debounce time.Duration
	Tick     time.Duration
	Reload   time.Duration

	// Sync overrides the real sync (tests).
	Sync SyncFunc
	// Log receives one line per interesting event; nil discards them.
	Log func(format string, args ...any)
	// OnStart, when set, is called just before a pair syncs. The tray needs it:
	// "syncing now" is the state a progress indicator exists for, and a result
	// callback alone can only ever report the past.
	OnStart func(p syncconfig.Pair)
	// OnResult, when set, is called after every run (the tray repaints from it).
	OnResult func(p syncconfig.Pair, res files.SyncResult, err error)

	// Hold, when set, is asked before every scheduled run. A non-empty answer
	// is the reason to skip, which is logged once per reason rather than per
	// pair; nil means never hold.
	//
	// This is where a desktop client's "pause syncing", "not on battery" and
	// "not on metered connections" live. They are the caller's policy, not the
	// runner's: the CLI's `sync service` deliberately has none of them, because
	// a service somebody started explicitly should run.
	Hold func() string

	// Skip is passed through to every sync: names the caller never wants
	// carried. Nil means the sync's own rules (hidden files) are the only ones.
	Skip func(name string) bool
}

// Runner watches and syncs. Create with New and call Run.
type Runner struct {
	client ClientFunc
	opts   Options

	mu      sync.Mutex
	watcher *fsnotify.Watcher
	watched map[string]string // watched local root -> pair id
	timers  map[string]*time.Timer
	running map[string]bool // pair ids with a run in flight
}

// New builds a runner. clientFn is required; everything else has a default.
func New(clientFn ClientFunc, opts Options) *Runner {
	if opts.Debounce <= 0 {
		opts.Debounce = DefaultDebounce
	}
	if opts.Tick <= 0 {
		opts.Tick = DefaultTick
	}
	if opts.Reload <= 0 {
		opts.Reload = DefaultReload
	}
	if opts.Sync == nil {
		// Bound to the caller's skip list here rather than inside realSync,
		// which has no access to the options.
		skip := opts.Skip
		opts.Sync = func(ctx context.Context, c *api.Client, p syncconfig.Pair) (files.SyncResult, error) {
			return syncPair(ctx, c, p, skip)
		}
	}
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	return &Runner{
		client:  clientFn,
		opts:    opts,
		watched: map[string]string{},
		timers:  map[string]*time.Timer{},
		running: map[string]bool{},
	}
}

// Run blocks until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		// Without a watcher the runner still honours intervals; losing change
		// detection is a degradation, not a reason to stop syncing.
		r.opts.Log("file watching unavailable: %v", err)
	} else {
		r.watcher = w
		defer w.Close()
	}

	r.rearm()
	r.runDue(ctx)

	tick := time.NewTicker(r.opts.Tick)
	defer tick.Stop()
	reload := time.NewTicker(r.opts.Reload)
	defer reload.Stop()

	var events <-chan fsnotify.Event
	var errs <-chan error
	if r.watcher != nil {
		events, errs = r.watcher.Events, r.watcher.Errors
	}

	for {
		select {
		case <-ctx.Done():
			r.stopTimers()
			return
		case <-tick.C:
			r.runDue(ctx)
		case <-reload.C:
			// The pair list is edited elsewhere (the CLI, the tray window), so
			// the runner re-reads it instead of holding a stale copy.
			r.rearm()
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			r.onEvent(ctx, ev)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			r.opts.Log("watch error: %v", err)
		}
	}
}

// onEvent maps a changed path back to its pair and schedules a debounced run.
func (r *Runner) onEvent(ctx context.Context, ev fsnotify.Event) {
	if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}
	// A new directory has to be watched too, or files created inside it later
	// would go unnoticed.
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			r.addTree(ev.Name)
		}
	}

	id := r.pairFor(ev.Name)
	if id == "" {
		return
	}
	r.schedule(ctx, id)
}

// schedule (re)starts the debounce timer for a pair.
func (r *Runner) schedule(ctx context.Context, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.timers[id]; ok {
		t.Stop()
	}
	r.timers[id] = time.AfterFunc(r.opts.Debounce, func() {
		p, err := syncconfig.Get(id)
		if err != nil || !p.Enabled {
			return
		}
		if reason := r.held(); reason != "" {
			r.opts.Log("change in %s, not syncing: %s", p.Local, reason)
			return
		}
		r.opts.Log("change detected in %s", p.Local)
		r.runPair(ctx, p)
	})
}

func (r *Runner) stopTimers() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.timers {
		t.Stop()
	}
	r.timers = map[string]*time.Timer{}
}

// pairFor returns the id of the pair whose local root contains path.
func (r *Runner) pairFor(path string) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	best, bestID := 0, ""
	for root, id := range r.watched {
		if underRoot(path, root) && len(root) > best {
			best, bestID = len(root), id
		}
	}
	return bestID
}

// underRoot reports whether path is root or lives inside it. Compared
// case-insensitively: Windows hands back whatever case the event carried.
func underRoot(path, root string) bool {
	p := strings.ToLower(filepath.Clean(path))
	r := strings.ToLower(filepath.Clean(root))
	if p == r {
		return true
	}
	return strings.HasPrefix(p, r+string(filepath.Separator))
}

// rearm re-reads the configuration and makes the watch set match it.
func (r *Runner) rearm() {
	f, err := syncconfig.Load()
	if err != nil {
		r.opts.Log("could not read the sync configuration: %v", err)
		return
	}

	want := map[string]string{}
	for _, p := range f.Pairs {
		if p.Enabled && p.Watch {
			want[filepath.Clean(p.Local)] = p.ID
		}
	}

	r.mu.Lock()
	current := make(map[string]string, len(r.watched))
	for k, v := range r.watched {
		current[k] = v
	}
	r.watched = want
	r.mu.Unlock()

	if r.watcher == nil {
		return
	}
	for root := range current {
		if _, keep := want[root]; !keep {
			_ = r.watcher.Remove(root)
		}
	}
	for root := range want {
		if _, had := current[root]; !had {
			r.addTree(root)
		}
	}
}

// addTree watches a directory and everything under it. fsnotify is not
// recursive, so every subdirectory needs its own watch.
func (r *Runner) addTree(root string) {
	if r.watcher == nil {
		return
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable subtree is not worth aborting the walk
		}
		if !d.IsDir() {
			return nil
		}
		if p != root && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir // the sync engine skips these too
		}
		_ = r.watcher.Add(p)
		return nil
	})
}

// runDue syncs every pair whose interval has elapsed.
func (r *Runner) runDue(ctx context.Context) {
	f, err := syncconfig.Load()
	if err != nil {
		return
	}
	now := time.Now()
	for _, p := range f.Pairs {
		if ctx.Err() != nil {
			return
		}
		if p.Due(now) {
			r.runPair(ctx, p)
		}
	}
}

// runPair syncs one pair, records the outcome and reports it. A pair already
// running is skipped rather than queued: the next tick or the next change will
// pick it up, and two concurrent runs over one directory would fight.
func (r *Runner) runPair(ctx context.Context, p syncconfig.Pair) {
	r.mu.Lock()
	if r.running[p.ID] {
		r.mu.Unlock()
		return
	}
	r.running[p.ID] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, p.ID)
		r.mu.Unlock()
	}()

	client, err := r.client()
	if err != nil {
		return // signed out: nothing to say, every pair would say the same
	}
	if reason := r.held(); reason != "" {
		return // the caller's policy; the interval tick already logged it
	}

	if r.opts.OnStart != nil {
		r.opts.OnStart(p)
	}
	res, err := r.opts.Sync(ctx, client, p)
	summary := formatResult(res)
	_ = syncconfig.RecordRun(p.ID, time.Now(), summary, err)

	if err != nil {
		r.opts.Log("sync %s failed: %v", p.Local, err)
	} else if res.Pushed+res.Pulled+res.Conflicts+res.Failed > 0 {
		// A no-op run is the common case; logging it would bury the real ones.
		r.opts.Log("synced %s: %s", p.Local, summary)
	}
	if r.opts.OnResult != nil {
		r.opts.OnResult(p, res, err)
	}
}

func formatResult(res files.SyncResult) string {
	return fmt.Sprintf("%d pushed, %d pulled, %d conflicts, %d failed",
		res.Pushed, res.Pulled, res.Conflicts, res.Failed)
}

// realSync is the production sync call.
func syncPair(ctx context.Context, c *api.Client, p syncconfig.Pair, skip func(string) bool) (files.SyncResult, error) {
	return files.Sync(ctx, c, p.Local, files.SyncOptions{
		Direction:  p.Direction,
		Conflict:   p.Conflict,
		RemoteRoot: p.Remote,
		Skip:       skip,
	}, discard{}, false)
}

// discard swallows the sync engine's progress output: neither front end shows
// it live, and both record the summary instead.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// held asks the caller whether scheduled syncing should stand down, and returns
// the reason. It is asked at the last moment rather than once per tick: a laptop
// unplugged mid-loop should stop, not finish the round.
func (r *Runner) held() string {
	if r.opts.Hold == nil {
		return ""
	}
	return r.opts.Hold()
}
