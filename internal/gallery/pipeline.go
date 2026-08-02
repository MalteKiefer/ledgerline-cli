package gallery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/canonicaljson"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
	"github.com/MalteKiefer/ledgerline-cli/internal/ml"
)

// sigCap is the head/tail window the exact-file signature hashes (1 MiB), same
// as the web client's _fileSig.
const sigCap = 1024 * 1024

// defaultClipModel is the CLIP model the record's embModel is tagged with when an
// embedding is present. It matches the web/server default Smart Search model
// (README --ml-clip-model default), which must equal the server's for search to
// compare embeddings coherently (§8.5).
const defaultClipModel = "ViT-B-32__openai"

// Uploader runs the zero-knowledge upload pipeline for one photo at a time. It
// is safe for concurrent use across items (the store it writes to is guarded).
type Uploader struct {
	client    *api.Client
	store     *Store
	vk        []byte
	process   bool        // opt-in server derivation (/process); off = partial records
	withML    bool        // run the CLIP + face pass on the server
	analyzer  ml.Analyzer // run it on a local ML instance instead (nil if unused)
	clipModel string      // CLIP model name tagged on embModel when an embedding exists
}

// NewUploader builds an uploader. process opts into the transient-plaintext
// server derivation (/process); with it off (and no ML), the CLI writes a partial
// record (basics + thumbPending) with NO plaintext egress (§8.1/§8.2), deferring
// thumb/EXIF derivation to a GUI client. withML enables the server-side CLIP +
// face pass; analyzer runs that pass on a local ML instance instead. Any ML mode
// implies derivation. At most one of withML/analyzer is in effect.
func NewUploader(client *api.Client, store *Store, vaultKey []byte, process, withML bool, analyzer ml.Analyzer) *Uploader {
	return &Uploader{
		client: client, store: store, vk: vaultKey,
		process: process, withML: withML, analyzer: analyzer,
		clipModel: defaultClipModel,
	}
}

// SetClipModel overrides the CLIP model name tagged on embModel (from
// --ml-clip-model), so a record's embModel matches the model that produced its
// embedding.
func (u *Uploader) SetClipModel(name string) {
	if name != "" {
		u.clipModel = name
	}
}

