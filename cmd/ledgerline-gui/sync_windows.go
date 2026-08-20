//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/clientset"
	"github.com/MalteKiefer/ledgerline-cli/internal/files"
	"github.com/MalteKiefer/ledgerline-cli/internal/syncconfig"
	"github.com/MalteKiefer/ledgerline-cli/internal/win32ui"
)

// Messages the sync window handles. They start above the login dialog's block
// so the two never collide if both are open.
const (
	msgSyncRefresh = win32ui.UserMessage + 100 + iota
	msgSyncFinished
)

// syncTick is how often the background runner looks for a pair whose interval
// has elapsed. The interval itself lives on the pair; this is only the
// granularity of the check.
const syncTick = time.Minute

// syncWindow lists the folder pairs and runs them on demand. It is the same
// list `ledgerline-cli sync ls` prints, read from and written to the same file,
// so the two front ends cannot drift apart.
type syncWindow struct {
	win  *win32ui.Window
	list *win32ui.Control

	status *win32ui.Control
	remove *win32ui.Control
	toggle *win32ui.Control
	runNow *win32ui.Control

	pairs []syncconfig.Pair
	busy  atomic.Bool

	// message is filled by the worker before it posts msgSyncFinished.
	message string
}

// runSyncWindow shows the window and blocks until it closes.
func runSyncWindow() {
	win32ui.EnableDPIAwareness()

	win, err := win32ui.NewWindow("Ledgerline — synced folders", 620, 380)
	if err != nil {
		return
	}
	w := &syncWindow{win: win}
	w.build()
	win.Run()
}

func (w *syncWindow) build() {
	w.win.Label("Folders kept in sync with the server:", 16, 12, 400, 20)
	w.list = w.win.List(16, 36, 588, 200)

	w.win.Button("Add folder…", 16, 248, 110, 26, true, w.onAdd)
	w.remove = w.win.Button("Remove", 134, 248, 90, 26, false, w.onRemove)
	w.toggle = w.win.Button("Pause", 232, 248, 90, 26, false, w.onToggle)
	w.runNow = w.win.Button("Sync now", 330, 248, 100, 26, false, w.onRunSelected)
	w.win.Button("Sync all", 438, 248, 90, 26, false, w.onRunAll)
	w.win.Button("Close", 536, 248, 68, 26, false, func() { w.win.Close() })

	w.status = w.win.Label("", 16, 288, 588, 60)

	w.win.OnMessage(msgSyncRefresh, func(uintptr) { w.reload() })
	w.win.OnMessage(msgSyncFinished, func(uintptr) {
		w.setBusy(false)
		w.reload()
		w.status.SetText(w.message)
	})

	w.reload()
	w.status.SetText("Deleting a pair here removes the arrangement only — no files are deleted,\r\nlocally or on the server.")
}

// reload re-reads the configuration and repaints the list.
func (w *syncWindow) reload() {
	f, err := syncconfig.Load()
	if err != nil {
		w.status.SetText("Could not read the sync configuration: " + err.Error())
		return
	}
	selected := w.list.ListSelected()
	w.pairs = f.Pairs

	w.list.ListClear()
	for _, p := range f.Pairs {
		w.list.ListAdd(describeForList(p))
	}
	if selected >= 0 && selected < len(w.pairs) {
		w.list.ListSelect(selected)
	} else if len(w.pairs) > 0 {
		w.list.ListSelect(0)
	}
	w.updateButtons()
}

