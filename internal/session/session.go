// Package session persists the durable credential obtained from `auth login`.
//
// The design mirrors the Android client: only the first-party Sanctum bearer and
// a little non-secret identity (server URL, user id/name/email) live here.
//
// Storage is layered for security:
//
//   - The bearer token is stored in the operating system keychain (macOS
//     Keychain / Linux Secret Service) via go-keyring.
//   - When no keychain is available (headless Linux, SSH sessions), it falls
//     back to a 0600 file inside the config directory, and Backend() reports
//     that so the user can make an informed choice.
//   - Non-secret identity (server URL, user id/name/email) always lives in a
//     0600 config.json alongside it.
//
// The short-lived pairing code is never written to disk.
package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"

	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// keyringService is the collection name used in the OS keychain.
const keyringService = "ledgerline-cli"

// fileName holds the non-secret session metadata.
const fileName = "config.json"

// Backend identifies where the bearer token is stored.
type Backend string

const (
	// BackendKeyring means the token lives in the OS keychain.
	BackendKeyring Backend = "keyring"
	// BackendFile means the token lives in a 0600 file (keychain unavailable).
	BackendFile Backend = "file"
)

// ErrNotAuthenticated is returned by Load when no session is stored.
var ErrNotAuthenticated = errors.New("not authenticated")

// Session is the persisted login state. The Token field is never written to the
// plaintext config file; it is fetched from / stored to the chosen backend.
type Session struct {
	ServerURL string `json:"server_url"`
	UserID    int64  `json:"user_id"`
	UserName  string `json:"user_name"`
	UserEmail string `json:"user_email"`
	Token     string `json:"-"`

	// Backend records where the token is stored so `auth status` can report it
	// and Clear can undo the right thing. Persisted in config.json.
	Backend Backend `json:"backend"`
}

// diskState is the on-disk shape of config.json. Token is populated only in the
// file-fallback backend.
type diskState struct {
	ServerURL string  `json:"server_url"`
	UserID    int64   `json:"user_id"`
	UserName  string  `json:"user_name"`
	UserEmail string  `json:"user_email"`
	Backend   Backend `json:"backend"`
	Token     string  `json:"token,omitempty"`
}

// keyringUser derives the keychain account name from the server URL so multiple
// servers can be stored side by side without collision.
func keyringUser(serverURL string) string { return serverURL }

// Save persists s, writing the token to the OS keychain when possible and
// otherwise to the 0600 config file. It records the backend actually used on the
// returned session's Backend field (and on disk).
func Save(s Session) (Session, error) {
	dir, err := config.Dir()
	if err != nil {
		return s, err
	}

	state := diskState{
		ServerURL: s.ServerURL,
		UserID:    s.UserID,
		UserName:  s.UserName,
		UserEmail: s.UserEmail,
	}

	// Prefer the OS keychain; fall back to an inline 0600 token on failure.
	if err := keyring.Set(keyringService, keyringUser(s.ServerURL), s.Token); err != nil {
		state.Backend = BackendFile
		state.Token = s.Token
	} else {
		state.Backend = BackendKeyring
	}

	if err := writeState(filepath.Join(dir, fileName), state); err != nil {
		// Roll back a keychain write so we never leave a dangling secret.
		if state.Backend == BackendKeyring {
			_ = keyring.Delete(keyringService, keyringUser(s.ServerURL))
		}
		return s, err
	}

	s.Backend = state.Backend
	return s, nil
}

// Load restores the stored session, resolving the token from its backend.
// It returns ErrNotAuthenticated when nothing is stored.
func Load() (Session, error) {
	dir, err := config.Dir()
	if err != nil {
		return Session{}, err
	}

	state, err := readState(filepath.Join(dir, fileName))
	if err != nil {
		return Session{}, err
	}

	s := Session{
		ServerURL: state.ServerURL,
		UserID:    state.UserID,
		UserName:  state.UserName,
		UserEmail: state.UserEmail,
		Backend:   state.Backend,
	}

	switch state.Backend {
	case BackendFile:
		s.Token = state.Token
	default:
		token, err := keyring.Get(keyringService, keyringUser(state.ServerURL))
		if err != nil {
			return Session{}, ErrNotAuthenticated
		}
		s.Token = token
	}

	if s.Token == "" {
		return Session{}, ErrNotAuthenticated
	}
	return s, nil
}

// Clear removes the stored session from both the keychain and disk. It is
// idempotent: clearing an already-empty session is not an error.
func Clear() error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}

	path := filepath.Join(dir, fileName)
	if state, err := readState(path); err == nil {
		// Best-effort: delete the keychain entry. A missing entry is not an error.
		_ = keyring.Delete(keyringService, keyringUser(state.ServerURL))
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// WipeLocal erases all local state: the credential (keychain + config) plus
// everything else in the config directory (sync state, settings). Used by the
// remote kill switch.
func WipeLocal() error {
	// Clear() first so it can read the state to delete the right keychain entry.
	_ = Clear()
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// writeState atomically writes state as pretty JSON with 0600 permissions.
func writeState(path string, state diskState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	// Write to a temp file in the same directory, then rename, so a crash never
	// leaves a half-written credential file.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readState reads config.json, translating a missing file into
// ErrNotAuthenticated.
func readState(path string) (diskState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return diskState{}, ErrNotAuthenticated
		}
		return diskState{}, err
	}

	var state diskState
	if err := json.Unmarshal(data, &state); err != nil {
		return diskState{}, err
	}
	if state.ServerURL == "" {
		return diskState{}, ErrNotAuthenticated
	}
	return state, nil
}
