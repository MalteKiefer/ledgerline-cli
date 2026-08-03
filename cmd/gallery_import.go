package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/MalteKiefer/ledgerline-cli/internal/gallery"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
)

// newGalleryImportCommand defines the `gallery import` command. Today the only
// backend is Immich (--immich, a required marker leaving room for others later):
// it enumerates a self-hosted Immich library and imports every asset into the
// gallery through the SAME sealing/upload pipeline as `gallery upload`, resuming
// from a per-server ledger so a re-run never re-downloads what already landed.
func newGalleryImportCommand() *cobra.Command {
	var opts importOptions

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a library from another photo service",
		Long: "Import media from a self-hosted Immich server into the gallery,\n" +
			"end-to-end encrypted on this machine before it leaves it, reusing the\n" +
			"gallery upload pipeline (sealing, sharded store, partial records,\n" +
			"--jobs/--batch, dedup).\n\n" +
			"  ledgerline-cli gallery import --immich --immich-url http://host:2283\n\n" +
			"The whole library is swept in batches: only ~one batch of originals is\n" +
			"staged on disk at a time, and assets already in the local import ledger\n" +
			"are skipped before download, so a re-run resumes where it stopped.\n\n" +
			"The Immich API key is a secret: prefer the IMMICH_API_KEY environment\n" +
			"variable or the interactive no-echo prompt; --immich-key works but leaks\n" +
			"into the process list. Pass --ml/--ml-local to compute face detection and\n" +
			"search embeddings inline (Immich's own embeddings cannot be reused);\n" +
			"otherwise partial records are written for a richer client to backfill.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateImportFlags(opts); err != nil {
				return err
			}
			return runImport(cmd, opts)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&opts.immich, "immich", false, "import from an Immich server (required)")
	f.StringVar(&opts.immichURL, "immich-url", "", "Immich base URL, e.g. http://host:2283")
	f.StringVar(&opts.immichKey, "immich-key", "", "Immich API key (leaks into the process list; prefer IMMICH_API_KEY or the prompt)")
	f.BoolVar(&opts.saveKey, "save-key", false, "store the resolved Immich API key in the OS keychain (keyed by --immich-url) so future runs need no key")
	f.BoolVar(&opts.forgetKey, "forget-key", false, "remove the saved Immich API key for --immich-url from the OS keychain and exit")
	f.BoolVar(&opts.withML, "ml", false, "run face detection + search embeddings on the server (needs the server ML service; implies --process)")
	f.StringVar(&opts.mlLocalURL, "ml-local", "", "run ML on a local immich-machine-learning instance at this URL instead of the server")
	f.StringVar(&opts.mlClipModel, "ml-clip-model", defaultClipModel, "CLIP model name for --ml-local (must match the server's Smart Search model)")
	f.StringVar(&opts.mlFaceModel, "ml-face-model", defaultFaceModel, "face model name for --ml-local (must match the server's Facial Recognition model)")
	f.Float64Var(&opts.mlMinScore, "ml-min-score", defaultMinScore, "minimum face-detection score for --ml-local")
	f.IntVarP(&opts.jobs, "jobs", "j", defaultJobs, "number of assets to seal/upload in parallel")
	f.IntVar(&opts.batch, "batch", defaultBatch, "download, seal and save this many assets before each checkpoint")
	f.BoolVar(&opts.dryRun, "dry-run", false, "enumerate and count new vs already-imported assets without downloading or writing")
	return cmd
}

// importOptions carries the resolved `gallery import` flags.
type importOptions struct {
	immich      bool
	immichURL   string
	immichKey   string
	saveKey     bool
	forgetKey   bool
	withML      bool
	mlLocalURL  string
	mlClipModel string
	mlFaceModel string
	mlMinScore  float64
	jobs        int
	batch       int
	dryRun      bool
}

