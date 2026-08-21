//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/applog"
	"github.com/MalteKiefer/ledgerline-cli/internal/clientset"
	"github.com/MalteKiefer/ledgerline-cli/internal/deskprefs"
	"github.com/MalteKiefer/ledgerline-cli/internal/deskui"
	"github.com/MalteKiefer/ledgerline-cli/internal/syncconfig"
)

// The Explorer context-menu handler.
//
// Each verb runs as its own short-lived process: Explorer launches
// `ledgerline-gui --context <verb> "<path>"`, a window opens, the work happens,
// the window closes. That is deliberate. The alternative — routing verbs to the
// already-running tray — needs an IPC channel, and a local channel that can
// make the tray upload, share or decrypt a file is a channel worth attacking.
// A separate process reads the same session from the keyring and needs no
// listener at all.
//
// Every verb needs the same thing first: what is this path, to the server? A
// file inside a synced folder has a counterpart there, and share, encrypt and
// decrypt are about that counterpart. A file outside one does not, and the only
// honest answers are "upload it" or "say so".

// contextRequest is one invocation.
type contextRequest struct {
	Verb string
	Path string
}

// runContextVerb is the entry point when --context was passed. It never returns
// an error to the caller: this process exists to show the user what happened,
// so a failure is a sentence in a window, not an exit code nobody sees.
func runContextVerb(log *applog.Logger, req contextRequest) {
	c := &contextWindow{log: log, req: req}
	if err := deskui.Run(deskui.Options{
		Title:    "Ledgerline",
		Width:    560,
		Height:   440,
		Body:     contextBody,
		Script:   contextScript,
		Bindings: c.bindings(),
	}); err != nil && log != nil {
		log.Printf("context window (%s): %v", req.Verb, err)
	}
}

type contextWindow struct {
	log *applog.Logger
	req contextRequest
}

func (c *contextWindow) bindings() []deskui.Binding {
	return []deskui.Binding{
		{Name: "describe", Func: c.describe},
		{Name: "runVerb", Func: c.run},
		{Name: "loadKeyring", Func: c.keyring},
		{Name: "remoteFolderList", Func: remoteFolders},
		{Name: "copyText", Func: copyToClipboard},
		{Name: "openPath", Func: func(kind, target string) error {
			switch kind {
			case "folder":
				openFolder(target)
			case "url":
				openBrowserURL(target)
			}
			return nil
		}},
	}
}

// targetView tells the page what it is working with, so it can ask the right
// question before doing anything.
type targetView struct {
	Verb     string `json:"verb"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size"`
	SignedIn bool   `json:"signedIn"`

	// Synced is true when the path lies inside a configured pair, which is what
	// makes a server-side action possible at all.
	Synced bool   `json:"synced"`
	Remote string `json:"remote"` // the remote path, when synced
	Error  string `json:"error"`
}

func (c *contextWindow) describe() (targetView, error) {
	v := targetView{Verb: c.req.Verb, Path: c.req.Path, Name: filepath.Base(c.req.Path)}

	info, err := os.Stat(c.req.Path)
	if err != nil {
		v.Error = "That path is no longer there."
		return v, nil
	}
	v.IsDir = info.IsDir()
	if !v.IsDir {
		v.Size = info.Size()
	}

	if _, _, err := clientset.Authenticated(); err == nil {
		v.SignedIn = true
	}

	if remote, ok := remoteForLocal(c.req.Path); ok {
		v.Synced, v.Remote = true, remote
	}
	return v, nil
}

// remoteForLocal maps a local path to its counterpart on the server, using the
// configured pairs. It returns false when the path is not inside any of them.
func remoteForLocal(local string) (string, bool) {
	abs, err := filepath.Abs(local)
	if err != nil {
		return "", false
	}
	f, err := syncconfig.Load()
	if err != nil {
		return "", false
	}
	for _, p := range f.Pairs {
		root, err := filepath.Abs(p.Local)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if rel == "." {
			return p.Remote, true
		}
		return path.Join(p.Remote, filepath.ToSlash(rel)), true
	}
	return "", false
}

