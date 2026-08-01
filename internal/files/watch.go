package files

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
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

// fsWatcher is the real filesystem watcher backed by fsnotify. fsnotify only
// watches individual directories (non-recursive), so fsWatcher walks the tree
// at startup and adds a watch for every newly created subdirectory as it
// appears.
type fsWatcher struct {
	root   string
	inner  *fsnotify.Watcher
	events chan string
	errs   chan error
	done   chan struct{}
}

// newFSWatcher recursively watches root and emits root on Events() whenever a
// non-ignored path under it is created, written, removed, or renamed.
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
