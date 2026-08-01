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
