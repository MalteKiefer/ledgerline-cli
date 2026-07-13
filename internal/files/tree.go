package files

import (
	"strings"
)

// Tree resolves between folder ids and slash-joined paths over the store's
// current folders, creating folders on demand. It stays in sync with folders it
// creates so repeated resolutions reuse them.
type Tree struct {
	store        *Store
	folders      map[string]FolderView // id -> folder (non-trashed)
	byParentName map[string]string     // parentKey -> folder id
}

// NewTree indexes the store's current (non-trashed) folders.
func NewTree(store *Store) *Tree {
	t := &Tree{store: store, folders: map[string]FolderView{}, byParentName: map[string]string{}}
	for _, raw := range store.Folders() {
		fv, err := parseFolder(raw)
		if err != nil || fv.Trashed != "" || fv.ID == "" {
			continue
		}
		t.folders[fv.ID] = fv
		t.byParentName[parentKey(fv.Parent, fv.Name)] = fv.ID
	}
	return t
}

// FolderPath returns the slash-joined path of a folder id (empty for root).
func (t *Tree) FolderPath(id *string) string {
	var parts []string
	cur := id
	seen := map[string]bool{}
	for cur != nil {
		fv, ok := t.folders[*cur]
		if !ok || seen[*cur] {
			break
		}
		seen[*cur] = true
		parts = append([]string{fv.Name}, parts...)
		cur = fv.Parent
	}
	return strings.Join(parts, "/")
}

// FilePath returns a file's full slash path (folder path + name).
func (t *Tree) FilePath(f FileView) string {
	dir := t.FolderPath(f.Folder)
	if dir == "" {
		return f.Name
	}
	return dir + "/" + f.Name
}

// EnsureFolder resolves a slash path to a folder id, creating any missing
// folders under the tree. An empty path returns nil (root).
func (t *Tree) EnsureFolder(path string) (*string, error) {
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	if path == "" {
		return nil, nil
	}
	var cur *string
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." {
			continue
		}
		key := parentKey(cur, part)
		if id, ok := t.byParentName[key]; ok {
			idCopy := id
			cur = &idCopy
			continue
		}
		raw, id, err := newFolderRecord(part, cur)
		if err != nil {
			return nil, err
		}
		t.store.AddFolder(raw)
		t.folders[id] = FolderView{ID: id, Name: part, Parent: cur}
		t.byParentName[key] = id
		idCopy := id
		cur = &idCopy
	}
	return cur, nil
}

// parentKey builds the lookup key for a folder name under a parent (case-folded,
// matching the web's case-insensitive folder reuse).
func parentKey(parent *string, name string) string {
	p := ""
	if parent != nil {
		p = *parent
	}
	return p + "/" + strings.ToLower(name)
}
