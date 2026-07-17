package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// Conflict policies.
const (
	ConflictKeepBoth = "keep-both"
	ConflictNewest   = "newest"
	ConflictSkip     = "skip"
)

// Delete policies.
const (
	DeleteBoth     = "both"      // propagate deletions in both directions
	DeleteAdditive = "additive"  // never delete; recreate the missing side
	DeleteToRemote = "to-remote" // local deletions trash remote; remote never deletes local
)

// SyncOptions configures one sync run.
type SyncOptions struct {
	Conflict string
	Delete   string
	Hidden   bool
	Override bool // on any difference, push the local copy over the remote
	Ignore   *Matcher
	DryRun   bool
	Log      func(string)
}

// SyncResult tallies what a run did.
type SyncResult struct {
	Uploaded      int
	Downloaded    int
	TrashedRemote int
	DeletedLocal  int
	Conflicts     int
	Skipped       int
	Failed        int
	ConflictPaths []string
}

// fileState is the last-synced snapshot of one path, used to detect which side
// changed since.
type fileState struct {
	LocalSize  int64  `json:"ls"`
	LocalMtime int64  `json:"lm"` // unix nano
	RemoteID   string `json:"rid"`
	RemoteBlob string `json:"rb"`
	RemoteSize int64  `json:"rs"`
}

// Syncer performs a bidirectional sync between one local directory and one
// remote folder subtree.
type Syncer struct {
	store      *Store
	up         *Uploader
	dl         *Downloader
	localDir   string
	remoteBase string
	opts       SyncOptions
}

// NewSyncer builds a syncer for a single mapping.
func NewSyncer(client *api.Client, store *Store, vaultKey []byte, localDir, remoteBase string, opts SyncOptions) *Syncer {
	if opts.Log == nil {
		opts.Log = func(string) {}
	}
	return &Syncer{
		store:      store,
		up:         NewUploader(client, store, vaultKey),
		dl:         NewDownloader(client, vaultKey),
		localDir:   localDir,
		remoteBase: strings.Trim(strings.ReplaceAll(remoteBase, "\\", "/"), "/"),
		opts:       opts,
	}
}

// localEntry is a scanned local file.
type localEntry struct {
	abs   string
	size  int64
	mtime int64 // unix nano
}

// Run executes one bidirectional pass and persists the new sync state.
func (s *Syncer) Run(ctx context.Context) (SyncResult, error) {
	var res SyncResult

	prev, err := loadSyncState(s.localDir, s.remoteBase)
	if err != nil {
		return res, err
	}
	local, err := s.scanLocal()
	if err != nil {
		return res, err
	}
	remote := map[string]Entry{}
	for _, e := range List(s.store, s.remoteBase) {
		if s.ignored(e.Path) {
			continue
		}
		remote[e.Path] = e
	}

	next := syncState{}
	for _, rel := range unionKeys(local, remote, prev) {
		if ctx.Err() != nil {
			break
		}
		st, keep := s.reconcile(ctx, rel, local[rel], remote[rel], prev[rel], &res)
		if keep {
			next[rel] = st
		}
	}

	if s.store.Dirty() && !s.opts.DryRun {
		if err := s.store.Save(context.WithoutCancel(ctx)); err != nil {
			return res, fmt.Errorf("save files manifest: %w", err)
		}
	}
	if !s.opts.DryRun {
		if err := saveSyncState(s.localDir, s.remoteBase, next); err != nil {
			return res, err
		}
	}
	return res, nil
}

