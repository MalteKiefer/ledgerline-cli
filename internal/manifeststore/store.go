// Package manifeststore is the shared engine behind every module that lives in
// the opaque workspace manifest (files, todos, and any future module). The
// manifest is one sealed JSON document holding several named arrays; each module
// owns a subset of keys and must preserve the rest verbatim. Edits are recorded
// as logical operations so an optimistic-concurrency conflict can be resolved by
// reloading and re-applying them.
package manifeststore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
)

// maxSaveRetries bounds the optimistic-concurrency retry loop.
const maxSaveRetries = 5

// Kind is the type of a staged operation.
type Kind int

const (
	// Add stages a new record.
	Add Kind = iota
	// Update stages a field patch on an existing record.
	Update
	// Delete stages permanent removal of a record.
	Delete
)

// CollectionSpec configures one manifest array a module manages.
type CollectionSpec struct {
	// Key is the manifest object key holding the array (e.g. "files").
	Key string
	// PrependAdds puts newly added records at the front of the array (the web
	// client unshifts todos); the zero value appends them.
	PrependAdds bool
}

// op is one pending change scoped to a collection.
type op struct {
	coll  string
	kind  Kind
	id    string
	raw   json.RawMessage
	patch map[string]any
}

// Store is a conflict-safe view of one module's slice of the workspace manifest.
// It keeps the full decrypted manifest so keys owned by other modules survive a
// save untouched.
type Store struct {
	client *api.Client
	vk     []byte
	label  string
	module string // Store v3 per-module row: GET/PUT /store/{module}

	order []string                  // collection keys, in registration order
	specs map[string]CollectionSpec // key -> spec

	version  int64
	manifest map[string]json.RawMessage
	base     map[string][]json.RawMessage
	ops      []op

	// rawSet holds pending writes to NON-collection top-level keys (e.g. the
	// health module's singular "healthProfile" object), applied to the manifest at
	// save time and preserved across a 409 rebase like ops.
	rawSet map[string]json.RawMessage
}

// New builds a store for the given collections held in one Store v3 per-module
// row (GET/PUT /store/{module}). label names the module in error messages;
// module is the server-allowlisted module key (e.g. "todos").
func New(client *api.Client, vaultKey []byte, label, module string, specs ...CollectionSpec) *Store {
	s := &Store{
		client: client,
		vk:     vaultKey,
		label:  label,
		module: module,
		specs:  make(map[string]CollectionSpec, len(specs)),
		base:   make(map[string][]json.RawMessage, len(specs)),
		rawSet: map[string]json.RawMessage{},
	}
	for _, sp := range specs {
		s.specs[sp.Key] = sp
		s.order = append(s.order, sp.Key)
	}
	return s
}

// Load fetches and decrypts the workspace manifest and extracts each collection.
func (s *Store) Load(ctx context.Context) error {
	sealed, err := s.client.ModuleStore(ctx, s.module)
	if err != nil {
		return err
	}
	s.version = sealed.Version
	s.ops = nil
	s.manifest = map[string]json.RawMessage{}
	if sealed.Ciphertext != "" {
		raw, derr := crypto.OpenManifest(sealed.Ciphertext, s.vk)
		if derr != nil {
			return errors.New("decrypt workspace manifest failed (wrong passphrase?)")
		}
		if err := json.Unmarshal(TrimJSON(raw), &s.manifest); err != nil {
			return err
		}
	}
	for _, key := range s.order {
		s.base[key] = decodeArray(s.manifest[key])
	}
	return nil
}

// Records returns a collection's current records (base + pending ops applied).
func (s *Store) Records(collKey string) []json.RawMessage { return s.applyOps(collKey) }

// Add stages a new record in a collection; its id is read from the record.
func (s *Store) Add(collKey string, raw json.RawMessage) {
	s.ops = append(s.ops, op{coll: collKey, kind: Add, id: RecordID(raw), raw: raw})
}

// Update stages a field patch on a record (other fields preserved; nil deletes).
func (s *Store) Update(collKey, id string, patch map[string]any) {
	s.ops = append(s.ops, op{coll: collKey, kind: Update, id: id, patch: patch})
}

