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
	"github.com/MalteKiefer/ledgerline-cli/internal/ui"
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

	// IncludeHidden includes dotfiles/dot-directories on the local side.
	// Default (false) skips them, matching historical behaviour.
	IncludeHidden bool

	// RemoteFolder scopes the sync to this remote folder's subtree instead of
	// the whole remote root — the local root then corresponds to this remote
	// folder, not the account's top level. Lets multiple independent sync
	// pairs (different local dirs) each mirror a different remote folder.
	// nil means the whole remote root, the historical/default behaviour.
	RemoteFolder *int64

	// KeepLocalVersions, when set, snapshots a local file into
	// <localDir>/.ledgerline-versions/ (Syncthing-.stversions-shaped: one
	// timestamped copy per overwrite, pruned to MaxLocalVersions) right
	// before a pull would overwrite it — a local safety net independent of
	// the server's own file version history (ledgerline-cli's `files
	// versions`), which only protects the *remote* copy. Default off: it
	// costs local disk space, so it's opt-in per sync pair.
	KeepLocalVersions bool
	// MaxLocalVersions caps how many snapshots KeepLocalVersions keeps per
	// file; <= 0 means the default of 5.
	MaxLocalVersions int
}

// SyncResult counts what one pass did.
type SyncResult struct {
	Pushed, Pulled, Conflicts, Skipped, Failed int
}

// Sync reconciles localDir against the remote file tree in one pass. It never
// deletes on either side (safe by default: with no persisted last-seen state a
// missing file is indistinguishable from a deletion). It pushes files that are
// new/changed locally, pulls files that are new/changed remotely, and resolves a
// both-sides change by opts.Conflict. Per-file lines and an in-place progress bar
// (when progress is true and w is a terminal) are written to w.
func Sync(ctx context.Context, c *api.Client, localDir string, opts SyncOptions, w io.Writer, progress bool) (SyncResult, error) {
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

	rm, err := newRemoteModel(folders, files, opts.RemoteFolder)
	if err != nil {
		return res, err
	}
	local, err := scanLocal(localDir, opts.IncludeHidden, loadIgnore(localDir))
	if err != nil {
		return res, err
	}

	pushOK := opts.Direction == DirectionBoth || opts.Direction == DirectionPush
	pullOK := opts.Direction == DirectionBoth || opts.Direction == DirectionPull

	relPaths := make([]string, 0, len(local))
	for rel := range local {
		relPaths = append(relPaths, rel)
	}
	sort.Strings(relPaths)
	remotePaths := make([]string, 0, len(rm.fileByPath))
	for rel := range rm.fileByPath {
		if _, ok := local[rel]; !ok {
			remotePaths = append(remotePaths, rel)
		}
	}
	sort.Strings(remotePaths)

	bar := ui.NewProgressBar(w, len(relPaths)+len(remotePaths), progress && ui.IsTTY(w))
	done := 0
	step := func(label string) { done++; bar.Update(done, label) }

	// Local → remote (push new / changed).
	for _, rel := range relPaths {
		le := local[rel]
		re, onRemote := rm.fileByPath[rel]
		switch {
		case !onRemote:
			if pushOK {
				if err := rm.pushNew(ctx, c, localDir, rel); err != nil {
					res.Failed++
					bar.Println(fmt.Sprintf("failed   push %s: %v", rel, err))
				} else {
					res.Pushed++
					bar.Println(fmt.Sprintf("pushed   %s", rel))
				}
			}
		case sameContent(le, re):
			// unchanged
		default:
			resolveConflict(ctx, c, localDir, rel, le, re, opts, pushOK, pullOK, &res, bar)
		}
		step(rel)
	}

	// Remote → local (pull files not present locally).
	if pullOK {
		for _, rel := range remotePaths {
			re := rm.fileByPath[rel]
			if err := pullTo(ctx, c, localDir, rel, re.ID, opts); err != nil {
				res.Failed++
				bar.Println(fmt.Sprintf("failed   pull %s: %v", rel, err))
			} else {
				res.Pulled++
				bar.Println(fmt.Sprintf("pulled   %s", rel))
			}
			step(rel)
		}
	}
	bar.Finish()
	return res, nil
}

