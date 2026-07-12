// Package config resolves where ledgerline-cli keeps its per-user state and
// reads/writes the non-secret portion of it. Secrets (the API token) are held
// separately by the session package so they can go to the OS keychain.
package config

import (
	"os"
	"path/filepath"
)

// dirName is the application's directory under the user's config home.
const dirName = "ledgerline-cli"

// Dir returns the directory holding this user's ledgerline-cli state, honouring
// XDG_CONFIG_HOME on Linux and the platform default elsewhere (via
// os.UserConfigDir). The directory is created with 0700 permissions if missing.
func Dir() (string, error) {
	// LEDGERLINE_CLI_CONFIG_DIR is an explicit override, mainly for tests and
	// unusual deployments; it takes precedence over the platform default.
	if override := os.Getenv("LEDGERLINE_CLI_CONFIG_DIR"); override != "" {
		return ensureDir(override)
	}

	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return ensureDir(filepath.Join(base, dirName))
}

// ensureDir creates dir (0700) if needed and returns it.
func ensureDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
