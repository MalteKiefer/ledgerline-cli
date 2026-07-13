package files

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// SafeJoin joins a local base directory with a manifest-derived slash path and
// returns ok=false if the result would escape base (via ".." or an absolute
// component). The Files store is zero-knowledge over an untrusted server, so a
// hostile record name like "../../.ssh/authorized_keys" must never write outside
// the target directory.
func SafeJoin(base, rel string) (string, bool) {
	p := filepath.Join(base, filepath.FromSlash(rel))
	r, err := filepath.Rel(base, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

// Subtree returns every non-trashed file at or under a folder path, plus the ids
// of that folder and all its descendant folders. The path must be a folder.
func Subtree(store *Store, path string) (files []FileView, folderIDs []string, err error) {
	tree := NewTree(store)
	rootID, ok := tree.FindFolder(path)
	if !ok {
		return nil, nil, fmt.Errorf("no such folder: %s", path)
	}

	// Collect the folder and all descendants via child links.
	inSet := map[string]bool{} // folder ids inside the subtree
	if rootID != nil {
		inSet[*rootID] = true
		folderIDs = append(folderIDs, *rootID)
	}
	changed := true
	for changed {
		changed = false
		for _, raw := range store.Folders() {
			fv, perr := parseFolder(raw)
			if perr != nil || fv.ID == "" || inSet[fv.ID] {
				continue
			}
			if fv.Parent != nil && inSet[*fv.Parent] {
				inSet[fv.ID] = true
				folderIDs = append(folderIDs, fv.ID)
				changed = true
			}
		}
	}

	for _, raw := range store.Files() {
		fv, perr := parseFile(raw)
		if perr != nil || fv.Trashed != "" || fv.ID == "" {
			continue
		}
		if (fv.Folder == nil && rootID == nil) || (fv.Folder != nil && inSet[*fv.Folder]) {
			files = append(files, fv)
		}
	}
	return files, folderIDs, nil
}

// FindFile resolves a slash path to a single non-trashed file, if one exists at
// exactly that path.
func FindFile(store *Store, path string) (FileView, bool) {
	want := strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	if want == "" {
		return FileView{}, false
	}
	tree := NewTree(store)
	for _, raw := range store.Files() {
		fv, err := parseFile(raw)
		if err != nil || fv.Trashed != "" || fv.Blob == "" {
			continue
		}
		if tree.FilePath(fv) == want {
			return fv, true
		}
	}
	return FileView{}, false
}

// IsFolder reports whether a path resolves to an existing folder (root counts).
func IsFolder(store *Store, path string) bool {
	_, ok := NewTree(store).FindFolder(path)
	return ok
}

// FindFolder resolves a slash path to a folder id without creating anything.
// The root ("") returns (nil, true); an unknown path returns (nil, false).
func (t *Tree) FindFolder(path string) (*string, bool) {
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	if path == "" {
		return nil, true
	}
	var cur *string
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || seg == "." {
			continue
		}
		id, ok := t.byParentName[parentKey(cur, seg)]
		if !ok {
			return nil, false
		}
		idc := id
		cur = &idc
	}
	return cur, true
}

// Children returns the immediate subfolders and files of a remote folder path
// (non-trashed). Membership is decided by resolved folder PATH, not id equality,
// so a file whose parent folder no longer exists is shown at the root — matching
// the web client. An unknown non-root path is an error.
func Children(store *Store, path string) (folders []FolderView, files []FileView, err error) {
	tree := NewTree(store)
	want := strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	if want != "" {
		if _, ok := tree.FindFolder(want); !ok {
			return nil, nil, fmt.Errorf("no such folder: %s", path)
		}
	}

	for _, raw := range store.Folders() {
		fv, perr := parseFolder(raw)
		if perr != nil || fv.Trashed != "" || fv.ID == "" {
			continue
		}
		if tree.FolderPath(fv.Parent) == want {
			folders = append(folders, fv)
		}
	}
	for _, raw := range store.Files() {
		fv, perr := parseFile(raw)
		if perr != nil || fv.Trashed != "" || fv.ID == "" || fv.Blob == "" {
			continue
		}
		if tree.FolderPath(fv.Folder) == want {
			files = append(files, fv)
		}
	}

	sort.Slice(folders, func(i, j int) bool { return strings.ToLower(folders[i].Name) < strings.ToLower(folders[j].Name) })
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name) })
	return folders, files, nil
}
