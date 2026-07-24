package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/gallery"
	"github.com/MalteKiefer/ledgerline-cli/internal/ml"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
	"github.com/MalteKiefer/ledgerline-cli/internal/vault"
)

// defaultBatch is how many photos are uploaded before the manifest is flushed
// (and, with --delete, verified local files removed) when --batch is not set.
const defaultBatch = 50

// defaultJobs is how many items are uploaded in parallel when --jobs is not set.
const defaultJobs = 4

// Default immich-machine-learning model names and detection threshold for
// --ml-local. They must match the server's configured models for cross-client
// search and face-clustering consistency.
const (
	defaultClipModel = "ViT-B-32__openai"
	defaultFaceModel = "buffalo_l"
	defaultMinScore  = 0.7
)

// newGalleryCommand builds the `gallery` group.
func newGalleryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gallery",
		Short: "Work with the photo gallery",
	}
	cmd.AddCommand(newGalleryUploadCommand(), newGalleryDownloadCommand())
	return cmd
}

// newGalleryUploadCommand defines and runs the upload command (folder and Google
// Photos modes).
func newGalleryUploadCommand() *cobra.Command {
	var opts uploadOptions

	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload images from a folder or a Google Photos export",
		Long: "Upload images to the gallery, end-to-end encrypted on this machine\n" +
			"before they leave it.\n\n" +
			"Folder mode:\n" +
			"  ledgerline-cli gallery upload -f /path/to/folder [-r]\n\n" +
			"Google Photos (Takeout) mode:\n" +
			"  ledgerline-cli gallery upload --google-photos -z /path/to/takeout.zip\n\n" +
			"A same-named video next to a photo is uploaded as its Live Photo motion\n" +
			"clip. Duplicate files (already in the gallery) are skipped. Pass --ml to\n" +
			"run face detection and semantic-search embeddings inline (needs the\n" +
			"server's ML service); otherwise the web client analyses them later.\n\n" +
			"With --delete, a local file is removed ONLY after its upload is saved and\n" +
			"the stored copy has been re-downloaded, decrypted and verified byte-for-\n" +
			"byte against the local file.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateUploadFlags(opts); err != nil {
				return err
			}
			return runUpload(cmd, opts)
		},
	}

	f := cmd.Flags()
	f.StringVarP(&opts.folder, "folder", "f", "", "source folder to upload images from")
	f.BoolVarP(&opts.recursive, "recursive", "r", false, "include images in subfolders")
	f.BoolVar(&opts.googlePhoto, "google-photos", false, "import from a Google Photos (Takeout) export")
	f.StringVarP(&opts.zipPath, "zip", "z", "", "path to the Google Photos export .zip")
	f.BoolVar(&opts.process, "process", false, "derive thumbnails/EXIF on the server (transient plaintext egress); off = partial records, no plaintext leaves your machine")
	f.BoolVar(&opts.withML, "ml", false, "run face detection + search embeddings on the server (needs the server ML service; implies --process)")
	f.StringVar(&opts.mlLocalURL, "ml-local", "", "run ML on a local immich-machine-learning instance at this URL instead of the server")
	f.StringVar(&opts.mlClipModel, "ml-clip-model", defaultClipModel, "CLIP model name for --ml-local (must match the server's Smart Search model)")
	f.StringVar(&opts.mlFaceModel, "ml-face-model", defaultFaceModel, "face model name for --ml-local (must match the server's Facial Recognition model)")
	f.Float64Var(&opts.mlMinScore, "ml-min-score", defaultMinScore, "minimum face-detection score for --ml-local")
	f.BoolVarP(&opts.deleteLocal, "delete", "d", false, "delete each local file after its upload is saved and verified")
	f.IntVar(&opts.batch, "batch", defaultBatch, "save (and, with --delete, delete verified files) after this many uploads")
	f.IntVarP(&opts.jobs, "jobs", "j", defaultJobs, "number of items to upload in parallel")
	return cmd
}

// uploadOptions carries the resolved upload flags.
type uploadOptions struct {
	folder      string
	recursive   bool
	googlePhoto bool
	zipPath     string
	process     bool
	withML      bool
	mlLocalURL  string
	mlClipModel string
	mlFaceModel string
	mlMinScore  float64
	deleteLocal bool
	batch       int
	jobs        int
}