// resolveConflict handles a file that differs on both sides per opts.Conflict.
func resolveConflict(ctx context.Context, c *api.Client, localDir, rel string, le localEntry, re api.FileEntry, opts SyncOptions, pushOK, pullOK bool, res *SyncResult, bar *ui.ProgressBar) {
	res.Conflicts++
	switch opts.Conflict {
	case ConflictSkip:
		res.Skipped++
		bar.Println(fmt.Sprintf("conflict %s (skipped)", rel))
	case ConflictKeepBoth:
		if !pullOK {
			res.Skipped++
			return
		}
		alt := suffixName(rel, "remote")
		if err := pullTo(ctx, c, localDir, alt, re.ID, opts); err != nil {
			res.Failed++
			bar.Println(fmt.Sprintf("failed   keep-both %s: %v", rel, err))
			return
		}
		bar.Println(fmt.Sprintf("conflict %s (kept remote as %s)", rel, alt))
	default: // ConflictNewest
		if localNewer(le, re) {
			if !pushOK {
				res.Skipped++
				return
			}
			if err := replaceRemote(ctx, c, localDir, rel, re.ID); err != nil {
				res.Failed++
				bar.Println(fmt.Sprintf("failed   push %s: %v", rel, err))
				return
			}
			res.Pushed++
			bar.Println(fmt.Sprintf("pushed   %s (newer local)", rel))
		} else {
			if !pullOK {
				res.Skipped++
				return
			}
			if err := pullTo(ctx, c, localDir, rel, re.ID, opts); err != nil {
				res.Failed++
				bar.Println(fmt.Sprintf("failed   pull %s: %v", rel, err))
				return
			}
			res.Pulled++
			bar.Println(fmt.Sprintf("pulled   %s (newer remote)", rel))
		}
	}
}

// --- local scan ---

type localEntry struct {
	sha   string
	mtime time.Time
}

