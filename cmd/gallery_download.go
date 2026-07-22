package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/gallery"
)

// downloadOptions carries the resolved download flags.
type downloadOptions struct {
	outDir string
	from   string
	to     string
	images bool
	videos bool
	edited bool
	force  bool
}

// newGalleryDownloadCommand defines and runs the download command.
func newGalleryDownloadCommand() *cobra.Command {
	var opts downloadOptions

	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download and decrypt the gallery to a local folder",
		Long: "Download photos and videos from the gallery, decrypting each on this\n" +
			"machine, into an output folder.\n\n" +
			"  ledgerline-cli gallery download -o /path/to/folder\n\n" +
			"Restrict what is downloaded:\n" +
			"  --from / --to    only photos taken in a date range (YYYY-MM-DD, inclusive)\n" +
			"  --images         only images\n" +
			"  --videos         only videos (pass both, or neither, for everything)\n\n" +
			"  --edited         write edited date/location into each file's metadata\n" +
			"                   and export Live Photo motion (needs exiftool)\n\n" +
			"Files already present in the target are skipped; pass --force to overwrite.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDownload(cmd, opts)
		},
	}

	f := cmd.Flags()
	f.StringVarP(&opts.outDir, "output", "o", "", "destination folder (required)")
	f.StringVar(&opts.from, "from", "", "only photos taken on or after this date (YYYY-MM-DD)")
	f.StringVar(&opts.to, "to", "", "only photos taken on or before this date (YYYY-MM-DD)")
	f.BoolVar(&opts.images, "images", false, "download only images")
	f.BoolVar(&opts.videos, "videos", false, "download only videos")
	f.BoolVar(&opts.edited, "edited", false, "bake edited date/GPS into metadata and export Live Photo motion (requires exiftool)")
	f.BoolVar(&opts.force, "force", false, "overwrite files that already exist in the target")
	return cmd
}

