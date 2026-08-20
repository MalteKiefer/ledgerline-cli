package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
	"github.com/MalteKiefer/ledgerline-cli/internal/files"
	"github.com/MalteKiefer/ledgerline-cli/internal/syncconfig"
)

// newSyncCommand builds the `sync` group: several folder pairs, remembered.
//
// `files sync <dir>` stays what it is — one directory, right now, no state.
// This group is for the standing arrangement: a set of pairs the desktop tray
// and the terminal both read, each with its own remote folder and schedule.
func newSyncCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Keep several folders in sync with the server",
		Long: "Manage the folder pairs this machine keeps in step with the server. Each\n" +
			"pair is one local directory and one remote folder, with its own direction,\n" +
			"conflict policy and schedule. The desktop tray runs the same list.\n\n" +
			"Deletions are never propagated: a file missing on one side is treated as\n" +
			"absent, not as an instruction to delete it on the other.",
	}
	cmd.AddCommand(
		newSyncAddCommand(),
		newSyncListCommand(),
		newSyncRemoveCommand(),
		newSyncSetCommand(),
		newSyncRunCommand(),
		newSyncServiceCommand(),
	)
	return cmd
}

func newSyncAddCommand() *cobra.Command {
	var remote, direction, conflict string
	var interval time.Duration
	var disabled bool

	cmd := &cobra.Command{
		Use:   "add <local-dir>",
		Short: "Add a folder pair",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pair, err := syncconfig.Add(syncconfig.Pair{
				Local:           args[0],
				Remote:          remote,
				Direction:       direction,
				Conflict:        conflict,
				IntervalMinutes: int(interval.Minutes()),
				Enabled:         !disabled,
			})
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "sync.pair.added", Outcome: audit.OutcomeOK, Target: pair.Remote})
			fmt.Fprintf(cmd.OutOrStdout(), "Added pair %s: %s\n", pair.ID, pair.Describe())
			return nil
		},
	}
	cmd.Flags().StringVar(&remote, "remote", "", "remote folder path (default: the remote root)")
	cmd.Flags().StringVar(&direction, "direction", syncconfig.DirectionBoth, "both, push, or pull")
	cmd.Flags().StringVar(&conflict, "conflict", syncconfig.ConflictNewest, "both-sides change policy: newest, keep-both, or skip")
	cmd.Flags().DurationVar(&interval, "interval", syncconfig.DefaultInterval, "automatic re-sync period; 0 for manual only")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "add the pair but do not run it yet")
	return cmd
}

func newSyncListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the configured folder pairs",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := syncconfig.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(f.Pairs) == 0 {
				fmt.Fprintln(out, "No folder pairs configured. Add one with: ledgerline-cli sync add <dir>")
				return nil
			}
			for _, p := range f.Pairs {
				fmt.Fprintf(out, "%-3s %s\n", p.ID, p.Describe())
				switch {
				case p.LastFailure != "":
					fmt.Fprintf(out, "    last run %s: %s\n", humanWhen(p.LastRun), p.LastFailure)
				case p.LastResult != "":
					fmt.Fprintf(out, "    last run %s: %s\n", humanWhen(p.LastRun), p.LastResult)
				}
			}
			return nil
		},
	}
}

func newSyncRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <id>",
		Aliases: []string{"remove"},
		Short:   "Remove a folder pair (local and remote files are untouched)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := syncconfig.Remove(args[0]); err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "sync.pair.removed", Outcome: audit.OutcomeOK, Target: args[0]})
			fmt.Fprintf(cmd.OutOrStdout(), "Removed pair %s. No files were deleted.\n", args[0])
			return nil
		},
	}
}

func newSyncSetCommand() *cobra.Command {
	var direction, conflict string
	var interval time.Duration
	var enable, disable bool

	cmd := &cobra.Command{
		Use:   "set <id>",
		Short: "Change a pair's direction, conflict policy, schedule or state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if enable && disable {
				return errors.New("--enable and --disable are mutually exclusive")
			}
			flags := cmd.Flags()
			pair, err := syncconfig.Update(args[0], func(p *syncconfig.Pair) {
				if flags.Changed("direction") {
					p.Direction = direction
				}
				if flags.Changed("conflict") {
					p.Conflict = conflict
				}
				if flags.Changed("interval") {
					p.IntervalMinutes = int(interval.Minutes())
				}
				if enable {
					p.Enabled = true
				}
				if disable {
					p.Enabled = false
				}
			})
			if err != nil {
				return err
			}
			// Validate after the fact so a bad value cannot be stored silently.
			if _, verr := syncconfig.Normalise(pair); verr != nil {
				return verr
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-3s %s\n", pair.ID, pair.Describe())
			return nil
		},
	}
	cmd.Flags().StringVar(&direction, "direction", "", "both, push, or pull")
	cmd.Flags().StringVar(&conflict, "conflict", "", "newest, keep-both, or skip")
	cmd.Flags().DurationVar(&interval, "interval", 0, "automatic re-sync period; 0 for manual only")
	cmd.Flags().BoolVar(&enable, "enable", false, "run this pair again")
	cmd.Flags().BoolVar(&disable, "disable", false, "stop running this pair")
	return cmd
}

