package authflow

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/clientset"
	"github.com/MalteKiefer/ledgerline-cli/internal/installid"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
)

// ErrTwoFactorRequired reports that the account has a confirmed second factor
// and the attempt carried no valid code. The caller collects one and calls
// Password again with the same credentials plus Code or RecoveryCode.
var ErrTwoFactorRequired = errors.New("a two-factor code is required")

// ErrEmailUnverified reports that the address has not been confirmed. No token
// is issued until it is, so retrying with a code cannot help.
var ErrEmailUnverified = errors.New("this account's e-mail address is not verified yet")

// ErrBadCredentials reports a rejected e-mail/password pair. The server answers
// the same way for a blocked account on purpose, so the message must not claim
// which of the two it was.
var ErrBadCredentials = errors.New("wrong e-mail or password")

// PasswordOptions describes one password sign-in attempt.
type PasswordOptions struct {
	Server     string
	Email      string
	Password   string
	Code       string // TOTP from the authenticator app
	Recovery   string // recovery code, as an alternative to Code
	DeviceName string
	AppVersion string
}

// Password signs in with credentials and stores the resulting session.
//
// The second factor is enforced by the server, not by this function: it is sent
// only if the caller supplies one, and the server keeps answering
// ErrTwoFactorRequired until it is right. Talking to the API instead of the web
// app therefore does not skip the factor.
func Password(ctx context.Context, opts PasswordOptions) (session.Session, error) {
	server := strings.TrimSpace(opts.Server)
	email := strings.TrimSpace(opts.Email)
	switch {
	case server == "":
		return session.Session{}, errors.New("no server URL given")
	case email == "":
		return session.Session{}, errors.New("no e-mail address given")
	case opts.Password == "":
		return session.Session{}, errors.New("no password given")
	}

	device := strings.TrimSpace(opts.DeviceName)
	if device == "" {
		device = "ledgerline client"
	}

	client, err := clientset.New(server)
	if err != nil {
		return session.Session{}, err
	}

	result, err := client.Login(ctx, api.LoginRequest{
		Email:        email,
		Password:     opts.Password,
		Code:         strings.TrimSpace(opts.Code),
		RecoveryCode: strings.TrimSpace(opts.Recovery),
		DeviceName:   device,
		InstallID:    installid.Get(),
		AppVersion:   opts.AppVersion,
		OSVersion:    runtime.GOOS,
	})
	if err != nil {
		return session.Session{}, loginError(err)
	}

	// Verify before storing, as in the pairing flow.
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

// loginError maps the endpoint's answers onto the sentinels above.
func loginError(err error) error {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch {
	case apiErr.StatusCode == 422 && apiErr.TwoFactor:
		return ErrTwoFactorRequired
	case apiErr.StatusCode == 422:
		return ErrBadCredentials
	case apiErr.StatusCode == 403 && apiErr.Code == "verify-email":
		return ErrEmailUnverified
	case apiErr.StatusCode == 429:
		return errors.New("too many attempts; wait a moment and try again")
	default:
		return err
	}
}
