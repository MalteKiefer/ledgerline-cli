package files

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
	"github.com/MalteKiefer/ledgerline-cli/internal/canonicaljson"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
	"github.com/MalteKiefer/ledgerline-cli/internal/shard"
)

// Store v3 collection keys owned by the Files module.
const (
	collFiles   = "files"
	collFolders = "fileFolders"
)

// maxSaveRetries bounds the optimistic-concurrency retry loop.
const maxSaveRetries = 5

// shardDesc describes one content-addressed file-record shard in the root.
type shardDesc struct {
	Ref    string `json:"ref"`
	Key    string `json:"key"`
	Hash   string `json:"hash"`
	Count  int    `json:"count"`
	Bucket int    `json:"bucket"`
}

// collDesc describes a content-addressed collection blob (fileFolders).
type collDesc struct {
	Ref  string
	Key  string
	Hash string
}

type opKind int

const (
	opAdd opKind = iota
	opUpdate
	opDelete
)

type op struct {
	coll  string
	kind  opKind
	id    string
	raw   json.RawMessage
	patch map[string]any
}

// Store is the Store v3 sharded view of the Files module: file records live in
// content-addressed, id-bucketed shards (like the gallery); folders live in a
// single fileFolders collection blob. A save re-seals only the buckets whose
// canonical content changed, plus the folders blob if it changed, plus the tiny
// root. It exposes the same surface as the old monolith-backed store so the rest
// of the Files module is unchanged.
type Store struct {
	client *api.Client
	vk     []byte

	version     int64
	baseFiles   []json.RawMessage
	baseFolders []json.RawMessage
	shardBits   int
	shards      []shardDesc
	foldersDesc *collDesc
	root        map[string]json.RawMessage // preserve unknown root keys
	ops         []op
}

// NewStore builds a Files store bound to a client and unlocked vault key.
func NewStore(client *api.Client, vaultKey []byte) *Store {
	return &Store{client: client, vk: vaultKey, root: map[string]json.RawMessage{}}
}

// Load fetches and decrypts the v3 Files root, its file-record shards and the
// fileFolders collection blob. v3 only — no v1/v2 read paths.
func (s *Store) Load(ctx context.Context) error {
	sealed, err := s.client.FilesStore(ctx)
	if err != nil {
		return err
	}
	s.version = sealed.Version
	s.ops = nil
	s.baseFiles = nil
	s.baseFolders = nil
	s.shards = nil
	s.shardBits = 0
	s.foldersDesc = nil
	s.root = map[string]json.RawMessage{}

	if sealed.Ciphertext == "" {
		return nil
	}
	raw, err := crypto.OpenManifest(sealed.Ciphertext, s.vk)
	if err != nil {
		return errors.New("decrypt files manifest failed (wrong passphrase?)")
	}
	if err := json.Unmarshal(trimJSON(raw), &s.root); err != nil {
		return err
	}

	var v int
	if b, ok := s.root["v"]; ok {
		_ = json.Unmarshal(b, &v)
	}
	if v != 3 {
		s.root = map[string]json.RawMessage{}
		return nil
	}
	if b, ok := s.root["shardBits"]; ok {
		_ = json.Unmarshal(b, &s.shardBits)
	}
	if b, ok := s.root["shards"]; ok {
		if err := json.Unmarshal(b, &s.shards); err != nil {
			return fmt.Errorf("parse files shards: %w", err)
		}
	}
	files, err := s.loadShards(ctx, s.shards)
	if err != nil {
		return err
	}
	s.baseFiles = files

	if ref, key := rootStr(s.root, "foldersRef"), rootStr(s.root, "foldersKey"); ref != "" {
		s.foldersDesc = &collDesc{Ref: ref, Key: key, Hash: rootStr(s.root, "foldersHash")}
		folders, err := s.loadCollection(ctx, ref, key)
		if err != nil {
			return err
		}
		s.baseFolders = folders
	}
	return nil
}

