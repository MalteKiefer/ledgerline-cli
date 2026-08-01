// Package settings reads and writes the user-editable CLI settings file
// (ignore patterns, sync folder mappings, hidden-file default). It lives beside
// the credential config so `files sync` can be configured once and re-run.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// fileName is the settings file within the config directory.
const fileName = "settings.json"

// Mapping is one sync pairing plus optional per-mapping policy overrides. Zero
// values fall back to the service defaults, then the command defaults.
type Mapping struct {
	Remote   string   `json:"remote"`
	Local    string   `json:"local"`
	Conflict string   `json:"conflict,omitempty"`
	Delete   string   `json:"delete,omitempty"`
	Interval string   `json:"interval,omitempty"`
	Hidden   *bool    `json:"hidden,omitempty"`
	Ignore   []string `json:"ignore,omitempty"`
}

// Service holds the continuous-service defaults.
type Service struct {
	Interval string `json:"interval,omitempty"` // server-poll cadence, default 60s
	Debounce string `json:"debounce,omitempty"` // coalesce local FS events, default 2s
}

// Settings is the persisted CLI configuration users may edit by hand.
type Settings struct {
	// Hidden includes dotfiles by default when true.
	Hidden bool `json:"hidden"`
	// Ignore is a list of gitignore-style patterns excluded from sync/upload.
	Ignore []string `json:"ignore"`
	// Sync is the list of folder mappings used by `files sync` with no args.
	Sync []Mapping `json:"sync"`
	// Service holds the continuous-service defaults.
	Service Service `json:"service,omitempty"`
}

// Path returns the settings file path.
func Path() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// Load reads the settings file, returning defaults when it does not exist.
func Load() (Settings, error) {
	path, err := Path()
	if err != nil {
		return Settings{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Settings{}, nil
		}
		return Settings{}, err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// Save writes the settings file (0600, pretty-printed for hand editing).
func Save(s Settings) error {
	path, err := Path()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// Upsert adds or replaces a mapping, keyed by its cleaned absolute local path.
func (s *Settings) Upsert(m Mapping) {
	key := filepath.Clean(m.Local)
	for i := range s.Sync {
		if filepath.Clean(s.Sync[i].Local) == key {
			s.Sync[i] = m
			return
		}
	}
	s.Sync = append(s.Sync, m)
}

// unmarshal is a thin seam so tests can decode without touching disk.
func (s *Settings) unmarshal(b []byte) error { return json.Unmarshal(b, s) }

// ParseDurationOr parses a duration string, returning def on empty/invalid input.
func ParseDurationOr(v string, def time.Duration) time.Duration {
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}
