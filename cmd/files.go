package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/files"
	"github.com/MalteKiefer/ledgerline-cli/internal/ui"
)

// nowISO is the current time as an ISO-8601 UTC string (matches new Date().toISOString()).
func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// newFilesCommand builds the `files` group.
func newFilesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files",
		Short: "Work with encrypted files",
	}
	cmd.AddCommand(newFilesLsCommand(), newFilesOpenCommand(), newFilesDownloadCommand(), newFilesUploadCommand(), newFilesRmCommand(), newFilesSyncCommand())
	return cmd
}

// newFilesDownloadCommand downloads and decrypts files to a local folder.
func newFilesDownloadCommand() *cobra.Command {
	var out, remote string
	var force bool

	cmd := &cobra.Command{
		Use:   "download [path]",
		Short: "Download and decrypt a file or folder to a local folder",
		Long: "Download by path — a single file, a folder subtree, or the whole store.\n\n" +
			"  ledgerline-cli files download -o /local/dir                 # everything\n" +
			"  ledgerline-cli files download Photos/2024 -o /local/dir     # a folder subtree\n" +
			"  ledgerline-cli files download Docs/report.pdf -o /local/dir # a single file\n\n" +
			"The path is auto-detected as a file or a folder. Files already present with\n" +
			"the same size are skipped unless --force.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				return errors.New("an output folder is required: -o/--output")
			}
			path := remote
			if len(args) == 1 {
				path = args[0]
			}
			return runFilesDownload(cmd, out, path, force)
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "destination folder (required)")
	cmd.Flags().StringVar(&remote, "remote", "", "remote file or folder path (alternative to the positional argument)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing local files")
	return cmd
}

// runFilesDownload loads the manifest and writes each file's decrypted content.
// The path may name a single file, a folder subtree, or (empty) the whole store.
func runFilesDownload(cmd *cobra.Command, outDir, path string, force bool) error {
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
	store.SetShardCache(shardCache("files-shards"))
	fmt.Fprintln(w, "Loading files…")
	if err := store.Load(ctx); err != nil {
		return err
	}
	warnIfDegraded(w, "files", store)

	// A path that names a single file downloads just that file.
	if fv, ok := files.FindFile(store, path); ok {
		return downloadSingleFile(ctx, w, client, vk, outDir, fv, force)
	}
	if path != "" && !files.IsFolder(store, path) {
		return fmt.Errorf("no such file or folder: %s", path)
	}

	entries := files.List(store, path)
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
		dest, ok := files.SafeJoin(outDir, e.Path)
		if !ok {
			failed++
			fmt.Fprintf(w, "  [%d/%d] %s — skipped: unsafe path\n", i+1, len(entries), e.Path)
			continue
		}
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
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
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

// downloadSingleFile fetches one file into outDir (as its own name).
func downloadSingleFile(ctx context.Context, w io.Writer, client *api.Client, vk []byte, outDir string, fv files.FileView, force bool) error {
	dest, ok := files.SafeJoin(outDir, fv.Name)
	if !ok {
		return fmt.Errorf("refusing unsafe file name %q", fv.Name)
	}
	if !force {
		if info, err := os.Stat(dest); err == nil && info.Size() == fv.Size {
			fmt.Fprintf(w, "%s — exists, skipped\n", fv.Name)
			return nil
		}
	}
	data, err := files.NewDownloader(client, vk).Fetch(ctx, fv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	if err := writeAtomic(dest, data); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s — downloaded\n", fv.Name)
	return nil
}

// newFilesUploadCommand uploads a local folder tree into the encrypted files.
func newFilesUploadCommand() *cobra.Command {
	var folder, remote string
	var hidden bool
	var batch, jobs int

	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload a local file or folder into encrypted files",
		Long: "Upload a local file or folder, recreating any subfolders under the files tree.\n\n" +
			"  ledgerline-cli files upload /local/file.jpg [--remote Target]   # a single file\n" +
			"  ledgerline-cli files upload /local/dir [--remote Target]        # a folder tree\n" +
			"  ledgerline-cli files upload -f /local/dir [--remote Target]     # same, via flag\n\n" +
			"A file already present with the same size is skipped; a changed file adds\n" +
			"a new version. Hidden files (dotfiles) are skipped unless --hidden.\n" +
			"Progress is saved every --batch uploads so an interrupted run keeps what\n" +
			"it already stored.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := folder
			if len(args) == 1 {
				if src != "" {
					return errors.New("pass the source as a positional argument or -f/--folder, not both")
				}
				src = args[0]
			}
			if src == "" {
				return errors.New("a source file or folder is required")
			}
			return runFilesUpload(cmd, src, remote, hidden, batch, jobs)
		},
	}
	cmd.Flags().StringVarP(&folder, "folder", "f", "", "local source file or folder (alternative to the positional argument)")
	cmd.Flags().StringVar(&remote, "remote", "", "remote target folder path (default: root)")
	cmd.Flags().BoolVar(&hidden, "hidden", false, "include hidden files (dotfiles)")
	cmd.Flags().IntVar(&batch, "batch", defaultBatch, "save progress after this many uploaded/updated files (0 = save once at the end)")
	cmd.Flags().IntVarP(&jobs, "jobs", "j", defaultJobs, "number of files to upload in parallel")
	return cmd
}

