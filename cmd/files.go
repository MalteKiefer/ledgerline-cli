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
	"github.com/MalteKiefer/ledgerline-cli/internal/files"
)

// newFilesCommand builds the `files` group.
func newFilesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files",
		Short: "Upload, list, download, remove files and folders; two-way sync",
	}
	cmd.AddCommand(
		newFilesUploadCommand(),
		newFilesLsCommand(),
		newFilesDownloadCommand(),
		newFilesRmCommand(),
		newFilesMkdirCommand(),
		newFilesSyncCommand(),
	)
	return cmd
}

// newFilesUploadCommand uploads files into a folder (flat; sync handles trees).
func newFilesUploadCommand() *cobra.Command {
	var folder int64
	var jobs int
	cmd := &cobra.Command{
		Use:   "upload <file...>",
		Short: "Upload files into a folder (--folder; root by default)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobs < 1 {
				jobs = 1
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			var folderPtr *int64
			if cmd.Flags().Changed("folder") {
				folderPtr = &folder
			}
			out := cmd.OutOrStdout()

			var mu sync.Mutex
			var ok, failed int
			sem := make(chan struct{}, jobs)
			var wg sync.WaitGroup
			for _, path := range args {
				if info, serr := os.Stat(path); serr != nil || info.IsDir() {
					mu.Lock()
					failed++
					fmt.Fprintf(out, "skip     %s (not a regular file)\n", path)
					mu.Unlock()
					continue
				}
				wg.Add(1)
				sem <- struct{}{}
				go func(p string) {
					defer wg.Done()
					defer func() { <-sem }()
					name := filepath.Base(p)
					open := func() (io.ReadCloser, error) { return os.Open(p) }
					_, uerr := client.UploadFile(cmd.Context(), name, folderPtr, open)
					mu.Lock()
					defer mu.Unlock()
					if uerr != nil {
						failed++
						fmt.Fprintf(out, "failed   %s: %v\n", name, uerr)
						return
					}
					ok++
					fmt.Fprintf(out, "uploaded %s\n", name)
				}(path)
			}
			wg.Wait()

			auditLog().Log(audit.Event{Event: "files.upload", Outcome: audit.OutcomeOK, Count: ok})
			fmt.Fprintf(out, "\nDone: %d uploaded, %d failed.\n", ok, failed)
			if failed > 0 {
				return fmt.Errorf("%d file(s) failed to upload", failed)
			}
			return nil
		},
	}
	cmd.Flags().Int64Var(&folder, "folder", 0, "destination folder id (default: root)")
	cmd.Flags().IntVar(&jobs, "jobs", 4, "number of concurrent uploads")
	return cmd
}

// newFilesLsCommand prints the remote folder/file tree + usage.
func newFilesLsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List the whole folder/file tree",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			folders, entries, usage, err := client.FilesData(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(folders) == 0 && len(entries) == 0 {
				fmt.Fprintln(out, "No files.")
			} else {
				files.BuildTree(folders, entries).Print(out)
			}
			fmt.Fprintf(out, "\nUsage: %s used", humanBytes(usage.Used))
			if usage.Quota != nil {
				fmt.Fprintf(out, " of %s", humanBytes(*usage.Quota))
			}
			fmt.Fprintln(out)
			return nil
		},
	}
}

// newFilesDownloadCommand downloads files by id.
func newFilesDownloadCommand() *cobra.Command {
	var outDir string
	cmd := &cobra.Command{
		Use:   "download <id...>",
		Short: "Download files by id",
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
			names := map[int64]string{}
			if _, entries, _, lerr := client.FilesData(cmd.Context()); lerr == nil {
				for _, e := range entries {
					names[e.ID] = e.Name
				}
			}
			out := cmd.OutOrStdout()
			for _, id := range ids {
				name := names[id]
				if name == "" {
					name = strconv.FormatInt(id, 10)
				}
				dest := filepath.Join(outDir, name)
				if derr := downloadFileTo(cmd.Context(), client, id, dest); derr != nil {
					return fmt.Errorf("download %d: %w", id, derr)
				}
				fmt.Fprintf(out, "downloaded %s\n", dest)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out", ".", "directory to write downloads into")
	return cmd
}

// downloadFileTo streams one file to a destination path.
func downloadFileTo(ctx context.Context, client *api.Client, id int64, dest string) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	return client.DownloadFile(ctx, id, f)
}

// newFilesRmCommand trashes files by id.
func newFilesRmCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id...>",
		Short: "Move files to the trash by id",
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
			for _, id := range ids {
				if derr := client.DeleteFile(cmd.Context(), id); derr != nil {
					return fmt.Errorf("delete %d: %w", id, derr)
				}
			}
			auditLog().Log(audit.Event{Event: "files.rm", Outcome: audit.OutcomeOK, Count: len(ids)})
			fmt.Fprintf(cmd.OutOrStdout(), "Trashed %d file(s).\n", len(ids))
			return nil
		},
	}
}

// newFilesMkdirCommand creates a folder.
func newFilesMkdirCommand() *cobra.Command {
	var parent int64
	cmd := &cobra.Command{
		Use:   "mkdir <name>",
		Short: "Create a folder (--parent for a subfolder)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			var parentPtr *int64
			if cmd.Flags().Changed("parent") {
				parentPtr = &parent
			}
			folder, err := client.CreateFolder(cmd.Context(), args[0], parentPtr)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created folder %q (id %d).\n", folder.Name, folder.ID)
			return nil
		},
	}
	cmd.Flags().Int64Var(&parent, "parent", 0, "parent folder id (default: root)")
	return cmd
}