// reconcile decides and performs the action for one path, returning the new
// state entry and whether to keep it.
func (s *Syncer) reconcile(ctx context.Context, rel string, l *localEntry, r Entry, prev fileState, res *SyncResult) (fileState, bool) {
	_, hadState := prevSeen(prev)
	hasLocal := l != nil
	hasRemote := r.View.ID != ""

	switch {
	case hasLocal && hasRemote:
		// Byte-identical (size + mtime) → nothing to do, even without a baseline.
		// This stops a first sync from flagging every pre-existing file as a
		// conflict.
		if s.sameFile(l, r) {
			res.Skipped++
			return stateOf(l, r), true
		}
		// --override: local always wins on any difference.
		if s.opts.Override {
			return s.push(ctx, rel, l, r.View.ID, res)
		}
		localChanged := !hadState || l.size != prev.LocalSize || l.mtime != prev.LocalMtime
		remoteChanged := !hadState || r.View.Blob != prev.RemoteBlob
		switch {
		case !localChanged && !remoteChanged:
			return stateOf(l, r), true
		case localChanged && !remoteChanged:
			return s.push(ctx, rel, l, r.View.ID, res)
		case !localChanged && remoteChanged:
			return s.pull(ctx, rel, l.abs, r, res)
		default:
			return s.conflict(ctx, rel, l, r, res)
		}

	case hasLocal && !hasRemote:
		if !hadState {
			return s.create(ctx, rel, l, res) // brand-new local
		}
		// Remote vanished since last sync.
		localChanged := l.size != prev.LocalSize || l.mtime != prev.LocalMtime
		if localChanged || s.opts.Delete == DeleteAdditive || s.opts.Delete == DeleteToRemote {
			return s.create(ctx, rel, l, res) // recreate remote (or keep a changed local)
		}
		return s.deleteLocal(rel, l.abs, res) // propagate remote deletion locally

	case !hasLocal && hasRemote:
		if !hadState {
			return s.download(ctx, rel, r, res) // brand-new remote
		}
		remoteChanged := r.View.Blob != prev.RemoteBlob
		if remoteChanged || s.opts.Delete == DeleteAdditive {
			return s.download(ctx, rel, r, res) // remote changed, or additive → keep it locally
		}
		return s.trashRemote(rel, r.View.ID, res) // propagate local deletion to remote

	default:
		return fileState{}, false // gone on both sides
	}
}

// --- actions ---

func (s *Syncer) create(ctx context.Context, rel string, l *localEntry, res *SyncResult) (fileState, bool) {
	if s.opts.DryRun {
		s.opts.Log("  + upload  " + rel)
		res.Uploaded++
		return fileState{}, false
	}
	data, err := os.ReadFile(l.abs)
	if err != nil {
		return s.fail(rel, err, res)
	}
	id, blob, err := s.up.Create(ctx, s.remotePath(rel), mimeOf(rel), toISO(l.mtime), data)
	if err != nil {
		return s.fail(rel, err, res)
	}
	res.Uploaded++
	s.opts.Log("  ↑ " + rel)
	return fileState{LocalSize: l.size, LocalMtime: l.mtime, RemoteID: id, RemoteBlob: blob, RemoteSize: l.size}, true
}

func (s *Syncer) push(ctx context.Context, rel string, l *localEntry, remoteID string, res *SyncResult) (fileState, bool) {
	if s.opts.DryRun {
		s.opts.Log("  ↑ update " + rel)
		res.Uploaded++
		return fileState{}, false
	}
	data, err := os.ReadFile(l.abs)
	if err != nil {
		return s.fail(rel, err, res)
	}
	blob, err := s.up.Replace(ctx, remoteID, mimeOf(rel), data)
	if err != nil {
		return s.fail(rel, err, res)
	}
	res.Uploaded++
	s.opts.Log("  ↑ " + rel)
	return fileState{LocalSize: l.size, LocalMtime: l.mtime, RemoteID: remoteID, RemoteBlob: blob, RemoteSize: l.size}, true
}

func (s *Syncer) pull(ctx context.Context, rel, dest string, r Entry, res *SyncResult) (fileState, bool) {
	if s.opts.DryRun {
		s.opts.Log("  ↓ " + rel)
		res.Downloaded++
		return fileState{}, false
	}
	data, err := s.dl.Fetch(ctx, r.View)
	if err != nil {
		return s.fail(rel, err, res)
	}
	mtime, err := writeLocalFile(dest, data)
	if err != nil {
		return s.fail(rel, err, res)
	}
	res.Downloaded++
	s.opts.Log("  ↓ " + rel)
	return fileState{LocalSize: int64(len(data)), LocalMtime: mtime, RemoteID: r.View.ID, RemoteBlob: r.View.Blob, RemoteSize: r.View.Size}, true
}

