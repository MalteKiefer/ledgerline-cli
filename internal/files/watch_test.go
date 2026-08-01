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