// Delete stages permanent removal of a record.
func (s *Store) Delete(collKey, id string) {
	s.ops = append(s.ops, op{coll: collKey, kind: Delete, id: id})
}

// Dirty reports whether there are unsaved changes.
func (s *Store) Dirty() bool { return len(s.ops) > 0 || len(s.rawSet) > 0 }

// RawKey returns the current raw JSON of a NON-collection top-level manifest key
// (e.g. health's "healthProfile"), with any pending SetRawKey applied. Returns nil
// when absent.
func (s *Store) RawKey(key string) json.RawMessage {
	if v, ok := s.rawSet[key]; ok {
		return v
	}
	return s.manifest[key]
}

// SetRawKey stages a write to a NON-collection top-level manifest key. The value
// is applied verbatim at save time (and survives a 409 rebase). Passing raw=nil
// stages deletion of the key.
func (s *Store) SetRawKey(key string, raw json.RawMessage) { s.rawSet[key] = raw }

// Save seals the manifest with all staged changes and PUTs it, retrying on a
// version conflict by reloading and re-applying the operations.
func (s *Store) Save(ctx context.Context) error {
	if !s.Dirty() {
		return nil
	}
	for attempt := 0; attempt < maxSaveRetries; attempt++ {
		if err := s.saveOnce(ctx); err != nil {
			if errors.Is(err, api.ErrVersionConflict) {
				ops := s.ops
				if rerr := s.Load(ctx); rerr != nil {
					return rerr
				}
				s.ops = ops // rawSet survives Load and is re-applied by the next saveOnce
				continue
			}
			return err
		}
		s.ops = nil
		s.rawSet = map[string]json.RawMessage{}
		return nil
	}
	return fmt.Errorf("%s: manifest kept conflicting; try again", s.label)
}

// saveOnce writes the current manifest at the loaded version and, on success,
// folds the applied operations into the base so a subsequent Save starts clean.
func (s *Store) saveOnce(ctx context.Context) error {
	applied := make(map[string][]json.RawMessage, len(s.order))
	for _, key := range s.order {
		recs := s.applyOps(key)
		raw, err := json.Marshal(recs)
		if err != nil {
			return err
		}
		s.manifest[key] = raw
		applied[key] = recs
	}
	// Store v3: the per-module row carries v:3 (canonical JSON + suite envelope
	// are applied by crypto.SealManifest).
	if s.manifest == nil {
		s.manifest = map[string]json.RawMessage{}
	}
	s.manifest["v"] = json.RawMessage("3")

	// Apply staged non-collection key writes (e.g. healthProfile) verbatim.
	for key, raw := range s.rawSet {
		if raw == nil {
			delete(s.manifest, key)
			continue
		}
		s.manifest[key] = raw
	}

	manifestJSON, err := json.Marshal(s.manifest)
	if err != nil {
		return err
	}
	sealed, err := crypto.SealManifest(manifestJSON, s.vk)
	if err != nil {
		return err
	}
	newVersion, err := s.client.SaveModuleStore(ctx, s.module, sealed, s.version)
	if err != nil {
		return err
	}
	s.version = newVersion
	for key, recs := range applied {
		s.base[key] = recs
	}
	return nil
}

// applyOps returns a collection's base records with its staged ops applied.
func (s *Store) applyOps(collKey string) []json.RawMessage {
	base := s.base[collKey]
	deleted := map[string]bool{}
	patches := map[string]map[string]any{}
	var adds []json.RawMessage
	for _, o := range s.ops {
		if o.coll != collKey {
			continue
		}
		switch o.kind {
		case Add:
			adds = append(adds, o.raw)
		case Delete:
			deleted[o.id] = true
		case Update:
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
	if s.specs[collKey].PrependAdds {
		combined = append(combined, adds...)
		combined = append(combined, base...)
	} else {
		combined = append(combined, base...)
		combined = append(combined, adds...)
	}

	out := make([]json.RawMessage, 0, len(combined))
	for _, raw := range combined {
		id := RecordID(raw)
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
