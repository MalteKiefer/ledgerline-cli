package cmd

import (
	"path/filepath"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/blobcache"
	"github.com/MalteKiefer/ledgerline-cli/internal/certpin"
	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// newAPIClient builds an API client with trust-on-first-use certificate pinning
// enabled, storing pins in the config directory. All commands reach the server
// through this helper so pinning is applied uniformly.
func newAPIClient(server string, opts ...api.Option) (*api.Client, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	opts = append(opts, api.WithCertPinner(certpin.NewStore(dir)))
	return api.New(server, opts...)
}

// shardCacheDir is the on-disk cache root (under the config dir) for content-
// addressed ciphertext shard caches.
func shardCacheDir() (string, bool) {
	dir, err := config.Dir()
	if err != nil {
		return "", false
	}
	return filepath.Join(dir, "cache"), true
}

// shardCache returns a ciphertext shard cache under the given subdirectory
// (e.g. "files-shards"), or nil if the config dir cannot be resolved — a nil
// cache is a valid, disabled cache.
func shardCache(sub string) *blobcache.Cache {
	base, ok := shardCacheDir()
	if !ok {
		return nil
	}
	c, err := blobcache.New(filepath.Join(base, sub))
	if err != nil {
		return nil
	}
	return c
}

// purgeShardCaches removes the on-disk shard caches (best-effort), called on
// logout so no cached ciphertext lingers after the credential is cleared.
func purgeShardCaches() {
	if base, ok := shardCacheDir(); ok {
		_ = blobcache.Purge(base)
	}
}
