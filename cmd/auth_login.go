package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
	"github.com/MalteKiefer/ledgerline-cli/internal/authflow"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
	"github.com/MalteKiefer/ledgerline-cli/internal/ui"
	"github.com/MalteKiefer/ledgerline-cli/internal/version"
)

// passwordTimeout bounds a credential sign-in. Unlike pairing there is no human
// step in the middle, so a slow answer means a slow server, not a slow user.
const passwordTimeout = 60 * time.Second

// newAuthLoginCommand implements the credential sign-in: e-mail, password and,
// when the account has one, its second factor.
//
// The factor is not optional and cannot be skipped here: the server answers 422
// {two_factor:true} until a valid code arrives, so signing in through the API
// is exactly as gated as signing in through the web app.
func newAuthLoginCommand() *cobra.Command {
	var serverFlag, emailFlag, passwordFlag, otpFlag, recoveryFlag, deviceFlag string
	var passwordStdin bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in with e-mail, password and your second factor",
		Long: "Sign in with your account credentials. The password is read from the\n" +
			"terminal without echoing it, or from stdin with --password-stdin; passing\n" +
			"it as --password puts it in argv, where other users on the machine can\n" +
			"read it.\n\n" +
			"If the account has two-factor authentication, the code is prompted for\n" +
			"when the server asks for it. Use --recovery-code instead when the\n" +
			"authenticator is unavailable.\n\n" +
			"To keep the password out of this program entirely, use `auth pair`, which\n" +
			"exchanges a one-time code generated in the web profile.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in := cmd.InOrStdin()
			out := cmd.OutOrStdout()

			password, err := secretFlag(cmd, "password", passwordFlag, passwordStdin)
			if err != nil {
				return err
			}

			server := serverFlag
			if server == "" {
				if v, perr := ui.Prompt(in, out, "Server URL: "); perr != nil {
					return perr
				} else {
					server = v
				}
			}
			email := emailFlag
			if email == "" {
				if v, perr := ui.Prompt(in, out, "E-mail: "); perr != nil {
					return perr
				} else {
					email = v
				}
			}
			if password == nil {
				v, perr := readPassword(cmd, "Password: ")
				if perr != nil {
					return perr
				}
				password = &v
			}

			opts := authflow.PasswordOptions{
				Server:     server,
				Email:      email,
				Password:   *password,
				Code:       otpFlag,
				Recovery:   recoveryFlag,
				DeviceName: deviceFlag,
				AppVersion: version.Version,
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), passwordTimeout)
			defer cancel()

			sess, err := authflow.Password(ctx, opts)
			if errors.Is(err, authflow.ErrTwoFactorRequired) && opts.Code == "" && opts.Recovery == "" {
				// Only prompt once, and only when nothing was supplied: a wrong
				// code should fail visibly rather than loop.
				code, perr := ui.Prompt(in, out, "Two-factor code: ")
				if perr != nil {
					return perr
				}
				if strings.TrimSpace(code) == "" {
					return errors.New("no two-factor code entered")
				}
				opts.Code = code
				ctx2, cancel2 := context.WithTimeout(cmd.Context(), passwordTimeout)
				defer cancel2()
				sess, err = authflow.Password(ctx2, opts)
			}
			if err != nil {
				auditLog().Log(audit.Event{Event: "auth.login", Outcome: audit.OutcomeError, Target: server})
				return err
			}

			// Non-secret: server host and identity id. The token is never logged.
			auditLog().Log(audit.Event{
				Event: "auth.login", Outcome: audit.OutcomeOK, Target: sess.ServerURL,
				Detail: fmt.Sprintf("user #%d via %s (password)", sess.UserID, backendLabel(sess.Backend)),
			})
			reportSignedIn(cmd, sess)
			return nil
		},
	}

	cmd.Flags().StringVar(&serverFlag, "server", "", "server base URL (e.g. https://ledger.example.com); prompted if omitted")
	cmd.Flags().StringVar(&emailFlag, "email", "", "account e-mail address; prompted if omitted")
	cmd.Flags().StringVar(&passwordFlag, "password", "", "account password (visible in argv; prefer --password-stdin)")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	cmd.Flags().StringVar(&otpFlag, "otp", "", "two-factor code from your authenticator app")
	cmd.Flags().StringVar(&recoveryFlag, "recovery-code", "", "recovery code, when the authenticator is unavailable")
	cmd.Flags().StringVar(&deviceFlag, "device-name", defaultDeviceName(), "name shown for this device in the web app")
	return cmd
}

// newAuthPairCommand implements the one-time-code exchange, for signing in
// without ever typing the password into this program.
func newAuthPairCommand() *cobra.Command {
	var serverFlag, codeFlag, deviceFlag string

	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Sign in with a one-time code from the web profile",
		Long: "Pair this machine using a one-time code. In the Ledgerline web profile,\n" +
			"open the \"Command-line client\" card and generate a code, then paste it\n" +
			"here. The code is valid for about a minute; after pasting it, approve the\n" +
			"device in the web app and this command completes on its own.\n\n" +
			"Nothing but the code crosses the boundary: the password and the second\n" +
			"factor stay in the web app.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			code := codeFlag
			if code == "" {
				v, err := ui.Prompt(in, out, "One-time code (from the web profile): ")
				if err != nil {
					return err
				}
				code = v
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), loginTimeout)
			defer cancel()

			spinner := ui.NewSpinner(out, "Waiting for approval…")
			sess, err := authflow.Run(ctx, authflow.Options{
				Server:     server,
				Code:       code,
				DeviceName: deviceFlag,
				OnWaiting: func() {
					fmt.Fprintf(out, "Code accepted. Approve %q in the web app to continue.\n", deviceFlag)
					spinner.Start()
				},
			})
			spinner.Stop()
			if err != nil {
				auditLog().Log(audit.Event{Event: "auth.pair", Outcome: audit.OutcomeError, Target: server})
				return err
			}

			auditLog().Log(audit.Event{
				Event: "auth.pair", Outcome: audit.OutcomeOK, Target: sess.ServerURL,
				Detail: fmt.Sprintf("user #%d via %s (code)", sess.UserID, backendLabel(sess.Backend)),
			})
			reportSignedIn(cmd, sess)
			return nil
		},
	}

	cmd.Flags().StringVar(&serverFlag, "server", "", "server base URL; prompted if omitted")
	cmd.Flags().StringVar(&codeFlag, "code", "", "one-time code from the web profile; prompted if omitted")
	cmd.Flags().StringVar(&deviceFlag, "device-name", defaultDeviceName(), "name shown for this device in the web app")
	return cmd
}

// reportSignedIn prints the identity and where the token ended up.
func reportSignedIn(cmd *cobra.Command, sess session.Session) {
	out := cmd.OutOrStdout()
	name := sess.UserName
	if name == "" {
		name = sess.UserEmail
	}
	fmt.Fprintf(out, "Signed in as %s <%s> on %s.\n", name, sess.UserEmail, sess.ServerURL)
	fmt.Fprintf(out, "Token stored in the %s.\n", backendLabel(sess.Backend))
}

// readPassword reads a password from the terminal without echoing it. When
// stdin is not a terminal (a pipe, a CI job) there is nothing to echo to, so it
// falls back to a plain line read rather than failing.
func readPassword(cmd *cobra.Command, label string) (string, error) {
	out := cmd.OutOrStdout()
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(out, label)
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", err
		}
		if len(raw) == 0 {
			return "", errors.New("no password entered")
		}
		return string(raw), nil
	}
	value, err := ui.Prompt(cmd.InOrStdin(), out, label)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", errors.New("no password entered")
	}
	return value, nil
}
