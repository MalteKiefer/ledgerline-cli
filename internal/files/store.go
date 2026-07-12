package files

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
)

// maxSaveRetries bounds the optimistic-concurrency retry loop.
const maxSaveRetries = 5

// Store is a view of the Files portion of the shared workspace manifest. It
// keeps the full manifest so other modules (notes, bookmarks, todos, contacts)
// are preserved verbatim, and records edits as logical operations so a version
// conflict can be resolved by reloading and re-applying them.
type Store struct {
	client *api.Client
	vk     []byte

	version     int64
	manifest    map[string]json.RawMessage // full decrypted manifest
	baseFiles   []json.RawMessage
	baseFolders []json.RawMessage

	ops []op
}

// op is one pending change to the file tree.
type op struct {
	kind  opKind
	id    string
	raw   json.RawMessage
	patch map[string]any
}

type opKind int

const (
	opAddFile opKind = iota
	opAddFolder
	opUpdateFile
	opUpdateFolder
	opDeleteFile
	opDeleteFolder
)

// NewStore builds a Files store bound to a client and unlocked vault key.
func NewStore(client *api.Client, vaultKey []byte) *Store {
	return &Store{client: client, vk: vaultKey}
}

// Load fetches and decrypts the workspace manifest, extracting the file tree.
func (s *Store) Load(ctx context.Context) error {
	sealed, err := s.client.Store(ctx)
	if err != nil {
		return err
	}
	s.version = sealed.Version
	s.ops = nil

	s.manifest = map[string]json.RawMessage{}
	if sealed.Ciphertext != "" {
		raw, err := crypto.OpenManifest(sealed.Ciphertext, s.vk)
		if err != nil {
			return errors.New("decrypt workspace manifest failed (wrong passphrase?)")
		}
		if err := json.Unmarshal(trimJSON(raw), &s.manifest); err != nil {
			return err
		}
	}

	s.baseFiles = decodeArray(s.manifest["files"])
	s.baseFolders = decodeArray(s.manifest["fileFolders"])
	return nil
}

// Files returns the current file records (base + pending ops applied).
func (s *Store) Files() []json.RawMessage { return applyOps(s.baseFiles, s.ops, true) }

// Folders returns the current folder records (base + pending ops applied).
func (s *Store) Folders() []json.RawMessage { return applyOps(s.baseFolders, s.ops, false) }

// AddFile stages a new file record and returns its id.
func (s *Store) AddFile(raw json.RawMessage) {
	s.ops = append(s.ops, op{kind: opAddFile, id: recordID(raw), raw: raw})
}

// AddFolder stages a new folder record.
func (s *Store) AddFolder(raw json.RawMessage) {
	s.ops = append(s.ops, op{kind: opAddFolder, id: recordID(raw), raw: raw})
}

// UpdateFile stages a field patch on an existing file (preserves other fields).
func (s *Store) UpdateFile(id string, patch map[string]any) {
	s.ops = append(s.ops, op{kind: opUpdateFile, id: id, patch: patch})
}

// DeleteFile stages permanent removal of a file record.
func (s *Store) DeleteFile(id string) {
	s.ops = append(s.ops, op{kind: opDeleteFile, id: id})
}

// TrashFile stages a soft-delete (sets the trashed timestamp).
func (s *Store) TrashFile(id, whenISO string) { s.UpdateFile(id, map[string]any{"trashed": whenISO}) }

// DeleteFolder stages permanent removal of a folder record.
func (s *Store) DeleteFolder(id string) {
	s.ops = append(s.ops, op{kind: opDeleteFolder, id: id})
}

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
func (s *Store) Dirty() bool { return len(s.ops) > 0 }

// Save seals the manifest (with the file tree updated) and PUTs it, retrying on
// a version conflict by reloading and re-applying the staged operations.
func (s *Store) Save(ctx context.Context) error {
	if len(s.ops) == 0 {
		return nil
	}
	for attempt := 0; attempt < maxSaveRetries; attempt++ {
		if err := s.saveOnce(ctx); err != nil {
			if errors.Is(err, api.ErrVersionConflict) {
				ops := s.ops
				if rerr := s.Load(ctx); rerr != nil {
					return rerr
				}
				s.ops = ops
				continue
			}
			return err
		}
		s.ops = nil
		return nil
	}
	return errors.New("files: manifest kept conflicting; try again")
}

// saveOnce writes the current manifest at the loaded version.
func (s *Store) saveOnce(ctx context.Context) error {
	files := applyOps(s.baseFiles, s.ops, true)
	folders := applyOps(s.baseFolders, s.ops, false)

	filesRaw, err := json.Marshal(files)
	if err != nil {
		return err
	}
	foldersRaw, err := json.Marshal(folders)
	if err != nil {
		return err
	}
	s.manifest["files"] = filesRaw
	s.manifest["fileFolders"] = foldersRaw
	if _, ok := s.manifest["v"]; !ok {
		s.manifest["v"] = json.RawMessage("1")
	}

	manifestJSON, err := json.Marshal(s.manifest)
	if err != nil {
		return err
	}
	sealed, err := crypto.SealManifest(manifestJSON, s.vk)
	if err != nil {
		return err
	}

	newVersion, err := s.client.SaveStore(ctx, sealed, s.version)
	if err != nil {
		return err
	}
	s.version = newVersion
	// Fold applied ops into the base so a subsequent Save starts clean.
	s.baseFiles = files
	s.baseFolders = folders
	return nil
}

// applyOps returns base with the ops applied. forFiles selects file vs folder ops.
func applyOps(base []json.RawMessage, ops []op, forFiles bool) []json.RawMessage {
	deleted := map[string]bool{}
	patches := map[string]map[string]any{}
	var adds []json.RawMessage

	addKind, updKind, delKind := opAddFolder, opUpdateFolder, opDeleteFolder
	if forFiles {
		addKind, updKind, delKind = opAddFile, opUpdateFile, opDeleteFile
	}
	for _, o := range ops {
		switch o.kind {
		case addKind:
			adds = append(adds, o.raw)
		case delKind:
			deleted[o.id] = true
		case updKind:
			if patches[o.id] == nil {
				patches[o.id] = map[string]any{}
			}
			for k, v := range o.patch {
				patches[o.id][k] = v
			}
		}
	}

	// Patches and deletes apply across BOTH base and same-session adds.
	combined := make([]json.RawMessage, 0, len(base)+len(adds))
	combined = append(combined, base...)
	combined = append(combined, adds...)

	out := make([]json.RawMessage, 0, len(combined))
	for _, raw := range combined {
		id := recordID(raw)
		if deleted[id] {
			continue
		}
		if p := patches[id]; p != nil {
			if patched, err := patchRecord(raw, p); err == nil {
				raw = patched
			}
		}
		out = append(out, raw)
	}
	return out
}

// decodeArray unmarshals a manifest array key into raw elements (nil-safe).
func decodeArray(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	return arr
}

// trimJSON drops trailing manifest padding so a strict unmarshal succeeds.
func trimJSON(b []byte) []byte {
	i := len(b)
	for i > 0 {
		switch b[i-1] {
		case ' ', '\n', '\t', '\r', 0:
			i--
			continue
		}
		break
	}
	return b[:i]
}
