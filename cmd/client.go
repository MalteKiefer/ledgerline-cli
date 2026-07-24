package cmd

import (
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
	"github.com/MalteKiefer/ledgerline-cli/internal/blobcache"
	"github.com/MalteKiefer/ledgerline-cli/internal/certpin"
	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// auditOnce lazily opens the shared audit logger under the config directory.
var (
	auditOnce   sync.Once
	auditLogger *audit.Logger
)

// auditLog returns the process-wide audit logger (best-effort; a nil logger
// silently discards, so callers never nil-check). It records LOCAL operation
// metadata only — never keys, tokens, passphrases, or content (§18).
func auditLog() *audit.Logger {
	auditOnce.Do(func() {
		if dir, err := config.Dir(); err == nil {
			auditLogger = audit.New(dir)
		}
	})
	return auditLogger
}

// purgeAudit removes the local audit trail (used by logout and `audit purge`).
func purgeAudit() {
	if dir, err := config.Dir(); err == nil {
		_ = audit.Purge(dir)
	}
}

// degradable is a sharded store that can load in a read-only degraded state when
// a record shard is permanently missing (gallery.Store, files.Store).
type degradable interface {
	Degraded() bool
	MissingShards() int
}

// warnIfDegraded prints a clear stderr warning and audits the event when a store
// loaded degraded (a record shard is missing → read-only, no writes). kind names
// the module ("gallery"/"files").
func warnIfDegraded(w io.Writer, kind string, store degradable) {
	if !store.Degraded() {
		return
	}
	n := store.MissingShards()
	fmt.Fprintf(w, "Warning: %d %s record shard(s) are missing on the server. Showing what remains; "+
		"this store is READ-ONLY and will not be saved so nothing is lost.\n", n, kind)
	auditLog().Log(audit.Event{
		Event: kind + ".degraded", Outcome: audit.OutcomeError, Count: n,
		Detail: "record shard(s) missing; store read-only",
	})
}

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
