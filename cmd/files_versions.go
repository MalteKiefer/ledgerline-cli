package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
)

// newFilesVersionsCommand builds the `files versions` group: a file's archived
// revision history (created by uploading new bytes over an existing file).
func newFilesVersionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "versions",
		Short: "List, download or restore a file's version history",
	}
	cmd.AddCommand(
		newFilesVersionsLsCommand(),
		newFilesVersionsDownloadCommand(),
		newFilesVersionsRestoreCommand(),
	)
	return cmd
}

func newFilesVersionsLsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls <file-id>",
		Short: "List a file's archived versions",
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
			versions, err := client.FileVersions(cmd.Context(), ids[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(versions) == 0 {
				fmt.Fprintln(out, "No archived versions.")
				return nil
			}
			for _, v := range versions {
				fmt.Fprintf(out, "%d  %s  %s\n", v.ID, humanBytes(v.Size), derefStr(v.CreatedAt))
			}
			return nil
		},
	}
}

func newFilesVersionsDownloadCommand() *cobra.Command {
	var outDir string
	cmd := &cobra.Command{
		Use:   "download <file-id> <version-id>",
		Short: "Download one archived version's bytes",
		Args:  cobra.ExactArgs(2),
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
			if err := os.MkdirAll(outDir, 0o750); err != nil {
				return err
			}
			dest := filepath.Join(outDir, "v"+strconv.FormatInt(ids[1], 10)+"-"+strconv.FormatInt(ids[0], 10))
			f, err := os.Create(dest)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := client.DownloadFileVersion(cmd.Context(), ids[0], ids[1], f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "downloaded %s\n", dest)
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out", ".", "directory to write the download into")
	return cmd
}

func newFilesVersionsRestoreCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <file-id> <version-id>",
		Short: "Restore an archived version as the file's current bytes",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			f, err := client.RestoreFileVersion(cmd.Context(), ids[0], ids[1])
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.versions.restore", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Restored version %d as the current bytes of %q.\n", ids[1], f.Name)
			return nil
		},
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