func newSyncRunCommand() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "run [id]",
		Short: "Sync one pair now, or every enabled pair with --all",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && !all {
				return errors.New("give a pair id, or --all to run every enabled pair")
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}

			var pairs []syncconfig.Pair
			if all {
				f, lerr := syncconfig.Load()
				if lerr != nil {
					return lerr
				}
				for _, p := range f.Pairs {
					if p.Enabled {
						pairs = append(pairs, p)
					}
				}
				if len(pairs) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No enabled pairs to run.")
					return nil
				}
			} else {
				p, gerr := syncconfig.Get(args[0])
				if gerr != nil {
					return gerr
				}
				pairs = []syncconfig.Pair{p}
			}

			var failed int
			for _, p := range pairs {
				if err := runPair(cmd.Context(), client, p, cmd.OutOrStdout()); err != nil {
					failed++
					fmt.Fprintf(cmd.OutOrStdout(), "pair %s failed: %v\n", p.ID, err)
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d pairs failed", failed, len(pairs))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "run every enabled pair")
	return cmd
}

func newSyncServiceCommand() *cobra.Command {
	var tick time.Duration
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Keep running, syncing each pair on its own schedule",
		Long: "Run until interrupted, syncing every enabled pair when its interval is\n" +
			"due. This is the same loop the desktop tray runs; use it on a machine\n" +
			"without a tray, or under a service manager.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Watching configured pairs (checking every %s). Ctrl-C to stop.\n", tick)
			ticker := time.NewTicker(tick)
			defer ticker.Stop()
			for {
				RunDuePairs(ctx, client, out)
				select {
				case <-ctx.Done():
					fmt.Fprintln(out, "Stopped.")
					return nil
				case <-ticker.C:
				}
			}
		},
	}
	cmd.Flags().DurationVar(&tick, "check-every", time.Minute, "how often to look for pairs that are due")
	return cmd
}

// RunDuePairs syncs every pair whose interval has elapsed. It is exported
// because the tray runs exactly this loop body.
func RunDuePairs(ctx context.Context, client *api.Client, out io.Writer) {
	f, err := syncconfig.Load()
	if err != nil {
		fmt.Fprintf(out, "could not read the sync configuration: %v\n", err)
		return
	}
	now := time.Now()
	for _, p := range f.Pairs {
		if ctx.Err() != nil {
			return
		}
		if !p.Due(now) {
			continue
		}
		if err := runPair(ctx, client, p, out); err != nil {
			fmt.Fprintf(out, "pair %s failed: %v\n", p.ID, err)
		}
	}
}

// runPair syncs one pair and records the outcome, so a pair that has been
// failing quietly can be seen in `sync ls` and in the tray.
func runPair(ctx context.Context, client *api.Client, p syncconfig.Pair, out io.Writer) error {
	fmt.Fprintf(out, "Syncing %s\n", p.Describe())

	res, err := files.Sync(ctx, client, p.Local, files.SyncOptions{
		Direction:  p.Direction,
		Conflict:   p.Conflict,
		RemoteRoot: p.Remote,
	}, out, false)

	summary := fmt.Sprintf("%d pushed, %d pulled, %d conflicts, %d failed",
		res.Pushed, res.Pulled, res.Conflicts, res.Failed)
	_ = syncconfig.RecordRun(p.ID, time.Now(), summary, err)

	auditLog().Log(audit.Event{
		Event: "sync.pair.run", Outcome: outcome(err), Target: p.Remote,
		Count: res.Pushed + res.Pulled, Detail: summary,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "  %s\n", summary)
	return nil
}

// humanWhen renders a timestamp for a list, or "never" for the zero time.
func humanWhen(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}
