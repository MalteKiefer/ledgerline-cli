// Package passwords implements the zero-knowledge Passwords module client. It is a
// Store v3 SHARDED store structurally identical to Files: the secret records live
// in content-addressed, id-bucketed shard blobs (recordKey "secrets") and the
// folders live in a single "secretFolders" collection blob at the root's
// foldersRef/foldersKey/foldersHash keys (same root layout the web LLPasswordsStore
// uses). Every secret field (fields/custom/versions) is plaintext IN the record —
// there are no per-secret content blobs, only the record-shard + folders blobs.
//
// The engine mirrors internal/files/store.go and reuses the shared crypto,
// canonicaljson and shard seams unchanged, so the byte contract is identical.
package passwords

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

const (
	module      = "passwords"
	collSecrets = "secrets"       // sharded record collection
	collFolders = "secretFolders" // sibling collection blob (root foldersRef/Key/Hash)
)

const maxSaveRetries = 5

// shardDesc describes one content-addressed secret-record shard in the root.
type shardDesc struct {
	Ref    string `json:"ref"`
	Key    string `json:"key"`
	Hash   string `json:"hash"`
	Count  int    `json:"count"`
	Bucket int    `json:"bucket"`
}

// collDesc describes the content-addressed secretFolders collection blob.
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

// Store is the Store v3 sharded view of the Passwords module.
type Store struct {
	client *api.Client
	vk     []byte

	version       int64
	baseSecrets   []json.RawMessage
	baseFolders   []json.RawMessage
	shardBits     int
	shards        []shardDesc
	foldersDesc   *collDesc
	root          map[string]json.RawMessage // preserve unknown root keys
	ops           []op
	cache         *blobcache.Cache
	degraded      bool
	missingShards int
}

// Degraded reports whether the last load skipped a permanently-missing shard.
func (s *Store) Degraded() bool     { return s.degraded }
func (s *Store) MissingShards() int { return s.missingShards }

// NewStore builds a Passwords store bound to a client and unlocked vault key.
func NewStore(client *api.Client, vaultKey []byte) *Store {
	return &Store{client: client, vk: vaultKey, root: map[string]json.RawMessage{}}
}

// SetShardCache attaches a ciphertext shard cache (ciphertext only at rest).
func (s *Store) SetShardCache(c *blobcache.Cache) { s.cache = c }

// Load fetches and decrypts the v3 Passwords root, its secret-record shards and
// the secretFolders collection blob.
func (s *Store) Load(ctx context.Context) error {
	sealed, err := s.client.ShardedStore(ctx, module)
	if err != nil {
		return err
	}
	s.version = sealed.Version
	s.ops = nil
	s.baseSecrets = nil
	s.baseFolders = nil
	s.shards = nil
	s.shardBits = 0
	s.foldersDesc = nil
	s.root = map[string]json.RawMessage{}
	s.degraded = false
	s.missingShards = 0

	if sealed.Ciphertext == "" {
		return nil
	}
	raw, err := crypto.OpenManifest(sealed.Ciphertext, s.vk)
	if err != nil {
		return errors.New("decrypt passwords manifest failed (wrong passphrase?)")
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
			return fmt.Errorf("parse passwords shards: %w", err)
		}
	}
	secrets, err := s.loadShards(ctx, s.shards)
	if err != nil {
		return err
	}
	s.baseSecrets = secrets

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

// loadShards downloads, decrypts and concatenates every shard's secret records.
// A permanently-missing (404) shard is tolerated (store goes degraded READ-ONLY);
// any other failure aborts.
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
					return nil, fmt.Errorf("fetch passwords shard %d/%d: %w", i+1, len(shards), err)
				}
			}
			s.cache.Put(sh.Ref, blob)
		}
		plain, err := crypto.DecryptContent(blob, sh.Key, s.vk)
		if err != nil {
			return nil, fmt.Errorf("decrypt passwords shard %d/%d: %w", i+1, len(shards), err)
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(plain, &arr); err != nil {
			return nil, fmt.Errorf("parse passwords shard %d/%d: %w", i+1, len(shards), err)
		}
		recs = append(recs, arr...)
	}
	s.cache.Prune(shardRefsOf(shards))
	return recs, nil
}

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

