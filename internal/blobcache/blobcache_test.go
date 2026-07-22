package blobcache

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPutGetPrune(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shards")
	c, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.Put("aaaa", []byte("cipher-a"))
	c.Put("bbbb", []byte("cipher-b"))

	if b, ok := c.Get("aaaa"); !ok || string(b) != "cipher-a" {
		t.Fatalf("get aaaa: %q ok=%v", b, ok)
	}
	if _, ok := c.Get("cccc"); ok {
		t.Fatal("miss expected for cccc")
	}

	// Prune to only aaaa → bbbb removed.
	c.Prune([]string{"aaaa"})
	if _, ok := c.Get("bbbb"); ok {
		t.Fatal("bbbb should have been pruned")
	}
	if _, ok := c.Get("aaaa"); !ok {
		t.Fatal("aaaa should have survived prune")
	}
}

func TestValidRefRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", "../etc", "a/b", "a\\b", "a.b", "x/../y", "z\x00"} {
		if validRef(bad) {
			t.Fatalf("validRef accepted hostile ref %q", bad)
		}
	}
	for _, ok := range []string{"018f4b2e-1234-7abc-9def-000000000001", "deadBEEF"} {
		if !validRef(ok) {
			t.Fatalf("validRef rejected legit ref %q", ok)
		}
	}
	// A hostile ref must not read/write outside the dir.
	c, _ := New(t.TempDir())
	c.Put("../escape", []byte("x"))
	if _, ok := c.Get("../escape"); ok {
		t.Fatal("hostile ref must never resolve")
	}
}

func TestFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX bits n/a")
	}
	dir := filepath.Join(t.TempDir(), "shards")
	c, _ := New(dir)
	c.Put("aaaa", []byte("x"))

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("cache dir perm = %o want 0700", di.Mode().Perm())
	}
	fi, err := os.Stat(filepath.Join(dir, "aaaa"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("cache file perm = %o want 0600", fi.Mode().Perm())
	}
}
