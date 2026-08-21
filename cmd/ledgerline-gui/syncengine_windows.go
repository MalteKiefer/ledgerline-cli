//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/applog"
	"github.com/MalteKiefer/ledgerline-cli/internal/clientset"
	"github.com/MalteKiefer/ledgerline-cli/internal/files"
	"github.com/MalteKiefer/ledgerline-cli/internal/syncconfig"
	"github.com/MalteKiefer/ledgerline-cli/internal/syncrunner"
	"github.com/MalteKiefer/ledgerline-cli/internal/trayui"
)

// syncPairs runs each pair and returns a one-line summary for the status area.
// It is shared with the background runner, which reports nothing on success.
func syncPairs(ctx context.Context, pairs []syncconfig.Pair) string {
	client, _, err := clientset.Authenticated()
	if err != nil {
		if errors.Is(err, clientset.ErrNotAuthenticated) {
			return "Not signed in. Sign in from the tray first."
		}
		return "Cannot reach the server: " + err.Error()
	}

	var pushed, pulled, failed int
	var firstErr error
	for _, p := range pairs {
		if ctx.Err() != nil {
			break
		}
		done := markRunning(p.ID)
		runCtx, cancel := context.WithTimeout(ctx, syncPairTimeout)
		var out bytes.Buffer // the progress writer is not shown in the GUI
		res, err := files.Sync(runCtx, client, p.Local, files.SyncOptions{
			Direction:  p.Direction,
			Conflict:   p.Conflict,
			RemoteRoot: p.Remote,
		}, &out, false)
		cancel()
		done()

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

// running tracks which pairs are syncing at this moment, so the tray icon and
// the sync row can say "syncing" while it happens rather than only afterwards.
var running sync.Map // pair id -> struct{}

// markRunning records a pair as in flight and returns the function to clear it.
func markRunning(id string) func() {
	running.Store(id, struct{}{})
	return func() { running.Delete(id) }
}

// isRunning reports whether a pair is syncing right now.
func isRunning(id string) bool {
	_, ok := running.Load(id)
	return ok
}

// startSyncRunner keeps the configured pairs up to date for as long as the tray
// is running: on each pair's interval, and as soon as a watched folder changes.
// It is the same loop `ledgerline-cli sync service` runs.
//
// onChange is called whenever a pair starts or finishes, so the menu repaints
// without waiting for the five-minute refresh.
func startSyncRunner(ctx context.Context, log *applog.Logger, onChange func()) {
	runner := syncrunner.New(
		func() (*api.Client, error) {
			c, _, err := clientset.Authenticated()
			return c, err
		},
		syncrunner.Options{
			Log: func(format string, args ...any) { log.Printf(format, args...) },
			OnStart: func(p syncconfig.Pair) {
				markRunning(p.ID)
				onChange()
			},
			OnResult: func(p syncconfig.Pair, _ files.SyncResult, _ error) {
				running.Delete(p.ID)
				onChange()
			},
		})
	go runner.Run(ctx)
}

// syncState reads the configuration and the in-flight set into what the menu
// renders. Kept here rather than in trayui because it is the one place that
// knows both.
func syncState() trayui.SyncState {
	f, err := syncconfig.Load()
	if err != nil || len(f.Pairs) == 0 {
		return trayui.SyncState{Phase: trayui.SyncNone}
	}

	st := trayui.SyncState{Phase: trayui.SyncIdle}
	for _, p := range f.Pairs {
		live := isRunning(p.ID)
		if live {
			st.Running++
		}
		if p.LastRun.After(st.LastRun) {
			st.LastRun = p.LastRun
		}
		result := p.LastResult
		if p.LastFailure != "" {
			result = p.LastFailure
		}
		st.Pairs = append(st.Pairs, trayui.SyncPair{
			Label:   pairLabel(p),
			Running: live,
			LastRun: p.LastRun,
			Result:  result,
			Failed:  p.LastFailure != "",
			Paused:  !p.Enabled,
		})
		// A failure that has not been superseded by a good run outranks "idle";
		// a run in flight outranks everything, because it is what is happening.
		if p.LastFailure != "" && st.Phase == trayui.SyncIdle {
			st.Phase = trayui.SyncFailed
		}
	}
	if st.Running > 0 {
		st.Phase = trayui.SyncRunning
	}
	return st
}

// pairLabel is the short form for a menu row: the local folder's name and where
// it goes, not the full path, which would not fit.
func pairLabel(p syncconfig.Pair) string {
	local := filepath.Base(p.Local)
	remote := p.Remote
	if remote == "" {
		remote = "top level"
	}
	arrow := "<->"
	switch p.Direction {
	case syncconfig.DirectionPush:
		arrow = "->"
	case syncconfig.DirectionPull:
		arrow = "<-"
	}
	return local + " " + arrow + " " + remote
}
