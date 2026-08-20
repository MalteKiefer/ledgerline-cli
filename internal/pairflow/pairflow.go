// Package pairflow drives the device-pairing exchange: claim a one-time code,
// wait for the owner to approve the device in the web app, verify the issued
// token and store the session. The CLI prompts for the code on a terminal and
// the tray GUI collects it in a local browser dialog; both need the same
// sequence, so it lives here rather than in either front end.
//
// This client never sees a password or a second factor: the user signs in to the
// web app (with whatever two-factor step their account has) to get the code and
// to approve the device. All that crosses this boundary is a short-lived code
// and, on success, a bearer token.
package pairflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/clientset"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
)

// DefaultPollInterval is how often the pairing endpoint is polled while waiting
// for approval.
const DefaultPollInterval = 2 * time.Second

// Options describes one pairing attempt.
type Options struct {
	Server     string // base URL of the Ledgerline server
	Code       string // one-time code from the web profile
	DeviceName string // name shown in the web app's device list

	// PollInterval overrides DefaultPollInterval (tests use a short one).
	PollInterval time.Duration
	// OnWaiting, when set, is called once the code has been accepted and the
	// flow is waiting for the owner to approve the device.
	OnWaiting func()
}

// Run performs claim → wait for approval → verify → save. It returns the stored
// session (its Token field carries the bearer) on success. The caller controls
// the overall deadline through ctx: the code itself is only valid for about a
// minute, but approval can take longer.
func Run(ctx context.Context, opts Options) (session.Session, error) {
	server := strings.TrimSpace(opts.Server)
	code := strings.TrimSpace(opts.Code)
	if server == "" {
		return session.Session{}, errors.New("no server URL given")
	}
	if code == "" {
		return session.Session{}, errors.New("no one-time code given")
	}
	device := strings.TrimSpace(opts.DeviceName)
	if device == "" {
		device = "ledgerline client"
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}

	client, err := clientset.New(server)
	if err != nil {
		return session.Session{}, err
	}

	if err := client.ClaimPair(ctx, code, device); err != nil {
		return session.Session{}, ClaimError(err)
	}
	if opts.OnWaiting != nil {
		opts.OnWaiting()
	}

	result, err := poll(ctx, client, code, interval)
	if err != nil {
		return session.Session{}, err
	}

	// Verify the token before storing it: a credential that cannot fetch /me is
	// worse than none, because every later command would fail confusingly.
	authed, err := clientset.New(server, api.WithToken(result.Token))
	if err != nil {
		return session.Session{}, err
	}
	user, _, _, err := authed.Me(ctx)
	if err != nil {
		return session.Session{}, fmt.Errorf("token verification failed: %w", err)
	}

	saved, err := session.Save(session.Session{
		ServerURL: authed.BaseURL(),
		UserID:    user.ID,
		UserName:  user.Name,
		UserEmail: user.Email,
		Token:     result.Token,
	})
	if err != nil {
		return session.Session{}, fmt.Errorf("could not store credential: %w", err)
	}
	return saved, nil
}

// poll waits for the owner's approval.
func poll(ctx context.Context, client *api.Client, code string, interval time.Duration) (*api.PairResult, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		status, result, err := client.PollPair(ctx, code)
		if err != nil {
			return nil, PollError(err)
		}
		if status == api.PairApproved && result != nil {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("timed out waiting for approval; the code may have expired")
		case <-ticker.C:
		}
	}
}

// ClaimError turns the claim call's failure into something a user can act on.
func ClaimError(err error) error {
	switch api.Status(err) {
	case 404, 410, 422:
		return errors.New("the code was rejected: check it was copied correctly and is still valid (it expires after about a minute)")
	case 429:
		return errors.New("too many attempts; wait a moment and try again")
	default:
		return err
	}
}

// PollError does the same for the polling call.
func PollError(err error) error {
	switch api.Status(err) {
	case 404, 410:
		return errors.New("the pairing request expired or was rejected; generate a new code")
	case 429:
		return errors.New("too many attempts; wait a moment and try again")
	default:
		return err
	}
}