// runDownload authenticates, unlocks the vault, plans the download and writes
// each decrypted original, skipping existing files unless --force.
func runDownload(cmd *cobra.Command, opts downloadOptions) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	if opts.outDir == "" {
		return errors.New("an output folder is required: -o/--output")
	}
	filter, err := buildFilter(opts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(opts.outDir, 0o700); err != nil {
		return err
	}

	editing := opts.edited
	if editing && !gallery.ExiftoolAvailable() {
		fmt.Fprintln(out, "Note: exiftool not found on PATH — exporting originals unchanged (no metadata merge, no motion).")
		editing = false
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
	fmt.Fprintln(out, "Loading gallery…")
	if err := store.Load(ctx); err != nil {
		return err
	}

	targets := gallery.Plan(store.Records(), opts.outDir, filter)
	if len(targets) == 0 {
		fmt.Fprintln(out, "Nothing to download for the given filters.")
		return nil
	}
	fmt.Fprintf(out, "Downloading %d item(s)…\n", len(targets))

	var downloaded, skipped, failed int
	for i, t := range targets {
		if ctx.Err() != nil {
			break
		}
		label := shortPath(t.Path)

		if !opts.force {
			if _, statErr := os.Stat(t.Path); statErr == nil {
				skipped++
				fmt.Fprintf(out, "  [%d/%d] %s — exists, skipped\n", i+1, len(targets), label)
				continue
			}
		}

		if editing {
			motion, warn, derr := downloadOneEdited(ctx, client, vk, t)
			if derr != nil {
				failed++
				fmt.Fprintf(out, "  [%d/%d] %s — failed: %v\n", i+1, len(targets), label, derr)
				continue
			}
			downloaded++
			suffix := ""
			if motion {
				suffix = " (+motion)"
			}
			fmt.Fprintf(out, "  [%d/%d] %s — downloaded%s\n", i+1, len(targets), label, suffix)
			if warn != "" {
				fmt.Fprintf(out, "      - %s\n", warn)
			}
			continue
		}
		if err := downloadOne(ctx, client, vk, t); err != nil {
			failed++
			fmt.Fprintf(out, "  [%d/%d] %s — failed: %v\n", i+1, len(targets), label, err)
			continue
		}
		downloaded++
		fmt.Fprintf(out, "  [%d/%d] %s — downloaded\n", i+1, len(targets), label)
	}

	fmt.Fprintf(out, "Done: %d downloaded, %d skipped, %d failed.\n", downloaded, skipped, failed)
	return nil
}

// downloadOne fetches, decrypts and atomically writes one photo, preserving its
// capture time as the file's modification time when known.
func downloadOne(ctx context.Context, client *api.Client, vk []byte, t gallery.Target) error {
	data, err := gallery.FetchOriginal(ctx, client, vk, t.Rec)
	if err != nil {
		return err
	}
	if err := writeAtomic(t.Path, data); err != nil {
		return err
	}
	if !t.When.IsZero() {
		_ = os.Chtimes(t.Path, t.When, t.When)
	}
	return nil
}

// writeAtomic writes data to a temp file in the same directory and renames it
// into place, so an interrupted download never leaves a truncated file.
func writeAtomic(path string, data []byte) error {
	// Never write decrypted content through an existing symlink at the target.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write through a symlink: %s", path)
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// motionSidecarPath returns the still's path with its extension replaced by
// .mov, so a Live Photo's motion clip sits beside the still with the same stem.
func motionSidecarPath(stillPath string) string {
	ext := filepath.Ext(stillPath)
	return strings.TrimSuffix(stillPath, ext) + ".mov"
}

// writePatchRename writes data to a temp file, patches it in place with
// exiftool, then atomically renames it to path — reusing the symlink guard so an
// existing symlink at the destination is never written through.
func writePatchRename(ctx context.Context, path string, data []byte, e gallery.ExifEdits) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write through a symlink: %s", path)
	}
	// Temp name is safe only because the download loop is sequential; future parallelism must revisit this.
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := gallery.RunExiftool(ctx, tmp, e); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// downloadOneEdited writes the still with its edited date/GPS baked in, and for
// a Live Photo also writes the motion clip beside it with a matching Apple
// ContentIdentifier so the pair re-associates on import. A failure to write the
// motion half is non-fatal: the still is kept and motionWritten is false.
// warn is a short message describing any non-fatal motion export failure.
func downloadOneEdited(ctx context.Context, client *api.Client, vk []byte, t gallery.Target) (bool, string, error) {
	data, err := gallery.FetchOriginal(ctx, client, vk, t.Rec)
	if err != nil {
		return false, "", err
	}

	lat, lng := t.Rec.LatLngFloat()
	edits := gallery.ExifEdits{
		TakenAt: t.When,
		Lat:     lat,
		Lng:     lng,
		Video:   t.Rec.MediaType == "video",
	}

	cid := ""
	live := t.Rec.MotionRef != "" && t.Rec.MotionKey != ""
	if live {
		if got, merr := gallery.FetchMeta(ctx, client, vk, t.Rec); merr == nil && gallery.ValidContentID(got) {
			cid = got
			edits.ContentID = got
		}
	}

	if err := writePatchRename(ctx, t.Path, data, edits); err != nil {
		return false, "", err
	}
	if !t.When.IsZero() {
		_ = os.Chtimes(t.Path, t.When, t.When)
	}

	// Motion sidecar only when we have a still (not a standalone video) and a
	// usable content id to guarantee re-pairing.
	if !live || cid == "" || t.Rec.MediaType == "video" {
		if live && cid == "" {
			return false, "motion export failed: content identifier unavailable", nil
		}
		return false, "", nil
	}
	motionPath := motionSidecarPath(t.Path)
	if !gallery.WithinDir(filepath.Dir(t.Path), motionPath) {
		return false, "motion sidecar path rejected", nil
	}
	motion, merr := gallery.FetchMotion(ctx, client, vk, t.Rec)
	if merr != nil {
		return false, "motion clip fetch failed", nil // non-fatal: still is already written
	}
	if err := writePatchRename(ctx, motionPath, motion, gallery.ExifEdits{ContentID: cid, Video: true}); err != nil {
		return false, "motion export failed: " + err.Error(), nil
	}
	if !t.When.IsZero() {
		_ = os.Chtimes(motionPath, t.When, t.When)
	}
	return true, "", nil
}

// buildFilter resolves the media-type and date flags into a gallery.Filter.
// With neither --images nor --videos, everything is included.
func buildFilter(opts downloadOptions) (gallery.Filter, error) {
	f := gallery.Filter{Images: opts.images, Videos: opts.videos}
	if !opts.images && !opts.videos {
		f.Images, f.Videos = true, true
	}
	if opts.from != "" {
		t, err := time.Parse("2006-01-02", opts.from)
		if err != nil {
			return f, fmt.Errorf("invalid --from date (use YYYY-MM-DD): %w", err)
		}
		f.From = t
	}
	if opts.to != "" {
		t, err := time.Parse("2006-01-02", opts.to)
		if err != nil {
			return f, fmt.Errorf("invalid --to date (use YYYY-MM-DD): %w", err)
		}
		// Make --to inclusive of the whole day.
		f.To = t.Add(24*time.Hour - time.Nanosecond)
	}
	return f, nil
}