// resolveRemoteFile finds the server's file record for a remote path. The files
// listing is the only way: there is no "get by path" endpoint, and inventing a
// client-side cache of the tree would be one more thing to go stale.
func resolveRemoteFile(ctx context.Context, client *api.Client, remote string) (api.FileEntry, error) {
	folders, files, _, err := client.FilesData(ctx)
	if err != nil {
		return api.FileEntry{}, err
	}
	byID := make(map[int64]api.FileFolder, len(folders))
	for _, f := range folders {
		byID[f.ID] = f
	}
	want := strings.TrimPrefix(path.Clean(remote), "/")
	for _, f := range files {
		var dir string
		if f.FileFolderID != nil {
			dir = folderPath(byID, *f.FileFolderID)
		}
		if strings.EqualFold(path.Join(dir, f.Name), want) {
			return f, nil
		}
	}
	return api.FileEntry{}, fmt.Errorf("the server has no file at %s yet", want)
}

// resolveRemoteFolderID finds the folder id for a remote path. Nil is the root,
// which is a real destination rather than a missing one — hence a pointer.
func resolveRemoteFolderID(ctx context.Context, client *api.Client, remote string) (*int64, error) {
	remote = strings.Trim(path.Clean(remote), "/.")
	if remote == "" {
		return nil, nil
	}
	folders, _, _, err := client.FilesData(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]api.FileFolder, len(folders))
	for _, f := range folders {
		byID[f.ID] = f
	}
	for _, f := range folders {
		if strings.EqualFold(folderPath(byID, f.ID), remote) {
			id := f.ID
			return &id, nil
		}
	}
	return nil, fmt.Errorf("the server has no folder %s", remote)
}

// keyEntry is one signing or recipient key, for the encrypt dialog.
type keyEntry struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"` // "own" or "recipient"
	Type        string `json:"type"` // "pgp" or "smime"
	Label       string `json:"label"`
	Fingerprint string `json:"fingerprint"`
}

// keyring reads the account's own keys and its stored recipients.
//
// The keys live on the server, which is where the encryption happens: this is
// the same keyring the web app's Files module uses, so a file encrypted from
// Explorer opens in the browser and the other way round.
func (c *contextWindow) keyring() ([]keyEntry, error) {
	client, _, err := clientset.Authenticated()
	if err != nil {
		return nil, errors.New("sign in first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	keys, recipients, err := client.Keyring(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]keyEntry, 0, len(keys)+len(recipients))
	add := func(k api.CryptoKey, kind string) {
		fp := ""
		if k.Fingerprint != nil {
			fp = *k.Fingerprint
		}
		out = append(out, keyEntry{
			ID: k.ID, Kind: kind, Type: k.Type,
			Label: firstNonEmpty(k.Label, shortFingerprint(fp)), Fingerprint: fp,
		})
	}
	for _, k := range keys {
		add(k, "own")
	}
	for _, r := range recipients {
		add(r, "recipient")
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "own" // your own keys first: one of them signs
		}
		return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label)
	})
	return out, nil
}

// verbResult is what the page shows when the work is done.
type verbResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	// Link is a share URL to copy, when the verb produced one.
	Link string `json:"link"`
	// Folder is a local path to reveal, when the verb wrote a file.
	Folder string `json:"folder"`
}

