package cmd

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/net/webdav"

	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
	"github.com/MalteKiefer/ledgerline-cli/internal/webdavfs"
)

// newFilesWebdavCommand serves the remote Files module as a local WebDAV
// endpoint so the operating system can mount it as a network drive. Every
// operation is a REST call against the server; the only local state is the temp
// file of a body currently in flight.
func newFilesWebdavCommand() *cobra.Command {
	var addr, user string
	var readOnly, noAuth, allowRemote bool
	cmd := &cobra.Command{
		Use:   "webdav",
		Short: "Serve the remote files as a local WebDAV drive to mount",
		Long: "Serve the remote Files module over WebDAV on a local address so the\n" +
			"operating system can mount it as a network drive. The endpoint is\n" +
			"protected by generated Basic-auth credentials and bound to loopback\n" +
			"unless told otherwise; it runs until interrupted.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return fmt.Errorf("--addr must be host:port: %w", err)
			}
			loopback := isLoopbackHost(host)
			if !loopback && !allowRemote {
				return errors.New("--addr is not loopback; pass --allow-remote to expose the mount on the network (anyone who can reach it and has the credentials gets your files)")
			}
			if !loopback && noAuth {
				return errors.New("--no-auth is refused on a non-loopback address")
			}

			// A local socket is reachable by every user on this host, so the
			// endpoint requires credentials by default. --no-auth is an explicit
			// opt-out for a single-user machine.
			password := ""
			if !noAuth {
				password, err = randomPassword()
				if err != nil {
					return err
				}
			}

			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			handler := &webdav.Handler{
				FileSystem: webdavfs.New(client, readOnly),
				LockSystem: webdav.NewMemLS(),
			}
			out := cmd.OutOrStdout()
			var mux http.Handler = handler
			if !noAuth {
				mux = basicAuth(user, password, handler)
			}
			server := &http.Server{
				Addr:              addr,
				Handler:           mux,
				ReadHeaderTimeout: 20 * time.Second,
			}

			// A ListenConfig-based listen binds under the command's context, so a
			// cancelled command cannot leave the socket open.
			listenCfg := &net.ListenConfig{}
			listener, err := listenCfg.Listen(cmd.Context(), "tcp", addr)
			if err != nil {
				return err
			}
			url := "http://" + listener.Addr().String() + "/"
			fmt.Fprintf(out, "Serving %s over WebDAV at %s", client.BaseURL(), url)
			if readOnly {
				fmt.Fprint(out, " (read-only)")
			}
			fmt.Fprintln(out)
			if noAuth {
				fmt.Fprintln(out, "WARNING: no authentication — every local user can read and write your files through this endpoint.")
			} else {
				fmt.Fprintf(out, "User: %s\nPassword: %s\n", user, password)
			}
			fmt.Fprint(out, mountHint(url, user))
			fmt.Fprintln(out, "Press Ctrl-C to stop.")

			auditLog().Log(audit.Event{
				Event: "files.webdav.serve", Outcome: audit.OutcomeStart,
				Target: listener.Addr().String(),
				Detail: fmt.Sprintf("read_only=%t auth=%t", readOnly, !noAuth),
			})

			// Serve until the context is cancelled (Ctrl-C) and then shut down
			// cleanly so an in-flight upload is not cut mid-body.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			errCh := make(chan error, 1)
			go func() { errCh <- server.Serve(listener) }()
			select {
			case serr := <-errCh:
				if errors.Is(serr, http.ErrServerClosed) {
					return nil
				}
				return serr
			case <-ctx.Done():
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = server.Shutdown(shutdownCtx)
				auditLog().Log(audit.Event{Event: "files.webdav.serve", Outcome: audit.OutcomeOK})
				fmt.Fprintln(out, "Stopped.")
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:9800", "address to serve on (host:port)")
	cmd.Flags().StringVar(&user, "user", "ledgerline", "Basic-auth user name for the local endpoint")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "refuse every write through the mount")
	cmd.Flags().BoolVar(&noAuth, "no-auth", false, "serve without Basic auth (loopback only)")
	cmd.Flags().BoolVar(&allowRemote, "allow-remote", false, "allow binding a non-loopback address")
	return cmd
}

// isLoopbackHost reports whether the bind host stays on this machine. An empty
// host or 0.0.0.0/:: means "all interfaces", which is not loopback.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// randomPassword mints a single-run credential for the local endpoint. It is
// printed once and never stored, so it cannot leak from disk.
func randomPassword() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// basicAuth wraps next with a constant-time Basic-auth check.
func basicAuth(user, password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(gotUser), []byte(user)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(gotPass), []byte(password)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="ledgerline"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mountHint prints the platform's mount command, since that is the step every
// user needs next and it differs per OS.
func mountHint(url, user string) string {
	var b strings.Builder
	b.WriteString("\nMount it with:\n")
	switch runtime.GOOS {
	case "windows":
		// net use prompts for the password when only the user is given.
		b.WriteString("  net use Z: " + strings.TrimSuffix(url, "/") + " /user:" + user + "\n")
		b.WriteString("  (unmount: net use Z: /delete)\n")
	case "darwin":
		b.WriteString("  open " + url + "        # Finder: Go > Connect to Server\n")
		b.WriteString("  or: mount_webdav -i " + url + " /Volumes/ledgerline\n")
	default:
		b.WriteString("  gio mount " + url + "\n")
		b.WriteString("  or: sudo mount -t davfs " + url + " /mnt/ledgerline\n")
	}
	return b.String()
}
