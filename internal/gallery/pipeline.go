package gallery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
)

// sigCap is the head/tail window the exact-file signature hashes (1 MiB), same
// as the web client's _fileSig.
const sigCap = 1024 * 1024

// Uploader runs the zero-knowledge upload pipeline for one photo at a time.
type Uploader struct {
	client *api.Client
	store  *Store
	vk     []byte
	withML bool
}

// NewUploader builds an uploader. withML enables the CLIP embedding + face
// detection pass (needs the server ML sidecar).
func NewUploader(client *api.Client, store *Store, vaultKey []byte, withML bool) *Uploader {
	return &Uploader{client: client, store: store, vk: vaultKey, withML: withML}
}

// Item is one thing to upload: a still, plus an optional paired motion clip
// (Live Photo) and an optional fallback capture time from a sidecar.
type Item struct {
	StillPath    string
	MotionPath   string    // paired .MOV/.MP4, or empty
	SidecarTaken time.Time // fallback capture time (zero if none)
}

// Outcome describes what happened to one item.
type Outcome int

const (
	// Uploaded means a new photo record was created.
	Uploaded Outcome = iota
	// Duplicate means the file was already in the gallery (skipped).
	Duplicate
)

// Upload runs the full pipeline for one item and, on success, stages the photo
// record in the store and returns it. It returns Duplicate (and a nil record)
// when the file's signature already exists.
func (u *Uploader) Upload(ctx context.Context, item Item, plain []byte) (Outcome, *PhotoRecord, error) {
	name := filepath.Base(item.StillPath)
	sig := fileSig(plain)
	if u.store.HasSig(sig) {
		return Duplicate, nil, nil
	}

	// 1. Encrypt + upload the original.
	originalRef, originalKey, err := u.encStore(ctx, plain)
	if err != nil {
		return 0, nil, fmt.Errorf("upload original: %w", err)
	}

	id, err := newID()
	if err != nil {
		return 0, nil, err
	}
	rec := &PhotoRecord{
		ID:           id,
		OriginalRef:  originalRef,
		OriginalKey:  originalKey,
		Name:         name,
		Mime:         mimeFor(name),
		Size:         int64(len(plain)),
		MediaType:    mediaType(name),
		Sig:          sig,
		Created:      nowISO(),
		FaceCropRefs: []string{},
	}

	// 2. Paired Live Photo motion clip (folder/zip basename pairing).
	if item.MotionPath != "" {
		motionBytes, rerr := readFileCapped(item.MotionPath)
		if rerr == nil {
			if ref, key, merr := u.encStore(ctx, motionBytes); merr == nil {
				rec.MotionRef, rec.MotionKey = ref, key
			}
		}
	}

	// 3. Transient-plaintext transform: thumbnails, EXIF, (optionally) ML. The
	// declared mime tells the server whether to treat this as a video.
	d, err := u.client.ProcessPhoto(ctx, name, rec.Mime, plain, u.withML)
	if err != nil {
		return 0, nil, fmt.Errorf("process: %w", err)
	}

	if err := u.applyDerived(ctx, rec, d, item); err != nil {
		return 0, nil, err
	}

	if err := u.store.Add(rec); err != nil {
		return 0, nil, err
	}
	return Uploaded, rec, nil
}

// VerifyBlob re-downloads and decrypts a stored blob and confirms it matches the
// expected plaintext. It is the safety check gating --delete: a local file is
// only removed once its ciphertext is provably retrievable and byte-correct.
func (u *Uploader) VerifyBlob(ctx context.Context, ref, key string, expected []byte) error {
	blob, err := u.client.GetGalleryBlob(ctx, ref)
	if err != nil {
		return fmt.Errorf("re-fetch blob: %w", err)
	}
	got, err := crypto.DecryptContent(blob, key, u.vk)
	if err != nil {
		return fmt.Errorf("re-decrypt blob: %w", err)
	}
	if !bytes.Equal(got, expected) {
		return fmt.Errorf("verification mismatch: uploaded bytes differ from local file")
	}
	return nil
}

// VerifyOriginal confirms the uploaded original round-trips to the local bytes.
func (u *Uploader) VerifyOriginal(ctx context.Context, rec *PhotoRecord, plain []byte) error {
	return u.VerifyBlob(ctx, rec.OriginalRef, rec.OriginalKey, plain)
}

