package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
	"github.com/MalteKiefer/ledgerline-cli/internal/files"
)

// newFilesSyncCommand runs a two-way sync between a local directory and the
// remote file tree, optionally as a continuous watch service.
func newFilesSyncCommand() *cobra.Command {
	var direction, conflict, remote string
	var interval time.Duration
	var service bool

	cmd := &cobra.Command{
		Use:   "sync <local-dir>",
		Short: "Two-way sync a local directory with the remote files",
		Long: "Reconcile a local directory against the remote file tree: new/changed\n" +
			"local files are uploaded, new/changed remote files are downloaded, and a\n" +
			"file changed on both sides is resolved by --conflict. Deletions are NOT\n" +
			"propagated (a missing file is never treated as a delete). With --service it\n" +
			"keeps running, re-syncing on local changes and on --interval.\n\n" +
			"For several folders on a schedule, see `ledgerline-cli sync`, which\n" +
			"remembers the list and is what the desktop tray runs.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				return fmt.Errorf("%q is not a directory", dir)
			}
			switch direction {
			case files.DirectionBoth, files.DirectionPush, files.DirectionPull:
			default:
				return fmt.Errorf("invalid --direction %q (both|push|pull)", direction)
			}
			switch conflict {
			case files.ConflictNewest, files.ConflictKeepBoth, files.ConflictSkip:
			default:
				return fmt.Errorf("invalid --conflict %q (newest|keep-both|skip)", conflict)
			}

			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			opts := files.SyncOptions{Direction: direction, Conflict: conflict, RemoteRoot: remote}
			out := cmd.OutOrStdout()

			if service {
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				fmt.Fprintf(out, "Watching %s (interval %s). Ctrl-C to stop.\n", dir, interval)
				serr := files.RunService(ctx, client, dir, opts, interval, out)
				auditLog().Log(audit.Event{Event: "files.sync.service", Outcome: outcome(serr)})
				if ctx.Err() != nil {
					fmt.Fprintln(out, "Stopped.")
					return nil
				}
				return serr
			}

			res, err := files.Sync(cmd.Context(), client, dir, opts, out, true)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{
				Event: "files.sync", Outcome: audit.OutcomeOK,
				Count: res.Pushed + res.Pulled,
				Detail: fmt.Sprintf("%d pushed, %d pulled, %d conflicts, %d failed",
					res.Pushed, res.Pulled, res.Conflicts, res.Failed),
			})
			fmt.Fprintf(out, "\nDone: %d pushed, %d pulled, %d conflicts, %d failed.\n",
				res.Pushed, res.Pulled, res.Conflicts, res.Failed)
			return nil
		},
	}
	cmd.Flags().StringVar(&remote, "remote", "", "remote folder to sync against (default: the whole remote tree)")
	cmd.Flags().StringVar(&direction, "direction", "both", "sync direction: both, push, or pull")
	cmd.Flags().StringVar(&conflict, "conflict", "newest", "both-sides change policy: newest, keep-both, or skip")
	cmd.Flags().DurationVar(&interval, "interval", 5*time.Minute, "re-sync interval in --service mode")
	cmd.Flags().BoolVar(&service, "service", false, "keep running and watch for changes")
	return cmd
}

// outcome maps an error to an audit outcome string.
func outcome(err error) string {
	if err != nil {
		return audit.OutcomeError
	}
	return audit.OutcomeOK
}