// runUpload authenticates, unlocks the vault, collects the items and runs the
// pipeline, saving progress periodically and (with --delete) removing verified
// local files after each save.
func runUpload(cmd *cobra.Command, opts uploadOptions) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	client, err := authedClient(ctx)
	if err != nil {
		return err
	}

	vk, err := unlockVault(cmd, client)
	if err != nil {
		return err
	}

	items, cleanup, err := collectItems(opts)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(out, "No supported images or videos found.")
		return nil
	}

	store := gallery.NewStore(client, vk)
	store.SetShardCache(shardCache("gallery-shards"))
	fmt.Fprintln(out, "Loading gallery…")
	if err := store.Load(ctx); err != nil {
		return err
	}
	warnIfDegraded(out, "gallery", store)

	analyzer, err := buildAnalyzer(opts)
	if err != nil {
		return err
	}
	uploader := gallery.NewUploader(client, store, vk, opts.process, opts.withML, analyzer)
	uploader.SetClipModel(opts.mlClipModel)
	jobs := opts.jobs
	if jobs < 1 {
		jobs = defaultJobs
	}
	fmt.Fprintf(out, "Uploading %d item(s)%s, %d in parallel…\n", len(items), mlNote(opts), jobs)

	if reportSync(ctx, client, "syncing", "gallery") {
		return wipedError()
	}
	defer reportSync(context.WithoutCancel(ctx), client, "idle", "")

	batch := opts.batch
	if batch < 1 {
		batch = defaultBatch
	}
	run := &uploadRun{out: out, opts: opts, ctx: ctx, store: store, uploader: uploader}
	// Process the work in batches; within each batch, upload up to `jobs` items
	// concurrently, then flush (save + optional delete) at the batch barrier so a
	// manifest save never races an in-flight upload.
	for start := 0; start < len(items); start += batch {
		if ctx.Err() != nil {
			break
		}
		end := start + batch
		if end > len(items) {
			end = len(items)
		}
		run.processBatch(items[start:end], len(items), jobs)
		if err := run.flush(); err != nil {
			return err
		}
		if reportSync(ctx, client, "syncing", "gallery") {
			return wipedError()
		}
	}

	// Pair Live Photo halves (iCloud exports split them across unrelated names)
	// before the final save.
	if merged := store.MergeLivePhotos(); merged > 0 {
		fmt.Fprintf(out, "Merged %d Live Photo motion clip(s).\n", merged)
	}
	if err := run.flush(); err != nil {
		return err
	}

	fmt.Fprintf(out, "Done: %d uploaded, %d duplicate, %d failed.%s\n",
		run.uploaded, run.duplicate, run.failed, deletedNote(opts.deleteLocal, run.deleted))
	return nil
}

// uploadRun holds the mutable progress of one upload command.
type uploadRun struct {
	out      io.Writer
	opts     uploadOptions
	ctx      context.Context
	store    *gallery.Store
	uploader *gallery.Uploader

	// mu guards the counters, progress output and pendingDelete list, which the
	// parallel upload workers update concurrently.
	mu                                         sync.Mutex
	done, uploaded, duplicate, failed, deleted int
	pendingDelete                              []string // verified files to remove on the next flush
}

// processBatch uploads a batch of items using up to jobs concurrent workers and
// returns once all of them finish (the barrier before a flush).
func (r *uploadRun) processBatch(batch []gallery.Item, total, jobs int) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, jobs)
	for _, item := range batch {
		if r.ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(it gallery.Item) {
			defer wg.Done()
			defer func() { <-sem }()
			r.one(total, it)
		}(item)
	}
	wg.Wait()
}

// one processes a single item: upload, and (with --delete) verify + stage for
// deletion after the next save. It is safe to call from several workers at once;
// the slow network steps run unlocked and only the shared counters, output and
// delete list are guarded.
func (r *uploadRun) one(total int, item gallery.Item) {
	label := shortPath(item.StillPath)

	plain, rerr := os.ReadFile(item.StillPath)
	if rerr != nil {
		r.record(total, label, "skipped: "+rerr.Error(), &r.failed)
		return
	}

	outcome, rec, uerr := r.uploader.Upload(r.ctx, item, plain)
	switch {
	case uerr != nil && (errors.Is(uerr, context.Canceled) || r.ctx.Err() != nil):
		// Interrupted (Ctrl-C): not a real failure. Staged progress is still
		// saved on the way out, and a re-run skips what already uploaded.
		r.record(total, label, "interrupted", nil)
		return
	case uerr != nil:
		r.record(total, label, "failed: "+uerr.Error(), &r.failed)
		return
	case outcome == gallery.Duplicate:
		// Already safely in the gallery — eligible for local cleanup.
		r.record(total, label, "duplicate, skipped", &r.duplicate)
		r.stageDelete(item)
		return
	default:
		r.record(total, label, "uploaded", &r.uploaded)
		if rec != nil && rec.MotionWarning() != nil {
			r.mu.Lock()
			fmt.Fprintf(r.out, "        %v\n", rec.MotionWarning())
			r.mu.Unlock()
		}
	}

	if r.opts.deleteLocal {
		if err := r.verifyForDelete(item, rec, plain); err != nil {
			r.mu.Lock()
			fmt.Fprintf(r.out, "        keeping local file — %v\n", err)
			r.mu.Unlock()
			return
		}
		r.stageDelete(item)
	}
}

