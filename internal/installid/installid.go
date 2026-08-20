// Package installid provides a stable identifier for this installation.
//
// The server treats a login carrying an install id as a real device: it appears
// in "Connected devices" with revoke and wipe, counts against the device cap,
// and — the reason this exists — a second login from the same installation
// REPLACES its slot instead of stacking another entry. Without it, every
// re-login left another dead "device" behind until the cap evicted a live one.
//
// The value is not a secret and identifies nothing about the machine: it is 16
// random bytes generated once and kept next to the configuration.
package installid

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

const fileName = "install_id"

var (
	once   sync.Once
	cached string

	valid = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Get returns the installation id, creating and persisting one on first use.
// It never fails the caller: if the id cannot be stored (read-only home, for
// example) a fresh in-memory one is returned, which costs a duplicate device
// entry but does not block signing in.
func Get() string {
	once.Do(func() { cached = load() })
	return cached
}

func load() string {
	dir, err := config.Dir()
	if err != nil {
		return generate()
	}
	path := filepath.Join(dir, fileName)

	if raw, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(raw)); valid.MatchString(id) {
			return id
		}
		// Anything else is corrupt rather than meaningful; replace it.
	}

	id := generate()
	if err := os.MkdirAll(dir, 0o700); err == nil {
		// 0600: not a secret, but nothing else has any business writing it.
		_ = os.WriteFile(path, []byte(id+"\n"), 0o600)
	}
	return id
}

func generate() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is fatal for the rest of the program anyway; an
		// empty id simply means the server registers a plain token.
		return ""
	}
	return hex.EncodeToString(buf)
}