// loadShards downloads, decrypts and concatenates every shard's file records. A
// failed shard aborts the load rather than silently dropping records.
func (s *Store) loadShards(ctx context.Context, shards []shardDesc) ([]json.RawMessage, error) {
	batched := s.prefetchShards(ctx, shards)
	var recs []json.RawMessage
	for i, sh := range shards {
		blob, ok := batched[sh.Ref]
		if !ok {
			var err error
			blob, err = s.fetchBlobWithRetry(ctx, sh.Ref)
			if err != nil {
				return nil, fmt.Errorf("fetch files shard %d/%d: %w", i+1, len(shards), err)
			}
		}
		plain, err := crypto.DecryptContent(blob, sh.Key, s.vk)
		if err != nil {
			return nil, fmt.Errorf("decrypt files shard %d/%d: %w", i+1, len(shards), err)
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(plain, &arr); err != nil {
			return nil, fmt.Errorf("parse files shard %d/%d: %w", i+1, len(shards), err)
		}
		recs = append(recs, arr...)
	}
	return recs, nil
}

// loadCollection downloads + decrypts a content-addressed collection blob → array.
func (s *Store) loadCollection(ctx context.Context, ref, key string) ([]json.RawMessage, error) {
	blob, err := s.fetchBlobWithRetry(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("fetch files collection blob: %w", err)
	}
	plain, err := crypto.DecryptContent(blob, key, s.vk)
	if err != nil {
		return nil, fmt.Errorf("decrypt files collection blob: %w", err)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(plain, &arr); err != nil {
		return nil, fmt.Errorf("parse files collection blob: %w", err)
	}
	return arr, nil
}

// prefetchShards fetches all file-record shard ciphertexts in one raw-batch
// round-trip (§10). Best-effort: on error or a single shard it returns nil and
// the caller falls back to a per-blob GET; an omitted ref is simply absent.
func (s *Store) prefetchShards(ctx context.Context, shards []shardDesc) map[string][]byte {
	if len(shards) < 2 {
		return nil
	}
	refs := make([]string, 0, len(shards))
	for _, sh := range shards {
		if sh.Ref != "" {
			refs = append(refs, sh.Ref)
		}
	}
	batched, err := s.client.GetFilesBlobsBatch(ctx, refs)
	if err != nil {
		return nil
	}
	return batched
}

// fetchBlobWithRetry fetches a blob, retrying on transient failures (a fresh
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
		blob, err := s.client.GetFileBlob(ctx, ref)
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

// Files returns the current file records (base + staged ops applied).
func (s *Store) Files() []json.RawMessage { return s.applyOps(collFiles, s.baseFiles) }

// Folders returns the current folder records (base + staged ops applied).
func (s *Store) Folders() []json.RawMessage { return s.applyOps(collFolders, s.baseFolders) }

// AddFile stages a new file record.
func (s *Store) AddFile(raw json.RawMessage) {
	s.ops = append(s.ops, op{coll: collFiles, kind: opAdd, id: recordID(raw), raw: raw})
}

// AddFolder stages a new folder record.
func (s *Store) AddFolder(raw json.RawMessage) {
	s.ops = append(s.ops, op{coll: collFolders, kind: opAdd, id: recordID(raw), raw: raw})
}

// UpdateFile stages a field patch on an existing file (preserves other fields).
func (s *Store) UpdateFile(id string, patch map[string]any) {
	s.ops = append(s.ops, op{coll: collFiles, kind: opUpdate, id: id, patch: patch})
}

// DeleteFile stages permanent removal of a file record.
func (s *Store) DeleteFile(id string) {
	s.ops = append(s.ops, op{coll: collFiles, kind: opDelete, id: id})
}

// TrashFile stages a soft-delete (sets the trashed timestamp).
func (s *Store) TrashFile(id, whenISO string) { s.UpdateFile(id, map[string]any{"trashed": whenISO}) }

// DeleteFolder stages permanent removal of a folder record.
func (s *Store) DeleteFolder(id string) {
	s.ops = append(s.ops, op{coll: collFolders, kind: opDelete, id: id})
}

// FileBlobs returns every content blob a file references — its current blob plus
// all version-history blobs — so a permanent delete can reclaim them.
func (s *Store) FileBlobs(id string) []string {
	for _, raw := range s.Files() {
		if recordID(raw) != id {
			continue
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
	return nil
}

// Dirty reports whether there are unsaved changes.
func (s *Store) Dirty() bool { return len(s.ops) > 0 }

// Save re-seals only the changed buckets + folders blob + root, retrying on a
// version conflict by reloading and re-applying the staged operations.
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

func (s *Store) saveOnce(ctx context.Context) error {
	files := s.applyOps(collFiles, s.baseFiles)
	folders := s.applyOps(collFolders, s.baseFolders)

	descriptors, shardBits, err := s.buildShards(ctx, files)
	if err != nil {
		return err
	}
	foldersDesc, err := s.buildCollection(ctx, folders, s.foldersDesc)
	if err != nil {
		return err
	}

	root, err := s.buildRoot(shardBits, descriptors, foldersDesc)
	if err != nil {
		return err
	}
	sealed, err := crypto.SealManifest(root, s.vk)
	if err != nil {
		return err
	}
	newVersion, err := s.client.SaveFilesStore(ctx, sealed, s.version)
	if err != nil {
		return err
	}
	s.version = newVersion
	s.shards = descriptors
	s.shardBits = shardBits
	s.foldersDesc = foldersDesc
	// Fold the applied ops into the base so a subsequent Save starts clean.
	s.baseFiles = files
	s.baseFolders = folders
	return nil
}

// buildShards buckets file records by id, re-seals only the buckets whose
// canonical content changed, and returns the descriptors + chosen shardBits.
func (s *Store) buildShards(ctx context.Context, files []json.RawMessage) ([]shardDesc, int, error) {
	type idVal struct {
		id  string
		val any
	}
	items := make([]idVal, 0, len(files))
	for _, raw := range files {
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
			return nil, 0, fmt.Errorf("files: canonicalize bucket %d: %w", b, err)
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

// buildCollection seals the folders array into its own content-addressed blob,
// reusing the previous blob when the canonical bytes are unchanged. An empty
// collection yields no descriptor.
func (s *Store) buildCollection(ctx context.Context, arr []json.RawMessage, prev *collDesc) (*collDesc, error) {
	if len(arr) == 0 {
		return nil, nil
	}
	vals := make([]any, len(arr))
	for i, raw := range arr {
		v, err := decodeAny(raw)
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	canon, err := canonicaljson.Marshal(vals)
	if err != nil {
		return nil, err
	}
	hash := sha256Hex(canon)
	if prev != nil && prev.Hash == hash && prev.Ref != "" {
		return prev, nil
	}
	ref, key, err := s.sealBlob(ctx, canon)
	if err != nil {
		return nil, err
	}
	return &collDesc{Ref: ref, Key: key, Hash: hash}, nil
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
	ref, err = s.client.UploadFileBlob(ctx, padded)
	if err != nil {
		return "", "", err
	}
	return ref, encKey, nil
}

// buildRoot assembles the v3 Files root, preserving unknown keys from the load.
func (s *Store) buildRoot(shardBits int, descriptors []shardDesc, folders *collDesc) ([]byte, error) {
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
	if folders != nil {
		if err := set("foldersRef", folders.Ref); err != nil {
			return nil, err
		}
		if err := set("foldersKey", folders.Key); err != nil {
			return nil, err
		}
		if err := set("foldersHash", folders.Hash); err != nil {
			return nil, err
		}
	} else {
		delete(root, "foldersRef")
		delete(root, "foldersKey")
		delete(root, "foldersHash")
	}
	return json.Marshal(root)
}

// applyOps returns a collection's base records with its staged ops applied
// (patches and deletes apply across both base and same-session adds).
func (s *Store) applyOps(coll string, base []json.RawMessage) []json.RawMessage {
	deleted := map[string]bool{}
	patches := map[string]map[string]any{}
	var adds []json.RawMessage
	for _, o := range s.ops {
		if o.coll != coll {
			continue
		}
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

// decodeAny decodes canonical JSON bytes into a generic value for
// re-canonicalization alongside other records.
func decodeAny(raw json.RawMessage) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// rootStr reads a string-valued key from the root map (empty when absent).
func rootStr(root map[string]json.RawMessage, key string) string {
	b, ok := root[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return ""
	}
	return s
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
