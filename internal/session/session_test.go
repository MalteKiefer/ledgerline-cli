package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// isolate points the config directory at a temp dir and uses the in-memory
// keyring mock so tests never touch the real OS keychain.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", dir)
	keyring.MockInit()
	return dir
}

func sample() Session {
	return Session{
		ServerURL: "https://ledger.example.com",
		UserID:    42,
		UserName:  "Ada",
		UserEmail: "ada@example.com",
		Token:     "secret-bearer-token",
	}
}

func TestSaveLoadRoundTripUsesKeyring(t *testing.T) {
	dir := isolate(t)

	saved, err := Save(sample())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Backend != BackendKeyring {
		t.Fatalf("backend = %q, want keyring", saved.Backend)
	}

	// The token must NOT be written to the plaintext config file.
	raw, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-bearer-token") {
		t.Fatal("token leaked into the plaintext config file")
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Token != "secret-bearer-token" || got.UserEmail != "ada@example.com" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestConfigFileIsOwnerOnly(t *testing.T) {
	dir := isolate(t)
	if _, err := Save(sample()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config perms = %o, want 600", perm)
	}
}

func TestLoadWithoutSessionIsNotAuthenticated(t *testing.T) {
	isolate(t)
	if _, err := Load(); err != ErrNotAuthenticated {
		t.Fatalf("Load on empty = %v, want ErrNotAuthenticated", err)
	}
}

func TestClearRemovesEverything(t *testing.T) {
	dir := isolate(t)
	if _, err := Save(sample()); err != nil {
		t.Fatal(err)
	}
	if err := Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, fileName)); !os.IsNotExist(err) {
		t.Fatal("config file survived Clear")
	}
	if _, err := Load(); err != ErrNotAuthenticated {
		t.Fatalf("Load after Clear = %v", err)
	}
	// Clearing again is a no-op, not an error.
	if err := Clear(); err != nil {
		t.Fatalf("second Clear: %v", err)
	}
}

