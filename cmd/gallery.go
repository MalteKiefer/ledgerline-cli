package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
	"github.com/MalteKiefer/ledgerline-cli/internal/gallery"
	"github.com/MalteKiefer/ledgerline-cli/internal/ui"
	"github.com/MalteKiefer/ledgerline-cli/internal/uploadledger"
)

// newGalleryCommand builds the `gallery` group.
func newGalleryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gallery",
		Short: "Upload, list, download and remove gallery photos and videos",
	}
	cmd.AddCommand(
		newGalleryUploadCommand(),
		newGalleryListCommand(),
		newGalleryDownloadCommand(),
		newGalleryRmCommand(),
	)
	return cmd
}

// newGalleryUploadCommand uploads photos/videos (files or whole directories),
// skipping files already uploaded (content dedup by sha256) so nothing is sent
// twice.
func newGalleryUploadCommand() *cobra.Command {
	var jobs, batch int
	var force bool
	cmd := &cobra.Command{
		Use:   "upload <path...>",
		Short: "Upload photos/videos (files or directories, recursively)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobs < 1 {
				jobs = 1
			}
			files, err := gallery.WalkMedia(args)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				return fmt.Errorf("no image or video files found in the given paths")
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			ledger, err := uploadLedger("gallery", client.BaseURL())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			bar := ui.NewProgressBar(out, len(files), ui.IsTTY(out))

			var mu sync.Mutex
			var uploaded, dup, skipped, failed, done int
			sem := make(chan struct{}, jobs)
			var wg sync.WaitGroup
			for _, f := range files {
				wg.Add(1)
				sem <- struct{}{}
				go func(path string) {
					defer wg.Done()
					defer func() { <-sem }()
					name := gallery.GuessName(path)

					// Content dedup: skip a file whose bytes were already uploaded,
					// so a re-run of the same folder sends nothing.
					sha, herr := uploadledger.HashFile(path)
					if herr == nil && !force && ledger.Has(sha) {
						mu.Lock()
						skipped++
						bar.Println(fmt.Sprintf("skip     %s (already uploaded)", name))
						done++
						bar.Update(done, name)
						mu.Unlock()
						return
					}

					open := func() (io.ReadCloser, error) { return os.Open(path) }
					_, isDup, uerr := client.UploadPhoto(cmd.Context(), name, open)
					mu.Lock()
					defer mu.Unlock()
					switch {
					case uerr != nil:
						failed++
						bar.Println(fmt.Sprintf("failed   %s: %v", name, uerr))
					case isDup:
						dup++
						bar.Println(fmt.Sprintf("duplicate %s", name))
					default:
						uploaded++
						bar.Println(fmt.Sprintf("uploaded %s", name))
					}
					if uerr == nil && herr == nil {
						ledger.Add(sha)
						_ = ledger.MaybeCheckpoint(batch)
					}
					done++
					bar.Update(done, name)
				}(f)
			}
			wg.Wait()
			bar.Finish()
			if serr := ledger.Save(); serr != nil {
				fmt.Fprintf(out, "warning: could not save upload ledger: %v\n", serr)
			}

			auditLog().Log(audit.Event{
				Event: "gallery.upload", Outcome: audit.OutcomeOK,
				Count:  uploaded + dup,
				Detail: fmt.Sprintf("%d new, %d duplicate, %d skipped, %d failed", uploaded, dup, skipped, failed),
			})
			fmt.Fprintf(out, "Done: %d uploaded, %d duplicate, %d skipped, %d failed.\n", uploaded, dup, skipped, failed)
			if failed > 0 {
				return fmt.Errorf("%d file(s) failed to upload", failed)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&jobs, "jobs", 4, "number of concurrent uploads")
	cmd.Flags().IntVar(&batch, "batch", 50, "checkpoint the dedup ledger every N uploads (0 disables)")
	cmd.Flags().BoolVar(&force, "force", false, "upload even files already recorded as uploaded")
	return cmd
}

// newGalleryListCommand prints the photo list.
func newGalleryListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all photos and videos",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			photos, err := client.ListPhotos(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(photos) == 0 {
				fmt.Fprintln(out, "No photos.")
				return nil
			}
			fmt.Fprintf(out, "%-8s  %-6s  %-10s  %-19s  %s\n", "ID", "TYPE", "SIZE", "TAKEN", "NAME")
			for _, p := range photos {
				fmt.Fprintf(out, "%-8d  %-6s  %-10s  %-19s  %s\n",
					p.ID, p.MediaType, humanBytes(p.Size), whenTaken(p), p.Name)
			}
			return nil
		},
	}
}

// whenTaken renders the capture date (falling back to created_at) for the list.
func whenTaken(p api.GalleryPhoto) string {
	if p.TakenAt != nil && *p.TakenAt != "" {
		return *p.TakenAt
	}
	if p.CreatedAt != nil {
		return *p.CreatedAt
	}
	return ""
}

// newGalleryDownloadCommand downloads originals by id.
func newGalleryDownloadCommand() *cobra.Command {
	var outDir, variant string
	cmd := &cobra.Command{
		Use:   "download <id...>",
		Short: "Download photo/video originals by id",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			if outDir == "" {
				outDir = "."
			}
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return err
			}
			// Resolve names once so downloads land under their real file names.
			names := map[int64]string{}
			if photos, lerr := client.ListPhotos(cmd.Context()); lerr == nil {
				for _, p := range photos {
					names[p.ID] = p.Name
				}
			}
			out := cmd.OutOrStdout()
			for _, id := range ids {
				name := names[id]
				if name == "" {
					name = strconv.FormatInt(id, 10)
				}
				dest := filepath.Join(outDir, name)
				if derr := downloadPhotoTo(cmd.Context(), client, id, variant, dest); derr != nil {
					return fmt.Errorf("download %d: %w", id, derr)
				}
				fmt.Fprintf(out, "downloaded %s\n", dest)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out", ".", "directory to write downloads into")
	cmd.Flags().StringVar(&variant, "variant", "original", "which bytes to fetch: original or edited")
	return cmd
}

// downloadPhotoTo streams one photo to a destination file.
func downloadPhotoTo(ctx context.Context, client *api.Client, id int64, variant, dest string) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	return client.DownloadPhoto(ctx, id, variant, f)
}

// newGalleryRmCommand trashes photos by id.
func newGalleryRmCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id...>",
		Short: "Move photos to the trash by id",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			if len(ids) == 1 {
				err = client.DeletePhoto(cmd.Context(), ids[0])
			} else {
				err = client.BulkDeletePhotos(cmd.Context(), ids)
			}
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "gallery.rm", Outcome: audit.OutcomeOK, Count: len(ids)})
			fmt.Fprintf(cmd.OutOrStdout(), "Trashed %d photo(s).\n", len(ids))
			return nil
		},
	}
}

// parseIDs turns positional string ids into int64s.
func parseIDs(args []string) ([]int64, error) {
	ids := make([]int64, 0, len(args))
	for _, a := range args {
		id, err := strconv.ParseInt(a, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid id %q", a)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
