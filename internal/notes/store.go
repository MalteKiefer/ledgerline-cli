// Package notes implements the zero-knowledge Notes module client. Notes are a
// Store v3 SHARDED store (like files/gallery, "mirroring /files/store"): the note
// records live in content-addressed, id-bucketed shard blobs and the sealed root
// is a small pointer table. Note bodies are plaintext markdown carried IN the
// record (no separate content blob), so — unlike files — there are no per-note
// content blobs, only the record-shard blobs.
//
// The engine mirrors internal/files/store.go (the proven sharded engine) with a
// single collection and the generic per-module API. It reuses the shared crypto,
// canonicaljson and shard seams unchanged, so the byte contract is identical.
package notes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/blobcache"
	"github.com/MalteKiefer/ledgerline-cli/internal/canonicaljson"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
	"github.com/MalteKiefer/ledgerline-cli/internal/shard"
)

// module is the sharded-store module key + collection key (both "notes").
const module = "notes"
const collNotes = "notes"

// maxSaveRetries bounds the optimistic-concurrency retry loop.
const maxSaveRetries = 5

// shardDesc describes one content-addressed note-record shard in the root
// (byte-identical to the files/gallery shard descriptor).
type shardDesc struct {
	Ref    string `json:"ref"`
	Key    string `json:"key"`
	Hash   string `json:"hash"`
	Count  int    `json:"count"`
	Bucket int    `json:"bucket"`
}

type opKind int

const (
	opAdd opKind = iota
	opUpdate
	opDelete
)

type op struct {
	kind  opKind
	id    string
	raw   json.RawMessage
	patch map[string]any
}

// Store is the Store v3 sharded view of the Notes module.
type Store struct {
	client *api.Client
	vk     []byte

	version   int64
	baseNotes []json.RawMessage
	shardBits int
	shards    []shardDesc
	root      map[string]json.RawMessage // preserve unknown root keys
	ops       []op
	cache     *blobcache.Cache

	// degraded is set when a load skipped a permanently-missing (404) shard; the
	// store is then READ-ONLY so a partial set is never re-sealed (data loss).
	degraded      bool
	missingShards int
}

// Degraded reports whether the last load skipped a permanently-missing shard.
func (s *Store) Degraded() bool     { return s.degraded }
func (s *Store) MissingShards() int { return s.missingShards }

// NewStore builds a Notes store bound to a client and unlocked vault key.
func NewStore(client *api.Client, vaultKey []byte) *Store {
	return &Store{client: client, vk: vaultKey, root: map[string]json.RawMessage{}}
}

// SetShardCache attaches a ciphertext shard cache (ciphertext only, no plaintext
// at rest) so repeated/resumed loads skip re-fetching unchanged shards.
func (s *Store) SetShardCache(c *blobcache.Cache) { s.cache = c }

// Load fetches and decrypts the v3 Notes root and its note-record shards.
func (s *Store) Load(ctx context.Context) error {
	sealed, err := s.client.ShardedStore(ctx, module)
	if err != nil {
		return err
	}
	s.version = sealed.Version
	s.ops = nil
	s.baseNotes = nil
	s.shards = nil
	s.shardBits = 0
	s.root = map[string]json.RawMessage{}
	s.degraded = false
	s.missingShards = 0

	if sealed.Ciphertext == "" {
		return nil
	}
	raw, err := crypto.OpenManifest(sealed.Ciphertext, s.vk)
	if err != nil {
		return errors.New("decrypt notes manifest failed (wrong passphrase?)")
	}
	if err := json.Unmarshal(trimJSON(raw), &s.root); err != nil {
		return err
	}

	var v int
	if b, ok := s.root["v"]; ok {
		_ = json.Unmarshal(b, &v)
	}
	if v != 3 {
		// Not a v3 sharded root (e.g. an empty/legacy monolith row) — treat as empty.
		s.root = map[string]json.RawMessage{}
		return nil
	}
	if b, ok := s.root["shardBits"]; ok {
		_ = json.Unmarshal(b, &s.shardBits)
	}
	if b, ok := s.root["shards"]; ok {
		if err := json.Unmarshal(b, &s.shards); err != nil {
			return fmt.Errorf("parse notes shards: %w", err)
		}
	}
	recs, err := s.loadShards(ctx, s.shards)
	if err != nil {
		return err
	}
	s.baseNotes = recs
	return nil
}

