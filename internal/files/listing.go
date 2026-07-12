package files

import (
	"fmt"
	"sort"
	"strings"
)

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
