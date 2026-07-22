package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestDirCreatedWith0700 asserts the config/state directory is created owner-only
// (0700), so tokens and any cached key material are never group/world-readable on
// a shared host (§21 / §29). Skipped on Windows where POSIX mode bits differ.
func TestDirCreatedWith0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits not applicable on Windows")
	}
	base := t.TempDir()
	target := filepath.Join(base, "state")
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", target)

	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != target {
		t.Fatalf("Dir() = %q want %q", dir, target)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("config dir perm = %o want 0700 (must not be group/world-accessible)", perm)
	}
}
