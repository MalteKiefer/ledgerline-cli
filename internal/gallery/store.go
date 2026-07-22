package gallery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
	"github.com/MalteKiefer/ledgerline-cli/internal/shard"
)

// maxSaveRetries bounds the optimistic-concurrency retry loop on the manifest.
const maxSaveRetries = 5

// shardDesc describes one content-addressed record shard in the root (§4.1/§5.1).
type shardDesc struct {
	Ref    string `json:"ref"`
	Key    string `json:"key"`
	Hash   string `json:"hash"`
	Count  int    `json:"count"`
	Bucket int    `json:"bucket"`
}

// Store is an in-memory view of the Store v3 gallery manifest: a small sealed
// root pointer table plus content-addressed, id-bucketed record shards. New
// photos are appended and saved back, re-sealing only the buckets that changed
// plus the tiny root. Albums/people live in their own collection blobs, whose
// descriptors this client preserves verbatim (it never edits them).
type Store struct {
	client *api.Client
	vk     []byte

	// mu guards the fields mutated during a parallel upload: the added records
	// and the signature index. Blob/manifest saves run at a batch barrier, when
	// no upload workers are in flight, so they need no locking.
	mu sync.Mutex

	version    int64
	basePhotos []json.RawMessage // records loaded from the server (canonical, verbatim)
	added      []*PhotoRecord    // records appended this session (typed, so they merge)
	shardBits  int
	shards     []shardDesc                // descriptors from the last load/save
	root       map[string]json.RawMessage // full root, so unknown/collection keys survive
	sigs       map[string]bool
}

// NewStore builds a store bound to a client and unlocked vault key.
func NewStore(client *api.Client, vaultKey []byte) *Store {
	return &Store{client: client, vk: vaultKey, sigs: map[string]bool{}, root: map[string]json.RawMessage{}}
}

// Load fetches and decrypts the v3 root, then loads every record shard so new
// uploads can be de-duplicated and appended. v3 only — no v1/v2 read paths.
func (s *Store) Load(ctx context.Context) error {
	sealed, err := s.client.GalleryStore(ctx)
	if err != nil {
		return err
	}
	s.version = sealed.Version
	s.added = nil
	s.sigs = map[string]bool{}
	s.basePhotos = nil
	s.shards = nil
	s.shardBits = 0
	s.root = map[string]json.RawMessage{}

	if sealed.Ciphertext == "" {
		return nil
	}

	raw, err := crypto.OpenManifest(sealed.Ciphertext, s.vk)
	if err != nil {
		return fmt.Errorf("decrypt gallery manifest (wrong passphrase?): %w", err)
	}
	if err := json.Unmarshal(trimJSON(raw), &s.root); err != nil {
		return fmt.Errorf("parse gallery manifest: %w", err)
	}

	var v int
	if b, ok := s.root["v"]; ok {
		_ = json.Unmarshal(b, &v)
	}
	if v != 3 {
		// Clean slate: an absent or non-v3 root is treated as a fresh store.
		s.root = map[string]json.RawMessage{}
		return nil
	}

	if b, ok := s.root["shardBits"]; ok {
		_ = json.Unmarshal(b, &s.shardBits)
	}
	if b, ok := s.root["shards"]; ok {
		if err := json.Unmarshal(b, &s.shards); err != nil {
			return fmt.Errorf("parse gallery shards: %w", err)
		}
	}

	photos, err := s.loadShards(ctx, s.shards)
	if err != nil {
		return err
	}
	s.basePhotos = photos
	s.indexSigs(s.basePhotos)
	return nil
}

