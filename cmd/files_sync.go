package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/MalteKiefer/ledgerline-cli/internal/files"
	"github.com/MalteKiefer/ledgerline-cli/internal/settings"
	"github.com/MalteKiefer/ledgerline-cli/internal/ui"
)

// newFilesSyncCommand builds the bidirectional `files sync` command.
func newFilesSyncCommand() *cobra.Command {
	var (
		maps     []string
		conflict string
		delete   string
		hidden   bool
		override bool
		ignore   []string
		dryRun   bool
		service  bool
	)

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Two-way sync between local folders and encrypted files",
		Long: "Bidirectionally sync local folders with the encrypted files store.\n\n" +
			"Map one or more remote folders to local directories:\n" +
			"  ledgerline-cli files sync --map Photos:/home/me/photos --map Docs:/home/me/docs\n" +
			"  ledgerline-cli files sync --map /home/me/ledger        # whole store into one folder\n\n" +
			"With no --map, the mappings from the settings file are used.\n\n" +
			"Changes flow both ways. Files with identical size and modification time are\n" +
			"left untouched. When a file differs on both sides, --conflict decides; the\n" +
			"default (newest) keeps whichever side changed last. Use --override to make\n" +
			"the local copy always win. Deletions are propagated per --delete. A local\n" +
			"sync-state database (in the config dir) records the last-synced state to\n" +
			"tell which side changed. Hidden files are skipped unless --hidden; ignore\n" +
			"patterns come from the settings file plus any --ignore flags.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFilesSync(cmd, syncFlags{
				maps:     maps,
				conflict: conflict,
				delete:   delete,
				hidden:   hidden,
				override: override,
				ignore:   ignore,
				dryRun:   dryRun,
				service:  service,
			})
		},
	}

	f := cmd.Flags()
	f.StringArrayVar(&maps, "map", nil, "mapping remote:local (repeatable); a value with no colon maps the store root")
	f.StringVar(&conflict, "conflict", files.ConflictNewest, "conflict resolution: newest | keep-both | skip")
	f.StringVar(&delete, "delete", files.DeleteBoth, "deletion propagation: both | additive | to-remote")
	f.BoolVar(&hidden, "hidden", false, "include hidden files (dotfiles)")
	f.BoolVar(&override, "override", false, "on any difference, overwrite the remote copy with the local one")
	f.StringArrayVar(&ignore, "ignore", nil, "extra ignore pattern (repeatable); adds to the settings ignore list")
	f.BoolVar(&dryRun, "dry-run", false, "show what would change without modifying anything")
	f.BoolVar(&service, "service", false, "run continuously: watch + interval sync until interrupted")
	return cmd
}

// syncFlags carries the resolved sync flags.
type syncFlags struct {
	maps     []string
	conflict string
	delete   string
	hidden   bool
	override bool
	ignore   []string
	dryRun   bool
	service  bool
}

