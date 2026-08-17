package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
	"github.com/MalteKiefer/ledgerline-cli/internal/ui"
)

// loginJSONResult is the `auth login --json` output shape: one line on stdout,
// success or failure, for a non-interactive caller (e.g. a GUI front-end) to
// parse instead of scraping human progress text.
type loginJSONResult struct {
	OK     bool      `json:"ok"`
	Error  string    `json:"error,omitempty"`
	Server string    `json:"server,omitempty"`
	User   *api.User `json:"user,omitempty"`
}

// statusJSONResult is the `auth status --json` output shape.
type statusJSONResult struct {
	Authenticated bool       `json:"authenticated"`
	Error         string     `json:"error,omitempty"`
	Server        string     `json:"server,omitempty"`
	User          *api.User  `json:"user,omitempty"`
	Usage         *api.Usage `json:"usage,omitempty"`
}

// pollInterval is how often `auth login` polls for the owner's approval.
const pollInterval = 2 * time.Second

// loginTimeout bounds the whole approval wait. It comfortably exceeds the
// server's 60-second code lifetime so an expiring code surfaces as a clean
// server 410 rather than a client timeout.
const loginTimeout = 2 * time.Minute

// newAuthCommand builds the `auth` group.
func newAuthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate with a Ledgerline server",
		Long: "Manage the local credential. `auth login` exchanges a short-lived code\n" +
			"from the web profile for a durable token; `auth status` shows who you are;\n" +
			"`auth logout` revokes and removes it.",
	}
	cmd.AddCommand(newAuthLoginCommand(), newAuthLogoutCommand(), newAuthStatusCommand(), newAuthAvatarCommand(), newAuthWebdavCommand())
	return cmd
}

// newAuthLoginCommand implements the copy/paste pairing flow.
func newAuthLoginCommand() *cobra.Command {
	var serverFlag, codeFlag, deviceFlag string
	var jsonFlag bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate using a code from the web app",
		Long: "Log in with a one-time code. In the Ledgerline web profile, open the\n" +
			"\"Command-line client\" card and generate a code, then paste it here. The\n" +
			"code is valid for 60 seconds; after pasting it, approve the device in the\n" +
			"web app and this command completes automatically.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLogin(cmd, serverFlag, codeFlag, deviceFlag, jsonFlag)
		},
	}

	cmd.Flags().StringVar(&serverFlag, "server", "", "server base URL (e.g. https://ledger.example.com); prompted if omitted")
	cmd.Flags().StringVar(&codeFlag, "code", "", "one-time code from the web profile; prompted if omitted")
	cmd.Flags().StringVar(&deviceFlag, "device-name", defaultDeviceName(), "name shown for this device in the web app")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON (one line) instead of human progress text")
	return cmd
}

// runLogin drives the full claim → approve → collect exchange. In --json mode
// all interactive progress text is suppressed (server/code must be passed as
// flags — a caller wanting structured output has no terminal to prompt on
// anyway) and exactly one loginJSONResult line is written to stdout at the end,
// success or failure, so a non-interactive front-end never has to scrape text.
func runLogin(cmd *cobra.Command, serverFlag, codeFlag, deviceFlag string, jsonOut bool) error {
	in := cmd.InOrStdin()
	out := cmd.OutOrStdout()

	fail := func(err error) error {
		if jsonOut {
			_ = json.NewEncoder(out).Encode(loginJSONResult{OK: false, Error: err.Error(), Server: serverFlag})
		}
		return err
	}

	server := serverFlag
	if server == "" {
		if jsonOut {
			return fail(errors.New("--server is required with --json"))
		}
		v, err := ui.Prompt(in, out, "Server URL: ")
		if err != nil {
			return err
		}
		server = v
	}

	client, err := newAPIClient(server)
	if err != nil {
		return fail(err)
	}

	code := codeFlag
	if code == "" {
		if jsonOut {
			return fail(errors.New("--code is required with --json"))
		}
		v, err := ui.Prompt(in, out, "One-time code (from the web profile): ")
		if err != nil {
			return err
		}
		code = v
	}
	if code == "" {
		return fail(errors.New("no code entered"))
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), loginTimeout)
	defer cancel()

	// Step 1 — claim the pasted code.
	if err := client.ClaimPair(ctx, code, deviceFlag); err != nil {
		return fail(claimError(err))
	}

	progressOut := out
	if jsonOut {
		progressOut = io.Discard
	} else {
		fmt.Fprintf(out, "Code accepted. Approve \"%s\" in the web app to continue.\n", deviceFlag)
	}

	// Step 2 — poll until the owner approves and a token is issued.
	result, err := pollUntilApproved(ctx, client, code, progressOut)
	if err != nil {
		return fail(err)
	}

	// Step 3 — confirm the token works, then persist the session.
	authed, err := newAPIClient(server, api.WithToken(result.Token))
	if err != nil {
		return fail(err)
	}
	user, _, _, err := authed.Me(ctx)
	if err != nil {
		return fail(fmt.Errorf("token verification failed: %w", err))
	}

	saved, err := session.Save(session.Session{
		ServerURL: authed.BaseURL(),
		UserID:    user.ID,
		UserName:  user.Name,
		UserEmail: user.Email,
		Token:     result.Token,
	})
	if err != nil {
		return fail(fmt.Errorf("could not store credential: %w", err))
	}

	// Audit the successful pairing (server host + identity id — non-secret; the
	// token itself is never logged, §18).
	auditLog().Log(audit.Event{
		Event: "auth.login", Outcome: audit.OutcomeOK,
		Target: authed.BaseURL(), Detail: fmt.Sprintf("user #%d via %s", user.ID, backendLabel(saved.Backend)),
	})

	if jsonOut {
		return json.NewEncoder(out).Encode(loginJSONResult{OK: true, Server: authed.BaseURL(), User: &user})
	}
	fmt.Fprintf(out, "Logged in as %s <%s> on %s.\n", user.Name, user.Email, authed.BaseURL())
	fmt.Fprintf(out, "Token stored in the %s.\n", backendLabel(saved.Backend))
	return nil
}