// run performs the verb. options carries whatever the page collected: the
// remote folder for an upload, the chosen keys for an encryption.
func (c *contextWindow) run(options map[string]string) (verbResult, error) {
	client, sess, err := clientset.Authenticated()
	if err != nil {
		return verbResult{Message: "Sign in from the tray first."}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel()

	switch c.req.Verb {
	case "share":
		return c.doShare(ctx, client, sess.ServerURL)
	case "encrypt":
		return c.doEncrypt(ctx, client, options)
	case "decrypt":
		return c.doDecrypt(ctx, client)
	case "upload":
		return c.doUpload(ctx, client, options["folder"])
	case "gallery":
		return c.doGallery(ctx, client)
	case "openweb":
		return c.doOpenWeb(sess.ServerURL)
	case "sync":
		return c.doSync(options)
	}
	return verbResult{Message: "Unknown action: " + c.req.Verb}, nil
}

func (c *contextWindow) doShare(ctx context.Context, client *api.Client, server string) (verbResult, error) {
	remote, ok := remoteForLocal(c.req.Path)
	if !ok {
		return verbResult{Message: notSyncedMessage}, nil
	}
	file, err := resolveRemoteFile(ctx, client, remote)
	if err != nil {
		return verbResult{Message: firstLine(err.Error())}, nil
	}
	share, err := client.CreateFileShare(ctx, api.CreateFileShareInput{Kind: "file", FileID: &file.ID})
	if err != nil {
		return verbResult{Message: "Could not create the link: " + firstLine(err.Error())}, nil
	}
	link := strings.TrimSuffix(server, "/") + "/file-share/" + share.Token
	if err := copyToClipboard(link); err != nil {
		return verbResult{OK: true, Link: link,
			Message: "Link created, but it could not be put on the clipboard."}, nil
	}
	if c.log != nil {
		c.log.Printf("context share: created a link for %s", file.Name)
	}
	return verbResult{OK: true, Link: link, Message: "Link copied to the clipboard."}, nil
}

func (c *contextWindow) doEncrypt(ctx context.Context, client *api.Client, options map[string]string) (verbResult, error) {
	remote, ok := remoteForLocal(c.req.Path)
	if !ok {
		return verbResult{Message: notSyncedMessage}, nil
	}
	keyID, err := parseID(options["key"])
	if err != nil {
		return verbResult{Message: "Choose the key to encrypt with."}, nil
	}
	recipients := parseIDs(options["recipients"])

	info, err := os.Stat(c.req.Path)
	if err != nil {
		return verbResult{Message: "That path is no longer there."}, nil
	}

	if info.IsDir() {
		folderID, err := resolveRemoteFolderID(ctx, client, remote)
		if err != nil {
			return verbResult{Message: firstLine(err.Error())}, nil
		}
		if folderID == nil {
			return verbResult{Message: "The top-level folder cannot be encrypted as one file."}, nil
		}
		if _, err := client.EncryptFolder(ctx, *folderID, keyID, recipients); err != nil {
			return verbResult{Message: "Could not encrypt it: " + firstLine(err.Error())}, nil
		}
		return verbResult{OK: true,
			Message: "Encrypted on the server. The next sync brings the encrypted copies here."}, nil
	}

	file, err := resolveRemoteFile(ctx, client, remote)
	if err != nil {
		return verbResult{Message: firstLine(err.Error())}, nil
	}
	if _, err := client.EncryptFile(ctx, file.ID, keyID, recipients); err != nil {
		return verbResult{Message: "Could not encrypt it: " + firstLine(err.Error())}, nil
	}
	if c.log != nil {
		c.log.Printf("context encrypt: %s with key %d and %d recipients", file.Name, keyID, len(recipients))
	}
	return verbResult{OK: true, Folder: filepath.Dir(c.req.Path),
		Message: "Encrypted on the server. The next sync brings the encrypted copy here."}, nil
}

func (c *contextWindow) doDecrypt(ctx context.Context, client *api.Client) (verbResult, error) {
	remote, ok := remoteForLocal(c.req.Path)
	if !ok {
		return verbResult{Message: notSyncedMessage}, nil
	}
	file, err := resolveRemoteFile(ctx, client, remote)
	if err != nil {
		return verbResult{Message: firstLine(err.Error())}, nil
	}
	// No passphrase and no chosen key: the server holds the private material and
	// picks the one that fits. A key that needs a passphrase fails with a clear
	// error, which is better than a prompt for something we cannot verify.
	if _, err := client.DecryptFile(ctx, file.ID, 0, nil); err != nil {
		return verbResult{Message: "Could not decrypt it: " + firstLine(err.Error())}, nil
	}
	return verbResult{OK: true, Folder: filepath.Dir(c.req.Path),
		Message: "Decrypted on the server. The next sync brings the plain copy here."}, nil
}

// opener hands the client a way to re-open the file, which is how it streams an
// upload instead of holding the whole thing in memory.
func opener(path string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return os.Open(path) } //nolint:gosec // the path the user right-clicked
}

