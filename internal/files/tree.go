// Package files holds the local-side helpers for the files commands: rendering
// the remote folder/file tree and the two-way sync engine. All server
// interaction goes through internal/api; there is no local crypto.
package files

import (
	"fmt"
	"io"
	"sort"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

// Node is one folder in the rendered tree. The root Node has ID 0 and holds the
// top-level (parent_id == nil) folders and files.
type Node struct {
	ID       int64
	Name     string
	Children []*Node
	Files    []api.FileEntry
}

// BuildTree assembles folders + files into a folder tree rooted at a synthetic
// node (ID 0). Folders whose parent is unknown (e.g. a trashed ancestor) attach
// to the root so nothing is dropped.
func BuildTree(folders []api.FileFolder, files []api.FileEntry) *Node {
	root := &Node{}
	byID := map[int64]*Node{0: root}
	for _, f := range folders {
		byID[f.ID] = &Node{ID: f.ID, Name: f.Name}
	}
	for _, f := range folders {
		n := byID[f.ID]
		parent := root
		if f.ParentID != nil {
			if p, ok := byID[*f.ParentID]; ok {
				parent = p
			}
		}
		parent.Children = append(parent.Children, n)
	}
	for _, file := range files {
		parent := root
		if file.FileFolderID != nil {
			if p, ok := byID[*file.FileFolderID]; ok {
				parent = p
			}
		}
		parent.Files = append(parent.Files, file)
	}
	root.sort()
	return root
}

// sort orders children and files by name for stable output.
func (n *Node) sort() {
	sort.Slice(n.Children, func(i, j int) bool { return n.Children[i].Name < n.Children[j].Name })
	sort.Slice(n.Files, func(i, j int) bool { return n.Files[i].Name < n.Files[j].Name })
	for _, c := range n.Children {
		c.sort()
	}
}

// Print writes an indented tree: folders with a trailing slash, files with their
// id and size. The synthetic root itself is not printed.
func (n *Node) Print(w io.Writer) {
	for _, c := range n.Children {
		c.printAt(w, 0)
	}
	for _, f := range n.Files {
		printFile(w, f, 0)
	}
}

func (n *Node) printAt(w io.Writer, depth int) {
	fmt.Fprintf(w, "%*s%s/  [folder %d]\n", depth*2, "", n.Name, n.ID)
	for _, c := range n.Children {
		c.printAt(w, depth+1)
	}
	for _, f := range n.Files {
		printFile(w, f, depth+1)
	}
}

func printFile(w io.Writer, f api.FileEntry, depth int) {
	fmt.Fprintf(w, "%*s%s  [%d, %s]\n", depth*2, "", f.Name, f.ID, humanBytes(f.Size))
}

// humanBytes formats a byte count with a binary unit suffix.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