// runFilesSync resolves mappings and runs a bidirectional pass for each. With
// --service it instead hands off to runFilesSyncService, which runs
// continuously (watch + interval) until interrupted.
func runFilesSync(cmd *cobra.Command, fl syncFlags) error {
	if err := validateSyncPolicies(fl); err != nil {
		return err
	}

	if fl.service {
		return runFilesSyncService(cmd, fl)
	}

	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	cfg, err := settings.Load()
	if err != nil {
		return err
	}
	mappings, err := resolveMappings(fl.maps, cfg)
	if err != nil {
		return err
	}
	warnServiceRunning(w)

	// A live bar (only on a terminal) shows position and running tallies so a
	// slow pass — content comparisons download blobs — never looks frozen. It is
	// (re)created per mapping once its item count is known. Action lines are
	// printed as scrollback above the bar.
	active := term.IsTerminal(int(os.Stdout.Fd()))
	var bar *ui.ProgressBar

	opts := files.SyncOptions{
		Conflict: fl.conflict,
		Delete:   fl.delete,
		Hidden:   fl.hidden || cfg.Hidden,
		Override: fl.override,
		Ignore:   files.NewMatcher(append(append([]string{}, cfg.Ignore...), fl.ignore...)),
		DryRun:   fl.dryRun,
		Log: func(s string) {
			if bar != nil {
				bar.Println(s)
				return
			}
			fmt.Fprintln(w, s)
		},
		Progress: func(p files.SyncProgress) {
			if !active {
				return
			}
			if bar == nil {
				bar = ui.NewProgressBar(w, p.Total, active)
			}
			bar.Update(p.Done, fmt.Sprintf("%s  ⏭%d ↑%d ↓%d ✗%d !%d", p.Current,
				p.Res.Skipped, p.Res.Uploaded, p.Res.Downloaded,
				p.Res.TrashedRemote+p.Res.DeletedLocal, p.Res.Conflicts))
		},
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
	store.SetShardCache(shardCache("files-shards"))
	fmt.Fprintln(w, "Loading files…")
	if err := store.Load(ctx); err != nil {
		return err
	}
	warnIfDegraded(w, "files", store)

	if !fl.dryRun {
		// Heartbeat carries only a generic module tag — never a folder name or
		// counts, which would leak sealed-manifest structure to the server.
		if reportSync(ctx, client, "syncing", "files") {
			return wipedError()
		}
		defer reportSync(context.WithoutCancel(ctx), client, "idle", "")
	}

	var total files.SyncResult
	for _, m := range mappings {
		fmt.Fprintf(w, "Sync %q ⇄ %s\n", displayRemote(m.Remote), m.Local)
		bar = nil // fresh bar per mapping (Progress creates it with the right total)
		syncer := files.NewSyncer(client, store, vk, m.Local, m.Remote, opts)
		res, err := syncer.Run(ctx)
		if bar != nil {
			bar.Finish()
		}
		if err != nil {
			return err
		}
		total.Uploaded += res.Uploaded
		total.Downloaded += res.Downloaded
		total.TrashedRemote += res.TrashedRemote
		total.DeletedLocal += res.DeletedLocal
		total.Conflicts += res.Conflicts
		total.Skipped += res.Skipped
		total.Failed += res.Failed
		total.ConflictPaths = append(total.ConflictPaths, res.ConflictPaths...)
	}

	if fl.dryRun {
		fmt.Fprintln(w, "(dry run — nothing changed)")
	}
	fmt.Fprintf(w, "Done: %d up, %d down, %d unchanged, %d remote-trashed, %d local-deleted, %d conflicts, %d failed.\n",
		total.Uploaded, total.Downloaded, total.Skipped, total.TrashedRemote, total.DeletedLocal, total.Conflicts, total.Failed)
	if len(total.ConflictPaths) > 0 && fl.conflict == files.ConflictSkip {
		fmt.Fprintln(w, "Conflicts (resolve manually):")
		for _, p := range total.ConflictPaths {
			fmt.Fprintln(w, "  "+p)
		}
	}
	return nil
}

// runFilesSyncService runs `files sync --service`: with --map it first
// persists the mapping(s) into settings.json (so a later plain `--service`
// run without --map picks them back up), then resolves the full mapping list
// from settings and runs files.Service until interrupted (SIGINT/SIGTERM).
// It always prompts for the passphrase (unlockVaultPrompt, not unlockVault) so
// a long-running daemon derives its own VK rather than depending on a cache
// entry that could go stale or be cleared out from under it.
func runFilesSyncService(cmd *cobra.Command, fl syncFlags) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	cfg, err := settings.Load()
	if err != nil {
		return err
	}

	if len(fl.maps) > 0 {
		for _, a := range fl.maps {
			parsed, err := parseMapping(a)
			if err != nil {
				return err
			}
			// Seed from any existing saved mapping for this local path so that
			// re-registering it without repeating a policy flag preserves the
			// previously saved per-mapping override (Upsert replaces wholesale,
			// so an empty policy field would otherwise clobber the saved one).
			base := parsed
			if existing, ok := findMapping(cfg, parsed.Local); ok {
				base = existing
				base.Remote = parsed.Remote
				base.Local = parsed.Local
			}
			cfg.Upsert(mappingFromFlags(cmd, fl, base))
		}
		if err := settings.Save(cfg); err != nil {
			return err
		}
	}

	mappings, err := resolveMappings(nil, cfg)
	if err != nil {
		return err
	}

	release, err := acquireLock("service")
	if err != nil {
		return err
	}
	defer release()

	client, err := authedClient(ctx)
	if err != nil {
		return err
	}
	vk, err := unlockVaultPrompt(cmd, client)
	if err != nil {
		return err
	}

	store := files.NewStore(client, vk)
	store.SetShardCache(shardCache("files-shards"))
	fmt.Fprintln(w, "Loading files…")
	if err := store.Load(ctx); err != nil {
		return err
	}
	warnIfDegraded(w, "files", store)

	globalInterval := settings.ParseDurationOr(cfg.Service.Interval, 60*time.Second)
	debounce := settings.ParseDurationOr(cfg.Service.Debounce, 2*time.Second)

	sms := make([]files.ServiceMapping, 0, len(mappings))
	for _, m := range mappings {
		hidden := fl.hidden || cfg.Hidden
		if m.Hidden != nil {
			hidden = *m.Hidden
		}
		conflict := fl.conflict
		if m.Conflict != "" {
			conflict = m.Conflict
		}
		del := fl.delete
		if m.Delete != "" {
			del = m.Delete
		}
		patterns := append(append([]string{}, cfg.Ignore...), fl.ignore...)
		patterns = append(patterns, m.Ignore...)
		sms = append(sms, files.ServiceMapping{
			Remote: m.Remote,
			Local:  m.Local,
			Opts: files.SyncOptions{
				Conflict: conflict,
				Delete:   del,
				Hidden:   hidden,
				Override: false,
				Ignore:   files.NewMatcher(patterns),
			},
			Interval: settings.ParseDurationOr(m.Interval, globalInterval),
		})
	}

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc := &files.Service{
		Client:   client,
		Store:    store,
		VK:       vk,
		Mappings: sms,
		Debounce: debounce,
		Log:      func(s string) { fmt.Fprintln(w, s) },
		Audit:    auditLog(),
		Heartbeat: func(state, detail string) bool {
			return reportSync(context.WithoutCancel(ctx), client, state, detail)
		},
	}
	fmt.Fprintf(w, "Service running (%d mappings). Ctrl-C to stop.\n", len(sms))
	return svc.Run(sigCtx)
}