// runImport resolves the API key, connects to Immich, unlocks the vault and runs
// the resumable batch-streaming import through the shared gallery pipeline.
func runImport(cmd *cobra.Command, opts importOptions) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	if opts.forgetKey {
		if err := session.ClearImmichKey(opts.immichURL); err != nil {
			return err
		}
		fmt.Fprintf(out, "Removed any saved Immich API key for %s.\n", opts.immichURL)
		return nil
	}

	apiKey, err := resolveImmichKey(cmd, opts.immichURL, opts.immichKey, opts.saveKey)
	if err != nil {
		return err
	}

	immich, err := gallery.NewImmichClient(opts.immichURL, apiKey, nil)
	if err != nil {
		return err
	}
	if err := immich.Ping(ctx); err != nil {
		return fmt.Errorf("cannot reach Immich at %s — check --immich-url/key/permissions: %w", opts.immichURL, err)
	}

	client, err := authedClient(ctx)
	if err != nil {
		return err
	}

	vk, err := unlockVault(cmd, client)
	if err != nil {
		return err
	}

	store := gallery.NewStore(client, vk)
	store.SetShardCache(shardCache("gallery-shards"))
	fmt.Fprintln(out, "Loading gallery…")
	if err := store.Load(ctx); err != nil {
		return err
	}
	warnIfDegraded(out, "gallery", store)

	// Reuse the gallery upload analyzer wiring exactly: --ml-local builds a local
	// immich-machine-learning analyzer, everything else leaves it nil (server ML
	// or none). Server --ml implies the /process egress path, like `gallery upload`.
	analyzer, err := buildAnalyzer(uploadOptions{
		mlLocalURL:  opts.mlLocalURL,
		mlClipModel: opts.mlClipModel,
		mlFaceModel: opts.mlFaceModel,
		mlMinScore:  opts.mlMinScore,
	})
	if err != nil {
		return err
	}
	uploader := gallery.NewUploader(client, store, vk, opts.withML, opts.withML, analyzer)
	uploader.SetClipModel(opts.mlClipModel)

	ledger, err := gallery.OpenImportLedger(opts.immichURL)
	if err != nil {
		return err
	}
	defer ledger.Close()

	jobs := opts.jobs
	if jobs < 1 {
		jobs = defaultJobs
	}
	batch := opts.batch
	if batch < 1 {
		batch = defaultBatch
	}

	if opts.dryRun {
		fmt.Fprintln(out, "Dry run: enumerating Immich library…")
	} else {
		fmt.Fprintf(out, "Importing from Immich%s, %d in parallel…\n", mlNote(uploadOptions{withML: opts.withML, mlLocalURL: opts.mlLocalURL}), jobs)
		if reportSync(ctx, client, "syncing", "gallery") {
			return wipedError()
		}
		defer reportSync(context.WithoutCancel(ctx), client, "idle", "")
	}

	logf := func(line string) { fmt.Fprintln(out, line) }
	stats, err := gallery.RunImmichImport(ctx, uploader, store, immich, ledger, gallery.ImportOptions{
		Jobs:   jobs,
		Batch:  batch,
		DryRun: opts.dryRun,
	}, logf)

	if opts.dryRun {
		fmt.Fprintf(out, "Dry run: %d new asset(s) to import, %d already imported.\n", stats.Imported, stats.Skipped)
	} else {
		fmt.Fprintf(out, "Done: %d imported, %d duplicate, %d skipped (already imported), %d failed.\n",
			stats.Imported, stats.Duplicate, stats.Skipped, stats.Failed)
	}
	// An intended Ctrl-C leaves a consistent, resumable ledger + saved manifest — a
	// clean interruption, not a command error, so it exits 0 with a resume hint.
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(out, "Interrupted; re-run to resume from the import ledger.")
		return nil
	}
	return err
}

// validateImportFlags enforces a coherent backend + ML selection.
func validateImportFlags(opts importOptions) error {
	if !opts.immich {
		return errors.New("choose an import backend: --immich")
	}
	if strings.TrimSpace(opts.immichURL) == "" {
		return errors.New("--immich requires --immich-url, e.g. http://host:2283")
	}
	if opts.withML && opts.mlLocalURL != "" {
		return errors.New("choose one ML mode: --ml (server) or --ml-local (local instance), not both")
	}
	if opts.jobs < 1 {
		return errors.New("--jobs must be at least 1")
	}
	return nil
}

// resolveImmichKey yields the Immich API key without ever putting it on the
// required argv. Order: IMMICH_API_KEY env → interactive no-echo prompt (only on
// a terminal) → --immich-key flag (documented as leaking into the process list).
// resolveImmichKey finds the Immich API key in precedence order — explicit
// --immich-key, the IMMICH_API_KEY env var, the OS keychain (saved for this
// immichURL), then an interactive no-echo prompt. A key that did NOT come from
// the keychain is written back to it when saveKey is set, so a later run needs
// no key. The keychain lookup/save is keyed by immichURL so multiple servers
// don't collide.
func resolveImmichKey(cmd *cobra.Command, immichURL, flagKey string, saveKey bool) (string, error) {
	out := cmd.OutOrStdout()
	persist := func(k string) (string, error) {
		if saveKey {
			if err := session.SaveImmichKey(immichURL, k); err != nil {
				return "", fmt.Errorf("save Immich key: %w", err)
			}
			fmt.Fprintln(out, "Immich API key saved to the OS keychain.")
		}
		return k, nil
	}

	if k := strings.TrimSpace(os.Getenv("IMMICH_API_KEY")); k != "" {
		return persist(k)
	}
	if k, err := session.LoadImmichKey(immichURL); err == nil && k != "" {
		return k, nil // already stored — nothing to persist
	}
	if in := cmd.InOrStdin(); isTerminalIn(in) {
		fmt.Fprint(out, "Immich API key: ")
		k, err := readPassword(in)
		fmt.Fprintln(out)
		if err != nil {
			return "", err
		}
		if k = strings.TrimSpace(k); k != "" {
			return persist(k)
		}
	}
	if k := strings.TrimSpace(flagKey); k != "" {
		return persist(k)
	}
	return "", errors.New("no Immich API key: set IMMICH_API_KEY, save one with --save-key, run interactively to be prompted, or pass --immich-key (leaks into the process list)")
}

// isTerminalIn reports whether the given reader is an interactive terminal, so
// the key prompt is only shown when it can be read without echo.
func isTerminalIn(in any) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