func (s *Syncer) download(ctx context.Context, rel string, r Entry, res *SyncResult) (fileState, bool) {
	dest, ok := SafeJoin(s.localDir, rel)
	if !ok {
		return s.fail(rel, fmt.Errorf("unsafe remote path, refusing to write outside %s", s.localDir), res)
	}
	return s.pull(ctx, rel, dest, r, res)
}

func (s *Syncer) trashRemote(rel, id string, res *SyncResult) (fileState, bool) {
	if s.opts.DryRun {
		s.opts.Log("  ✗ trash remote " + rel)
		res.TrashedRemote++
		return fileState{}, false
	}
	s.store.TrashFile(id, nowISOFiles())
	res.TrashedRemote++
	s.opts.Log("  ✗ remote " + rel)
	return fileState{}, false
}

func (s *Syncer) deleteLocal(rel, abs string, res *SyncResult) (fileState, bool) {
	if s.opts.DryRun {
		s.opts.Log("  ✗ delete local " + rel)
		res.DeletedLocal++
		return fileState{}, false
	}
	if err := os.Remove(abs); err != nil {
		return s.fail(rel, err, res)
	}
	res.DeletedLocal++
	s.opts.Log("  ✗ local " + rel)
	return fileState{}, false
}

func (s *Syncer) conflict(ctx context.Context, rel string, l *localEntry, r Entry, res *SyncResult) (fileState, bool) {
	res.Conflicts++
	res.ConflictPaths = append(res.ConflictPaths, rel)

	switch s.opts.Conflict {
	case ConflictSkip:
		s.opts.Log("  ! conflict (skipped) " + rel)
		return fileState{}, false // don't record state → re-detected next run

	case ConflictNewest:
		// Approximate remote mtime by its record's created time.
		remoteTime := parseISO(r.View.Created)
		if time.Unix(0, l.mtime).After(remoteTime) {
			s.opts.Log("  ! conflict → local newer " + rel)
			return s.push(ctx, rel, l, r.View.ID, res)
		}
		s.opts.Log("  ! conflict → remote newer " + rel)
		return s.pull(ctx, rel, l.abs, r, res)

	default: // keep-both
		// Save the remote copy alongside as a conflict-named file and upload it,
		// then push the local copy to the original path. Both sides keep both.
		conflictRel := conflictName(rel)
		if !s.opts.DryRun {
			data, err := s.dl.Fetch(ctx, r.View)
			if dest, ok := SafeJoin(s.localDir, conflictRel); err == nil && ok {
				if _, werr := writeLocalFile(dest, data); werr == nil {
					_, _, _ = s.up.Create(ctx, s.remotePath(conflictRel), mimeOf(conflictRel), nowISOFiles(), data)
				}
			}
		}
		s.opts.Log("  ! conflict → kept both " + rel)
		return s.push(ctx, rel, l, r.View.ID, res)
	}
}

func (s *Syncer) fail(rel string, err error, res *SyncResult) (fileState, bool) {
	res.Failed++
	s.opts.Log(fmt.Sprintf("  failed %s: %v", rel, err))
	return fileState{}, false
}

// --- helpers ---

// scanLocal walks the local directory into a rel-path map, applying ignore and
// hidden-file rules.
func (s *Syncer) scanLocal() (map[string]*localEntry, error) {
	out := map[string]*localEntry{}
	info, err := os.Stat(s.localDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil // an empty local side is valid (first download)
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", s.localDir)
	}

	err = filepath.WalkDir(s.localDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != s.localDir && !s.opts.Hidden && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if isJunkName(name) || (!s.opts.Hidden && strings.HasPrefix(name, ".")) {
			return nil
		}
		rel, rerr := filepath.Rel(s.localDir, p)
		if rerr != nil {
			return rerr
		}
		relSlash := filepath.ToSlash(rel)
		if s.ignored(relSlash) {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		out[relSlash] = &localEntry{abs: p, size: fi.Size(), mtime: fi.ModTime().UnixNano()}
		return nil
	})
	return out, err
}