// findMapping returns the saved mapping whose local path matches (after
// filepath.Clean), mirroring how settings.Upsert keys mappings.
func findMapping(cfg settings.Settings, local string) (settings.Mapping, bool) {
	key := filepath.Clean(local)
	for _, m := range cfg.Sync {
		if filepath.Clean(m.Local) == key {
			return m, true
		}
	}
	return settings.Mapping{}, false
}

// mappingFromFlags builds the settings.Mapping to persist for a --map value
// passed alongside --service, starting from base (the parsed --map arg, or the
// existing saved mapping when one exists for this local path). Every policy
// field (conflict/delete/hidden/ignore) is overwritten ONLY when the user
// explicitly set the corresponding flag, so re-registering an existing mapping
// without repeating a flag leaves its saved per-mapping override intact rather
// than resetting it to the command default. An unset Conflict/Delete resolves
// back to the default at run time (the resolution code treats "" as "fall back").
func mappingFromFlags(cmd *cobra.Command, fl syncFlags, base settings.Mapping) settings.Mapping {
	m := base
	if cmd.Flags().Changed("conflict") {
		m.Conflict = fl.conflict
	}
	if cmd.Flags().Changed("delete") {
		m.Delete = fl.delete
	}
	if cmd.Flags().Changed("hidden") {
		h := fl.hidden
		m.Hidden = &h
	}
	if cmd.Flags().Changed("ignore") {
		m.Ignore = fl.ignore
	}
	return m
}

// warnServiceRunning best-effort checks whether a files sync --service
// instance already holds the lock and, if so, prints a note — but never
// refuses the one-shot pass. A concurrent service and manual one-shot sync
// against the same store are safe (the store is single-writer per process,
// and each pass just contends briefly), just potentially noisy.
func warnServiceRunning(w io.Writer) {
	path, err := lockFilePath("service")
	if err != nil {
		return
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintln(w, "Note: a files sync --service instance appears to be running; proceeding with a one-shot sync anyway.")
	}
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
