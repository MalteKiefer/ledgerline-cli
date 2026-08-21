//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/applog"
	"github.com/MalteKiefer/ledgerline-cli/internal/clientset"
	"github.com/MalteKiefer/ledgerline-cli/internal/deskprefs"
	"github.com/MalteKiefer/ledgerline-cli/internal/win32ui"
)

// galleryView is what the Photos tab renders.
type galleryView struct {
	CameraFolder string        `json:"cameraFolder"`
	Recent       []uploadedRow `json:"recent"`
	Error        string        `json:"error"`
}

// uploadedRow is one entry in the "recently uploaded" list.
type uploadedRow struct {
	Name      string `json:"name"`
	When      string `json:"when"`
	Video     bool   `json:"video"`
	Duplicate bool   `json:"duplicate"`
}

func (s *settings) gallery() (galleryView, error) {
	p, err := deskprefs.Load()
	v := galleryView{CameraFolder: p.CameraFolder, Recent: recentUploads()}
	if err != nil {
		v.Error = err.Error()
	}
	return v, nil
}

// pickPhotos opens the shell's file chooser.
//
// It returns paths rather than bytes: the page has no filesystem access, and
// handing it megabytes of image through a JSON binding to hand them straight
// back would be pointless.
func (s *settings) pickPhotos() ([]string, error) {
	return win32ui.PickFiles(0, "Choose photos and videos", win32ui.MediaFilter), nil
}

func (s *settings) pickPhotoFolder() (string, error) {
	return win32ui.PickFolder(0, "Choose a folder to upload photos from"), nil
}

// sendPhotos uploads the given files, reporting one sentence for the page.
func (s *settings) sendPhotos(paths []string) (string, error) {
	client, _, err := clientset.Authenticated()
	if err != nil {
		return "Could not upload: sign in from the tray first.", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel()

	var sent, dupes, skipped int
	var failures []string
	for _, path := range paths {
		if !isGalleryMedia(path) {
			skipped++
			continue
		}
		duplicate, err := uploadPhoto(ctx, client, path)
		switch {
		case err != nil:
			failures = append(failures, filepath.Base(path)+": "+firstLine(err.Error()))
		case duplicate:
			dupes++
			noteUpload(path, true)
		default:
			sent++
			noteUpload(path, false)
			s.maybeRemoveOriginal(path)
		}
	}
	return uploadSummary(sent, dupes, skipped, failures), nil
}

// sendPhotoFolder walks a directory and uploads every photo and video in it.
func (s *settings) sendPhotoFolder(dir string) (string, error) {
	prefs, _ := deskprefs.Load()
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if prefs.Excluded(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !prefs.Excluded(d.Name()) && isGalleryMedia(p) {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return "Could not read that folder: " + firstLine(err.Error()), nil
	}
	if len(paths) == 0 {
		return "No photos or videos in that folder.", nil
	}
	return s.sendPhotos(paths)
}

// maybeRemoveOriginal deletes the local copy, if the user asked for that.
//
// It is checked per upload rather than once, because the preference can change
// while a long folder upload runs, and the safer reading of a mid-run change is
// the one the user just made.
func (s *settings) maybeRemoveOriginal(path string) {
	p, err := deskprefs.Load()
	if err != nil || !p.CameraDelete {
		return
	}
	removeUploaded(path, s.log)
}

// uploadPhoto sends one file and reports whether the server already had it.
func uploadPhoto(ctx context.Context, client *api.Client, path string) (duplicate bool, err error) {
	_, duplicate, err = client.UploadPhoto(ctx, filepath.Base(path), opener(path))
	return duplicate, err
}

// uploadSummary turns four counters into one sentence a person can act on.
//
// Failures are named rather than counted: "3 failed" tells you nothing, and the
// first two names usually tell you the cause.
func uploadSummary(sent, dupes, skipped int, failures []string) string {
	var parts []string
	if sent > 0 {
		parts = append(parts, fmt.Sprintf("Added %s", plural(sent, "item", "items")))
	}
	if dupes > 0 {
		parts = append(parts, fmt.Sprintf("%d already in the gallery", dupes))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d not a photo or video", skipped))
	}
	if len(failures) > 0 {
		shown := failures
		if len(shown) > 2 {
			shown = shown[:2]
		}
		part := fmt.Sprintf("%d failed (%s", len(failures), strings.Join(shown, "; "))
		if len(failures) > len(shown) {
			part += fmt.Sprintf("; and %d more", len(failures)-len(shown))
		}
		parts = append(parts, part+")")
	}
	if len(parts) == 0 {
		return "Nothing to upload."
	}
	return strings.Join(parts, ", ") + "."
}

// The recent-upload list.
//
// It is kept in memory rather than on disk. A record of "what this session
// uploaded" is what the tab is for; persisting it would mean a second little
// database to keep in step with the server, and the server already has the
// gallery.
var recent struct {
	rows []uploadedRow
}

const recentLimit = 40

func noteUpload(path string, duplicate bool) {
	row := uploadedRow{
		Name:      filepath.Base(path),
		When:      time.Now().Format("15:04"),
		Video:     isVideo(path),
		Duplicate: duplicate,
	}
	recent.rows = append([]uploadedRow{row}, recent.rows...)
	if len(recent.rows) > recentLimit {
		recent.rows = recent.rows[:recentLimit]
	}
}

func recentUploads() []uploadedRow {
	out := make([]uploadedRow, len(recent.rows))
	copy(out, recent.rows)
	return out
}

func isVideo(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mov", ".m4v", ".avi", ".mkv", ".webm", ".3gp", ".mts", ".m2ts":
		return true
	}
	return false
}

// mediaIn lists the photos and videos directly inside a folder, newest first.
// The camera watcher uses it to decide what is new.
func mediaIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	prefs, _ := deskprefs.Load()

	type item struct {
		path string
		mod  time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || prefs.Excluded(e.Name()) || !isGalleryMedia(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })

	paths := make([]string, 0, len(items))
	for _, i := range items {
		paths = append(paths, i.path)
	}
	return paths, nil
}

// fileSize reports a file's size, and false when it cannot be read — a file
// being written to over a card reader can vanish between the listing and the
// stat.
func fileSize(path string) (int64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return info.Size(), true
}

// removeUploaded deletes a local original after it reached the server. Failure
// is logged, not surfaced: the upload succeeded, which is what the user asked
// for, and a file left behind is a nuisance rather than a loss.
func removeUploaded(path string, log *applog.Logger) {
	if err := os.Remove(path); err != nil && log != nil {
		log.Printf("gallery: uploaded %s but could not remove the local copy: %v",
			filepath.Base(path), err)
	}
}
