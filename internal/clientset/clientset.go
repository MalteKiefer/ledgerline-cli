// Package clientset builds an authenticated API client from the stored
// session, applying the same certificate pinning and config-directory rules
// everywhere. The CLI and the tray GUI both go through it so a client can never
// end up talking to the server with weaker transport guarantees than the other.
package clientset

import (
	"errors"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/certpin"
	"github.com/MalteKiefer/ledgerline-cli/internal/config"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
)

// ErrNotAuthenticated is returned by Authenticated when no credential is
// stored. Callers present it in their own words (a CLI hint, a tray menu item).
var ErrNotAuthenticated = session.ErrNotAuthenticated

// New builds a client for server with trust-on-first-use certificate pinning
// against the config directory. Extra options (a token, a custom transport in
// tests) are appended, so a caller can still override.
func New(server string, opts ...api.Option) (*api.Client, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	opts = append(opts, api.WithCertPinner(certpin.NewStore(dir)))
	return api.New(server, opts...)
}

// Authenticated loads the stored session and returns a pinned client carrying
// its bearer, plus the session itself (server URL, identity) for display.
// It performs no network call: the caller decides whether to verify the token
// (the CLI does, through its kill-switch path).
func Authenticated() (*api.Client, session.Session, error) {
	sess, err := session.Load()
	if err != nil {
		if errors.Is(err, session.ErrNotAuthenticated) {
			return nil, session.Session{}, ErrNotAuthenticated
		}
		return nil, session.Session{}, err
	}
	client, err := New(sess.ServerURL, api.WithToken(sess.Token))
	if err != nil {
		return nil, session.Session{}, err
	}
	return client, sess, nil
}