// loadShards downloads, decrypts and concatenates every shard's note records. A
// permanently-missing (404) shard is tolerated — skipped, the store goes degraded
// READ-ONLY so a partial set is never re-sealed — while any other failure aborts.
func (s *Store) loadShards(ctx context.Context, shards []shardDesc) ([]json.RawMessage, error) {
	fromCache := make(map[string][]byte)
	var miss []string
	for _, sh := range shards {
		if b, ok := s.cache.Get(sh.Ref); ok {
			fromCache[sh.Ref] = b
		} else {
			miss = append(miss, sh.Ref)
		}
	}
	batched := s.batchFetch(ctx, miss)

	var recs []json.RawMessage
	for i, sh := range shards {
		blob, cached := fromCache[sh.Ref]
		if !cached {
			var ok bool
			if blob, ok = batched[sh.Ref]; !ok {
				var err error
				if blob, err = s.fetchBlobWithRetry(ctx, sh.Ref); err != nil {
					if api.Status(err) == 404 {
						s.degraded = true
						s.missingShards++
						continue
					}
					return nil, fmt.Errorf("fetch notes shard %d/%d: %w", i+1, len(shards), err)
				}
			}
			s.cache.Put(sh.Ref, blob)
		}
		plain, err := crypto.DecryptContent(blob, sh.Key, s.vk)
		if err != nil {
			return nil, fmt.Errorf("decrypt notes shard %d/%d: %w", i+1, len(shards), err)
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(plain, &arr); err != nil {
			return nil, fmt.Errorf("parse notes shard %d/%d: %w", i+1, len(shards), err)
		}
		recs = append(recs, arr...)
	}
	s.cache.Prune(shardRefsOf(shards))
	return recs, nil
}

// batchFetch fetches the given (cache-missed) refs in one raw-batch round-trip.
// Best-effort: for fewer than two refs, or on error, it returns nil and the
// caller falls back to a per-blob GET.
func (s *Store) batchFetch(ctx context.Context, refs []string) map[string][]byte {
	if len(refs) < 2 {
		return nil
	}
	batched, err := s.client.GetModuleBlobsBatch(ctx, module, refs)
	if err != nil {
		return nil
	}
	return batched
}

// fetchBlobWithRetry fetches a blob, retrying transient failures (a fresh
// object-store write can 404 briefly; 5xx/429 are transient).
func (s *Store) fetchBlobWithRetry(ctx context.Context, ref string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		blob, err := s.client.GetModuleBlob(ctx, module, ref)
		if err == nil {
			return blob, nil
		}
		lastErr = err
		if code := api.Status(err); code != 0 && code != 404 && code != 429 && code < 500 {
			break
		}
	}
	return nil, lastErr
}

// Notes returns the current note records (base + staged ops applied).
func (s *Store) Notes() []json.RawMessage { return s.applyOps(s.baseNotes) }

// AddNote stages a new note record.
func (s *Store) AddNote(raw json.RawMessage) {
	s.ops = append(s.ops, op{kind: opAdd, id: recordID(raw), raw: raw})
}

// UpdateNote stages a field patch on an existing note (preserves other fields).
func (s *Store) UpdateNote(id string, patch map[string]any) {
	s.ops = append(s.ops, op{kind: opUpdate, id: id, patch: patch})
}

// DeleteNote stages permanent removal of a note record.
func (s *Store) DeleteNote(id string) {
	s.ops = append(s.ops, op{kind: opDelete, id: id})
}

// Dirty reports whether there are unsaved changes.
func (s *Store) Dirty() bool { return len(s.ops) > 0 }

// ErrDegraded is returned by Save when the store loaded degraded (a shard is
// permanently missing) — re-sealing would drop those records for good.
var ErrDegraded = errors.New("notes: store is degraded (a record shard is missing) — refusing to save so no data is lost")

// Save re-seals only the changed buckets + root, retrying on a version conflict by
// reloading and re-applying the staged operations.
func (s *Store) Save(ctx context.Context) error {
	if len(s.ops) == 0 {
		return nil
	}
	if s.degraded {
		return ErrDegraded
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
	return errors.New("notes: manifest kept conflicting; try again")
}

func (s *Store) saveOnce(ctx context.Context) error {
	notes := s.applyOps(s.baseNotes)

	descriptors, shardBits, err := s.buildShards(ctx, notes)
	if err != nil {
		return err
	}

	root, err := s.buildRoot(shardBits, descriptors)
	if err != nil {
		return err
	}
	sealed, err := crypto.SealManifest(root, s.vk)
	if err != nil {
		return err
	}
	// Referential-integrity guard: every live record-shard ref the new root points
	// at. A superseded shard is NOT eagerly deleted (a concurrent writer may reuse
	// it); the server's grace-gated reconcile reclaims orphans. The counts map is
	// always complete (notes has one fully-known slice), feeding the anomaly-scan.
	live := shardRefsOf(descriptors)
	counts := map[string]int{collNotes: sumDescriptorCounts(descriptors)}
	newVersion, err := s.client.SaveShardedStore(ctx, module, sealed, s.version, live, counts)
	if err != nil {
		return err
	}
	s.version = newVersion
	s.shards = descriptors
	s.shardBits = shardBits
	s.baseNotes = notes
	return nil
}

// buildShards buckets note records by id, re-seals only the buckets whose
// canonical content changed, and returns the descriptors + chosen shardBits.
func (s *Store) buildShards(ctx context.Context, notes []json.RawMessage) ([]shardDesc, int, error) {
	type idVal struct {
		id  string
		val any
	}
	items := make([]idVal, 0, len(notes))
	for _, raw := range notes {
		v, err := decodeAny(raw)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, idVal{id: recordID(raw), val: v})
	}

	shardBits := shard.RecommendedBits(len(items))
	rebucket := shardBits != s.shardBits

	buckets := map[int][]idVal{}
	for _, it := range items {
		b := shard.BucketOf(it.id, shardBits)
		buckets[b] = append(buckets[b], it)
	}
	idxs := make([]int, 0, len(buckets))
	for b := range buckets {
		idxs = append(idxs, b)
	}
	sort.Ints(idxs)

	prev := map[int]shardDesc{}
	if !rebucket {
		for _, sh := range s.shards {
			prev[sh.Bucket] = sh
		}
	}

	descriptors := make([]shardDesc, 0, len(idxs))
	for _, b := range idxs {
		recs := buckets[b]
		sort.Slice(recs, func(i, j int) bool { return recs[i].id < recs[j].id })
		vals := make([]any, len(recs))
		for i, r := range recs {
			vals[i] = r.val
		}
		canon, err := canonicaljson.Marshal(vals)
		if err != nil {
			return nil, 0, fmt.Errorf("notes: canonicalize bucket %d: %w", b, err)
		}
		hash := sha256Hex(canon)
		if p, ok := prev[b]; ok && p.Hash == hash && p.Ref != "" {
			p.Count = len(recs)
			descriptors = append(descriptors, p)
			continue
		}
		ref, key, err := s.sealBlob(ctx, canon)
		if err != nil {
			return nil, 0, err
		}
		descriptors = append(descriptors, shardDesc{Ref: ref, Key: key, Hash: hash, Count: len(recs), Bucket: b})
	}
	return descriptors, shardBits, nil
}