// uploadOne picks the transfer shape by size. The chunked path exists for files
// one request cannot carry; using it for everything would add three round trips
// to a 2 kB text file.
func uploadOne(ctx context.Context, client *api.Client, path string, folderID *int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	const chunkAbove = 8 << 20
	if info.Size() > chunkAbove {
		_, err = client.UploadFileChunked(ctx, path, filepath.Base(path), folderID, nil)
		return err
	}
	_, err = client.UploadFile(ctx, filepath.Base(path), folderID, opener(path))
	return err
}

func (c *contextWindow) doUpload(ctx context.Context, client *api.Client, remoteFolder string) (verbResult, error) {
	folderID, err := resolveRemoteFolderID(ctx, client, remoteFolder)
	if err != nil {
		return verbResult{Message: firstLine(err.Error())}, nil
	}
	info, err := os.Stat(c.req.Path)
	if err != nil {
		return verbResult{Message: "That path is no longer there."}, nil
	}
	if info.IsDir() {
		n, err := uploadTree(ctx, client, c.req.Path, folderID)
		if err != nil {
			return verbResult{Message: fmt.Sprintf("Uploaded %d file(s), then stopped: %s", n, firstLine(err.Error()))}, nil
		}
		return verbResult{OK: true, Message: fmt.Sprintf("Uploaded %s.", plural(n, "file", "files"))}, nil
	}
	if err := uploadOne(ctx, client, c.req.Path, folderID); err != nil {
		return verbResult{Message: "Upload failed: " + firstLine(err.Error())}, nil
	}
	return verbResult{OK: true, Message: "Uploaded " + filepath.Base(c.req.Path) + "."}, nil
}

// uploadTree walks a directory, creating folders as it goes. It reports how
// many files landed before any failure, because "it stopped somewhere" is not
// an answer a user can act on.
func uploadTree(ctx context.Context, client *api.Client, root string, parent *int64) (int, error) {
	prefs, _ := deskprefs.Load()
	created := map[string]*int64{".": parent}
	count := 0

	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		if prefs.Excluded(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			parentID := created[filepath.ToSlash(filepath.Dir(rel))]
			folder, ferr := client.CreateFolder(ctx, d.Name(), parentID)
			if ferr != nil {
				return ferr
			}
			id := folder.ID
			created[filepath.ToSlash(rel)] = &id
			return nil
		}
		into := created[filepath.ToSlash(filepath.Dir(rel))]
		if uerr := uploadOne(ctx, client, p, into); uerr != nil {
			return uerr
		}
		count++
		return nil
	})
	return count, err
}

