package files

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

// debounceWindow coalesces a burst of filesystem events into one sync.
const debounceWindow = 1500 * time.Millisecond

// RunService runs a continuous sync of dir: an initial pass, then a pass whenever
// the local tree changes (debounced) or the interval elapses. It returns when ctx
// is cancelled (nil) or when the server rejects the credential (401 → stop so a
// revoked device does not spin). Other transient sync errors are logged and the
// loop continues.
func RunService(ctx context.Context, c *api.Client, dir string, opts SyncOptions, interval time.Duration, log io.Writer) error {
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	runOnce := func() error {
		res, err := Sync(ctx, c, dir, opts, log, false)
		if err != nil {
			if api.Status(err) == 401 {
				return err
			}
			fmt.Fprintf(log, "sync error: %v\n", err)
			return nil
		}
		fmt.Fprintf(log, "synced: %d pushed, %d pulled, %d conflicts, %d failed\n",
			res.Pushed, res.Pulled, res.Conflicts, res.Failed)
		return nil
	}

	if err := runOnce(); err != nil {
		return err
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	addTree(w, dir)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var debounce <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			// A newly created directory must be watched too.
			if ev.Op&fsnotify.Create != 0 {
				if info, serr := os.Stat(ev.Name); serr == nil && info.IsDir() {
					addTree(w, ev.Name)
				}
			}
			debounce = time.After(debounceWindow)
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(log, "watch error: %v\n", err)
		case <-debounce:
			debounce = nil
			if err := runOnce(); err != nil {
				return err
			}
		case <-ticker.C:
			if err := runOnce(); err != nil {
				return err
			}
		}
	}
}

// addTree adds root and every subdirectory to the watcher (best-effort; hidden
// dirs are skipped to match the sync scan).
func addTree(w *fsnotify.Watcher, root string) {
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != root && len(d.Name()) > 0 && d.Name()[0] == '.' {
			return filepath.SkipDir
		}
		_ = w.Add(p)
		return nil
	})
}
