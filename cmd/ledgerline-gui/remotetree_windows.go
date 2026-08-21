//go:build windows

package main

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/clientset"
)

// The server's folder tree, for the folder browser.
//
// One request returns the whole tree and every file in it, and the browser
// navigates that in the page. The alternative — a request per folder — would
// mean a spinner on every click and a listing that can disagree with itself
// halfway down. The files listing is the only endpoint there is anyway: there is
// no "children of this folder" call, so a per-click browser would fetch
// everything each time regardless.

// remoteTree is the whole picture the browser needs.
type remoteTree struct {
	Folders []remoteFolder `json:"folders"`
	Files   []remoteFile   `json:"files"`
	Error   string         `json:"error"`
}

// remoteFolder is one folder, identified by its slash path. Parent is the
// path of its container, empty for a top-level folder.
type remoteFolder struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Parent string `json:"parent"`
}

// remoteFile is shown but never selectable. A folder browser that hides the
// files makes you guess whether you are in the right place; showing them, greyed
// out, is what every file dialog does and why they are legible.
type remoteFile struct {
	Parent string `json:"parent"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
}

func (s *settings) remoteTree() (remoteTree, error) { return loadRemoteTree() }

func loadRemoteTree() (remoteTree, error) {
	client, _, err := clientset.Authenticated()
	if err != nil {
		return remoteTree{Error: "Sign in from the tray first."}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	folders, files, _, err := client.FilesData(ctx)
	if err != nil {
		return remoteTree{Error: "Could not read the folders: " + firstLine(err.Error())}, nil
	}

	byID := make(map[int64]api.FileFolder, len(folders))
	for _, f := range folders {
		byID[f.ID] = f
	}

	tree := remoteTree{
		Folders: make([]remoteFolder, 0, len(folders)),
		Files:   make([]remoteFile, 0, len(files)),
	}
	for _, f := range folders {
		full := folderPath(byID, f.ID)
		tree.Folders = append(tree.Folders, remoteFolder{
			Path:   full,
			Name:   f.Name,
			Parent: parentOf(full),
		})
	}
	for _, f := range files {
		var dir string
		if f.FileFolderID != nil {
			dir = folderPath(byID, *f.FileFolderID)
		}
		tree.Files = append(tree.Files, remoteFile{Parent: dir, Name: f.Name, Size: f.Size})
	}

	// Sorted here rather than in the page: the same order in both windows, and
	// one place to change it.
	sort.Slice(tree.Folders, func(i, j int) bool {
		return strings.ToLower(tree.Folders[i].Path) < strings.ToLower(tree.Folders[j].Path)
	})
	sort.Slice(tree.Files, func(i, j int) bool {
		if tree.Files[i].Parent != tree.Files[j].Parent {
			return tree.Files[i].Parent < tree.Files[j].Parent
		}
		return strings.ToLower(tree.Files[i].Name) < strings.ToLower(tree.Files[j].Name)
	})
	return tree, nil
}

// parentOf is the containing folder's path, empty at the top level.
func parentOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return ""
}

// createRemoteFolder makes a folder inside parent and returns its full path, so
// the browser can walk straight into it.
func (s *settings) createRemoteFolder(parent, name string) (string, error) {
	return createRemoteFolderIn(parent, name)
}

func createRemoteFolderIn(parent, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("give the folder a name")
	}
	// The server treats the name as one path element; a separator in it would
	// either be rejected or create something the browser cannot then find.
	if strings.ContainsAny(name, `/\`) {
		return "", errors.New("a folder name cannot contain a slash")
	}

	client, _, err := clientset.Authenticated()
	if err != nil {
		return "", errors.New("sign in from the tray first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	parentID, err := resolveRemoteFolderID(ctx, client, parent)
	if err != nil {
		return "", err
	}
	folder, err := client.CreateFolder(ctx, name, parentID)
	if err != nil {
		return "", errors.New(firstLine(err.Error()))
	}
	return path.Join(parent, folder.Name), nil
}
