package files

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
	"github.com/MalteKiefer/ledgerline-cli/internal/manifeststore"
)

// Entry is a non-trashed file with its resolved slash path.
type Entry struct {
	View FileView
	Path string // full slash path, folder + name
}

// List returns every non-trashed file with its full path, optionally restricted
// to a remote subtree (remoteBase, "" = whole tree). Paths are returned relative
// to remoteBase.
func List(store *Store, remoteBase string) []Entry {
	tree := NewTree(store)
	remoteBase = strings.Trim(strings.ReplaceAll(remoteBase, "\\", "/"), "/")

	var out []Entry
	for _, raw := range store.Files() {
		fv, err := parseFile(raw)
		if err != nil || fv.Trashed != "" || fv.ID == "" || fv.Blob == "" {
			continue
		}
		full := tree.FilePath(fv)
		rel, ok := underBase(full, remoteBase)
		if !ok {
			continue
		}
		out = append(out, Entry{View: fv, Path: rel})
	}
	return out
}

// IndexByPath maps each non-trashed file's path to its record (case-sensitive),
// for existence checks during upload/sync.
func IndexByPath(store *Store, remoteBase string) map[string]Entry {
	idx := map[string]Entry{}
	for _, e := range List(store, remoteBase) {
		idx[e.Path] = e
	}
	return idx
}

// underBase reports whether full is at or under base and returns the relative
// path. base "" matches everything.
func underBase(full, base string) (string, bool) {
	if base == "" {
		return full, true
	}
	if full == base {
		return "", true // the base itself is a folder path, not a file
	}
	if strings.HasPrefix(full, base+"/") {
		return strings.TrimPrefix(full, base+"/"), true
	}
	return "", false
}

// Downloader fetches and decrypts file content.
type Downloader struct {
	client *api.Client
	vk     []byte
}

// NewDownloader builds a downloader.
func NewDownloader(client *api.Client, vaultKey []byte) *Downloader {
	return &Downloader{client: client, vk: vaultKey}
}

// Fetch downloads and decrypts a file's current content.
func (d *Downloader) Fetch(ctx context.Context, fv FileView) ([]byte, error) {
	blob, err := d.client.GetFileBlob(ctx, fv.Blob)
	if err != nil {
		return nil, err
	}
	return crypto.DecryptContent(blob, fv.EncFileKey, d.vk)
}

// rawByID indexes raw file records by id (used by sync for version handling).
func rawByID(records []json.RawMessage) map[string]json.RawMessage {
	m := make(map[string]json.RawMessage, len(records))
	for _, r := range records {
		m[manifeststore.RecordID(r)] = r
	}
	return m
}
