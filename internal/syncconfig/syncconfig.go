// Package syncconfig stores the folder pairs this machine keeps in sync.
//
// One-off `files sync <dir>` needs no state. Several folders, each with its own
// remote root and schedule, does: the tray has to know what to run without
// being told again, and the terminal and the tray must agree on the list. It
// lives beside the credential in the configuration directory as sync.json.
//
// Nothing in here is a secret — local paths, remote folder names, intervals —
// but the file is written 0600 all the same, because the set of directories a
// person syncs is not other users' business either.
package syncconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

const fileName = "sync.json"

// Directions and conflict policies, mirroring the values files.Sync accepts.
// They are duplicated here rather than imported so that a configuration file
// can be validated without pulling the sync engine in.
const (
	DirectionBoth = "both"
	DirectionPush = "push"
	DirectionPull = "pull"

	ConflictNewest   = "newest"
	ConflictKeepBoth = "keep-both"
	ConflictSkip     = "skip"
)

// DefaultInterval is how often a pair re-syncs when the tray is running.
const DefaultInterval = 15 * time.Minute

// Pair is one local directory kept in step with one remote folder.
type Pair struct {
	ID     string `json:"id"`
	Local  string `json:"local"`
	Remote string `json:"remote"` // slash path; empty means the remote root

	Direction string `json:"direction"`
	Conflict  string `json:"conflict"`

	// IntervalMinutes is the automatic re-sync period. Zero means manual only.
	IntervalMinutes int  `json:"interval_minutes"`
	Enabled         bool `json:"enabled"`

	// Outcome of the last run, so the tray can show something other than a
	// blank row and a user can see that a pair has been failing quietly.
	LastRun     time.Time `json:"last_run,omitempty"`
	LastResult  string    `json:"last_result,omitempty"`
	LastFailure string    `json:"last_failure,omitempty"`
}

// Interval returns the effective period, or zero for manual-only.
func (p Pair) Interval() time.Duration {
	if p.IntervalMinutes <= 0 {
		return 0
	}
	return time.Duration(p.IntervalMinutes) * time.Minute
}

// Due reports whether an enabled, scheduled pair is ready to run again.
func (p Pair) Due(now time.Time) bool {
	if !p.Enabled {
		return false
	}
	every := p.Interval()
	if every == 0 {
		return false
	}
	return p.LastRun.IsZero() || !now.Before(p.LastRun.Add(every))
}

// Describe renders the pair for a list, in one line.
func (p Pair) Describe() string {
	remote := p.Remote
	if remote == "" {
		remote = "(root)"
	}
	arrow := "<->"
	switch p.Direction {
	case DirectionPush:
		arrow = "->"
	case DirectionPull:
		arrow = "<-"
	}
	schedule := "manual"
	if every := p.Interval(); every > 0 {
		schedule = every.String()
	}
	state := "on"
	if !p.Enabled {
		state = "off"
	}
	return fmt.Sprintf("%s %s %s  [%s, %s]", p.Local, arrow, remote, schedule, state)
}

// File is the whole configuration.
type File struct {
	Pairs []Pair `json:"pairs"`
}

// ErrNotFound is returned when an id matches no pair.
var ErrNotFound = errors.New("no sync pair with that id")

var mu sync.Mutex // serialises read-modify-write across the CLI and the tray

// Load reads the configuration, returning an empty one when there is none yet.
func Load() (File, error) {
	mu.Lock()
	defer mu.Unlock()
	return load()
}

