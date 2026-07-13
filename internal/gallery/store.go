package gallery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
)

// shardSize is GALLERY_SHARD_SIZE from the web client: photos per sealed shard.
const shardSize = 1000

// maxSaveRetries bounds the optimistic-concurrency retry loop on the manifest.
const maxSaveRetries = 5

// root is the top-level v2 manifest.
type root struct {
	V      int             `json:"v"`
	Shards []shardDesc     `json:"shards"`
	Albums json.RawMessage `json:"albums"`
	People json.RawMessage `json:"people"`
	// v1 fallback: an unsharded manifest carries photos inline.
	Photos []json.RawMessage `json:"photos"`
}

// shardDesc describes one photo shard blob in the root manifest.
type shardDesc struct {
	Ref   string `json:"ref"`
	Key   string `json:"key"`
	Hash  string `json:"hash"`
	Count int    `json:"count"`
}

// Store is an in-memory view of the gallery manifest that appends new photos and
// saves them back, re-sealing only the shards that changed.
type Store struct {
	client *api.Client
	vk     []byte

	// mu guards the fields mutated during a parallel upload: the added records
	// and the signature index. Blob/manifest saves run at a batch barrier, when
	// no upload workers are in flight, so they need no locking.
	mu sync.Mutex

	version    int64
	basePhotos []json.RawMessage // photos loaded from the server (preserved verbatim)
	added      []*PhotoRecord    // photos appended this session (typed, so they can be merged)
	albums     json.RawMessage
	people     json.RawMessage
	shards     []shardDesc // descriptors from the last load
	sigs       map[string]bool
}

// NewStore builds a store bound to a client and unlocked vault key.
func NewStore(client *api.Client, vaultKey []byte) *Store {
	return &Store{client: client, vk: vaultKey, sigs: map[string]bool{}}
}

// Load fetches and decrypts the manifest, loading all photo records (following
// v2 shards) so new uploads can be de-duplicated and appended.
func (s *Store) Load(ctx context.Context) error {
	sealed, err := s.client.GalleryStore(ctx)
	if err != nil {
		return err
	}
	s.version = sealed.Version
	s.added = nil
	s.sigs = map[string]bool{}

	if sealed.Ciphertext == "" {
		s.basePhotos = nil
		s.shards = nil
		s.albums = json.RawMessage("[]")
		s.people = json.RawMessage("[]")
		return nil
	}

	raw, err := crypto.OpenManifest(sealed.Ciphertext, s.vk)
	if err != nil {
		return fmt.Errorf("decrypt gallery manifest (wrong passphrase?): %w", err)
	}
	var r root
	if err := json.Unmarshal(trimJSON(raw), &r); err != nil {
		return fmt.Errorf("parse gallery manifest: %w", err)
	}

	s.albums = orEmptyArray(r.Albums)
	s.people = orEmptyArray(r.People)

	if r.V == 2 && len(r.Shards) > 0 {
		s.shards = r.Shards
		photos, err := s.loadShards(ctx, r.Shards)
		if err != nil {
			return err
		}
		s.basePhotos = photos
	} else {
		// v1 / blank: photos live inline.
		s.shards = nil
		s.basePhotos = r.Photos
	}

	s.indexSigs(s.basePhotos)
	return nil
}

// loadShards downloads, decrypts and concatenates every shard's photo records.
// A failed shard aborts the load rather than silently dropping photos.
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
			Sig     string `json:"sig"`
			Trashed any    `json:"trashed"`
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

// Save writes the manifest back, re-sealing only changed shards. On a version
// conflict it reloads the base photos, re-applies this session's additions and
// retries.
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

// saveOnce builds the shards and PUTs the sealed manifest at the current version.
func (s *Store) saveOnce(ctx context.Context) error {
	photos := make([]json.RawMessage, 0, len(s.basePhotos)+len(s.added))
	photos = append(photos, s.basePhotos...)
	for _, rec := range s.added {
		if rec.merged {
			continue // a video folded into a still's motion clip
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		photos = append(photos, data)
	}

	descriptors, err := s.buildShards(ctx, photos)
	if err != nil {
		return err
	}

	r := root{V: 2, Shards: descriptors, Albums: orEmptyArray(s.albums), People: orEmptyArray(s.people)}
	manifestJSON, err := json.Marshal(r)
	if err != nil {
		return err
	}
	sealed, err := crypto.SealManifest(manifestJSON, s.vk)
	if err != nil {
		return err
	}

	newVersion, err := s.client.SaveGalleryStore(ctx, sealed, s.version)
	if err != nil {
		return err
	}
	s.version = newVersion
	s.shards = descriptors
	return nil
}

// buildShards splits photos into shards and re-seals only the ones whose content
// changed since the last load, reusing unchanged shard blobs.
func (s *Store) buildShards(ctx context.Context, photos []json.RawMessage) ([]shardDesc, error) {
	var descriptors []shardDesc
	for i := 0; i < len(photos); i += shardSize {
		end := i + shardSize
		if end > len(photos) {
			end = len(photos)
		}
		chunk := photos[i:end]
		chunkJSON, err := json.Marshal(chunk)
		if err != nil {
			return nil, err
		}
		hash := sha256Hex(chunkJSON)

		idx := len(descriptors)
		if idx < len(s.shards) && s.shards[idx].Hash == hash && s.shards[idx].Ref != "" {
			descriptors = append(descriptors, s.shards[idx]) // unchanged → reuse
			continue
		}

		blob, encKey, err := crypto.EncryptContent(chunkJSON, s.vk)
		if err != nil {
			return nil, err
		}
		padded, err := crypto.PadBlob(blob)
		if err != nil {
			return nil, err
		}
		ref, err := s.client.UploadGalleryBlob(ctx, padded)
		if err != nil {
			return nil, err
		}
		descriptors = append(descriptors, shardDesc{Ref: ref, Key: encKey, Hash: hash, Count: len(chunk)})
	}
	return descriptors, nil
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

// orEmptyArray returns raw, or an empty JSON array when raw is nil/empty.
func orEmptyArray(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}
	return raw
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
