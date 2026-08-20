package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
)

// newFilesSharedCommand builds `files shared`: the receiving side of an internal
// share — folders/files another account granted this one. A viewer can browse and
// download; an editor can also upload, rename and delete inside the share.
func newFilesSharedCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shared",
		Short: "Browse and use folders shared with this account by others",
	}
	cmd.AddCommand(
		newFilesSharedLsCommand(),
		newFilesSharedBrowseCommand(),
		newFilesSharedDownloadCommand(),
		newFilesSharedUploadCommand(),
		newFilesSharedRenameCommand(),
		newFilesSharedRmCommand(),
	)
	return cmd
}

func newFilesSharedLsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List shares other accounts granted to this one",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			items, err := client.SharedWithMe(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(items) == 0 {
				fmt.Fprintln(out, "Nothing shared with this account.")
				return nil
			}
			for _, it := range items {
				name := it.FolderName
				if it.Kind == "file" {
					name = it.FileName
				}
				owner := "unknown"
				switch {
				case it.Owner.Email != nil:
					owner = *it.Owner.Email
				case it.Owner.Name != nil:
					owner = *it.Owner.Name
				}
				fmt.Fprintf(out, "%d  %-6s  %-6s  %s  (from %s)\n", it.ID, it.Kind, it.Role, name, owner)
			}
			return nil
		},
	}
}

func newFilesSharedBrowseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "browse <share-id>",
		Short: "List the contents of a share",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			res, err := client.SharedWithMeBrowse(cmd.Context(), ids[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "share %d  kind=%s  role=%s\n", res.ShareID, res.Kind, res.Role)
			if res.File != nil {
				fmt.Fprintf(out, "%d  %s  %s\n", res.File.ID, humanBytes(res.File.Size), res.File.Name)
			}
			for _, f := range res.Folders {
				fmt.Fprintf(out, "%d  <dir>  %s\n", f.ID, f.Name)
			}
			for _, f := range res.Files {
				fmt.Fprintf(out, "%d  %s  %s\n", f.ID, humanBytes(f.Size), f.Name)
			}
			if res.File == nil && len(res.Folders) == 0 && len(res.Files) == 0 {
				fmt.Fprintln(out, "(empty)")
			}
			return nil
		},
	}
}

func newFilesSharedDownloadCommand() *cobra.Command {
	var outDir string
	cmd := &cobra.Command{
		Use:   "download <share-id> <file-id...>",
		Short: "Download files out of a share",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			shareID, fileIDs := ids[0], ids[1:]
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			if outDir == "" {
				outDir = "."
			}
			if err := os.MkdirAll(outDir, 0o750); err != nil {
				return err
			}
			// Resolve display names so downloads land under their real filename;
			// best-effort, the id is the fallback.
			names := map[int64]string{}
			if res, berr := client.SharedWithMeBrowse(cmd.Context(), shareID); berr == nil {
				if res.File != nil {
					names[res.File.ID] = res.File.Name
				}
				for _, f := range res.Files {
					names[f.ID] = f.Name
				}
			}
			out := cmd.OutOrStdout()
			for _, id := range fileIDs {
				name := names[id]
				if name == "" {
					name = strconv.FormatInt(id, 10)
				}
				dest := filepath.Join(outDir, name)
				f, cerr := os.Create(dest)
				if cerr != nil {
					return cerr
				}
				derr := client.SharedWithMeDownload(cmd.Context(), shareID, id, f)
				closeErr := f.Close()
				if derr != nil {
					return fmt.Errorf("download %d: %w", id, derr)
				}
				if closeErr != nil {
					return closeErr
				}
				fmt.Fprintf(out, "downloaded %s\n", dest)
			}
			auditLog().Log(audit.Event{Event: "files.shared.download", Outcome: audit.OutcomeOK, Count: len(fileIDs)})
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out", ".", "directory to write downloads into")
	return cmd
}

func newFilesSharedUploadCommand() *cobra.Command {
	var folder int64
	cmd := &cobra.Command{
		Use:   "upload <share-id> <file...>",
		Short: "Upload into a share (editor role required)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args[:1])
			if err != nil {
				return err
			}
			shareID := ids[0]
			var folderPtr *int64
			if cmd.Flags().Changed("folder") {
				folderPtr = &folder
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var ok, failed int
			for _, path := range args[1:] {
				info, serr := os.Stat(path)
				if serr != nil || info.IsDir() {
					failed++
					fmt.Fprintf(out, "skip     %s (not a regular file)\n", path)
					continue
				}
				name := filepath.Base(path)
				open := func() (io.ReadCloser, error) { return os.Open(path) }
				if _, uerr := client.SharedWithMeUpload(cmd.Context(), shareID, name, folderPtr, open); uerr != nil {
					failed++
					fmt.Fprintf(out, "failed   %s: %v\n", name, uerr)
					continue
				}
				ok++
				fmt.Fprintf(out, "uploaded %s\n", name)
			}
			auditLog().Log(audit.Event{
				Event: "files.shared.upload", Outcome: audit.OutcomeOK, Count: ok,
				Detail: fmt.Sprintf("%d uploaded, %d failed", ok, failed),
			})
			fmt.Fprintf(out, "Done: %d uploaded, %d failed.\n", ok, failed)
			if failed > 0 {
				return fmt.Errorf("%d file(s) failed to upload", failed)
			}
			return nil
		},
	}
	cmd.Flags().Int64Var(&folder, "folder", 0, "destination folder id inside the share (default: its root)")
	return cmd
}

func newFilesSharedRenameCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <share-id> <file-id> <new-name>",
		Short: "Rename a file inside a share (editor role required)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args[:2])
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			f, err := client.SharedWithMeRename(cmd.Context(), ids[0], ids[1], args[2])
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.shared.rename", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Renamed to %q.\n", f.Name)
			return nil
		},
	}
}

func newFilesSharedRmCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <share-id> <file-id...>",
		Short: "Delete files inside a share (editor role required)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			shareID, fileIDs := ids[0], ids[1:]
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			for _, id := range fileIDs {
				if derr := client.SharedWithMeDelete(cmd.Context(), shareID, id); derr != nil {
					return fmt.Errorf("delete %d: %w", id, derr)
				}
			}
			auditLog().Log(audit.Event{Event: "files.shared.rm", Outcome: audit.OutcomeOK, Count: len(fileIDs)})
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %d file(s) from share %d.\n", len(fileIDs), shareID)
			return nil
		},
	}
}
