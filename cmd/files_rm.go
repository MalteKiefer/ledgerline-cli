package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/files"
)

// newFilesRmCommand deletes a file or folder from the encrypted store.
func newFilesRmCommand() *cobra.Command {
	var recursive, force bool

	cmd := &cobra.Command{
		Use:   "rm <path>",
		Short: "Delete a file or folder (trash by default; --force to erase)",
		Long: "Delete a file or folder.\n\n" +
			"By default items go to the trash and can be restored in the web app.\n" +
			"--force deletes permanently and reclaims the stored blobs (irreversible).\n" +
			"A folder needs --recursive.\n\n" +
			"  ledgerline-cli files rm Docs/old.pdf\n" +
			"  ledgerline-cli files rm Docs/old.pdf --force\n" +
			"  ledgerline-cli files rm Archive --recursive --force",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFilesRm(cmd, args[0], recursive, force)
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "delete a folder and everything under it")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "delete permanently (reclaim blobs) instead of trashing")
	return cmd
}

// runFilesRm resolves the path to a file or folder and trashes or erases it.
func runFilesRm(cmd *cobra.Command, path string, recursive, force bool) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	if path == "" {
		return errors.New("specify a file or folder to delete")
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
	if err := store.Load(ctx); err != nil {
		return err
	}

	// Single file.
	if fv, ok := files.FindFile(store, path); ok {
		return rmFiles(cmd, w, client, store, []files.FileView{fv}, nil, force, "file")
	}

	// Folder subtree.
	if !files.IsFolder(store, path) {
		return fmt.Errorf("no such file or folder: %s", path)
	}
	if !recursive {
		return fmt.Errorf("%q is a folder; pass --recursive to delete it and its contents", path)
	}
	subFiles, folderIDs, err := files.Subtree(store, path)
	if err != nil {
		return err
	}
	return rmFiles(cmd, w, client, store, subFiles, folderIDs, force, "folder")
}

// rmFiles trashes or permanently deletes a set of files (and folder ids), saving
// the manifest and then reclaiming blobs on a permanent delete.
func rmFiles(cmd *cobra.Command, w io.Writer, client *api.Client, store *files.Store, fvs []files.FileView, folderIDs []string, force bool, kind string) error {
	ctx := cmd.Context()

	var blobs []string
	for _, fv := range fvs {
		if force {
			blobs = append(blobs, store.FileBlobs(fv.ID)...)
			store.DeleteFile(fv.ID)
		} else {
			store.TrashFile(fv.ID, nowISO())
		}
	}
	// Folder records are removed in both cases (a trashed folder's files detach,
	// matching the web); permanent delete drops them too.
	for _, id := range folderIDs {
		store.DeleteFolder(id)
	}

	if !store.Dirty() {
		fmt.Fprintln(w, "Nothing to delete.")
		return nil
	}
	if force {
		fmt.Fprintf(w, "Permanently deleting %d file(s)…\n", len(fvs))
	} else {
		fmt.Fprintf(w, "Moving %d file(s) to the trash…\n", len(fvs))
	}
	if err := store.Save(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("save files: %w", err)
	}

	// Reclaim blobs only after the records are durably gone.
	var reclaimed, failed int
	for _, b := range blobs {
		if err := client.DeleteFileBlob(context.WithoutCancel(ctx), b); err != nil {
			failed++
			continue
		}
		reclaimed++
	}

	if force {
		msg := fmt.Sprintf("Deleted. %d blob(s) reclaimed", reclaimed)
		if failed > 0 {
			msg += fmt.Sprintf(", %d left for reconcile", failed)
		}
		fmt.Fprintln(w, msg+".")
	} else {
		fmt.Fprintln(w, "Moved to trash (restore in the web app).")
	}
	return nil
}