func (c *contextWindow) doGallery(ctx context.Context, client *api.Client) (verbResult, error) {
	info, err := os.Stat(c.req.Path)
	if err != nil {
		return verbResult{Message: "That path is no longer there."}, nil
	}
	if !info.IsDir() {
		if !isGalleryMedia(c.req.Path) {
			return verbResult{Message: "The gallery takes photos and videos; that file is neither."}, nil
		}
		if _, _, err := client.UploadPhoto(ctx, filepath.Base(c.req.Path), opener(c.req.Path)); err != nil {
			return verbResult{Message: "Upload failed: " + firstLine(err.Error())}, nil
		}
		return verbResult{OK: true, Message: "Added " + filepath.Base(c.req.Path) + " to the gallery."}, nil
	}

	prefs, _ := deskprefs.Load()
	count, skipped := 0, 0
	err = filepath.WalkDir(c.req.Path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if prefs.Excluded(d.Name()) {
			return nil
		}
		if !isGalleryMedia(p) {
			skipped++
			return nil
		}
		if _, _, uerr := client.UploadPhoto(ctx, d.Name(), opener(p)); uerr != nil {
			return uerr
		}
		count++
		return nil
	})
	if err != nil {
		return verbResult{Message: fmt.Sprintf("Added %d, then stopped: %s", count, firstLine(err.Error()))}, nil
	}
	msg := fmt.Sprintf("Added %s to the gallery.", plural(count, "item", "items"))
	if skipped > 0 {
		msg += fmt.Sprintf(" %d file(s) were not photos or videos.", skipped)
	}
	return verbResult{OK: true, Message: msg}, nil
}

func (c *contextWindow) doOpenWeb(server string) (verbResult, error) {
	remote, ok := remoteForLocal(c.req.Path)
	if !ok {
		return verbResult{Message: notSyncedMessage}, nil
	}
	// The web app routes by folder, not by path, so the honest link is the
	// Files module itself rather than a guessed deep link.
	openBrowserURL(strings.TrimSuffix(server, "/") + "/files")
	return verbResult{OK: true, Message: "Opened the Files module for " + remote + "."}, nil
}

func (c *contextWindow) doSync(options map[string]string) (verbResult, error) {
	info, err := os.Stat(c.req.Path)
	if err != nil || !info.IsDir() {
		return verbResult{Message: "Only a folder can be kept in sync."}, nil
	}
	if _, ok := remoteForLocal(c.req.Path); ok {
		return verbResult{Message: "This folder is already part of a synced pair."}, nil
	}
	pair, err := syncconfig.Add(syncconfig.Pair{
		Local:           c.req.Path,
		Remote:          strings.TrimSpace(options["folder"]),
		Direction:       syncconfig.DirectionBoth,
		Conflict:        syncconfig.ConflictNewest,
		IntervalMinutes: 15,
		Watch:           true,
		Enabled:         true,
	})
	if err != nil {
		return verbResult{Message: firstLine(err.Error())}, nil
	}
	if c.log != nil {
		c.log.Printf("context sync: added pair %s", pair.ID)
	}
	return verbResult{OK: true, Message: "This folder is now kept in sync."}, nil
}

// notSyncedMessage is the honest answer for a path the server knows nothing
// about. Silently uploading it first would be a surprise, and a surprise upload
// is the last thing a sync client should do.
const notSyncedMessage = "This path is not inside a synced folder, so the server has no copy of it. " +
	"Use “Upload to Ledgerline…” first, or add its folder under Settings › Synced folders."

// isGalleryMedia reports whether a file is something the gallery accepts. The
// server decides for certain; this only avoids sending it a spreadsheet.
func isGalleryMedia(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif", ".avif", ".tif", ".tiff", ".bmp",
		".mp4", ".mov", ".m4v", ".avi", ".mkv", ".webm", ".3gp", ".mts", ".m2ts":
		return true
	}
	return false
}

func shortFingerprint(fp string) string {
	fp = strings.ReplaceAll(fp, " ", "")
	if len(fp) <= 16 {
		return fp
	}
	return fp[len(fp)-16:]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func parseID(s string) (int64, error) {
	var n int64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err != nil || n <= 0 {
		return 0, errors.New("not an id")
	}
	return n, nil
}

func parseIDs(s string) []int64 {
	var out []int64
	for _, part := range strings.Split(s, ",") {
		if n, err := parseID(part); err == nil {
			out = append(out, n)
		}
	}
	return out
}
