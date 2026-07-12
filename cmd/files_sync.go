package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/files"
	"github.com/MalteKiefer/ledgerline-cli/internal/settings"
)

// newFilesSyncCommand builds the bidirectional `files sync` command.
func newFilesSyncCommand() *cobra.Command {
	var (
		maps     []string
		conflict string
		delete   string
		hidden   bool
		ignore   []string
		dryRun   bool
	)

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Two-way sync between local folders and encrypted files",
		Long: "Bidirectionally sync local folders with the encrypted files store.\n\n" +
			"Map one or more remote folders to local directories:\n" +
			"  ledgerline-cli files sync --map Photos:/home/me/photos --map Docs:/home/me/docs\n" +
			"  ledgerline-cli files sync --map /home/me/ledger        # whole store into one folder\n\n" +
			"With no --map, the mappings from the settings file are used.\n\n" +
			"Changes flow both ways. Deletions are propagated per --delete. When the\n" +
			"same file changed on both sides, --conflict decides. A local sync-state\n" +
			"database (in the config dir) records the last-synced state to tell which\n" +
			"side changed. Hidden files are skipped unless --hidden; ignore patterns\n" +
			"come from the settings file plus any --ignore flags.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFilesSync(cmd, syncFlags{
				maps:     maps,
				conflict: conflict,
				delete:   delete,
				hidden:   hidden,
				ignore:   ignore,
				dryRun:   dryRun,
			})
		},
	}

	f := cmd.Flags()
	f.StringArrayVar(&maps, "map", nil, "mapping remote:local (repeatable); a value with no colon maps the store root")
	f.StringVar(&conflict, "conflict", files.ConflictKeepBoth, "conflict resolution: keep-both | newest | skip")
	f.StringVar(&delete, "delete", files.DeleteBoth, "deletion propagation: both | additive | to-remote")
	f.BoolVar(&hidden, "hidden", false, "include hidden files (dotfiles)")
	f.StringArrayVar(&ignore, "ignore", nil, "extra ignore pattern (repeatable); adds to the settings ignore list")
	f.BoolVar(&dryRun, "dry-run", false, "show what would change without modifying anything")
	return cmd
}

// syncFlags carries the resolved sync flags.
type syncFlags struct {
	maps     []string
	conflict string
	delete   string
	hidden   bool
	ignore   []string
	dryRun   bool
}

// runFilesSync resolves mappings and runs a bidirectional pass for each.
func runFilesSync(cmd *cobra.Command, fl syncFlags) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	if err := validateSyncPolicies(fl); err != nil {
		return err
	}

	cfg, err := settings.Load()
	if err != nil {
		return err
	}
	mappings, err := resolveMappings(fl.maps, cfg)
	if err != nil {
		return err
	}

	opts := files.SyncOptions{
		Conflict: fl.conflict,
		Delete:   fl.delete,
		Hidden:   fl.hidden || cfg.Hidden,
		Ignore:   files.NewMatcher(append(append([]string{}, cfg.Ignore...), fl.ignore...)),
		DryRun:   fl.dryRun,
		Log:      func(s string) { fmt.Fprintln(w, s) },
	}

	client, err := authedClient(ctx)
	if err != nil {
		return err
	}
	vk, err := unlockVault(cmd, client)
	if err != nil {
		return err
	}

	store := files.NewStore(client, vk)
	fmt.Fprintln(w, "Loading files…")
	if err := store.Load(ctx); err != nil {
		return err
	}

	if !fl.dryRun {
		if reportSync(ctx, client, "syncing", "files sync") {
			return wipedError()
		}
		defer reportSync(context.WithoutCancel(ctx), client, "idle", "")
	}

	var total files.SyncResult
	for _, m := range mappings {
		fmt.Fprintf(w, "Sync %q ⇄ %s\n", displayRemote(m.Remote), m.Local)
		if !fl.dryRun && reportSync(ctx, client, "syncing", "files sync "+displayRemote(m.Remote)) {
			return wipedError()
		}
		syncer := files.NewSyncer(client, store, vk, m.Local, m.Remote, opts)
		res, err := syncer.Run(ctx)
		if err != nil {
			return err
		}
		total.Uploaded += res.Uploaded
		total.Downloaded += res.Downloaded
		total.TrashedRemote += res.TrashedRemote
		total.DeletedLocal += res.DeletedLocal
		total.Conflicts += res.Conflicts
		total.Failed += res.Failed
		total.ConflictPaths = append(total.ConflictPaths, res.ConflictPaths...)
	}

	if fl.dryRun {
		fmt.Fprintln(w, "(dry run — nothing changed)")
	}
	fmt.Fprintf(w, "Done: %d up, %d down, %d remote-trashed, %d local-deleted, %d conflicts, %d failed.\n",
		total.Uploaded, total.Downloaded, total.TrashedRemote, total.DeletedLocal, total.Conflicts, total.Failed)
	if len(total.ConflictPaths) > 0 && fl.conflict == files.ConflictSkip {
		fmt.Fprintln(w, "Conflicts (resolve manually):")
		for _, p := range total.ConflictPaths {
			fmt.Fprintln(w, "  "+p)
		}
	}
	return nil
}

// validateSyncPolicies checks the conflict/delete flag values.
func validateSyncPolicies(fl syncFlags) error {
	switch fl.conflict {
	case files.ConflictKeepBoth, files.ConflictNewest, files.ConflictSkip:
	default:
		return fmt.Errorf("invalid --conflict %q (use keep-both, newest or skip)", fl.conflict)
	}
	switch fl.delete {
	case files.DeleteBoth, files.DeleteAdditive, files.DeleteToRemote:
	default:
		return fmt.Errorf("invalid --delete %q (use both, additive or to-remote)", fl.delete)
	}
	return nil
}

// resolveMappings builds the mapping list from --map flags or the settings file.
func resolveMappings(mapArgs []string, cfg settings.Settings) ([]settings.Mapping, error) {
	if len(mapArgs) > 0 {
		out := make([]settings.Mapping, 0, len(mapArgs))
		for _, a := range mapArgs {
			m, err := parseMapping(a)
			if err != nil {
				return nil, err
			}
			out = append(out, m)
		}
		return out, nil
	}
	if len(cfg.Sync) == 0 {
		return nil, errors.New("no mappings: pass --map remote:local, or add a \"sync\" list to the settings file")
	}
	return cfg.Sync, nil
}

// parseMapping parses a "remote:local" argument. A value with no colon maps the
// store root to that local directory.
func parseMapping(a string) (settings.Mapping, error) {
	i := strings.IndexByte(a, ':')
	if i < 0 {
		if a == "" {
			return settings.Mapping{}, errors.New("empty --map value")
		}
		return settings.Mapping{Remote: "", Local: a}, nil
	}
	remote := strings.TrimSpace(a[:i])
	local := strings.TrimSpace(a[i+1:])
	if local == "" {
		return settings.Mapping{}, fmt.Errorf("mapping %q has no local path", a)
	}
	return settings.Mapping{Remote: remote, Local: local}, nil
}

// displayRemote renders an empty remote base as the store root.
func displayRemote(remote string) string {
	if strings.TrimSpace(remote) == "" {
		return "(files root)"
	}
	return remote
}