// loadCollection downloads + decrypts the secretFolders collection blob → array.
func (s *Store) loadCollection(ctx context.Context, ref, key string) ([]json.RawMessage, error) {
	blob, err := s.fetchBlobWithRetry(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("fetch passwords collection blob: %w", err)
	}
	plain, err := crypto.DecryptContent(blob, key, s.vk)
	if err != nil {
		return nil, fmt.Errorf("decrypt passwords collection blob: %w", err)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(plain, &arr); err != nil {
		return nil, fmt.Errorf("parse passwords collection blob: %w", err)
	}
	return arr, nil
}

// Secrets / Folders return the current records (base + staged ops applied).
func (s *Store) Secrets() []json.RawMessage { return s.applyOps(collSecrets, s.baseSecrets) }
func (s *Store) Folders() []json.RawMessage { return s.applyOps(collFolders, s.baseFolders) }

// AddSecret / UpdateSecret / DeleteSecret stage record mutations.
func (s *Store) AddSecret(raw json.RawMessage) {
	s.ops = append(s.ops, op{coll: collSecrets, kind: opAdd, id: recordID(raw), raw: raw})
}
func (s *Store) UpdateSecret(id string, patch map[string]any) {
	s.ops = append(s.ops, op{coll: collSecrets, kind: opUpdate, id: id, patch: patch})
}
func (s *Store) DeleteSecret(id string) {
	s.ops = append(s.ops, op{coll: collSecrets, kind: opDelete, id: id})
}

// addFolderRaw / UpdateFolder / DeleteFolder stage folder mutations. addFolderRaw
// is the low-level raw stager; the typed AddFolder(name, role) wrapper builds the
// record and calls it.
func (s *Store) addFolderRaw(raw json.RawMessage) {
	s.ops = append(s.ops, op{coll: collFolders, kind: opAdd, id: recordID(raw), raw: raw})
}
func (s *Store) UpdateFolder(id string, patch map[string]any) {
	s.ops = append(s.ops, op{coll: collFolders, kind: opUpdate, id: id, patch: patch})
}
func (s *Store) DeleteFolder(id string) {
	s.ops = append(s.ops, op{coll: collFolders, kind: opDelete, id: id})
}

// Dirty reports whether there are unsaved changes.
func (s *Store) Dirty() bool { return len(s.ops) > 0 }

// ErrDegraded is returned by Save when a shard is permanently missing.
var ErrDegraded = errors.New("passwords: store is degraded (a record shard is missing) — refusing to save so no data is lost")

// Save re-seals only the changed buckets + folders blob + root.
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
	return errors.New("passwords: manifest kept conflicting; try again")
}

func (s *Store) saveOnce(ctx context.Context) error {
	secrets := s.applyOps(collSecrets, s.baseSecrets)
	folders := s.applyOps(collFolders, s.baseFolders)

	descriptors, shardBits, err := s.buildShards(ctx, secrets)
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
	live := shardRefsOf(descriptors)
	if foldersDesc != nil && foldersDesc.Ref != "" {
		live = append(live, foldersDesc.Ref)
	}
	// Both slices (secrets + folders) are fully known to this client, so the
	// counts map is always complete (feeds the server's anomaly-scan).
	counts := map[string]int{
		collSecrets: sumDescriptorCounts(descriptors),
		collFolders: len(folders),
	}
	newVersion, err := s.client.SaveShardedStore(ctx, module, sealed, s.version, live, counts)
	if err != nil {
		return err
	}
	s.version = newVersion
	s.shards = descriptors
	s.shardBits = shardBits
	s.foldersDesc = foldersDesc
	s.baseSecrets = secrets
	s.baseFolders = folders
	return nil
}

func (s *Store) buildShards(ctx context.Context, secrets []json.RawMessage) ([]shardDesc, int, error) {
	type idVal struct {
		id  string
		val any
	}
	items := make([]idVal, 0, len(secrets))
	for _, raw := range secrets {
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
			return nil, 0, fmt.Errorf("passwords: canonicalize bucket %d: %w", b, err)
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

func sumDescriptorCounts(descriptors []shardDesc) int {
	total := 0
	for _, d := range descriptors {
		total += d.Count
	}
	return total
}

func shardRefsOf(shards []shardDesc) []string {
	refs := make([]string, 0, len(shards))
	for _, sh := range shards {
		if sh.Ref != "" {
			refs = append(refs, sh.Ref)
		}
	}
	return refs
}

func recordID(raw json.RawMessage) string {
	var r struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &r)
	return r.ID
}

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

func decodeAny(raw json.RawMessage) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

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

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

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
