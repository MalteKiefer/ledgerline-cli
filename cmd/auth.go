package cmd

import (
	"context"
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
	cmd.AddCommand(newAuthLoginCommand(), newAuthLogoutCommand(), newAuthStatusCommand())
	return cmd
}

// newAuthLoginCommand implements the copy/paste pairing flow.
func newAuthLoginCommand() *cobra.Command {
	var serverFlag, codeFlag, deviceFlag string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate using a code from the web app",
		Long: "Log in with a one-time code. In the Ledgerline web profile, open the\n" +
			"\"Command-line client\" card and generate a code, then paste it here. The\n" +
			"code is valid for 60 seconds; after pasting it, approve the device in the\n" +
			"web app and this command completes automatically.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLogin(cmd, serverFlag, codeFlag, deviceFlag)
		},
	}

	cmd.Flags().StringVar(&serverFlag, "server", "", "server base URL (e.g. https://ledger.example.com); prompted if omitted")
	cmd.Flags().StringVar(&codeFlag, "code", "", "one-time code from the web profile; prompted if omitted")
	cmd.Flags().StringVar(&deviceFlag, "device-name", defaultDeviceName(), "name shown for this device in the web app")
	return cmd
}

// runLogin drives the full claim → approve → collect exchange.
func runLogin(cmd *cobra.Command, serverFlag, codeFlag, deviceFlag string) error {
	in := cmd.InOrStdin()
	out := cmd.OutOrStdout()

	server := serverFlag
	if server == "" {
		v, err := ui.Prompt(in, out, "Server URL: ")
		if err != nil {
			return err
		}
		server = v
	}

	client, err := newAPIClient(server)
	if err != nil {
		return err
	}

	code := codeFlag
	if code == "" {
		v, err := ui.Prompt(in, out, "One-time code (from the web profile): ")
		if err != nil {
			return err
		}
		code = v
	}
	if code == "" {
		return errors.New("no code entered")
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), loginTimeout)
	defer cancel()

	// Step 1 — claim the pasted code.
	if err := client.ClaimPair(ctx, code, deviceFlag); err != nil {
		return claimError(err)
	}

	fmt.Fprintf(out, "Code accepted. Approve \"%s\" in the web app to continue.\n", deviceFlag)

	// Step 2 — poll until the owner approves and a token is issued.
	result, err := pollUntilApproved(ctx, client, code, out)
	if err != nil {
		return err
	}

	// Step 3 — confirm the token works, then persist the session.
	authed, err := newAPIClient(server, api.WithToken(result.Token))
	if err != nil {
		return err
	}
	user, _, _, err := authed.Me(ctx)
	if err != nil {
		return fmt.Errorf("token verification failed: %w", err)
	}

	saved, err := session.Save(session.Session{
		ServerURL: authed.BaseURL(),
		UserID:    user.ID,
		UserName:  user.Name,
		UserEmail: user.Email,
		Token:     result.Token,
	})
	if err != nil {
		return fmt.Errorf("could not store credential: %w", err)
	}

	// Audit the successful pairing (server host + identity id — non-secret; the
	// token itself is never logged, §18).
	auditLog().Log(audit.Event{
		Event: "auth.login", Outcome: audit.OutcomeOK,
		Target: authed.BaseURL(), Detail: fmt.Sprintf("user #%d via %s", user.ID, backendLabel(saved.Backend)),
	})

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
	return &cobra.Command{
		Use:   "status",
		Short: "Show the current authentication state and identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			sess, err := session.Load()
			if errors.Is(err, session.ErrNotAuthenticated) {
				fmt.Fprintln(out, "Not authenticated. Run 'ledgerline-cli auth login'.")
				return nil
			}
			if err != nil {
				return err
			}

			client, err := newAPIClient(sess.ServerURL, api.WithToken(sess.Token))
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()

			user, usage, _, err := client.Me(ctx)
			if err != nil {
				if api.Status(err) == 401 {
					// Revoked from the web or expired — clear local state cleanly.
					_ = session.Clear()
					fmt.Fprintln(out, "Stored credential is no longer valid (revoked or expired).")
					fmt.Fprintln(out, "Local credential cleared. Run 'ledgerline-cli auth login'.")
					return nil
				}
				return err
			}

			fmt.Fprintf(out, "Authenticated as %s <%s> (id %d)\n", user.Name, user.Email, user.ID)
			fmt.Fprintf(out, "Server:  %s\n", sess.ServerURL)
			fmt.Fprintf(out, "Token:   stored in the %s\n", backendLabel(sess.Backend))
			fmt.Fprintf(out, "Usage:   %s in files, %s in gallery\n", humanBytes(usage.Files), humanBytes(usage.Gallery))
			return nil
		},
	}
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