// record prints one progress line and, when counter is non-nil, increments it,
// holding the lock so counters and output stay consistent across workers. Lines
// are numbered by a monotonic completion counter, since parallel workers finish
// out of input order.
func (r *uploadRun) record(total int, label, msg string, counter *int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done++
	if counter != nil {
		*counter++
	}
	fmt.Fprintf(r.out, "  [%d/%d] %s — %s\n", r.done, total, label, msg)
}

// verifyForDelete confirms the original (and any motion clip) round-trip from the
// server before the local files are eligible for deletion.
func (r *uploadRun) verifyForDelete(item gallery.Item, rec *gallery.PhotoRecord, plain []byte) error {
	if err := r.uploader.VerifyOriginal(r.ctx, rec, plain); err != nil {
		return err
	}
	if item.MotionPath != "" && rec.MotionRef != "" {
		motion, err := os.ReadFile(item.MotionPath)
		if err != nil {
			return fmt.Errorf("read motion clip: %w", err)
		}
		if err := r.uploader.VerifyBlob(r.ctx, rec.MotionRef, rec.MotionKey, motion); err != nil {
			return err
		}
	}
	return nil
}

// stageDelete records an item's files to remove after the next successful save.
func (r *uploadRun) stageDelete(item gallery.Item) {
	if !r.opts.deleteLocal {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingDelete = append(r.pendingDelete, item.StillPath)
	if item.MotionPath != "" {
		r.pendingDelete = append(r.pendingDelete, item.MotionPath)
	}
}

// flush saves the manifest and then removes any staged local files (deletion
// happens only after the records are durably saved).
func (r *uploadRun) flush() error {
	w := r.out
	if r.store.PendingCount() > 0 {
		fmt.Fprintln(w, "Saving gallery…")
		if err := r.store.Save(context.WithoutCancel(r.ctx)); err != nil {
			return fmt.Errorf("save gallery: %w", err)
		}
	}

	for _, path := range r.pendingDelete {
		if err := os.Remove(path); err != nil {
			fmt.Fprintf(w, "  could not delete %s: %v\n", shortPath(path), err)
			continue
		}
		r.deleted++
	}
	r.pendingDelete = nil
	return nil
}

// authedClient loads the session and returns a bearer client, failing clearly
// when unauthenticated or the token has expired.
func authedClient(ctx context.Context) (*api.Client, error) {
	sess, err := session.Load()
	if errors.Is(err, session.ErrNotAuthenticated) {
		return nil, errors.New("not authenticated; run 'ledgerline-cli auth login' first")
	}
	if err != nil {
		return nil, err
	}
	client, err := newAPIClient(sess.ServerURL, api.WithToken(sess.Token))
	if err != nil {
		return nil, err
	}
	_, _, wipe, err := client.Me(ctx)
	if err != nil {
		if api.Status(err) == 401 {
			// The device was revoked from the web or the token expired. Wipe the
			// local credential AND any cached vault key so nothing stale lingers.
			_ = session.Clear()
			return nil, errors.New("this device was revoked or the session expired; local credential and cached key cleared — run 'ledgerline-cli auth login'")
		}
		return nil, err
	}
	if wipe {
		// Remote kill switch: the owner asked to wipe this client. Erase all local
		// state and stop.
		_ = session.WipeLocal()
		return nil, errors.New("this client was wiped remotely from the web; all local data was erased")
	}
	return client, nil
}

// reportSync sends a best-effort heartbeat (so the web shows sync activity) and
// returns whether a remote wipe is now pending. Errors are ignored — a heartbeat
// must never break an operation.
func reportSync(ctx context.Context, client *api.Client, state, detail string) (wipe bool) {
	w, err := client.Heartbeat(ctx, state, detail)
	if err != nil {
		return false
	}
	return w
}

// wipedError erases all local state (remote kill switch) and returns the error to
// stop the current command.
func wipedError() error {
	_ = session.WipeLocal()
	return errors.New("this client was wiped remotely from the web; all local data was erased")
}

// unlockVault yields the vault key: it uses a valid cached key (no prompt) if one
// exists, otherwise prompts for the passphrase.
func unlockVault(cmd *cobra.Command, client *api.Client) ([]byte, error) {
	if vk, _, err := session.LoadVaultKey(); err == nil {
		return vk, nil
	}
	return unlockVaultPrompt(cmd, client)
}

// unlockVaultPrompt always prompts for the passphrase (never echoed) and derives
// the vault key, ignoring any cache.
func unlockVaultPrompt(cmd *cobra.Command, client *api.Client) ([]byte, error) {
	out := cmd.OutOrStdout()
	fmt.Fprint(out, "Vault passphrase: ")
	pass, err := readPassword(cmd.InOrStdin())
	fmt.Fprintln(out)
	if err != nil {
		return nil, err
	}
	if pass == "" {
		return nil, errors.New("no passphrase entered")
	}

	vk, err := vault.Unlock(cmd.Context(), client, pass)
	if err != nil {
		if errors.Is(err, vault.ErrNotConfigured) {
			return nil, errors.New("this account has no vault yet; set one up in the web app first")
		}
		return nil, err
	}
	return vk, nil
}

// readPassword reads a passphrase without echo from a terminal, and falls back
// to a plain line read for pipes and tests.
func readPassword(in any) (string, error) {
	if f, ok := in.(*os.File); ok {
		if fd := int(f.Fd()); term.IsTerminal(fd) {
			b, err := term.ReadPassword(fd)
			return string(b), err
		}
	}
	reader, ok := in.(interface{ Read([]byte) (int, error) })
	if !ok {
		return "", errors.New("cannot read passphrase")
	}
	line, err := bufio.NewReader(reader).ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

// collectItems builds the upload work list for the chosen mode.
func collectItems(opts uploadOptions) ([]gallery.Item, func(), error) {
	if opts.googlePhoto {
		tmp, err := os.MkdirTemp("", "ledgerline-takeout-")
		if err != nil {
			return nil, nil, err
		}
		cleanup := func() { _ = os.RemoveAll(tmp) }
		items, err := gallery.CollectGooglePhotos(opts.zipPath, tmp)
		return items, cleanup, err
	}
	items, err := gallery.CollectFolder(opts.folder, opts.recursive)
	return items, nil, err
}

// validateUploadFlags enforces that exactly one coherent source mode is selected
// and that the ML options are consistent.
func validateUploadFlags(opts uploadOptions) error {
	switch {
	case opts.googlePhoto:
		if opts.zipPath == "" {
			return errors.New("--google-photos requires --zip/-z pointing to the export .zip")
		}
		if opts.folder != "" {
			return errors.New("--folder cannot be combined with --google-photos")
		}
	case opts.folder != "":
		if opts.zipPath != "" {
			return errors.New("--zip is only valid with --google-photos")
		}
	default:
		return errors.New("choose a source: --folder/-f, or --google-photos with --zip/-z")
	}

	if opts.withML && opts.mlLocalURL != "" {
		return errors.New("choose one ML mode: --ml (server) or --ml-local (local instance), not both")
	}
	if opts.jobs < 1 {
		return errors.New("--jobs must be at least 1")
	}
	return nil
}

// mlNote annotates the run header with the ML mode.
func mlNote(opts uploadOptions) string {
	switch {
	case opts.mlLocalURL != "":
		return " (face detection + embeddings on a local ML instance)"
	case opts.withML:
		return " (server face detection + embeddings)"
	default:
		return ""
	}
}

// buildAnalyzer returns a local ML analyzer when --ml-local is set, or nil for
// the server-ML or no-ML modes.
func buildAnalyzer(opts uploadOptions) (ml.Analyzer, error) {
	if opts.mlLocalURL == "" {
		return nil, nil
	}
	return ml.NewImmich(opts.mlLocalURL, opts.mlClipModel, opts.mlFaceModel, opts.mlMinScore)
}

// deletedNote appends a deletion count when --delete was used.
func deletedNote(deleteLocal bool, deleted int) string {
	if !deleteLocal {
		return ""
	}
	return fmt.Sprintf(" %d local file(s) deleted.", deleted)
}

// shortPath trims a path to its parent/name for compact progress output.
func shortPath(p string) string {
	parent := filepath.Base(filepath.Dir(p))
	if parent == "." || parent == string(filepath.Separator) {
		return filepath.Base(p)
	}
	return parent + "/" + filepath.Base(p)
}
