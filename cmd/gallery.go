package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/session"
)

// newGalleryCommand builds the `gallery` group. Upload itself is delivered in a
// later phase; the command surface and input validation are defined here so the
// interface is stable from the first release.
func newGalleryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gallery",
		Short: "Work with the photo gallery",
	}
	cmd.AddCommand(newGalleryUploadCommand())
	return cmd
}

// newGalleryUploadCommand defines the upload surface (folder and Google Photos
// modes) and validates flags. Actual uploading arrives in Phase 2.
func newGalleryUploadCommand() *cobra.Command {
	var (
		folder      string
		recursive   bool
		googlePhoto bool
		zipPath     string
	)

	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload images from a folder or a Google Photos export",
		Long: "Upload images to the gallery.\n\n" +
			"Folder mode:\n" +
			"  ledgerline-cli gallery upload -f /path/to/folder [-r]\n\n" +
			"Google Photos (Takeout) mode:\n" +
			"  ledgerline-cli gallery upload --google-photos -z /path/to/takeout.zip\n\n" +
			"Uploads are end-to-end encrypted on this machine before leaving it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateUploadFlags(folder, zipPath, googlePhoto); err != nil {
				return err
			}
			if _, err := session.Load(); errors.Is(err, session.ErrNotAuthenticated) {
				return errors.New("not authenticated; run 'ledgerline-cli auth login' first")
			} else if err != nil {
				return err
			}
			// Phase 2 wires the zero-knowledge upload pipeline (client-side
			// encryption, /gallery/process, sealed manifest update) here.
			return fmt.Errorf("gallery upload is not available in this release yet")
		},
	}

	cmd.Flags().StringVarP(&folder, "folder", "f", "", "source folder to upload images from")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "include images in subfolders")
	cmd.Flags().BoolVar(&googlePhoto, "google-photos", false, "import from a Google Photos (Takeout) export")
	cmd.Flags().StringVarP(&zipPath, "zip", "z", "", "path to the Google Photos export .zip")
	return cmd
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