// runFilesUpload walks the local folder and uploads changed/new files, up to
// --jobs in parallel, saving progress every --batch files.
func runFilesUpload(cmd *cobra.Command, folder, remote string, hidden bool, batch, jobs int) error {
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
	store.SetShardCache(shardCache("files-shards"))
	fmt.Fprintln(w, "Loading files…")
	if err := store.Load(ctx); err != nil {
		return err
	}
	warnIfDegraded(w, "files", store)

	remoteBase := strings.Trim(strings.ReplaceAll(remote, "\\", "/"), "/")
	index := files.IndexByPath(store, remoteBase)
	up := files.NewUploader(client, store, vk)

	if jobs < 1 {
		jobs = defaultJobs
	}
	fmt.Fprintf(w, "Uploading %d file(s), %d in parallel…\n", len(locals), jobs)
	bar := ui.NewProgressBar(w, len(locals), term.IsTerminal(int(os.Stdout.Fd())))

	r := &filesUploadRun{w: w, ctx: ctx, up: up, bar: bar, total: len(locals)}

	// Process in batches; within a batch, up to `jobs` files upload concurrently
	// (the slow network step). The manifest is saved once per batch boundary,
	// when no worker is in flight, so an interrupted run keeps what it stored.
	bsz := batch
	if bsz < 1 {
		bsz = len(locals)
	}
	for start := 0; start < len(locals); start += bsz {
		if ctx.Err() != nil {
			break
		}
		end := start + bsz
		if end > len(locals) {
			end = len(locals)
		}
		r.processBatch(locals[start:end], index, remoteBase, jobs)
		if store.Dirty() {
			if bar.Active() {
				bar.Update(r.doneCount(), "saving checkpoint…")
			}
			if err := store.Save(context.WithoutCancel(ctx)); err != nil {
				return fmt.Errorf("save files: %w", err)
			}
		}
	}
	bar.Finish()
	fmt.Fprintf(w, "Done: %d uploaded, %d updated, %d skipped, %d failed.\n", r.created, r.updated, r.skipped, r.failed)
	return nil
}

// filesUploadRun holds the shared, concurrently-updated state of a parallel files
// upload: counters, the progress bar and the file-done count.
type filesUploadRun struct {
	w     io.Writer
	ctx   context.Context
	up    *files.Uploader
	bar   *ui.ProgressBar
	total int

	mu                                      sync.Mutex
	done, created, updated, skipped, failed int
}

func (r *filesUploadRun) doneCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done
}

// processBatch uploads a batch of files using up to jobs concurrent workers and
// returns once all of them finish (the barrier before a save).
func (r *filesUploadRun) processBatch(batch []localFile, index map[string]files.Entry, remoteBase string, jobs int) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, jobs)
	for _, lf := range batch {
		if r.ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(lf localFile) {
			defer wg.Done()
			defer func() { <-sem }()
			r.one(lf, index, remoteBase)
		}(lf)
	}
	wg.Wait()
}

// one uploads a single file. The slow network steps run unlocked; only the shared
// counters, output and progress bar are guarded.
func (r *filesUploadRun) one(lf localFile, index map[string]files.Entry, remoteBase string) {
	if r.ctx.Err() != nil {
		return
	}
	data, rerr := os.ReadFile(lf.abs)
	if rerr != nil {
		r.record(lf.rel, "failed: "+rerr.Error(), &r.failed)
		return
	}
	mimeType := mimeForName(lf.rel)
	existing, exists := index[lf.rel]

	switch {
	case exists && existing.View.Size == int64(len(data)):
		r.record(lf.rel, "", &r.skipped)
	case exists:
		if _, err := r.up.Replace(r.ctx, existing.View.ID, mimeType, data); err != nil {
			r.record(lf.rel, "failed: "+err.Error(), &r.failed)
			return
		}
		r.record(lf.rel, "updated", &r.updated)
	default:
		if _, _, err := r.up.Create(r.ctx, joinRemote(remoteBase, lf.rel), mimeType, nowISO(), data); err != nil {
			r.record(lf.rel, "failed: "+err.Error(), &r.failed)
			return
		}
		r.record(lf.rel, "uploaded", &r.created)
	}
}

// record bumps a counter and advances the progress bar (or prints a line when the
// bar is inactive). Safe to call from several workers at once.
func (r *filesUploadRun) record(rel, note string, counter *int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*counter++
	r.done++
	if r.bar.Active() {
		r.bar.Update(r.done, rel)
	} else if note != "" {
		fmt.Fprintf(r.w, "  [%d/%d] %s — %s\n", r.done, r.total, rel, note)
	}
}

// localFile is a discovered local file with its path relative to the walk root.
type localFile struct {
	abs string
	rel string // slash-separated
}

// collectLocalFiles walks root, returning regular files (dotfiles only when
// hidden is set), always skipping junk metadata files. When root is a single
// file it is returned directly, keyed by its base name.
func collectLocalFiles(root string, hidden bool) ([]localFile, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		name := filepath.Base(root)
		if isJunk(name) {
			return nil, fmt.Errorf("%s is a metadata file and will not be uploaded", name)
		}
		return []localFile{{abs: root, rel: name}}, nil
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