// sealBlob encrypts + pads bytes and uploads them, returning the blob id and key.
func (s *Store) sealBlob(ctx context.Context, plain []byte) (ref, key string, err error) {
	blob, encKey, err := crypto.EncryptContent(plain, s.vk)
	if err != nil {
		return "", "", err
	}
	padded, err := crypto.PadBlob(blob)
	if err != nil {
		return "", "", err
	}
	ref, err = s.client.UploadModuleBlob(ctx, module, padded)
	if err != nil {
		return "", "", err
	}
	return ref, encKey, nil
}

// buildRoot assembles the v3 Notes root, preserving unknown keys from the load.
func (s *Store) buildRoot(shardBits int, descriptors []shardDesc) ([]byte, error) {
	root := map[string]json.RawMessage{}
	for k, v := range s.root {
		root[k] = v
	}
	set := func(key string, val any) error {
		b, err := json.Marshal(val)
		if err != nil {
			return err
		}
		root[key] = b
		return nil
	}
	if err := set("v", 3); err != nil {
		return nil, err
	}
	if err := set("suite", crypto.SuiteV3); err != nil {
		return nil, err
	}
	if err := set("shardBits", shardBits); err != nil {
		return nil, err
	}
	if err := set("shards", descriptors); err != nil {
		return nil, err
	}
	if _, ok := root["caps"]; !ok {
		root["caps"] = json.RawMessage("{}")
	}
	return json.Marshal(root)
}

// applyOps returns the base note records with staged ops applied.
func (s *Store) applyOps(base []json.RawMessage) []json.RawMessage {
	deleted := map[string]bool{}
	patches := map[string]map[string]any{}
	var adds []json.RawMessage
	for _, o := range s.ops {
		switch o.kind {
		case opAdd:
			adds = append(adds, o.raw)
		case opDelete:
			deleted[o.id] = true
		case opUpdate:
			if patches[o.id] == nil {
				patches[o.id] = map[string]any{}
			}
			for k, v := range o.patch {
				patches[o.id][k] = v
			}
		}
	}

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

// sumDescriptorCounts totals the per-bucket record counts across a shard set.
func sumDescriptorCounts(descriptors []shardDesc) int {
	total := 0
	for _, d := range descriptors {
		total += d.Count
	}
	return total
}

// shardRefsOf returns the non-empty refs of a shard set.
func shardRefsOf(shards []shardDesc) []string {
	refs := make([]string, 0, len(shards))
	for _, sh := range shards {
		if sh.Ref != "" {
			refs = append(refs, sh.Ref)
		}
	}
	return refs
}

// recordID extracts the "id" field from a raw record.
func recordID(raw json.RawMessage) string {
	var r struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &r)
	return r.ID
}

// patchRecord applies key/value updates to a raw record, preserving every other
// field (modelled or not). A nil value deletes the key.
func patchRecord(raw json.RawMessage, patch map[string]any) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, err
		}
	}
	for k, v := range patch {
		if v == nil {
			delete(obj, k)
			continue
		}
		enc, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		obj[k] = enc
	}
	return json.Marshal(obj)
}

// decodeAny decodes canonical JSON bytes into a generic value.
func decodeAny(raw json.RawMessage) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// sha256Hex returns the lowercase hex SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// trimJSON drops the trailing space padding SealManifest adds.
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