// applyDerived encrypts and stores the process outputs (thumb/medium/motion,
// face crops, metadata) and promotes the display fields onto the record.
func (u *Uploader) applyDerived(ctx context.Context, rec *PhotoRecord, d api.ProcessResult, item Item) error {
	if b, ok := decodeB64(d.Thumb); ok {
		ref, key, err := u.encStore(ctx, b)
		if err != nil {
			return err
		}
		rec.ThumbRef, rec.ThumbKey = ref, key
	}
	if b, ok := decodeB64(d.Medium); ok {
		ref, key, err := u.encStore(ctx, b)
		if err != nil {
			return err
		}
		rec.MediumRef, rec.MediumKey = ref, key
	}
	if b, ok := decodeB64(d.Motion); ok {
		ref, key, err := u.encStore(ctx, b)
		if err != nil {
			return err
		}
		rec.MotionRef, rec.MotionKey = ref, key // processed motion overrides a paired clip
	}

	// Faces: encrypt each crop into its own blob.
	faces := make([]metaFace, 0, len(d.Faces))
	var cropRefs []string
	for _, f := range d.Faces {
		mf := metaFace{Score: f.Score, Box: f.Box, Embedding: f.Embedding}
		if b, ok := decodeB64(f.Crop); ok {
			ref, key, err := u.encStore(ctx, b)
			if err != nil {
				return err
			}
			mf.CropRef, mf.CropKey = ref, key
			cropRefs = append(cropRefs, ref)
		}
		faces = append(faces, mf)
	}

	// Metadata blob.
	exifJSON, _ := json.Marshal(d.Exif)
	meta := metaBlob{
		Exif:      exifJSON,
		Place:     orNull(d.Place),
		Embedding: orNull(d.Embedding),
		Phash:     orNull(d.Phash),
		Faces:     faces,
		Width:     d.Width,
		Height:    d.Height,
		Duration:  d.Duration,
		ContentID: d.ContentID,
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	metaRef, metaKey, err := u.encStore(ctx, metaJSON)
	if err != nil {
		return err
	}
	rec.MetaRef, rec.MetaKey = metaRef, metaKey

	// Promote display fields (as the web does after process).
	rec.TakenAt = d.Exif.TakenAt
	if rec.TakenAt == "" && !item.SidecarTaken.IsZero() {
		rec.TakenAt = item.SidecarTaken.UTC().Format(time.RFC3339)
	}
	if rec.TakenAt == "" {
		rec.TakenAt = rec.Created
	}
	rec.Width, rec.Height, rec.Duration = d.Width, d.Height, d.Duration
	rec.Lat, rec.Lng, rec.Camera = d.Exif.Lat, d.Exif.Lon, d.Exif.Camera
	rec.GeoChecked = true
	if d.ContentID != nil {
		rec.contentID = *d.ContentID // used to pair Live Photo halves post-upload
	}

	if u.withML {
		// ML ran inline: faces are known now (0 when none detected).
		n := len(faces)
		rec.HasFaces = &n
		rec.FaceCropRefs = cropRefs
		if rec.FaceCropRefs == nil {
			rec.FaceCropRefs = []string{}
		}
		rec.MlPending = false
	} else {
		// Fast upload: leave the photo un-analysed so the web/Android client
		// runs the deferred face + embedding pass later (matches the web).
		rec.HasFaces = nil
		rec.FaceCropRefs = []string{}
		rec.MlPending = true
	}
	return nil
}

// encStore encrypts bytes with a fresh key, pads to a Padmé bucket, uploads the
// blob and returns its id and wrapped key.
func (u *Uploader) encStore(ctx context.Context, plain []byte) (ref, key string, err error) {
	blob, encKey, err := crypto.EncryptContent(plain, u.vk)
	if err != nil {
		return "", "", err
	}
	padded, err := crypto.PadBlob(blob)
	if err != nil {
		return "", "", err
	}
	ref, err = u.client.UploadGalleryBlob(ctx, padded)
	if err != nil {
		return "", "", err
	}
	return ref, encKey, nil
}

// fileSig reproduces the web _fileSig: "<size>:<sha256 of head||tail 1 MiB>".
func fileSig(data []byte) string {
	h := sha256.New()
	if len(data) <= sigCap {
		h.Write(data)
	} else {
		h.Write(data[:sigCap])
		h.Write(data[len(data)-sigCap:])
	}
	return fmt.Sprintf("%d:%s", len(data), hex.EncodeToString(h.Sum(nil)))
}

// decodeB64 decodes a base64 field, returning ok=false for an empty field.
func decodeB64(s string) ([]byte, bool) {
	if s == "" {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return b, true
}

// orNull returns raw, or JSON null when empty, so the meta blob keeps the web's
// null semantics for absent fields.
func orNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

// nowISO is the current time as an ISO-8601 UTC string (matches new Date().toISOString()).
func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// mediaType classifies a file as "video" or "image" by extension.
func mediaType(name string) string {
	if isVideoExtName(name) {
		return "video"
	}
	return "image"
}
