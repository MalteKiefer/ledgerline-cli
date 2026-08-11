package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

// Conflict policies for a file changed on both sides.
const (
	ConflictNewest   = "newest"    // keep whichever side is newer (mtime vs updated_at)
	ConflictKeepBoth = "keep-both" // pull the remote copy under a suffixed local name
	ConflictSkip     = "skip"      // leave both untouched
)

// Direction gates which half of the sync runs.
const (
	DirectionBoth = "both"
	DirectionPush = "push"
	DirectionPull = "pull"
)

// SyncOptions configures one reconcile pass.
type SyncOptions struct {
	Direction string // both | push | pull
	Conflict  string // newest | keep-both | skip
}

// SyncResult counts what one pass did.
type SyncResult struct {
	Pushed, Pulled, Conflicts, Skipped, Failed int
}

// Sync reconciles localDir against the remote file tree in one pass. It never
// deletes on either side (safe by default: with no persisted last-seen state a
// missing file is indistinguishable from a deletion). It pushes files that are
// new/changed locally, pulls files that are new/changed remotely, and resolves a
// both-sides change by opts.Conflict. Progress lines go to log.
func Sync(ctx context.Context, c *api.Client, localDir string, opts SyncOptions, log io.Writer) (SyncResult, error) {
	if opts.Direction == "" {
		opts.Direction = DirectionBoth
	}
	if opts.Conflict == "" {
		opts.Conflict = ConflictNewest
	}
	var res SyncResult

	folders, files, _, err := c.FilesData(ctx)
	if err != nil {
		return res, err
	}

	rm := newRemoteModel(folders, files)
	local, err := scanLocal(localDir)
	if err != nil {
		return res, err
	}

	pushOK := opts.Direction == DirectionBoth || opts.Direction == DirectionPush
	pullOK := opts.Direction == DirectionBoth || opts.Direction == DirectionPull

	// Local → remote (push new / changed).
	relPaths := make([]string, 0, len(local))
	for rel := range local {
		relPaths = append(relPaths, rel)
	}
	sort.Strings(relPaths)
	for _, rel := range relPaths {
		le := local[rel]
		re, onRemote := rm.fileByPath[rel]
		switch {
		case !onRemote:
			if !pushOK {
				continue
			}
			if err := rm.pushNew(ctx, c, localDir, rel, log); err != nil {
				res.Failed++
				fmt.Fprintf(log, "failed   push %s: %v\n", rel, err)
			} else {
				res.Pushed++
				fmt.Fprintf(log, "pushed   %s\n", rel)
			}
		case sameContent(le, re):
			// unchanged
		default:
			resolveConflict(ctx, c, localDir, rel, le, re, opts, pushOK, pullOK, &res, log)
		}
	}

	// Remote → local (pull files not present locally).
	if pullOK {
		remotePaths := make([]string, 0, len(rm.fileByPath))
		for rel := range rm.fileByPath {
			remotePaths = append(remotePaths, rel)
		}
		sort.Strings(remotePaths)
		for _, rel := range remotePaths {
			if _, ok := local[rel]; ok {
				continue // handled in the push loop
			}
			re := rm.fileByPath[rel]
			if err := pullTo(ctx, c, localDir, rel, re.ID); err != nil {
				res.Failed++
				fmt.Fprintf(log, "failed   pull %s: %v\n", rel, err)
			} else {
				res.Pulled++
				fmt.Fprintf(log, "pulled   %s\n", rel)
			}
		}
	}
	return res, nil
}

// resolveConflict handles a file that differs on both sides per opts.Conflict.
func resolveConflict(ctx context.Context, c *api.Client, localDir, rel string, le localEntry, re api.FileEntry, opts SyncOptions, pushOK, pullOK bool, res *SyncResult, log io.Writer) {
	res.Conflicts++
	switch opts.Conflict {
	case ConflictSkip:
		res.Skipped++
		fmt.Fprintf(log, "conflict %s (skipped)\n", rel)
	case ConflictKeepBoth:
		if !pullOK {
			res.Skipped++
			return
		}
		alt := suffixName(rel, "remote")
		if err := pullTo(ctx, c, localDir, alt, re.ID); err != nil {
			res.Failed++
			fmt.Fprintf(log, "failed   keep-both %s: %v\n", rel, err)
			return
		}
		fmt.Fprintf(log, "conflict %s (kept remote as %s)\n", rel, alt)
	default: // ConflictNewest
		if localNewer(le, re) {
			if !pushOK {
				res.Skipped++
				return
			}
			if err := replaceRemote(ctx, c, localDir, rel, re.ID, log); err != nil {
				res.Failed++
				fmt.Fprintf(log, "failed   push %s: %v\n", rel, err)
				return
			}
			res.Pushed++
			fmt.Fprintf(log, "pushed   %s (newer local)\n", rel)
		} else {
			if !pullOK {
				res.Skipped++
				return
			}
			if err := pullTo(ctx, c, localDir, rel, re.ID); err != nil {
				res.Failed++
				fmt.Fprintf(log, "failed   pull %s: %v\n", rel, err)
				return
			}
			res.Pulled++
			fmt.Fprintf(log, "pulled   %s (newer remote)\n", rel)
		}
	}
}