func (s *Syncer) ignored(rel string) bool {
	return s.opts.Ignore != nil && s.opts.Ignore.Match(rel)
}

func (s *Syncer) remotePath(rel string) string {
	if s.remoteBase == "" {
		return rel
	}
	return s.remoteBase + "/" + rel
}

// sameFile reports whether the local and remote copies are the same content,
// using a cheap size + mtime heuristic. The remote's mtime is its record's
// Created time, which this tool sets from the local mtime on upload (see
// create/toISO), so a previously-synced pair matches exactly. Files uploaded
// elsewhere match only when size and timestamp happen to line up (within 1s).
func (s *Syncer) sameFile(l *localEntry, r Entry) bool {
	if l == nil || r.View.ID == "" || l.size != r.View.Size {
		return false
	}
	rt := parseISO(r.View.Created)
	if rt.IsZero() {
		return false
	}
	d := time.Unix(0, l.mtime).Sub(rt)
	if d < 0 {
		d = -d
	}
	return d < time.Second
}

func stateOf(l *localEntry, r Entry) fileState {
	return fileState{LocalSize: l.size, LocalMtime: l.mtime, RemoteID: r.View.ID, RemoteBlob: r.View.Blob, RemoteSize: r.View.Size}
}

// prevSeen reports whether a state entry was populated (zero value = unseen).
func prevSeen(s fileState) (fileState, bool) {
	return s, s.LocalSize != 0 || s.LocalMtime != 0 || s.RemoteID != "" || s.RemoteBlob != "" || s.RemoteSize != 0
}

// unionKeys returns the sorted union of keys across the three maps.
func unionKeys(a map[string]*localEntry, b map[string]Entry, c syncState) []string {
	set := map[string]bool{}
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	for k := range c {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// conflictName inserts a " (conflict <timestamp>)" marker before the extension.
func conflictName(rel string) string {
	ext := path.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	return stem + " (conflict " + time.Now().UTC().Format("2006-01-02 150405") + ")" + ext
}

// writeLocalFile writes data atomically and returns the resulting mod time. It
// refuses to write decrypted content through an existing symlink at the target.
func writeLocalFile(dest string, data []byte) (int64, error) {
	if fi, err := os.Lstat(dest); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("refusing to write through a symlink: %s", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return 0, err
	}
	tmp := dest + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	fi, err := os.Stat(dest)
	if err != nil {
		return 0, err
	}
	return fi.ModTime().UnixNano(), nil
}

func mimeOf(rel string) string { return mimeTypeByExt(rel) }
func toISO(nano int64) string  { return time.Unix(0, nano).UTC().Format("2006-01-02T15:04:05.000Z") }
func nowISOFiles() string      { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
func parseISO(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t
		}
	}
	return time.Time{}
}

// --- sync state persistence ---

type syncState map[string]fileState

// stateKey derives a stable filename for a mapping.
func stateKey(localDir, remoteBase string) string {
	abs, err := filepath.Abs(localDir)
	if err != nil {
		abs = localDir
	}
	sum := sha256.Sum256([]byte(remoteBase + "\x00" + abs))
	return hex.EncodeToString(sum[:])
}

func stateDir() (string, error) {
	base, err := config.Dir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "sync-state")
	return dir, os.MkdirAll(dir, 0o700)
}

func loadSyncState(localDir, remoteBase string) (syncState, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, stateKey(localDir, remoteBase)+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return syncState{}, nil
		}
		return nil, err
	}
	var st syncState
	if err := json.Unmarshal(data, &st); err != nil {
		return syncState{}, nil // a corrupt state just forces a fresh reconcile
	}
	return st, nil
}

func saveSyncState(localDir, remoteBase string, st syncState) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, stateKey(localDir, remoteBase)+".json"), data, 0o600)
}
