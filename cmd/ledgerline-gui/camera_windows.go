//go:build windows

package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/applog"
	"github.com/MalteKiefer/ledgerline-cli/internal/clientset"
	"github.com/MalteKiefer/ledgerline-cli/internal/deskprefs"
)

// The camera watcher: photos dropped into one folder go to the gallery.
//
// This is the desktop half of what a phone does automatically, and it is what
// Google Drive's photo backup and Proton Drive's album upload are for. The
// folder is polled rather than watched with fsnotify, deliberately: the folder
// people point this at is usually a card reader or a phone mount, where a file
// appears in pieces and a create event arrives long before the last byte does.
// A poll that only takes files whose size stopped changing cannot upload half a
// video.
const (
	// cameraPoll is how often the folder is examined, and with it how long a
	// file has to keep the same size before it counts as finished being written:
	// a file is uploaded on the first pass that sees it unchanged since the last.
	// A photo is not urgent, so thirty seconds of patience is cheap.
	cameraPoll = 30 * time.Second
)

// startCameraWatcher uploads new media from the configured folder until ctx is
// cancelled. It does nothing at all while no folder is configured, which is the
// default.
func startCameraWatcher(ctx context.Context, log *applog.Logger, onChange func()) {
	w := &cameraWatcher{log: log, onChange: onChange, seen: map[string]int64{}}
	go w.loop(ctx)
}

type cameraWatcher struct {
	log      *applog.Logger
	onChange func()

	mu sync.Mutex
	// seen maps a path to the size it had when last examined. A file is uploaded
	// once its size has stopped changing; after that it is remembered as done.
	seen map[string]int64
	// done are paths already uploaded, so a file that is not deleted afterwards
	// is not offered again every thirty seconds.
	done map[string]bool
}

func (w *cameraWatcher) loop(ctx context.Context) {
	ticker := time.NewTicker(cameraPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.pass(ctx)
		}
	}
}

// pass examines the folder once.
func (w *cameraWatcher) pass(ctx context.Context) {
	prefs, err := deskprefs.Load()
	if err != nil || strings.TrimSpace(prefs.CameraFolder) == "" {
		return
	}
	if reason := syncHold(); reason != "" {
		return // the same policy the folder sync obeys; a photo can wait
	}
	client, _, err := clientset.Authenticated()
	if err != nil {
		return
	}

	paths, err := mediaIn(prefs.CameraFolder)
	if err != nil {
		return
	}

	ready := w.settled(paths)
	if len(ready) == 0 {
		return
	}

	uploadCtx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()

	var sent int
	for _, path := range ready {
		duplicate, err := uploadPhoto(uploadCtx, client, path)
		if err != nil {
			// Left out of done, so the next pass tries again: a failure here is
			// usually the network, and giving up permanently on one photo
			// because of a dropped connection is not a policy anyone wants.
			w.log.Printf("camera: %s failed: %v", filepath.Base(path), err)
			continue
		}
		w.markDone(path)
		noteUpload(path, duplicate)
		if !duplicate {
			sent++
		}
		if prefs.CameraDelete {
			removeUploaded(path, w.log)
		}
	}
	if sent > 0 {
		w.log.Printf("camera: uploaded %s from %s", plural(sent, "item", "items"), prefs.CameraFolder)
		if w.onChange != nil {
			w.onChange()
		}
	}
}

// settled returns the paths whose size has not changed since the previous pass
// and which have not been uploaded yet.
func (w *cameraWatcher) settled(paths []string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done == nil {
		w.done = map[string]bool{}
	}

	var ready []string
	current := make(map[string]int64, len(paths))
	for _, path := range paths {
		if w.done[path] {
			continue
		}
		size, ok := fileSize(path)
		if !ok {
			continue
		}
		current[path] = size
		previous, seenBefore := w.seen[path]
		if seenBefore && previous == size {
			ready = append(ready, path)
		}
	}
	// Replace rather than merge, so a deleted file stops being remembered and a
	// long-running tray does not accumulate a map of everything it ever saw.
	w.seen = current
	return ready
}

func (w *cameraWatcher) markDone(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done == nil {
		w.done = map[string]bool{}
	}
	w.done[path] = true
	delete(w.seen, path)
}
