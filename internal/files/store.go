package files

import (
	"context"
	"encoding/json"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/manifeststore"
)

// Manifest collection keys owned by the Files module.
const (
	collFiles   = "files"
	collFolders = "fileFolders"
)

// Store is a view of the Files portion of the shared workspace manifest. It is a
// thin domain wrapper over manifeststore, which handles decryption, conflict-safe
// saves and verbatim preservation of keys owned by other modules.
type Store struct {
	ms *manifeststore.Store
}

// NewStore builds a Files store bound to a client and unlocked vault key.
func NewStore(client *api.Client, vaultKey []byte) *Store {
	return &Store{ms: manifeststore.New(client, vaultKey, "files",
		manifeststore.CollectionSpec{Key: collFiles},
		manifeststore.CollectionSpec{Key: collFolders},
	)}
}

// Load fetches and decrypts the workspace manifest, extracting the file tree.
func (s *Store) Load(ctx context.Context) error { return s.ms.Load(ctx) }

// Files returns the current file records (base + pending ops applied).
func (s *Store) Files() []json.RawMessage { return s.ms.Records(collFiles) }

// Folders returns the current folder records (base + pending ops applied).
func (s *Store) Folders() []json.RawMessage { return s.ms.Records(collFolders) }

// AddFile stages a new file record.
func (s *Store) AddFile(raw json.RawMessage) { s.ms.Add(collFiles, raw) }

// AddFolder stages a new folder record.
func (s *Store) AddFolder(raw json.RawMessage) { s.ms.Add(collFolders, raw) }

// UpdateFile stages a field patch on an existing file (preserves other fields).
func (s *Store) UpdateFile(id string, patch map[string]any) { s.ms.Update(collFiles, id, patch) }

// DeleteFile stages permanent removal of a file record.
func (s *Store) DeleteFile(id string) { s.ms.Delete(collFiles, id) }

// TrashFile stages a soft-delete (sets the trashed timestamp).
func (s *Store) TrashFile(id, whenISO string) { s.UpdateFile(id, map[string]any{"trashed": whenISO}) }

// DeleteFolder stages permanent removal of a folder record.
func (s *Store) DeleteFolder(id string) { s.ms.Delete(collFolders, id) }

// FileBlobs returns every content blob a file references — its current blob plus
// all version-history blobs — so a permanent delete can reclaim them.
func (s *Store) FileBlobs(id string) []string {
	raw, ok := s.fileRawByID()[id]
	if !ok {
		return nil
	}
	var r struct {
		Blob     string `json:"blob"`
		Versions []struct {
			Blob string `json:"blob"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	var out []string
	if r.Blob != "" {
		out = append(out, r.Blob)
	}
	for _, v := range r.Versions {
		if v.Blob != "" {
			out = append(out, v.Blob)
		}
	}
	return out
}

// Dirty reports whether there are unsaved changes.
func (s *Store) Dirty() bool { return s.ms.Dirty() }

// Save seals the manifest (with the file tree updated) and PUTs it, retrying on
// a version conflict.
func (s *Store) Save(ctx context.Context) error { return s.ms.Save(ctx) }