func load() (File, error) {
	path, err := configPath()
	if err != nil {
		return File{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return f, nil
}

// Save writes the configuration back, atomically: a half-written sync.json
// would leave the tray with no list at all.
func Save(f File) error {
	mu.Lock()
	defer mu.Unlock()
	return save(f)
}

func save(f File) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Add validates and appends a pair, returning it with its assigned id.
func Add(p Pair) (Pair, error) {
	normalised, err := Normalise(p)
	if err != nil {
		return Pair{}, err
	}

	mu.Lock()
	defer mu.Unlock()
	f, err := load()
	if err != nil {
		return Pair{}, err
	}
	for _, existing := range f.Pairs {
		if sameLocation(existing, normalised) {
			return Pair{}, fmt.Errorf("%s is already synced with %q", normalised.Local, remoteLabel(normalised.Remote))
		}
	}
	normalised.ID = nextID(f.Pairs)
	f.Pairs = append(f.Pairs, normalised)
	if err := save(f); err != nil {
		return Pair{}, err
	}
	return normalised, nil
}

// Remove deletes a pair by id.
func Remove(id string) error {
	mu.Lock()
	defer mu.Unlock()
	f, err := load()
	if err != nil {
		return err
	}
	for i, p := range f.Pairs {
		if p.ID == id {
			f.Pairs = append(f.Pairs[:i], f.Pairs[i+1:]...)
			return save(f)
		}
	}
	return ErrNotFound
}

// Update applies a change to one pair, in place.
func Update(id string, apply func(*Pair)) (Pair, error) {
	mu.Lock()
	defer mu.Unlock()
	f, err := load()
	if err != nil {
		return Pair{}, err
	}
	for i := range f.Pairs {
		if f.Pairs[i].ID != id {
			continue
		}
		apply(&f.Pairs[i])
		if err := save(f); err != nil {
			return Pair{}, err
		}
		return f.Pairs[i], nil
	}
	return Pair{}, ErrNotFound
}

// RecordRun stores the outcome of a run. A failure keeps the message so the
// tray can show why a pair stopped working instead of just going quiet.
func RecordRun(id string, when time.Time, summary string, failure error) error {
	_, err := Update(id, func(p *Pair) {
		p.LastRun = when
		p.LastResult = summary
		if failure != nil {
			p.LastFailure = failure.Error()
			return
		}
		p.LastFailure = ""
	})
	return err
}

// Get returns one pair by id.
func Get(id string) (Pair, error) {
	f, err := Load()
	if err != nil {
		return Pair{}, err
	}
	for _, p := range f.Pairs {
		if p.ID == id {
			return p, nil
		}
	}
	return Pair{}, ErrNotFound
}

// Normalise validates a pair and fills in the defaults.
func Normalise(p Pair) (Pair, error) {
	p.Local = strings.TrimSpace(p.Local)
	if p.Local == "" {
		return Pair{}, errors.New("no local directory given")
	}
	abs, err := filepath.Abs(p.Local)
	if err != nil {
		return Pair{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Pair{}, fmt.Errorf("%s: %w", abs, err)
	}
	if !info.IsDir() {
		return Pair{}, fmt.Errorf("%s is not a directory", abs)
	}
	p.Local = abs

	p.Remote = strings.Trim(strings.ReplaceAll(strings.TrimSpace(p.Remote), "\\", "/"), "/")
	for _, part := range strings.Split(p.Remote, "/") {
		if part == "." || part == ".." {
			return Pair{}, fmt.Errorf("invalid remote folder %q", p.Remote)
		}
	}

	switch p.Direction {
	case "":
		p.Direction = DirectionBoth
	case DirectionBoth, DirectionPush, DirectionPull:
	default:
		return Pair{}, fmt.Errorf("invalid direction %q (both|push|pull)", p.Direction)
	}

	switch p.Conflict {
	case "":
		p.Conflict = ConflictNewest
	case ConflictNewest, ConflictKeepBoth, ConflictSkip:
	default:
		return Pair{}, fmt.Errorf("invalid conflict policy %q (newest|keep-both|skip)", p.Conflict)
	}

	if p.IntervalMinutes < 0 {
		return Pair{}, errors.New("interval cannot be negative")
	}
	return p, nil
}

// sameLocation reports whether two pairs describe the same job. Adding the same
// directory twice is a mistake worth catching: the two runs would race on the
// same files and each would see the other's writes as remote changes.
func sameLocation(a, b Pair) bool {
	return strings.EqualFold(a.Local, b.Local) && a.Remote == b.Remote
}

func remoteLabel(remote string) string {
	if remote == "" {
		return "the remote root"
	}
	return remote
}

// nextID returns the lowest unused small integer, as a string. Small ids are
// there to be typed: `sync rm 2` beats pasting a UUID.
func nextID(pairs []Pair) string {
	used := map[string]bool{}
	for _, p := range pairs {
		used[p.ID] = true
	}
	for i := 1; ; i++ {
		id := fmt.Sprintf("%d", i)
		if !used[id] {
			return id
		}
	}
}

func configPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}