// Item is one thing to upload: a still, plus an optional paired motion clip
// (Live Photo) and an optional fallback capture time from a sidecar.
type Item struct {
	StillPath    string
	MotionPath   string        // paired .MOV/.MP4, or empty
	SidecarTaken time.Time     // fallback capture time (zero if none)
	Imported     *ImportedMeta // authoritative source metadata (Immich), or nil
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
	sig := FileSig(plain)
	// Atomically claim the signature so two workers importing byte-identical
	// assets in the same batch can't both pass the dedup check and each create a
	// record. If we don't reach a successful Add (any error below), release the
	// claim so a later item — or a re-run — is not wrongly skipped as a duplicate.
	if !u.store.ReserveSig(sig) {
		return Duplicate, nil, nil
	}
	committed := false
	defer func() {
		if !committed {
			u.store.ReleaseSig(sig)
		}
	}()

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

	// 2. Paired Live Photo motion clip (folder/zip basename pairing). A failure
	// here is non-fatal — the still is already safe — but must not be silent, so
	// the motion half is not dropped without the user knowing.
	if item.MotionPath != "" {
		motionBytes, rerr := readFileCapped(item.MotionPath)
		switch {
		case rerr != nil:
			rec.motionWarn = fmt.Errorf("motion clip unreadable: %w", rerr)
		default:
			if ref, key, merr := u.encStore(ctx, motionBytes); merr != nil {
				rec.motionWarn = fmt.Errorf("motion clip upload failed: %w", merr)
			} else {
				rec.MotionRef, rec.MotionKey = ref, key
			}
		}
	}

	// 3. Derivation is OPT-IN (§8.1/§8.2). By default the CLI writes a partial
	// record (basics + thumbPending) with no plaintext egress; a GUI client
	// backfills thumb/medium/EXIF/ML later. --process (or an ML mode) opts into the
	// transient-plaintext server transform.
	if !u.process && !u.withML && u.analyzer == nil {
		// A direct-import source (Immich) already holds authoritative capture
		// metadata: write the cold meta blob and promote the display fields from
		// it, with NO plaintext egress, so a no-ML import still produces a rich
		// record. Thumb/ML stay pending for a GUI client to backfill (an empty
		// ProcessResult carries no renditions/faces/embedding; applyDerived folds
		// item.Imported in).
		if item.Imported != nil {
			// No /process and no ML ran, so nothing reverse-geocoded this photo:
			// pass geoAttempted=false so its injected GPS still invites a richer
			// client's place backfill (geoChecked stays false), just as thumb/ML
			// stay pending.
			if err := u.applyDerived(ctx, rec, api.ProcessResult{}, item, false, false); err != nil {
				return 0, nil, err
			}
			rec.ThumbPending = true
			if err := u.store.Add(rec); err != nil {
				return 0, nil, err
			}
			committed = true
			return Uploaded, rec, nil
		}
		rec.SetPartial(true)
		rec.ThumbPending = true
		if err := u.store.Add(rec); err != nil {
			return 0, nil, err
		}
		committed = true
		return Uploaded, rec, nil
	}

	// Transient-plaintext transform: thumbnails, EXIF, (optionally) server ML.
	// The declared mime tells the server whether to treat this as a video.
	d, err := u.client.ProcessPhoto(ctx, name, rec.Mime, plain, u.withML)
	if err != nil {
		return 0, nil, fmt.Errorf("process: %w", err)
	}

	// 4. Optional local ML: run CLIP + face detection on the server's rendition
	// and fold the results into the derived data, so the record is analysed
	// exactly as a server-ML upload would be.
	mlResolved := u.withML
	if u.analyzer != nil {
		if err := u.runLocalML(ctx, &d); err != nil {
			return 0, nil, fmt.Errorf("local ML: %w", err)
		}
		mlResolved = true
	}

	// The /process transform reverse-geocodes on the server, so a place was
	// genuinely attempted here (geoChecked=true) even when none was found.
	if err := u.applyDerived(ctx, rec, d, item, mlResolved, true); err != nil {
		return 0, nil, err
	}

	if err := u.store.Add(rec); err != nil {
		return 0, nil, err
	}
	committed = true
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

// runLocalML analyses the server's rendition on the local ML instance and folds
// the CLIP embedding and detected faces into d, so applyDerived stores them just
// like a server-ML result. It analyses the medium rendition (falling back to the
// thumbnail) because those are already decodable JPEGs regardless of the original
// format (HEIC/RAW/video), and face boxes then match the image being cropped.
func (u *Uploader) runLocalML(ctx context.Context, d *api.ProcessResult) error {
	img, ok := decodeB64(d.Medium)
	if !ok {
		img, ok = decodeB64(d.Thumb)
	}
	if !ok {
		return fmt.Errorf("no rendition to analyse")
	}
	res, err := u.analyzer.Analyze(ctx, img)
	if err != nil {
		return err
	}
	if len(res.Embedding) > 0 {
		if enc, merr := json.Marshal(res.Embedding); merr == nil {
			d.Embedding = enc
		}
	}
	d.Faces = make([]api.ProcessFace, 0, len(res.Faces))
	for _, f := range res.Faces {
		d.Faces = append(d.Faces, api.ProcessFace{
			Score:     f.Score,
			Box:       f.Box,
			Embedding: f.Embedding,
			Crop:      base64.StdEncoding.EncodeToString(f.CropJPEG),
		})
	}
	return nil
}

// applyDerived encrypts and stores the process outputs (thumb/medium/motion,
// face crops, metadata) and promotes the display fields onto the record.
// mlResolved reports whether the ML pass (server or local) ran, so the record is
// marked analysed instead of leaving it for the web client's deferred pass.
// geoAttempted reports whether a reverse-geocode pass actually ran (it does on
// /process, not on the no-egress injected import); it gates geoChecked so a photo
// with injected GPS but no place is not falsely marked "geo already checked",
// which would suppress a richer client's place backfill.
func (u *Uploader) applyDerived(ctx context.Context, rec *PhotoRecord, d api.ProcessResult, item Item, mlResolved, geoAttempted bool) error {
	// Fold in authoritative import metadata (Immich): injected TakenAt wins over
	// /process EXIF (§4.1 precedence); GPS/camera/dims/duration fill only where the
	// derived data has none, so recomputed thumbs/faces/embeddings still layer on
	// top. d is a value copy — mutating it here does not touch the caller's.
	if im := item.Imported; im != nil {
		if !im.TakenAt.IsZero() {
			d.Exif.TakenAt = im.TakenAt.UTC().Format(time.RFC3339)
		}
		if d.Exif.Lat == nil {
			d.Exif.Lat = im.Lat
		}
		if d.Exif.Lon == nil {
			d.Exif.Lon = im.Lon
		}
		if d.Exif.Camera == nil {
			if cam := importedCamera(im); cam != "" {
				d.Exif.Camera = &cam
			}
		}
		if d.Width == 0 {
			d.Width = im.Width
		}
		if d.Height == 0 {
			d.Height = im.Height
		}
		if d.Duration == nil {
			d.Duration = im.DurationSec
		}
	}

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

	// embModel tags the record + meta with the CLIP model that produced the
	// embedding (§8.5).
	embModel := pickEmbModel(hasEmbedding(d.Embedding), u.analyzer != nil, u.clipModel, d.Model)

	// Metadata blob (cold; per-photo, immutable, never hashed → floats allowed).
	exifJSON, _ := json.Marshal(d.Exif)
	meta := metaBlob{
		Exif:      exifJSON,
		Place:     orNull(d.Place),
		Embedding: orNull(d.Embedding),
		Phash:     orNull(d.Phash),
		EmbModel:  embModel,
		Faces:     faces,
		Width:     d.Width,
		Height:    d.Height,
		Duration:  d.Duration,
		ContentID: d.ContentID,
	}
	if item.Imported != nil {
		fav := item.Imported.Favorite
		meta.Favorite = &fav
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
	rec.Width, rec.Height = d.Width, d.Height
	rec.Duration = roundDuration(d.Duration)
	rec.Lat, rec.Lng = dec6Ptr(d.Exif.Lat), dec6Ptr(d.Exif.Lon)
	rec.Camera = d.Exif.Camera
	rec.EmbModel = embModel
	rec.GeoChecked = geoAttempted
	if d.ContentID != nil {
		rec.contentID = *d.ContentID // used to pair Live Photo halves post-upload
	}

	if mlResolved {
		// ML ran (server or local): faces are known now (0 when none detected).
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

// pickEmbModel chooses the CLIP model name to tag on a record's embModel (§8.5).
// Returns nil when there is no embedding to tag. When a local analyzer produced
// the embedding, our configured model name is authoritative; otherwise the server
// produced it and its returned `model` name wins, falling back to our configured
// name on an older server that does not return one.
func pickEmbModel(hasEmb, localAnalyzer bool, clipModel, serverModel string) *string {
	if !hasEmb {
		return nil
	}
	name := clipModel
	if !localAnalyzer && serverModel != "" {
		name = serverModel
	}
	return &name
}

// hasEmbedding reports whether a process result carried a real CLIP embedding
// array (not null/empty), so embModel is only tagged when there is one.
func hasEmbedding(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return len(s) > 0 && !bytes.Equal(s, []byte("null")) && !bytes.Equal(s, []byte("[]"))
}

// roundDuration converts a fractional-seconds duration to the integer-seconds the
// hot record stores (§4.1/§5.2 integer-only), matching the server's (int) round.
func roundDuration(f *float64) *int {
	if f == nil {
		return nil
	}
	n := int(math.Round(*f))
	return &n
}

// importedCamera joins an import source's camera make and model into the single
// camera string the record/meta blob use ("Make Model"), or "" when neither is set.
func importedCamera(m *ImportedMeta) string {
	return strings.TrimSpace(strings.TrimSpace(m.CameraMake) + " " + strings.TrimSpace(m.CameraModel))
}

// dec6Ptr formats a float coordinate as a fixed 6-dp decimal string (or nil), the
// canonical dec-string form for lat/lng in a hot record (§5.2).
func dec6Ptr(f *float64) *string {
	if f == nil {
		return nil
	}
	s := canonicaljson.FormatDecimal(*f)
	return &s
}

// FileSig reproduces the web fileSig (§6.5): "<size>:<sha256 of head‖tail 1 MiB>",
// the cross-client dedup signature. The tail is empty when size ≤ 1 MiB so head
// and tail never overlap-rehash the same region.
func FileSig(data []byte) string {
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
