// Package webdavfs exposes the remote Files module as a webdav.FileSystem, so
// the CLI can serve a local WebDAV endpoint that an operating system mounts as a
// network drive (Windows "net use", macOS Finder, Linux davfs2/gio).
//
// Everything is a REST call against the server: nothing is cached on disk except
// the body of a file currently being read or written, which lives in a temp file
// that is removed when the handle closes. Paths are mapped to folder/file ids
// through a short-lived snapshot of GET /files/data, refreshed on every mutation
// and whenever it goes stale, because a WebDAV client issues many small
// PROPFIND/GET calls in a row and re-listing the whole tree per call would be
// wasteful.
package webdavfs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/webdav"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

// snapshotTTL bounds how stale the path→id map may be. A mutation through this
// filesystem invalidates it immediately; the TTL only covers changes made
// elsewhere (web UI, another client) while a mount is open.
const snapshotTTL = 5 * time.Second

// ErrReadOnly is returned for every mutating operation when the filesystem was
// created read-only.
var ErrReadOnly = errors.New("webdav: mount is read-only")

// FS is a webdav.FileSystem backed by the Ledgerline Files API.
type FS struct {
	client   *api.Client
	readOnly bool

	mu       sync.Mutex
	folders  map[string]api.FileFolder // clean path ("/a/b") -> folder
	files    map[string]api.FileEntry  // clean path ("/a/b.txt") -> file
	children map[int64][]string        // folder id -> child paths (root = 0)
	fetched  time.Time
}

// New builds a filesystem over client. readOnly refuses every write, which is
// the safe default for a mount used just to browse.
func New(client *api.Client, readOnly bool) *FS {
	return &FS{client: client, readOnly: readOnly}
}

// clean normalises a WebDAV path to a rooted, slash-separated, trailing-slash-free
// form ("/" stays "/").
func clean(name string) string {
	if name == "" {
		return "/"
	}
	name = strings.ReplaceAll(name, "\\", "/")
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	name = path.Clean(name)
	return name
}

// refresh rebuilds the path→id maps from a fresh GET /files/data. The caller
// must hold mu.
func (f *FS) refresh(ctx context.Context) error {
	folders, files, _, err := f.client.FilesData(ctx)
	if err != nil {
		return err
	}
	byID := make(map[int64]api.FileFolder, len(folders))
	for _, fo := range folders {
		if fo.DeletedAt != nil {
			continue // trashed folders are not part of the mounted tree
		}
		byID[fo.ID] = fo
	}
	// Resolve each folder's full path by walking up to the root. A cycle (which
	// the server rejects, but a malformed response could still carry) is bounded
	// by the folder count so this can never spin.
	pathOf := make(map[int64]string, len(byID))
	var resolve func(id int64, depth int) string
	resolve = func(id int64, depth int) string {
		if p, ok := pathOf[id]; ok {
			return p
		}
		fo, ok := byID[id]
		if !ok || depth > len(byID)+1 {
			return ""
		}
		parent := "/"
		if fo.ParentID != nil {
			parent = resolve(*fo.ParentID, depth+1)
			if parent == "" {
				return ""
			}
		}
		p := path.Join(parent, fo.Name)
		pathOf[id] = p
		return p
	}

	f.folders = map[string]api.FileFolder{}
	f.files = map[string]api.FileEntry{}
	f.children = map[int64][]string{}
	for id := range byID {
		if p := resolve(id, 0); p != "" {
			f.folders[p] = byID[id]
		}
	}
	for p, fo := range f.folders {
		parentKey := int64(0)
		if fo.ParentID != nil {
			parentKey = *fo.ParentID
		}
		f.children[parentKey] = append(f.children[parentKey], p)
	}
	for _, fe := range files {
		if fe.DeletedAt != nil {
			continue
		}
		dir := "/"
		parentKey := int64(0)
		if fe.FileFolderID != nil {
			parentKey = *fe.FileFolderID
			d, ok := pathOf[parentKey]
			if !ok {
				continue // file in a folder we could not resolve: skip, do not guess
			}
			dir = d
		}
		p := path.Join(dir, fe.Name)
		f.files[p] = fe
		f.children[parentKey] = append(f.children[parentKey], p)
	}
	f.fetched = time.Now()
	return nil
}

// snapshot returns the current maps, refreshing them when stale.
func (f *FS) snapshot(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.folders != nil && time.Since(f.fetched) < snapshotTTL {
		return nil
	}
	return f.refresh(ctx)
}

// invalidate forces the next lookup to re-read the tree; called after every
// mutation so a client's follow-up PROPFIND sees its own write.
func (f *FS) invalidate() {
	f.mu.Lock()
	f.folders = nil
	f.mu.Unlock()
}

// lookupFolder resolves a folder path. The root always exists (id 0 = "no
// folder", the server's root).
func (f *FS) lookupFolder(name string) (api.FileFolder, bool) {
	name = clean(name)
	if name == "/" {
		return api.FileFolder{ID: 0, Name: ""}, true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fo, ok := f.folders[name]
	return fo, ok
}

// lookupFile resolves a file path.
func (f *FS) lookupFile(name string) (api.FileEntry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fe, ok := f.files[clean(name)]
	return fe, ok
}

// folderIDFor maps a directory path to the pointer form the API wants (nil at
// the root).
func (f *FS) folderIDFor(dir string) (*int64, bool) {
	fo, ok := f.lookupFolder(dir)
	if !ok {
		return nil, false
	}
	if fo.ID == 0 {
		return nil, true
	}
	id := fo.ID
	return &id, true
}

// Stat implements webdav.FileSystem.
func (f *FS) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	if err := f.snapshot(ctx); err != nil {
		return nil, err
	}
	name = clean(name)
	if fo, ok := f.lookupFolder(name); ok {
		return folderInfo{fo}, nil
	}
	if fe, ok := f.lookupFile(name); ok {
		return fileInfo{fe}, nil
	}
	return nil, os.ErrNotExist
}

