package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
	"github.com/MalteKiefer/ledgerline-cli/internal/ui"
)

// loginTimeout bounds the whole approval wait in `auth pair`. It comfortably
// exceeds the server's 60-second code lifetime so an expiring code surfaces as
// a clean server 410 rather than a client timeout.
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
	cmd.AddCommand(newAuthLoginCommand(), newAuthPairCommand(), newAuthLogoutCommand(), newAuthStatusCommand())
	return cmd
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
			fmt.Fprintf(out, "Usage:   %s\n", usageSummary(usage))
			return nil
		},
	}
}

// usageSummary renders the storage figures for `auth status`, breaking them out
// per module only when the server reported the split.
func usageSummary(u api.Usage) string {
	total := humanBytes(u.Total())
	if u.Quota != nil && *u.Quota > 0 {
		total += " of " + humanBytes(*u.Quota)
	}
	if !u.HasBreakdown() {
		return total
	}
	parts := make([]string, 0, 2)
	if u.Files != nil {
		parts = append(parts, humanBytes(*u.Files)+" in files")
	}
	if u.Gallery != nil {
		parts = append(parts, humanBytes(*u.Gallery)+" in gallery")
	}
	return total + " (" + strings.Join(parts, ", ") + ")"
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
func humanBytes(n int64) string { return ui.HumanBytes(n) }