// describeForList renders one row: the arrangement, then what happened last.
func describeForList(p syncconfig.Pair) string {
	row := p.ID + "   " + p.Describe()
	switch {
	case p.LastFailure != "":
		row += "   — failed: " + firstLine(p.LastFailure)
	case p.LastResult != "":
		row += "   — " + p.LastResult
	}
	return row
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	const max = 60
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

func (w *syncWindow) updateButtons() {
	has := len(w.pairs) > 0
	for _, c := range []*win32ui.Control{w.remove, w.toggle, w.runNow} {
		c.SetEnabled(has && !w.busy.Load())
	}
	if p, ok := w.selected(); ok {
		if p.Enabled {
			w.toggle.SetText("Pause")
		} else {
			w.toggle.SetText("Resume")
		}
	}
}

func (w *syncWindow) selected() (syncconfig.Pair, bool) {
	i := w.list.ListSelected()
	if i < 0 || i >= len(w.pairs) {
		return syncconfig.Pair{}, false
	}
	return w.pairs[i], true
}

// onAdd picks a local folder and adds a pair for it. The remote folder defaults
// to the folder's own name, which is what people expect and can be changed
// afterwards from the command line.
func (w *syncWindow) onAdd() {
	local := win32ui.PickFolder(w.win.Handle(), "Choose a folder to keep in sync")
	if local == "" {
		return
	}
	pair, err := syncconfig.Add(syncconfig.Pair{
		Local:           local,
		Remote:          filepath.Base(local),
		Direction:       syncconfig.DirectionBoth,
		Conflict:        syncconfig.ConflictNewest,
		IntervalMinutes: int(syncconfig.DefaultInterval.Minutes()),
		Enabled:         true,
	})
	if err != nil {
		win32ui.Error(w.win.Handle(), "Cannot add this folder", err.Error())
		return
	}
	w.reload()
	w.status.SetText(fmt.Sprintf("Added %s. It will sync with the remote folder %q every %s.",
		pair.Local, pair.Remote, syncconfig.DefaultInterval))
}

func (w *syncWindow) onRemove() {
	p, ok := w.selected()
	if !ok {
		return
	}
	if !win32ui.Confirm(w.win.Handle(), "Remove this pair?",
		"Stop syncing "+p.Local+"?\n\nNothing is deleted: the files stay where they are, here and on the server.") {
		return
	}
	if err := syncconfig.Remove(p.ID); err != nil {
		win32ui.Error(w.win.Handle(), "Could not remove the pair", err.Error())
		return
	}
	w.reload()
	w.status.SetText("Removed. No files were deleted.")
}

func (w *syncWindow) onToggle() {
	p, ok := w.selected()
	if !ok {
		return
	}
	updated, err := syncconfig.Update(p.ID, func(t *syncconfig.Pair) { t.Enabled = !t.Enabled })
	if err != nil {
		win32ui.Error(w.win.Handle(), "Could not change the pair", err.Error())
		return
	}
	w.reload()
	if updated.Enabled {
		w.status.SetText("Resumed " + updated.Local)
		return
	}
	w.status.SetText("Paused " + updated.Local)
}

func (w *syncWindow) onRunSelected() {
	if p, ok := w.selected(); ok {
		w.run([]syncconfig.Pair{p})
	}
}

func (w *syncWindow) onRunAll() {
	var due []syncconfig.Pair
	for _, p := range w.pairs {
		if p.Enabled {
			due = append(due, p)
		}
	}
	if len(due) == 0 {
		w.status.SetText("No enabled pairs to sync.")
		return
	}
	w.run(due)
}

// run syncs the given pairs off the UI thread and reports back by message.
func (w *syncWindow) run(pairs []syncconfig.Pair) {
	if !w.busy.CompareAndSwap(false, true) {
		return
	}
	w.setBusy(true)
	w.status.SetText(fmt.Sprintf("Syncing %s…", plural(len(pairs), "folder", "folders")))

	go func() {
		w.message = syncPairs(context.Background(), pairs)
		w.win.Post(msgSyncFinished, 0)
	}()
}

func (w *syncWindow) setBusy(on bool) {
	w.busy.Store(on)
	w.updateButtons()
}

// syncPairs runs each pair and returns a one-line summary for the status area.
// It is shared with the background runner, which reports nothing on success.
func syncPairs(ctx context.Context, pairs []syncconfig.Pair) string {
	client, _, err := clientset.Authenticated()
	if err != nil {
		if errors.Is(err, clientset.ErrNotAuthenticated) {
			return "Not signed in — sign in from the tray first."
		}
		return "Cannot reach the server: " + err.Error()
	}

	var pushed, pulled, failed int
	var firstErr error
	for _, p := range pairs {
		if ctx.Err() != nil {
			break
		}
		runCtx, cancel := context.WithTimeout(ctx, syncPairTimeout)
		var out bytes.Buffer // the progress writer is not shown in the GUI
		res, err := files.Sync(runCtx, client, p.Local, files.SyncOptions{
			Direction:  p.Direction,
			Conflict:   p.Conflict,
			RemoteRoot: p.Remote,
		}, &out, false)
		cancel()

		summary := fmt.Sprintf("%d pushed, %d pulled, %d conflicts, %d failed",
			res.Pushed, res.Pulled, res.Conflicts, res.Failed)
		_ = syncconfig.RecordRun(p.ID, time.Now(), summary, err)

		pushed += res.Pushed
		pulled += res.Pulled
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	if firstErr != nil {
		return fmt.Sprintf("%s failed: %v", plural(failed, "folder", "folders"), firstErr)
	}
	return fmt.Sprintf("Done: %d uploaded, %d downloaded across %s.",
		pushed, pulled, plural(len(pairs), "folder", "folders"))
}

// syncPairTimeout bounds one pair so a stalled transfer cannot hold the queue.
const syncPairTimeout = 30 * time.Minute

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// startSyncRunner runs due pairs in the background for as long as the tray is
// running. It is the same schedule `ledgerline-cli sync service` follows.
func startSyncRunner(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(syncTick)
		defer ticker.Stop()
		for {
			runDuePairs(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// runDuePairs syncs whatever is due right now. Failures are recorded on the
// pair (visible in the window and in `sync ls`) rather than interrupting the
// user with a message box they did not ask for.
func runDuePairs(ctx context.Context) {
	f, err := syncconfig.Load()
	if err != nil {
		return
	}
	now := time.Now()
	var due []syncconfig.Pair
	for _, p := range f.Pairs {
		if p.Due(now) {
			due = append(due, p)
		}
	}
	if len(due) == 0 {
		return
	}
	// A signed-out client has nothing to do; syncPairs would only re-discover
	// that for every pair.
	if _, _, err := clientset.Authenticated(); err != nil {
		return
	}
	syncPairs(ctx, due)
}