// loadShards downloads, decrypts and concatenates every shard's records. A failed
// shard aborts the load rather than silently dropping photos (a partial set could
// be saved and would free the "missing" shard, losing data for good).
func (s *Store) loadShards(ctx context.Context, shards []shardDesc) ([]json.RawMessage, error) {
	var photos []json.RawMessage
	for i, sh := range shards {
		blob, err := s.fetchShardWithRetry(ctx, sh.Ref)
		if err != nil {
			return nil, fmt.Errorf("fetch shard %d/%d (%s): %w", i+1, len(shards), sh.Ref, err)
		}
		plain, err := crypto.DecryptContent(blob, sh.Key, s.vk)
		if err != nil {
			return nil, fmt.Errorf("decrypt shard %d/%d: %w", i+1, len(shards), err)
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(plain, &arr); err != nil {
			return nil, fmt.Errorf("parse shard %d/%d: %w", i+1, len(shards), err)
		}
		photos = append(photos, arr...)
	}
	return photos, nil
}

// fetchShardWithRetry fetches a shard blob, retrying a few times on a transient
// failure (a 404 can briefly follow a fresh object-storage write, and 5xx/429
// are transient). It never deletes or overwrites anything.
func (s *Store) fetchShardWithRetry(ctx context.Context, ref string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		blob, err := s.client.GetGalleryBlob(ctx, ref)
		if err == nil {
			return blob, nil
		}
		lastErr = err
		if code := api.Status(err); code != 0 && code != 404 && code != 429 && code < 500 {
			break // a definitive client error won't fix itself
		}
	}
	return nil, lastErr
}

// indexSigs records the exact-file signatures already present so duplicates are
// skipped.
func (s *Store) indexSigs(photos []json.RawMessage) {
	for _, p := range photos {
		var meta struct {
			Sig string `json:"sig"`
		}
		if err := json.Unmarshal(p, &meta); err == nil && meta.Sig != "" {
			s.sigs[meta.Sig] = true
		}
	}
}

// HasSig reports whether a photo with this signature already exists (including
// ones appended this session).
func (s *Store) HasSig(sig string) bool {
	if sig == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sigs[sig]
}

// Records returns the loaded photo records (typed) for read-only use such as
// download. Unknown fields on a record are ignored.
func (s *Store) Records() []PhotoRecord {
	out := make([]PhotoRecord, 0, len(s.basePhotos))
	for _, raw := range s.basePhotos {
		var r PhotoRecord
		if err := json.Unmarshal(raw, &r); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// Add appends a finished photo record and remembers its signature.
func (s *Store) Add(rec *PhotoRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.added = append(s.added, rec)
	if rec.Sig != "" {
		s.sigs[rec.Sig] = true
	}
	return nil
}

// PendingCount is how many new photos are staged for the next save.
func (s *Store) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.added)
}

// MergeLivePhotos pairs a still and its separately-uploaded video by Apple
// content id (how iCloud exports split Live Photos, since the two files have
// unrelated names). The video's original blob becomes the still's motion clip
// and the video record is dropped. Returns the number of merges performed.
func (s *Store) MergeLivePhotos() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	stills := map[string]*PhotoRecord{}
	for _, rec := range s.added {
		if rec.MediaType != "video" && rec.contentID != "" && rec.MotionRef == "" {
			if _, seen := stills[rec.contentID]; !seen {
				stills[rec.contentID] = rec
			}
		}
	}

	merged := 0
	for _, rec := range s.added {
		if rec.MediaType != "video" || rec.contentID == "" {
			continue
		}
		still, ok := stills[rec.contentID]
		if !ok || still.MotionRef != "" {
			continue
		}
		still.MotionRef, still.MotionKey = rec.OriginalRef, rec.OriginalKey
		rec.merged = true // the video is now the still's motion; don't write it out
		merged++
	}
	return merged
}

// Save writes the manifest back, re-sealing only the buckets whose canonical
// content changed plus the tiny root. On a version conflict it reloads the base
// photos, re-applies this session's additions and retries.
func (s *Store) Save(ctx context.Context) error {
	if len(s.added) == 0 {
		return nil
	}
	for attempt := 0; attempt < maxSaveRetries; attempt++ {
		if err := s.saveOnce(ctx); err != nil {
			if errors.Is(err, api.ErrVersionConflict) {
				if rerr := s.reloadKeepingAdded(ctx); rerr != nil {
					return rerr
				}
				continue
			}
			return err
		}
		return nil
	}
	return errors.New("gallery: manifest kept conflicting; try again")
}

// idValue is a record's id plus its generic (canonicalizable) value.
type idValue struct {
	id  string
	val any
}