// Mkdir implements webdav.FileSystem.
func (f *FS) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	if f.readOnly {
		return ErrReadOnly
	}
	if err := f.snapshot(ctx); err != nil {
		return err
	}
	name = clean(name)
	if name == "/" {
		return os.ErrExist
	}
	if _, ok := f.lookupFolder(name); ok {
		return os.ErrExist
	}
	parentID, ok := f.folderIDFor(path.Dir(name))
	if !ok {
		return os.ErrNotExist
	}
	if _, err := f.client.CreateFolder(ctx, path.Base(name), parentID); err != nil {
		return err
	}
	f.invalidate()
	return nil
}

// RemoveAll implements webdav.FileSystem. A folder goes to the server-side trash
// with its subtree; a file goes to the trash too — nothing is force-deleted from
// a mount, so a client's stray delete stays recoverable.
func (f *FS) RemoveAll(ctx context.Context, name string) error {
	if f.readOnly {
		return ErrReadOnly
	}
	if err := f.snapshot(ctx); err != nil {
		return err
	}
	name = clean(name)
	if name == "/" {
		return errors.New("webdav: refusing to delete the mount root")
	}
	if fe, ok := f.lookupFile(name); ok {
		if err := f.client.DeleteFile(ctx, fe.ID); err != nil {
			return err
		}
		f.invalidate()
		return nil
	}
	if fo, ok := f.lookupFolder(name); ok && fo.ID != 0 {
		if err := f.client.DeleteFolder(ctx, fo.ID); err != nil {
			return err
		}
		f.invalidate()
		return nil
	}
	return os.ErrNotExist
}

// Rename implements webdav.FileSystem: a rename inside one directory, a move
// between directories, or both at once.
func (f *FS) Rename(ctx context.Context, oldName, newName string) error {
	if f.readOnly {
		return ErrReadOnly
	}
	if err := f.snapshot(ctx); err != nil {
		return err
	}
	oldName, newName = clean(oldName), clean(newName)
	if oldName == "/" || newName == "/" {
		return errors.New("webdav: refusing to rename the mount root")
	}
	newParent, ok := f.folderIDFor(path.Dir(newName))
	if !ok {
		return os.ErrNotExist
	}

	if fe, ok := f.lookupFile(oldName); ok {
		update := api.FileUpdate{}
		if base := path.Base(newName); base != fe.Name {
			update.Name = &base
		}
		oldParent, _ := f.folderIDFor(path.Dir(oldName))
		if !sameFolder(oldParent, newParent) {
			update.FolderID = &newParent
		}
		if update.Name == nil && update.FolderID == nil {
			return nil
		}
		if _, err := f.client.UpdateFile(ctx, fe.ID, update, fe.Version); err != nil {
			return err
		}
		f.invalidate()
		return nil
	}

	fo, ok := f.lookupFolder(oldName)
	if !ok || fo.ID == 0 {
		return os.ErrNotExist
	}
	oldParent, _ := f.folderIDFor(path.Dir(oldName))
	if !sameFolder(oldParent, newParent) {
		if _, err := f.client.MoveFolder(ctx, fo.ID, newParent); err != nil {
			return err
		}
	}
	if base := path.Base(newName); base != fo.Name {
		if _, err := f.client.RenameFolder(ctx, fo.ID, base); err != nil {
			return err
		}
	}
	f.invalidate()
	return nil
}

// sameFolder compares two optional folder ids (nil = root).
func sameFolder(a, b *int64) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// OpenFile implements webdav.FileSystem. A directory yields a listing handle; a
// file yields a temp-file-backed handle that downloads on first read and uploads
// on close when it was written to.
func (f *FS) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	if err := f.snapshot(ctx); err != nil {
		return nil, err
	}
	name = clean(name)
	writing := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0
	if writing && f.readOnly {
		return nil, ErrReadOnly
	}

	if fo, ok := f.lookupFolder(name); ok {
		if writing {
			return nil, os.ErrExist
		}
		return f.openDir(ctx, name, fo)
	}

	fe, exists := f.lookupFile(name)
	if !exists {
		if !writing || flag&os.O_CREATE == 0 {
			return nil, os.ErrNotExist
		}
		// A create needs its parent directory to exist; WebDAV clients rely on
		// the 404 to know a MKCOL is missing.
		if _, ok := f.folderIDFor(path.Dir(name)); !ok {
			return nil, os.ErrNotExist
		}
	}
	if exists && writing && flag&os.O_EXCL != 0 {
		return nil, os.ErrExist
	}
	return f.openFile(ctx, name, fe, exists, writing, flag)
}

// openDir builds a read-only directory handle with its children resolved.
func (f *FS) openDir(ctx context.Context, name string, fo api.FileFolder) (webdav.File, error) {
	f.mu.Lock()
	childPaths := append([]string(nil), f.children[fo.ID]...)
	infos := make([]fs.FileInfo, 0, len(childPaths))
	for _, p := range childPaths {
		if child, ok := f.folders[p]; ok {
			infos = append(infos, folderInfo{child})
			continue
		}
		if child, ok := f.files[p]; ok {
			infos = append(infos, fileInfo{child})
		}
	}
	f.mu.Unlock()
	_, _ = ctx, name
	return &dirHandle{info: folderInfo{fo}, entries: infos}, nil
}
