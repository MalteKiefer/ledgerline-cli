// Package certpin implements trust-on-first-use (TOFU) certificate pinning for
// the API server. The first time the CLI connects to a host it records the
// server certificate's public-key hash; on later connections a changed key is
// refused. This is defence-in-depth on top of normal CA validation: it detects a
// mis-issued or swapped certificate even from an otherwise-trusted CA.
package certpin

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// pinsFile is the on-disk store name inside the config directory.
const pinsFile = "pins.json"

// Store maps a host to the hex SHA-256 of its certificate's SubjectPublicKeyInfo.
// It is safe for concurrent use: parallel uploads share one client and its TLS
// handshakes can verify concurrently.
type Store struct {
	dir string

	mu     sync.Mutex
	pins   map[string]string
	loaded bool
}

// NewStore returns a pin store backed by <dir>/pins.json.
func NewStore(dir string) *Store { return &Store{dir: dir, pins: map[string]string{}} }

// path is the full path to the pins file.
func (s *Store) path() string { return filepath.Join(s.dir, pinsFile) }

// Check records the SPKI hash for host on first use and, on later calls, refuses
// a hash that differs from the one first seen. It returns nil when the pin
// matches or was just established.
func (s *Store) Check(host string, spkiSHA256 []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoaded(); err != nil {
		return err
	}

	got := hex.EncodeToString(spkiSHA256)
	if pinned, ok := s.pins[host]; ok {
		if subtle.ConstantTimeCompare([]byte(pinned), []byte(got)) == 1 {
			return nil
		}
		return fmt.Errorf("certificate pin mismatch for %s: the server key changed since first use. "+
			"If this change is expected, remove %s to trust the new certificate", host, s.path())
	}

	s.pins[host] = got
	return s.save()
}

// ensureLoaded reads the pins file once (a missing file is an empty store).
func (s *Store) ensureLoaded() error {
	if s.loaded {
		return nil
	}
	data, err := os.ReadFile(s.path())
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &s.pins); err != nil {
			return fmt.Errorf("read %s: %w", s.path(), err)
		}
	}
	s.loaded = true
	return nil
}

// save writes the pins file atomically with owner-only permissions.
func (s *Store) save() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.pins, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}
