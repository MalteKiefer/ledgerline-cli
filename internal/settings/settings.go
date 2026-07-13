// Package settings reads and writes the user-editable CLI settings file
// (ignore patterns, sync folder mappings, hidden-file default). It lives beside
// the credential config so `files sync` can be configured once and re-run.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// fileName is the settings file within the config directory.
const fileName = "settings.json"

// Mapping is one sync pairing: a remote folder path (empty = files root) to a
// local directory.
type Mapping struct {
	Remote string `json:"remote"`
	Local  string `json:"local"`
}

// Settings is the persisted CLI configuration users may edit by hand.
type Settings struct {
	// Hidden includes dotfiles by default when true.
	Hidden bool `json:"hidden"`
	// Ignore is a list of gitignore-style patterns excluded from sync/upload.
	Ignore []string `json:"ignore"`
	// Sync is the list of folder mappings used by `files sync` with no args.
	Sync []Mapping `json:"sync"`
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