// pollUntilApproved loops the poll endpoint, showing a spinner, until the code
// is approved (returns the token) or fails (expired/rejected/timeout).
func pollUntilApproved(ctx context.Context, client *api.Client, code string, out io.Writer) (*api.PairResult, error) {
	spinner := ui.NewSpinner(out, "Waiting for approval…")
	spinner.Start()
	defer spinner.Stop()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		status, result, err := client.PollPair(ctx, code)
		if err != nil {
			return nil, pollError(err)
		}
		if status == api.PairApproved {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return nil, errors.New("timed out waiting for approval; the code may have expired (valid for 60 seconds)")
		case <-ticker.C:
		}
	}
}

// newAuthLogoutCommand revokes and clears the local credential.
func newAuthLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Revoke and remove the local credential",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			sess, err := session.Load()
			if errors.Is(err, session.ErrNotAuthenticated) {
				fmt.Fprintln(out, "Not authenticated; nothing to do.")
				return nil
			}
			if err != nil {
				return err
			}

			// Best-effort server-side revocation — always clear locally regardless.
			if client, cerr := newAPIClient(sess.ServerURL, api.WithToken(sess.Token)); cerr == nil {
				ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
				defer cancel()
				if rerr := client.Logout(ctx); rerr != nil {
					fmt.Fprintf(out, "Warning: server-side revocation failed (%v); clearing locally anyway.\n", rerr)
				}
			}

			if err := session.Clear(); err != nil {
				return fmt.Errorf("could not clear local credential: %w", err)
			}
			purgeAudit()
			auditLog().Log(audit.Event{Event: "auth.logout", Outcome: audit.OutcomeOK})
			fmt.Fprintln(out, "Logged out.")
			return nil
		},
	}
}

