package cmd

import (
	"context"
	"encoding/json"
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
	"github.com/MalteKiefer/ledgerline-cli/internal/ui"
	"github.com/MalteKiefer/ledgerline-cli/internal/uploadledger"
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
		newFilesRenameCommand(),
		newFilesMvCommand(),
		newFilesCopyCommand(),
		newFilesFolderCommand(),
		newFilesTrashCommand(),
		newFilesVersionsCommand(),
		newFilesLabelsCommand(),
		newFilesSearchCommand(),
		newFilesStatsCommand(),
		newFilesActivityCommand(),
	)
	return cmd
}

// newFilesUploadCommand uploads files into a folder (flat; sync handles trees).
// It skips a file whose bytes already exist on the server (remote sha256) or were
// already uploaded from this host, so nothing is sent twice.
func newFilesUploadCommand() *cobra.Command {
	var folder int64
	var jobs, batch int
	var force bool
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
			ledger, err := uploadLedger("files", client.BaseURL())
			if err != nil {
				return err
			}
			// Authoritative remote content index: skip a file already on the server
			// even from a fresh host. Best-effort — an error just falls back to the
			// local ledger + server-side dedup.
			remoteSha := map[string]bool{}
			if !force {
				if _, entries, _, derr := client.FilesData(cmd.Context()); derr == nil {
					for _, e := range entries {
						if e.Sha256 != nil {
							remoteSha[*e.Sha256] = true
						}
					}
				}
			}
			out := cmd.OutOrStdout()
			bar := ui.NewProgressBar(out, len(args), ui.IsTTY(out))

			var mu sync.Mutex
			var ok, skipped, failed, done int
			sem := make(chan struct{}, jobs)
			var wg sync.WaitGroup
			for _, path := range args {
				if info, serr := os.Stat(path); serr != nil || info.IsDir() {
					mu.Lock()
					failed++
					done++
					bar.Println(fmt.Sprintf("skip     %s (not a regular file)", path))
					bar.Update(done, path)
					mu.Unlock()
					continue
				}
				wg.Add(1)
				sem <- struct{}{}
				go func(p string) {
					defer wg.Done()
					defer func() { <-sem }()
					name := filepath.Base(p)

					sha, herr := uploadledger.HashFile(p)
					if herr == nil && !force && (remoteSha[sha] || ledger.Has(sha)) {
						mu.Lock()
						skipped++
						bar.Println(fmt.Sprintf("skip     %s (already uploaded)", name))
						ledger.Add(sha)
						done++
						bar.Update(done, name)
						mu.Unlock()
						return
					}

					open := func() (io.ReadCloser, error) { return os.Open(p) }
					_, uerr := client.UploadFile(cmd.Context(), name, folderPtr, open)
					mu.Lock()
					defer mu.Unlock()
					if uerr != nil {
						failed++
						bar.Println(fmt.Sprintf("failed   %s: %v", name, uerr))
					} else {
						ok++
						bar.Println(fmt.Sprintf("uploaded %s", name))
						if herr == nil {
							ledger.Add(sha)
							_ = ledger.MaybeCheckpoint(batch)
						}
					}
					done++
					bar.Update(done, name)
				}(path)
			}
			wg.Wait()
			bar.Finish()
			if serr := ledger.Save(); serr != nil {
				fmt.Fprintf(out, "warning: could not save upload ledger: %v\n", serr)
			}

			auditLog().Log(audit.Event{
				Event: "files.upload", Outcome: audit.OutcomeOK, Count: ok,
				Detail: fmt.Sprintf("%d uploaded, %d skipped, %d failed", ok, skipped, failed),
			})
			fmt.Fprintf(out, "Done: %d uploaded, %d skipped, %d failed.\n", ok, skipped, failed)
			if failed > 0 {
				return fmt.Errorf("%d file(s) failed to upload", failed)
			}
			return nil
		},
	}
	cmd.Flags().Int64Var(&folder, "folder", 0, "destination folder id (default: root)")
	cmd.Flags().IntVar(&jobs, "jobs", 4, "number of concurrent uploads")
	cmd.Flags().IntVar(&batch, "batch", 50, "checkpoint the dedup ledger every N uploads (0 disables)")
	cmd.Flags().BoolVar(&force, "force", false, "upload even files already present on the server")
	return cmd
}

// filesLsJSONResult is `files ls --json`'s output: the raw folder/file tree +
// usage, for a non-interactive caller (e.g. a GUI's remote-folder picker)
// instead of the human tree render.
type filesLsJSONResult struct {
	Folders []api.FileFolder `json:"folders"`
	Files   []api.FileEntry  `json:"files"`
	Usage   api.FilesUsage   `json:"usage"`
}

// newFilesLsCommand prints the remote folder/file tree + usage.
func newFilesLsCommand() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{
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
			if jsonFlag {
				return json.NewEncoder(out).Encode(filesLsJSONResult{Folders: folders, Files: entries, Usage: usage})
			}
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
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print the raw folder/file tree + usage as JSON")
	return cmd
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
