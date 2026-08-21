// Package deskprefs holds the desktop client's own preferences: the choices
// that belong to this computer rather than to the account.
//
// The split matters. Which folders sync lives in internal/syncconfig, because
// both front ends act on it; how many versions the server keeps lives on the
// server, because it applies wherever you sign in. What is here is neither —
// whether this machine launches the tray at login, whether it pauses on
// battery, which language this desktop speaks. Putting those on the server
// would mean one laptop's power policy following the user to their desktop.
package deskprefs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// FileName is the preferences file inside the configuration directory.
const FileName = "desktop.json"

// Theme is how the windows pick their palette.
type Theme string

const (
	ThemeSystem Theme = "system"
	ThemeLight  Theme = "light"
	ThemeDark   Theme = "dark"
)

// Notify says which events raise a desktop notification.
type Notify string

const (
	// NotifyProblems is the default: a sync that fails is worth interrupting
	// someone for, a sync that worked is not.
	NotifyProblems Notify = "problems"
	NotifyAll      Notify = "all"
	NotifyNone     Notify = "none"
)

// Prefs is the whole file. Zero values are deliberately not the defaults —
// Load fills a fresh file from Defaults so a new install behaves sensibly
// rather than with everything switched off.
type Prefs struct {
	// LaunchAtLogin registers the tray in this user's Run key. Per-user, so it
	// needs no elevation and cannot affect anybody else on the machine.
	LaunchAtLogin bool `json:"launch_at_login"`

	// Language is the desktop UI language ("de", "en", "ru"), or "" to follow
	// the operating system.
	Language string `json:"language"`
	Theme    Theme  `json:"theme"`

	Notify Notify `json:"notify"`

	// PauseOnBattery and PauseOnMetered stop scheduled syncing when the machine
	// is on battery or on a connection the user pays by the byte. A manual
	// "Sync now" always runs: pausing an explicit request would be overriding
	// the user, not helping them.
	PauseOnBattery bool `json:"pause_on_battery"`
	PauseOnMetered bool `json:"pause_on_metered"`

	// Paused suspends all scheduled syncing until it is turned back on. This is
	// the "Pause syncing" every desktop client has, and it survives a restart:
	// a pause that quietly expires is worse than none.
	Paused bool `json:"paused"`

	// ExplorerMenu keeps the shell context menu registered.
	ExplorerMenu bool `json:"explorer_menu"`

	// Exclude are names skipped everywhere a sync walks a tree: editor
	// scratch files, OS metadata, partial downloads. Matched against the base
	// name with filepath.Match, case-insensitively.
	Exclude []string `json:"exclude"`

	// UploadKBps and DownloadKBps cap transfer speed. Zero means unlimited.
	UploadKBps   int `json:"upload_kbps"`
	DownloadKBps int `json:"download_kbps"`

	// CameraFolder is watched for new photos and videos, which are uploaded to
	// the gallery. Empty disables it.
	CameraFolder string `json:"camera_folder"`
	// CameraDelete removes the local copy once the server has it. Off by
	// default: a sync client that deletes the original by surprise is a sync
	// client people stop trusting.
	CameraDelete bool `json:"camera_delete"`
}

// Defaults is a fresh install: syncing on, notifications only for problems,
// the shell menu registered, and the exclusion list every sync client needs.
func Defaults() Prefs {
	return Prefs{
		LaunchAtLogin:  true,
		Language:       "",
		Theme:          ThemeSystem,
		Notify:         NotifyProblems,
		PauseOnBattery: false,
		PauseOnMetered: true,
		ExplorerMenu:   true,
		Exclude:        DefaultExclude(),
	}
}

// DefaultExclude is the list of names no sync should carry: editor lock and
// scratch files, OS folder metadata, and partial downloads. Every one of these
// is either machine-local by definition or actively harmful to copy — an Office
// owner file synced to another machine makes the document look locked there.
func DefaultExclude() []string {
	return []string{
		"~$*",          // Office owner files
		"*.tmp",        // generic scratch
		"*.crdownload", // Chrome partial download
		"*.part",       // Firefox partial download
		"desktop.ini",  // Windows folder metadata
		"Thumbs.db",    // Windows thumbnail cache
		".DS_Store",    // macOS folder metadata
		"$RECYCLE.BIN", // Windows recycle bin
		"System Volume Information",
	}
}

// Path is where the file lives.
func Path() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

var mu sync.Mutex

// Load reads the preferences, returning the defaults when the file does not
// exist yet. A file that exists but cannot be parsed is an error rather than a
// silent reset: overwriting somebody's settings because of one bad byte is
// worse than refusing to start.
func Load() (Prefs, error) {
	path, err := Path()
	if err != nil {
		return Defaults(), err
	}
	data, err := os.ReadFile(path) //nolint:gosec // a path this client owns under its config dir
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Defaults(), err
	}

	// Start from the defaults so a file written by an older version — one
	// without today's fields — does not read as "everything off".
	p := Defaults()
	if err := json.Unmarshal(data, &p); err != nil {
		return Defaults(), fmt.Errorf("read %s: %w", path, err)
	}
	return p.normalised(), nil
}

// Save writes the preferences atomically, so a crash mid-write cannot leave a
// truncated file that the next Load refuses.
func Save(p Prefs) error {
	mu.Lock()
	defer mu.Unlock()

	path, err := Path()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(p.normalised(), "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Update applies a change under the file lock, so two windows cannot lose each
// other's edit.
func Update(fn func(*Prefs)) (Prefs, error) {
	p, err := Load()
	if err != nil {
		return p, err
	}
	fn(&p)
	if err := Save(p); err != nil {
		return p, err
	}
	return p, nil
}

// normalised repairs values that are out of range rather than trusting a
// hand-edited file.
func (p Prefs) normalised() Prefs {
	switch p.Theme {
	case ThemeLight, ThemeDark, ThemeSystem:
	default:
		p.Theme = ThemeSystem
	}
	switch p.Notify {
	case NotifyAll, NotifyProblems, NotifyNone:
	default:
		p.Notify = NotifyProblems
	}
	switch strings.ToLower(strings.TrimSpace(p.Language)) {
	case "de", "en", "ru":
		p.Language = strings.ToLower(strings.TrimSpace(p.Language))
	default:
		p.Language = ""
	}
	if p.UploadKBps < 0 {
		p.UploadKBps = 0
	}
	if p.DownloadKBps < 0 {
		p.DownloadKBps = 0
	}

	// Drop blanks and duplicates: an empty pattern matches nothing and a
	// repeated one is noise in the list the user reads.
	seen := make(map[string]bool, len(p.Exclude))
	clean := make([]string, 0, len(p.Exclude))
	for _, e := range p.Exclude {
		e = strings.TrimSpace(e)
		if e == "" || seen[strings.ToLower(e)] {
			continue
		}
		seen[strings.ToLower(e)] = true
		clean = append(clean, e)
	}
	p.Exclude = clean
	return p
}

// Excluded reports whether a base name matches any exclusion pattern.
//
// The match is on the base name, not the path: a pattern like "*.tmp" is about
// what a file is, and a user who writes it does not mean "only at the root".
func (p Prefs) Excluded(name string) bool {
	lower := strings.ToLower(filepath.Base(name))
	for _, pattern := range p.Exclude {
		if ok, err := filepath.Match(strings.ToLower(pattern), lower); err == nil && ok {
			return true
		}
	}
	return false
}
