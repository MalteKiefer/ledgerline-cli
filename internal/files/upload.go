package files

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"sync"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
)

// maxVersions caps a file's retained version history (matches the web default).
const maxVersions = 10

// Uploader encrypts local content and records it in the manifest, creating a new
// file or adding a version to an existing one at the same path.
type Uploader struct {
	client   *api.Client
	store    *Store
	tree     *Tree
	vk       []byte
	progress func(sent, total int64)

	// stageMu serializes the fast in-memory staging (folder tree + manifest ops)
	// so Create/Replace are safe to call from several upload workers at once; the
	// slow blob upload runs OUTSIDE it, in parallel.
	stageMu sync.Mutex
}

// NewUploader builds an uploader over a store (and its tree).
func NewUploader(client *api.Client, store *Store, vaultKey []byte) *Uploader {
	return &Uploader{client: client, store: store, tree: NewTree(store), vk: vaultKey}
}

// SetProgress installs a callback fired while a blob's bytes stream to the
// server. Pass nil to disable. The callback is shared, so it is only meaningful
// for a single-worker upload; parallel uploads leave it nil and use the overall
// file-count progress instead.
func (u *Uploader) SetProgress(onProgress func(sent, total int64)) { u.progress = onProgress }

// Tree exposes the uploader's folder tree (shared so paths resolve consistently).
func (u *Uploader) Tree() *Tree { return u.tree }

// encryptAndStore encrypts, pads and uploads content, returning the blob id and
// wrapped key.
func (u *Uploader) encryptAndStore(ctx context.Context, plain []byte) (blob, encKey string, err error) {
	raw, encFileKey, err := crypto.EncryptContent(plain, u.vk)
	if err != nil {
		return "", "", err
	}
	padded, err := crypto.PadBlob(raw)
	if err != nil {
		return "", "", err
	}
	blob, err = u.client.UploadFileBlobProgress(ctx, padded, u.progress)
	if err != nil {
		return "", "", err
	}
	return blob, encFileKey, nil
}

// Create uploads content as a NEW file at remotePath (folders auto-created),
// returning the new record id and blob id.
func (u *Uploader) Create(ctx context.Context, remotePath, mime, createdISO string, plain []byte) (id, blob string, err error) {
	dir, name := splitPath(remotePath)
	// Slow network step runs unlocked (parallel across workers).
	blob, encKey, err := u.encryptAndStore(ctx, plain)
	if err != nil {
		return "", "", err
	}
	// Fast in-memory staging (folder tree + manifest op) is serialized.
	u.stageMu.Lock()
	defer u.stageMu.Unlock()
	folder, err := u.tree.EnsureFolder(dir)
	if err != nil {
		return "", "", err
	}
	rec, id, err := newFileRecord(name, mime, int64(len(plain)), blob, encKey, folder, createdISO)
	if err != nil {
		return "", "", err
	}
	u.store.AddFile(rec)
	return id, blob, nil
}

// Replace uploads new content for an existing file id, pushing the current blob
// onto the version history (trimmed to maxVersions). Returns the new blob id.
func (u *Uploader) Replace(ctx context.Context, id, mime string, plain []byte) (string, error) {
	// Slow network step runs unlocked (parallel across workers).
	blob, encKey, err := u.encryptAndStore(ctx, plain)
	if err != nil {
		return "", err
	}

	u.stageMu.Lock()
	defer u.stageMu.Unlock()
	raw, ok := u.store.fileRawByID()[id]
	if !ok {
		return "", nil
	}

	var cur struct {
		Blob       string            `json:"blob"`
		EncFileKey string            `json:"encFileKey"`
		Size       int64             `json:"size"`
		Mime       string            `json:"mime"`
		Name       string            `json:"name"`
		Created    string            `json:"created"`
		Versions   []json.RawMessage `json:"versions"`
	}
	_ = json.Unmarshal(raw, &cur)

	version, _, _ := newFileRecord(cur.Name, cur.Mime, cur.Size, cur.Blob, cur.EncFileKey, nil, cur.Created)
	versions := append([]json.RawMessage{version}, cur.Versions...)
	if len(versions) > maxVersions {
		versions = versions[:maxVersions]
	}

	u.store.UpdateFile(id, map[string]any{
		"blob":       blob,
		"encFileKey": encKey,
		"size":       int64(len(plain)),
		"mime":       mime,
		"versions":   versions,
		"trashed":    nil,
	})
	return blob, nil
}

// splitPath splits a slash path into its directory and file name.
func splitPath(p string) (dir, name string) {
	p = strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/")
	dir = path.Dir(p)
	if dir == "." {
		dir = ""
	}
	return dir, path.Base(p)
}

// fileRawByID exposes the store's current file records indexed by id.
func (s *Store) fileRawByID() map[string]json.RawMessage { return rawByID(s.Files()) }