// newAuthStatusCommand shows the current authentication state.
func newAuthStatusCommand() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the current authentication state and identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			enc := json.NewEncoder(out)

			sess, err := session.Load()
			if errors.Is(err, session.ErrNotAuthenticated) {
				if jsonFlag {
					return enc.Encode(statusJSONResult{Authenticated: false})
				}
				fmt.Fprintln(out, "Not authenticated. Run 'ledgerline-cli auth login'.")
				return nil
			}
			if err != nil {
				if jsonFlag {
					return enc.Encode(statusJSONResult{Authenticated: false, Error: err.Error()})
				}
				return err
			}

			client, err := newAPIClient(sess.ServerURL, api.WithToken(sess.Token))
			if err != nil {
				if jsonFlag {
					return enc.Encode(statusJSONResult{Authenticated: false, Error: err.Error()})
				}
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()

			user, usage, _, err := client.Me(ctx)
			if err != nil {
				if api.Status(err) == 401 {
					// Revoked from the web or expired — clear local state cleanly.
					_ = session.Clear()
					if jsonFlag {
						return enc.Encode(statusJSONResult{Authenticated: false, Error: "credential revoked or expired"})
					}
					fmt.Fprintln(out, "Stored credential is no longer valid (revoked or expired).")
					fmt.Fprintln(out, "Local credential cleared. Run 'ledgerline-cli auth login'.")
					return nil
				}
				if jsonFlag {
					return enc.Encode(statusJSONResult{Authenticated: false, Error: err.Error()})
				}
				return err
			}

			if jsonFlag {
				return enc.Encode(statusJSONResult{Authenticated: true, Server: sess.ServerURL, User: &user, Usage: &usage})
			}
			fmt.Fprintf(out, "Authenticated as %s <%s> (id %d)\n", user.Name, user.Email, user.ID)
			fmt.Fprintf(out, "Server:  %s\n", sess.ServerURL)
			fmt.Fprintf(out, "Token:   stored in the %s\n", backendLabel(sess.Backend))
			if usage.Quota != nil {
				fmt.Fprintf(out, "Usage:   %s of %s\n", humanBytes(usage.Used), humanBytes(*usage.Quota))
			} else {
				fmt.Fprintf(out, "Usage:   %s (unlimited)\n", humanBytes(usage.Used))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON instead of human text")
	return cmd
}

// newAuthAvatarCommand downloads the account's stored avatar, if any. Raw
// image bytes on stdout (or --out), no JSON wrapper — the same shape as
// `files download`, so a caller (e.g. the GUI) reads it exactly like any
// other blob download instead of needing a separate decoding path.
func newAuthAvatarCommand() *cobra.Command {
	var outPath string
	cmd := &cobra.Command{
		Use:   "avatar",
		Short: "Download the account's avatar image, if one is stored",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()

			out := cmd.OutOrStdout()
			if outPath != "" {
				f, ferr := os.Create(outPath)
				if ferr != nil {
					return ferr
				}
				defer f.Close()
				out = f
			}

			if err := client.Avatar(ctx, out); err != nil {
				if api.Status(err) == 404 {
					return errors.New("no avatar stored")
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outPath, "out", "", "write to this file instead of stdout")
	return cmd
}

// newAuthWebdavCommand builds the `auth webdav` group: manage the
// app-specific WebDAV/CardDAV/CalDAV password (distinct from the login
// password) that GNOME's Evolution Data Server, phones, and other DAV
// clients use to connect to the unified /dav server.
func newAuthWebdavCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webdav",
		Short: "Manage the app-specific WebDAV/CardDAV/CalDAV password",
	}
	cmd.AddCommand(newAuthWebdavShowCommand(), newAuthWebdavSetCommand(), newAuthWebdavClearCommand())
	return cmd
}

func newAuthWebdavShowCommand() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show WebDAV access status (URL, username, whether a password is set)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			access, err := client.WebDavStatus(ctx)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonFlag {
				return json.NewEncoder(out).Encode(access)
			}
			fmt.Fprintf(out, "URL:      %s\n", access.URL)
			fmt.Fprintf(out, "Username: %s\n", access.Username)
			if access.Enabled {
				fmt.Fprintln(out, "Password: set")
			} else {
				fmt.Fprintln(out, "Password: not set")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func newAuthWebdavSetCommand() *cobra.Command {
	var stdinFlag bool
	var jsonFlag bool
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set (or replace) the WebDAV password (min. 12 characters)",
		Long: "Replaces the single, shared app-specific WebDAV password. Every client\n" +
			"currently using the old one (another device, DAVx5, GNOME Contacts, ...)\n" +
			"stops working until reconfigured with the new one.\n\n" +
			"The password is never accepted as a plain --flag value (it would leak via\n" +
			"argv/process listing/shell history): pass --stdin to read it from stdin\n" +
			"(one line, e.g. from a non-interactive caller), or omit both for an\n" +
			"interactive terminal prompt.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			var password string
			var err error
			if stdinFlag {
				password, err = ui.Prompt(cmd.InOrStdin(), io.Discard, "")
				if err != nil {
					return err
				}
			} else {
				password, err = ui.Prompt(cmd.InOrStdin(), out, "New WebDAV password (min. 12 characters): ")
				if err != nil {
					return err
				}
			}
			if password == "" {
				return errors.New("no password given")
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			access, err := client.SetWebDavPassword(ctx, password)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "auth.webdav.set", Outcome: audit.OutcomeOK})
			if jsonFlag {
				return json.NewEncoder(out).Encode(access)
			}
			fmt.Fprintln(out, "WebDAV password set.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&stdinFlag, "stdin", false, "read the password from stdin instead of an interactive prompt")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func newAuthWebdavClearCommand() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Disable WebDAV access (removes the password; every client using it stops working)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			access, err := client.ClearWebDavPassword(ctx)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "auth.webdav.clear", Outcome: audit.OutcomeOK})
			out := cmd.OutOrStdout()
			if jsonFlag {
				return json.NewEncoder(out).Encode(access)
			}
			fmt.Fprintln(out, "WebDAV password cleared.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

// claimError translates a claim failure into a user-facing message.
func claimError(err error) error {
	switch api.Status(err) {
	case 410:
		return errors.New("the code is expired, unknown, or already used — generate a fresh one in the web profile")
	case 429:
		return errors.New("too many attempts; wait a moment and try again")
	default:
		return err
	}
}

// pollError translates a poll failure into a user-facing message.
func pollError(err error) error {
	switch api.Status(err) {
	case 410:
		return errors.New("the pairing was rejected or the code expired; run login again")
	default:
		return err
	}
}

// defaultDeviceName builds a recognisable per-machine device label.
func defaultDeviceName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "ledgerline-cli"
	}
	return "ledgerline-cli@" + host
}

// backendLabel renders a storage backend for humans.
func backendLabel(b session.Backend) string {
	if b == session.BackendFile {
		return "config file (0600; no OS keychain available)"
	}
	return "OS keychain"
}

// humanBytes formats a byte count with a binary unit suffix.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
