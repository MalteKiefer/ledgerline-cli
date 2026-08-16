package cmd

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
	"github.com/MalteKiefer/ledgerline-cli/internal/certpin"
	"github.com/MalteKiefer/ledgerline-cli/internal/config"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
	"github.com/MalteKiefer/ledgerline-cli/internal/uploadledger"
)

// auditOnce lazily opens the shared audit logger under the config directory.
var (
	auditOnce   sync.Once
	auditLogger *audit.Logger
)

// auditLog returns the process-wide audit logger (best-effort; a nil logger
// silently discards, so callers never nil-check). It records LOCAL operation
// metadata only — never keys, tokens, or content.
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

// uploadLedger opens the per-server upload dedup ledger for a module
// ("gallery"/"files"), stored under the config directory. A resolution failure
// yields an in-memory-only ledger (path "") so uploads still work, just without
// cross-run dedup.
func uploadLedger(kind, server string) (*uploadledger.Ledger, error) {
	dir, err := config.Dir()
	if err != nil {
		return uploadledger.Open("", server)
	}
	return uploadledger.Open(filepath.Join(dir, "uploads-"+kind+".json"), server)
}

// authedClient loads the stored session, builds an authenticated client, and
// checks the remote kill switch: a 401 clears the local credential; a pending
// remote wipe erases all local state. Every gallery/files command starts here so
// a revoked or wiped device fails closed.
func authedClient(ctx context.Context) (*api.Client, error) {
	sess, err := session.Load()
	if errors.Is(err, session.ErrNotAuthenticated) {
		return nil, errors.New("not authenticated; run 'ledgerline-cli auth login' first")
	}
	if err != nil {
		return nil, err
	}
	client, err := newAPIClient(sess.ServerURL, api.WithToken(sess.Token))
	if err != nil {
		return nil, err
	}
	_, _, wipe, err := client.Me(ctx)
	if err != nil {
		if api.Status(err) == 401 {
			// The device was revoked from the web or the token expired. Clear the
			// local credential so nothing stale lingers.
			_ = session.Clear()
			return nil, errors.New("this device was revoked or the session expired; local credential cleared — run 'ledgerline-cli auth login'")
		}
		return nil, err
	}
	if wipe {
		// Remote kill switch: the owner asked to wipe this client. Erase all local
		// state and stop.
		_ = session.WipeLocal()
		return nil, errors.New("this client was wiped remotely from the web; all local data was erased")
	}
	return client, nil
}
