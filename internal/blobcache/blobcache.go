// Package blobcache is a content-addressed on-disk cache of ENCRYPTED shard
// blobs, keyed by their immutable server ref. It stores ciphertext only — the
// same bytes the server already holds — so it keeps NO plaintext at rest (§7):
// decryption still happens in memory on every load. It saves the network
// round-trip of re-fetching unchanged shards on a repeated or resumed run
// against a large library.
//
// A ref is content-addressed and immutable, so a cache entry can never be stale:
// when a shard's content changes it gets a new ref (a cache miss) and the old
// entry is pruned. All files are 0600 inside a 0700 directory.
package blobcache

import (
	"os"
	"path/filepath"
	"strings"
)

// Cache is an on-disk ciphertext blob cache rooted at a directory. A nil *Cache
// is valid and behaves as a disabled cache (every method is a safe no-op / miss).
type Cache struct {
	dir string
}

// New creates (0700) the cache directory and returns a Cache rooted there.
func New(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Cache{dir: dir}, nil
}

// validRef guards against path traversal: server refs are UUIDs (hex + dashes).
func validRef(ref string) bool {
	if ref == "" || len(ref) > 128 {
		return false
	}
	for _, r := range ref {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F', r == '-':
		default:
			return false
		}
	}
	return true
}

func (c *Cache) path(ref string) string { return filepath.Join(c.dir, ref) }

// Get returns the cached ciphertext for ref, or ok=false on a miss.
func (c *Cache) Get(ref string) ([]byte, bool) {
	if c == nil || !validRef(ref) {
		return nil, false
	}
	b, err := os.ReadFile(c.path(ref))
	if err != nil {
		return nil, false
	}
	return b, true
}

// Put stores ciphertext for ref (atomic, 0600). Failures are ignored — the cache
// is best-effort and never blocks a load.
func (c *Cache) Put(ref string, data []byte) {
	if c == nil || !validRef(ref) {
		return
	}
	tmp := c.path(ref) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, c.path(ref))
}

// Prune removes every cache entry whose ref is not in live (plus leftover temp
// files), bounding the cache to the current library's shards.
func (c *Cache) Prune(live []string) {
	if c == nil {
		return
	}
	keep := make(map[string]bool, len(live))
	for _, r := range live {
		if validRef(r) {
			keep[r] = true
		}
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".tmp") || !keep[n] {
			_ = os.Remove(filepath.Join(c.dir, n))
		}
	}
}

// Purge removes the entire cache directory (used by logout / cache-clear).
func Purge(dir string) error { return os.RemoveAll(dir) }
