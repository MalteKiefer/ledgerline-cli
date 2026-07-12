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

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/gallery"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
	"github.com/MalteKiefer/ledgerline-cli/internal/vault"
)

// saveEvery bounds how many photos are uploaded before the manifest is flushed,
// so an interrupted run keeps most of its progress.
const saveEvery = 50

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
			if err := validateUploadFlags(opts.folder, opts.zipPath, opts.googlePhoto); err != nil {
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
	f.BoolVar(&opts.withML, "ml", false, "run face detection + search embeddings inline (needs the server ML service)")
	f.BoolVarP(&opts.deleteLocal, "delete", "d", false, "delete each local file after its upload is saved and verified")
	return cmd
}

// uploadOptions carries the resolved upload flags.
type uploadOptions struct {
	folder      string
	recursive   bool
	googlePhoto bool
	zipPath     string
	withML      bool
	deleteLocal bool
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
	fmt.Fprintln(out, "Loading gallery…")
	if err := store.Load(ctx); err != nil {
		return err
	}

	uploader := gallery.NewUploader(client, store, vk, opts.withML)
	fmt.Fprintf(out, "Uploading %d item(s)%s…\n", len(items), mlNote(opts.withML))

	run := &uploadRun{out: out, opts: opts, ctx: ctx, store: store, uploader: uploader}
	for i, item := range items {
		if ctx.Err() != nil {
			break
		}
		run.one(i, len(items), item)
		if run.sinceSave >= saveEvery {
			if err := run.flush(); err != nil {
				return err
			}
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

	uploaded, duplicate, failed, deleted, sinceSave int
	pendingDelete                                   []string // verified files to remove on the next flush
}

// one processes a single item: upload, and (with --delete) verify + stage for
// deletion after the next save.
func (r *uploadRun) one(idx, total int, item gallery.Item) {
	w := r.out
	label := shortPath(item.StillPath)

	plain, rerr := os.ReadFile(item.StillPath)
	if rerr != nil {
		r.failed++
		fmt.Fprintf(w, "  [%d/%d] %s — skipped: %v\n", idx+1, total, label, rerr)
		return
	}

	outcome, rec, uerr := r.uploader.Upload(r.ctx, item, plain)
	switch {
	case uerr != nil:
		r.failed++
		fmt.Fprintf(w, "  [%d/%d] %s — failed: %v\n", idx+1, total, label, uerr)
		return
	case outcome == gallery.Duplicate:
		r.duplicate++
		fmt.Fprintf(w, "  [%d/%d] %s — duplicate, skipped\n", idx+1, total, label)
		// Already safely in the gallery — eligible for local cleanup.
		r.stageDelete(item)
		return
	default:
		r.uploaded++
		r.sinceSave++
		fmt.Fprintf(w, "  [%d/%d] %s — uploaded\n", idx+1, total, label)
	}

	if r.opts.deleteLocal {
		if err := r.verifyForDelete(item, rec, plain); err != nil {
			fmt.Fprintf(w, "        keeping local file — %v\n", err)
			return
		}
		r.stageDelete(item)
	}
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
	r.sinceSave = 0

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
	client, err := api.New(sess.ServerURL, api.WithToken(sess.Token))
	if err != nil {
		return nil, err
	}
	if _, _, err := client.Me(ctx); err != nil {
		if api.Status(err) == 401 {
			return nil, errors.New("session expired; run 'ledgerline-cli auth login' again")
		}
		return nil, err
	}
	return client, nil
}

// unlockVault prompts for the passphrase (never echoed) and derives the vault key.
func unlockVault(cmd *cobra.Command, client *api.Client) ([]byte, error) {
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

// validateUploadFlags enforces that exactly one coherent mode is selected.
func validateUploadFlags(folder, zipPath string, googlePhoto bool) error {
	switch {
	case googlePhoto:
		if zipPath == "" {
			return errors.New("--google-photos requires --zip/-z pointing to the export .zip")
		}
		if folder != "" {
			return errors.New("--folder cannot be combined with --google-photos")
		}
	case folder != "":
		if zipPath != "" {
			return errors.New("--zip is only valid with --google-photos")
		}
	default:
		return errors.New("choose a source: --folder/-f, or --google-photos with --zip/-z")
	}
	return nil
}

// mlNote annotates the run header with the ML mode.
func mlNote(withML bool) string {
	if withML {
		return " (with face detection + embeddings)"
	}
	return ""
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