// saveOnce buckets all records by id, re-seals only changed buckets, and PUTs the
// sealed v3 root at the current version.
func (s *Store) saveOnce(ctx context.Context) error {
	// Unified record list: loaded base records (verbatim) + this session's adds
	// (rendered to the web-faithful map). Merged video halves are dropped.
	items := make([]idValue, 0, len(s.basePhotos)+len(s.added))
	for _, raw := range s.basePhotos {
		var idOnly struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &idOnly); err != nil {
			return fmt.Errorf("gallery: base record missing id: %w", err)
		}
		v, err := decodeCanonical(raw)
		if err != nil {
			return err
		}
		items = append(items, idValue{id: idOnly.ID, val: v})
	}
	for _, rec := range s.added {
		if rec.merged {
			continue
		}
		items = append(items, idValue{id: rec.ID, val: rec.asMap()})
	}

	shardBits := shard.RecommendedBits(len(items))
	rebucket := shardBits != s.shardBits

	// Group by bucket, each bucket sorted ascending by id (§5.1 canonical order).
	buckets := map[int][]idValue{}
	for _, it := range items {
		b := shard.BucketOf(it.id, shardBits)
		buckets[b] = append(buckets[b], it)
	}
	bucketIdxs := make([]int, 0, len(buckets))
	for b := range buckets {
		bucketIdxs = append(bucketIdxs, b)
	}
	sort.Ints(bucketIdxs)

	prevByBucket := map[int]shardDesc{}
	if !rebucket {
		for _, sh := range s.shards {
			prevByBucket[sh.Bucket] = sh
		}
	}

	descriptors := make([]shardDesc, 0, len(bucketIdxs))
	for _, b := range bucketIdxs {
		recs := buckets[b]
		sort.Slice(recs, func(i, j int) bool { return recs[i].id < recs[j].id })
		vals := make([]any, len(recs))
		for i, r := range recs {
			vals[i] = r.val
		}
		canon, err := canonicalizeRecords(vals)
		if err != nil {
			return fmt.Errorf("gallery: canonicalize bucket %d: %w", b, err)
		}
		hash := sha256Hex(canon)

		if prev, ok := prevByBucket[b]; ok && prev.Hash == hash && prev.Ref != "" {
			prev.Count = len(recs)
			descriptors = append(descriptors, prev) // unchanged → reuse blob
			continue
		}

		blob, encKey, err := crypto.EncryptContent(canon, s.vk)
		if err != nil {
			return err
		}
		padded, err := crypto.PadBlob(blob)
		if err != nil {
			return err
		}
		ref, err := s.client.UploadGalleryBlob(ctx, padded)
		if err != nil {
			return err
		}
		descriptors = append(descriptors, shardDesc{Ref: ref, Key: encKey, Hash: hash, Count: len(recs), Bucket: b})
	}

	root, err := s.buildRoot(shardBits, descriptors)
	if err != nil {
		return err
	}
	sealed, err := crypto.SealManifest(root, s.vk)
	if err != nil {
		return err
	}
	newVersion, err := s.client.SaveGalleryStore(ctx, sealed, s.version)
	if err != nil {
		return err
	}
	s.version = newVersion
	s.shards = descriptors
	s.shardBits = shardBits
	return nil
}

// buildRoot assembles the sealed v3 root: it sets v/suite/shardBits/shards and
// preserves every other key loaded from the server (albumsRef/peopleRef
// collection-blob descriptors, caps, and any field this client doesn't model).
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

// reloadKeepingAdded re-fetches the base photos after a conflict while keeping
// this session's new records staged.
func (s *Store) reloadKeepingAdded(ctx context.Context) error {
	added := s.added
	if err := s.Load(ctx); err != nil {
		return err
	}
	s.added = added
	for _, rec := range added {
		if rec.Sig != "" {
			s.sigs[rec.Sig] = true
		}
	}
	return nil
}

// sha256Hex returns the lowercase hex SHA-256 of b (shard change detection).
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// trimJSON drops the trailing space padding SealManifest adds so the strict
// unmarshaler sees clean JSON.
func trimJSON(b []byte) []byte {
	i := len(b)
	for i > 0 && (b[i-1] == ' ' || b[i-1] == '\n' || b[i-1] == '\t' || b[i-1] == '\r' || b[i-1] == 0) {
		i--
	}
	return b[:i]
}
