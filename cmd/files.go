package cmd

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/files"
)

// nowISO is the current time as an ISO-8601 UTC string (matches new Date().toISOString()).
func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// newFilesCommand builds the `files` group.
func newFilesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files",
		Short: "Work with encrypted files",
	}
	cmd.AddCommand(newFilesLsCommand(), newFilesDownloadCommand(), newFilesUploadCommand(), newFilesSyncCommand())
	return cmd
}

// newFilesDownloadCommand downloads and decrypts files to a local folder.
func newFilesDownloadCommand() *cobra.Command {
	var out, remote string
	var force bool

	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download and decrypt files to a local folder",
		Long: "Download files, preserving the folder tree, into an output folder.\n\n" +
			"  ledgerline-cli files download -o /local/dir [--remote SubFolder]\n\n" +
			"Files already present with the same size are skipped unless --force.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" {
				return errors.New("an output folder is required: -o/--output")
			}
			return runFilesDownload(cmd, out, remote, force)
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "destination folder (required)")
	cmd.Flags().StringVar(&remote, "remote", "", "restrict to a remote subfolder path")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing local files")
	return cmd
}

// runFilesDownload loads the manifest and writes each file's decrypted content.
func runFilesDownload(cmd *cobra.Command, outDir, remote string, force bool) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	client, err := authedClient(ctx)
	if err != nil {
		return err
	}
	vk, err := unlockVault(cmd, client)
	if err != nil {
		return err
	}

	store := files.NewStore(client, vk)
	fmt.Fprintln(w, "Loading files…")
	if err := store.Load(ctx); err != nil {
		return err
	}
	entries := files.List(store, remote)
	if len(entries) == 0 {
		fmt.Fprintln(w, "No files to download.")
		return nil
	}

	dl := files.NewDownloader(client, vk)
	var got, skipped, failed int
	for i, e := range entries {
		if ctx.Err() != nil {
			break
		}
		dest := filepath.Join(outDir, filepath.FromSlash(e.Path))
		if !force {
			if info, serr := os.Stat(dest); serr == nil && info.Size() == e.View.Size {
				skipped++
				continue
			}
		}
		data, ferr := dl.Fetch(ctx, e.View)
		if ferr != nil {
			failed++
			fmt.Fprintf(w, "  [%d/%d] %s — failed: %v\n", i+1, len(entries), e.Path, ferr)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := writeAtomic(dest, data); err != nil {
			failed++
			fmt.Fprintf(w, "  [%d/%d] %s — failed: %v\n", i+1, len(entries), e.Path, err)
			continue
		}
		got++
		fmt.Fprintf(w, "  [%d/%d] %s — downloaded\n", i+1, len(entries), e.Path)
	}
	fmt.Fprintf(w, "Done: %d downloaded, %d skipped, %d failed.\n", got, skipped, failed)
	return nil
}

// newFilesUploadCommand uploads a local folder tree into the encrypted files.
func newFilesUploadCommand() *cobra.Command {
	var folder, remote string
	var hidden bool

	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload a local folder into encrypted files",
		Long: "Upload a local folder, recreating its subfolders under the files tree.\n\n" +
			"  ledgerline-cli files upload -f /local/dir [--remote Target]\n\n" +
			"A file already present with the same size is skipped; a changed file adds\n" +
			"a new version. Hidden files (dotfiles) are skipped unless --hidden.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if folder == "" {
				return errors.New("a source folder is required: -f/--folder")
			}
			return runFilesUpload(cmd, folder, remote, hidden)
		},
	}
	cmd.Flags().StringVarP(&folder, "folder", "f", "", "local source folder (required)")
	cmd.Flags().StringVar(&remote, "remote", "", "remote target folder path (default: root)")
	cmd.Flags().BoolVar(&hidden, "hidden", false, "include hidden files (dotfiles)")
	return cmd
}

// runFilesUpload walks the local folder and uploads changed/new files.
func runFilesUpload(cmd *cobra.Command, folder, remote string, hidden bool) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	locals, err := collectLocalFiles(folder, hidden)
	if err != nil {
		return err
	}
	if len(locals) == 0 {
		fmt.Fprintln(w, "No files to upload.")
		return nil
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
	fmt.Fprintln(w, "Loading files…")
	if err := store.Load(ctx); err != nil {
		return err
	}

	remoteBase := strings.Trim(strings.ReplaceAll(remote, "\\", "/"), "/")
	index := files.IndexByPath(store, remoteBase)
	up := files.NewUploader(client, store, vk)

	var created, updated, skipped, failed int
	for i, lf := range locals {
		if ctx.Err() != nil {
			break
		}
		remotePath := joinRemote(remoteBase, lf.rel)
		existing, exists := index[lf.rel]

		data, rerr := os.ReadFile(lf.abs)
		if rerr != nil {
			failed++
			fmt.Fprintf(w, "  [%d/%d] %s — failed: %v\n", i+1, len(locals), lf.rel, rerr)
			continue
		}
		mimeType := mimeForName(lf.rel)

		switch {
		case exists && existing.View.Size == int64(len(data)):
			skipped++
			continue
		case exists:
			if _, err := up.Replace(ctx, existing.View.ID, mimeType, data); err != nil {
				failed++
				fmt.Fprintf(w, "  [%d/%d] %s — failed: %v\n", i+1, len(locals), lf.rel, err)
				continue
			}
			updated++
			fmt.Fprintf(w, "  [%d/%d] %s — updated\n", i+1, len(locals), lf.rel)
		default:
			if _, _, err := up.Create(ctx, remotePath, mimeType, nowISO(), data); err != nil {
				failed++
				fmt.Fprintf(w, "  [%d/%d] %s — failed: %v\n", i+1, len(locals), lf.rel, err)
				continue
			}
			created++
			fmt.Fprintf(w, "  [%d/%d] %s — uploaded\n", i+1, len(locals), lf.rel)
		}
	}

	if store.Dirty() {
		fmt.Fprintln(w, "Saving…")
		if err := store.Save(context.WithoutCancel(ctx)); err != nil {
			return fmt.Errorf("save files: %w", err)
		}
	}
	fmt.Fprintf(w, "Done: %d uploaded, %d updated, %d skipped, %d failed.\n", created, updated, skipped, failed)
	return nil
}

// localFile is a discovered local file with its path relative to the walk root.
type localFile struct {
	abs string
	rel string // slash-separated
}

// collectLocalFiles walks root, returning regular files (dotfiles only when
// hidden is set), always skipping junk metadata files.
func collectLocalFiles(root string, hidden bool) ([]localFile, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", root)
	}

	var out []localFile
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && !hidden && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if isJunk(name) || (!hidden && strings.HasPrefix(name, ".")) {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		out = append(out, localFile{abs: p, rel: filepath.ToSlash(rel)})
		return nil
	})
	return out, err
}

// isJunk reports OS/VCS metadata files that should never be uploaded.
func isJunk(name string) bool {
	switch name {
	case ".DS_Store", "Thumbs.db", "desktop.ini", ".localized", ".nomedia":
		return true
	}
	return false
}

// joinRemote joins a remote base folder and a relative slash path.
func joinRemote(base, rel string) string {
	rel = strings.TrimLeft(rel, "/")
	if base == "" {
		return rel
	}
	return base + "/" + rel
}

// mimeForName guesses a MIME type from a filename, defaulting to octet-stream.
func mimeForName(name string) string {
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		if i := strings.IndexByte(t, ';'); i >= 0 {
			t = t[:i]
		}
		return t
	}
	return "application/octet-stream"
}