// --- local scan ---

type localEntry struct {
	sha   string
	mtime time.Time
}

// scanLocal walks root, returning every regular file keyed by its slash-separated
// path relative to root. Hidden files/dirs (leading dot) are skipped.
func scanLocal(root string) (map[string]localEntry, error) {
	out := map[string]localEntry{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		base := d.Name()
		if p != root && strings.HasPrefix(base, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		sum, err := sha256File(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = localEntry{sha: sum, mtime: info.ModTime()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// --- remote model ---

type remoteModel struct {
	fileByPath map[string]api.FileEntry // slash path relative to root -> file
	folderIDs  map[string]int64         // slash folder path -> folder id
	folderByID map[int64]api.FileFolder
}

func newRemoteModel(folders []api.FileFolder, files []api.FileEntry) *remoteModel {
	rm := &remoteModel{
		fileByPath: map[string]api.FileEntry{},
		folderIDs:  map[string]int64{},
		folderByID: map[int64]api.FileFolder{},
	}
	for _, f := range folders {
		rm.folderByID[f.ID] = f
	}
	for _, f := range folders {
		rm.folderIDs[rm.folderPath(f.ID)] = f.ID
	}
	for _, file := range files {
		dir := ""
		if file.FileFolderID != nil {
			dir = rm.folderPath(*file.FileFolderID)
		}
		rm.fileByPath[joinRel(dir, file.Name)] = file
	}
	return rm
}

// folderPath builds a folder's slash path by walking parent links.
func (rm *remoteModel) folderPath(id int64) string {
	var parts []string
	seen := map[int64]bool{}
	for id != 0 && !seen[id] {
		seen[id] = true
		f, ok := rm.folderByID[id]
		if !ok {
			break
		}
		parts = append([]string{f.Name}, parts...)
		if f.ParentID == nil {
			break
		}
		id = *f.ParentID
	}
	return strings.Join(parts, "/")
}

// ensureFolder creates the folder chain for a slash dir path (memoized) and
// returns its folder id (0 for the root "").
func (rm *remoteModel) ensureFolder(ctx context.Context, c *api.Client, dir string) (int64, error) {
	if dir == "" {
		return 0, nil
	}
	if id, ok := rm.folderIDs[dir]; ok {
		return id, nil
	}
	parent := path.Dir(dir)
	if parent == "." {
		parent = ""
	}
	parentID, err := rm.ensureFolder(ctx, c, parent)
	if err != nil {
		return 0, err
	}
	var parentPtr *int64
	if parentID != 0 {
		parentPtr = &parentID
	}
	folder, err := c.CreateFolder(ctx, path.Base(dir), parentPtr)
	if err != nil {
		return 0, err
	}
	rm.folderByID[folder.ID] = folder
	rm.folderIDs[dir] = folder.ID
	return folder.ID, nil
}

// pushNew uploads a local file that has no remote counterpart.
func (rm *remoteModel) pushNew(ctx context.Context, c *api.Client, localDir, rel string, log io.Writer) error {
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	folderID, err := rm.ensureFolder(ctx, c, dir)
	if err != nil {
		return err
	}
	var folderPtr *int64
	if folderID != 0 {
		folderPtr = &folderID
	}
	abs := filepath.Join(localDir, filepath.FromSlash(rel))
	open := func() (io.ReadCloser, error) { return os.Open(abs) }
	_, err = c.UploadFile(ctx, path.Base(rel), folderPtr, open)
	return err
}

// --- shared helpers ---

func replaceRemote(ctx context.Context, c *api.Client, localDir, rel string, id int64, log io.Writer) error {
	abs := filepath.Join(localDir, filepath.FromSlash(rel))
	open := func() (io.ReadCloser, error) { return os.Open(abs) }
	_, err := c.ReplaceFileContent(ctx, id, path.Base(rel), open)
	return err
}

func pullTo(ctx context.Context, c *api.Client, localDir, rel string, id int64) error {
	abs := filepath.Join(localDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	f, err := os.Create(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	return c.DownloadFile(ctx, id, f)
}

// sameContent reports whether local and remote bytes are identical by sha256.
// When the remote sha is unknown, fall back to "not equal" so the conflict path
// decides (never a silent no-op that could hide a real difference).
func sameContent(le localEntry, re api.FileEntry) bool {
	return re.Sha256 != nil && *re.Sha256 == le.sha
}

// localNewer compares local mtime against the remote updated_at timestamp.
func localNewer(le localEntry, re api.FileEntry) bool {
	if re.UpdatedAt == nil {
		return true
	}
	rt, err := time.Parse(time.RFC3339, *re.UpdatedAt)
	if err != nil {
		return true
	}
	return le.mtime.After(rt)
}

// joinRel joins a dir and name with "/", dropping an empty dir.
func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// suffixName inserts a tag before the extension of a slash path
// ("a/b.txt","remote" -> "a/b.remote.txt").
func suffixName(rel, tag string) string {
	ext := path.Ext(rel)
	return strings.TrimSuffix(rel, ext) + "." + tag + ext
}