// scanLocal walks root, returning every regular file keyed by its
// slash-separated path relative to root. Hidden files/dirs (leading dot) are
// skipped unless includeHidden is set; ignore.Match further excludes paths
// per the synced directory's .ledgerline-ignore file (see ignore.go). The
// ignore file itself is never included, regardless of includeHidden.
func scanLocal(root string, includeHidden bool, ignore *ignoreMatcher) (map[string]localEntry, error) {
	out := map[string]localEntry{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		base := d.Name()

		if d.IsDir() && base == versionsDirName {
			return filepath.SkipDir // KeepLocalVersions' own snapshots — never synced, regardless of --hidden
		}
		if !includeHidden && strings.HasPrefix(base, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if ignore.Match(relSlash + "/") {
				return filepath.SkipDir
			}
			return nil
		}
		if relSlash == ignoreFileName || ignore.Match(relSlash) {
			return nil
		}

		sum, err := sha256File(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[relSlash] = localEntry{sha: sum, mtime: info.ModTime()}
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

// remoteModel indexes the remote tree by path relative to a root, which is
// either the account's true root (scopeID nil) or one specific remote folder
// (scopeID set) — see SyncOptions.RemoteFolder. This lets several independent
// sync pairs each scope to a different remote folder without seeing each
// other's files.
type remoteModel struct {
	scopeID    *int64
	fileByPath map[string]api.FileEntry // slash path relative to the sync root -> file
	folderIDs  map[string]int64         // slash folder path relative to the sync root -> folder id ("" = the sync root itself)
	folderByID map[int64]api.FileFolder // every folder, regardless of scope — needed to walk parent chains
}

// newRemoteModel indexes folders/files relative to scopeID's subtree (or the
// whole tree when scopeID is nil). It returns an error if scopeID names a
// folder that doesn't exist (e.g. deleted since the sync pair was set up).
func newRemoteModel(folders []api.FileFolder, files []api.FileEntry, scopeID *int64) (*remoteModel, error) {
	rm := &remoteModel{
		scopeID:    scopeID,
		fileByPath: map[string]api.FileEntry{},
		folderIDs:  map[string]int64{},
		folderByID: map[int64]api.FileFolder{},
	}
	for _, f := range folders {
		rm.folderByID[f.ID] = f
	}

	var scopeAbs string
	if scopeID != nil {
		if _, ok := rm.folderByID[*scopeID]; !ok {
			return nil, fmt.Errorf("remote folder id %d not found", *scopeID)
		}
		scopeAbs = rm.absFolderPath(*scopeID)
	}
	// rel translates an absolute (true-root-relative) path into one relative
	// to the sync root, or reports ok=false when it falls outside scope.
	rel := func(abs string) (string, bool) {
		if scopeID == nil {
			return abs, true
		}
		if abs == scopeAbs {
			return "", true
		}
		if strings.HasPrefix(abs, scopeAbs+"/") {
			return abs[len(scopeAbs)+1:], true
		}
		return "", false
	}

	for _, f := range folders {
		if r, ok := rel(rm.absFolderPath(f.ID)); ok {
			rm.folderIDs[r] = f.ID
		}
	}
	for _, file := range files {
		abs := ""
		if file.FileFolderID != nil {
			abs = rm.absFolderPath(*file.FileFolderID)
		}
		dir, ok := rel(abs)
		if !ok {
			continue
		}
		rm.fileByPath[joinRel(dir, file.Name)] = file
	}
	return rm, nil
}

// absFolderPath builds a folder's true, root-relative slash path by walking
// parent links — independent of any scope, so scope boundaries can be
// computed by comparing two absolute paths (see newRemoteModel's rel).
func (rm *remoteModel) absFolderPath(id int64) string {
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

// ensureFolder creates the folder chain for a slash dir path relative to the
// sync root (memoized) and returns its folder id (0 for the sync root itself,
// UNLESS the sync root is a scoped remote folder, in which case "" resolves
// to that folder's own id — so new top-level files land inside it, not at the
// true account root).
func (rm *remoteModel) ensureFolder(ctx context.Context, c *api.Client, dir string) (int64, error) {
	if dir == "" {
		if rm.scopeID != nil {
			return *rm.scopeID, nil
		}
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
func (rm *remoteModel) pushNew(ctx context.Context, c *api.Client, localDir, rel string) error {
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

func replaceRemote(ctx context.Context, c *api.Client, localDir, rel string, id int64) error {
	abs := filepath.Join(localDir, filepath.FromSlash(rel))
	open := func() (io.ReadCloser, error) { return os.Open(abs) }
	_, err := c.ReplaceFileContent(ctx, id, path.Base(rel), open)
	return err
}

func pullTo(ctx context.Context, c *api.Client, localDir, rel string, id int64, opts SyncOptions) error {
	abs := filepath.Join(localDir, filepath.FromSlash(rel))
	if opts.KeepLocalVersions {
		if err := snapshotLocalVersion(localDir, rel, opts.MaxLocalVersions); err != nil {
			return fmt.Errorf("local version snapshot: %w", err)
		}
	}
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

// versionsDirName holds KeepLocalVersions' snapshots. Always excluded from
// the sync scan itself (see scanLocal) — these are a local safety net, never
// content to upload.
const versionsDirName = ".ledgerline-versions"

// snapshotLocalVersion copies the existing local file at localDir/rel into
// .ledgerline-versions/<reldir>/<stem>~<timestamp><ext> before it is about to
// be overwritten by a pull, then prunes that file's own snapshots down to
// maxVersions (<= 0 means 5) — Syncthing's .stversions, scoped down to what a
// sync client needs. A no-op (not an error) when rel doesn't exist locally
// yet, since pullTo also calls this for a brand new file.
func snapshotLocalVersion(localDir, rel string, maxVersions int) error {
	abs := filepath.Join(localDir, filepath.FromSlash(rel))
	src, err := os.Open(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer src.Close()

	if maxVersions <= 0 {
		maxVersions = 5
	}
	versionDir := filepath.Join(localDir, versionsDirName, filepath.FromSlash(path.Dir(rel)))
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		return err
	}
	base := filepath.Base(rel)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	dst := filepath.Join(versionDir, stem+"~"+time.Now().Format("20060102-150405")+ext)

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return pruneVersions(versionDir, stem, ext, maxVersions)
}

// pruneVersions keeps only the maxVersions most recent snapshots for one
// base filename (<stem>~<timestamp><ext>), removing older ones. The
// timestamp format (YYYYMMDD-HHMMSS) sorts lexicographically the same as
// chronologically, so a plain string sort is enough to find the oldest.
func pruneVersions(versionDir, stem, ext string, maxVersions int) error {
	entries, err := os.ReadDir(versionDir)
	if err != nil {
		return err
	}
	prefix := stem + "~"
	var matches []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ext) {
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)
	for len(matches) > maxVersions {
		if err := os.Remove(filepath.Join(versionDir, matches[0])); err != nil {
			return err
		}
		matches = matches[1:]
	}
	return nil
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
