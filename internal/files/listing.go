package files

import (
	"fmt"
	"sort"
	"strings"
)

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
// (non-trashed). An unknown path is an error.
func Children(store *Store, path string) (folders []FolderView, files []FileView, err error) {
	tree := NewTree(store)
	folderID, ok := tree.FindFolder(path)
	if !ok {
		return nil, nil, fmt.Errorf("no such folder: %s", path)
	}

	for _, raw := range store.Folders() {
		fv, perr := parseFolder(raw)
		if perr != nil || fv.Trashed != "" || fv.ID == "" {
			continue
		}
		if samePtr(fv.Parent, folderID) {
			folders = append(folders, fv)
		}
	}
	for _, raw := range store.Files() {
		fv, perr := parseFile(raw)
		if perr != nil || fv.Trashed != "" || fv.ID == "" || fv.Blob == "" {
			continue
		}
		if samePtr(fv.Folder, folderID) {
			files = append(files, fv)
		}
	}

	sort.Slice(folders, func(i, j int) bool { return strings.ToLower(folders[i].Name) < strings.ToLower(folders[j].Name) })
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name) })
	return folders, files, nil
}

// samePtr reports whether two folder-id pointers refer to the same folder (both
// nil = root).
func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
