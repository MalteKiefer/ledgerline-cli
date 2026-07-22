// Package gallery replicates the web client's zero-knowledge gallery: the Store
// v3 content-addressed, id-bucketed sharded manifest (resources/js/shared/
// sharded-store.js), the per-photo record schema (components/gallery.js), and the
// upload pipeline. Photos written here are readable by the web, iOS and Android
// clients and vice versa. No v1/v2 read paths (clean slate).
package gallery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/MalteKiefer/ledgerline-cli/internal/canonicaljson"
)

// PhotoRecord is one hot record inside a shard. Field names and null/omit
// semantics match resources/js/components/gallery.js so the web client can read
// CLI-written photos and vice versa. Per §5.2 the hot record is integer-only:
// lat/lng are fixed 6-dp decimal STRINGS (never floats) and duration is integer
// seconds, so a float never corrupts a shard's canonical-JSON hash.
//
// The typed struct is used for READING (any subset of fields tolerated) and for
// holding a record in memory; WRITING goes through the web-faithful map builders
// (asMap) so field presence matches the web exactly and buckets don't thrash.
type PhotoRecord struct {
	ID          string `json:"id"`
	OriginalRef string `json:"originalRef"`
	OriginalKey string `json:"originalKey"`
	Name        string `json:"name"`
	Mime        string `json:"mime"`
	Size        int64  `json:"size"`
	MediaType   string `json:"media_type"`
	Sig         string `json:"sig"`
	Created     string `json:"created"`

	MotionRef string `json:"motionRef,omitempty"`
	MotionKey string `json:"motionKey,omitempty"`
	ThumbRef  string `json:"thumbRef,omitempty"`
	ThumbKey  string `json:"thumbKey,omitempty"`
	MediumRef string `json:"mediumRef,omitempty"`
	MediumKey string `json:"mediumKey,omitempty"`
	MetaRef   string `json:"metaRef,omitempty"`
	MetaKey   string `json:"metaKey,omitempty"`

	TakenAt      string   `json:"taken_at,omitempty"`
	Width        int      `json:"width,omitempty"`
	Height       int      `json:"height,omitempty"`
	Duration     *int     `json:"duration"`
	Lat          *string  `json:"lat"`
	Lng          *string  `json:"lng"`
	GeoChecked   bool     `json:"geoChecked,omitempty"`
	Camera       *string  `json:"camera"`
	EmbModel     *string  `json:"embModel"`
	HasFaces     *int     `json:"hasFaces"`
	FaceCropRefs []string `json:"faceCropRefs"`
	MlPending    bool     `json:"mlPending,omitempty"`
	ThumbPending bool     `json:"thumbPending,omitempty"`
	Trashed      string   `json:"trashed,omitempty"` // soft-delete timestamp; set = in the trash

	// Session-only bookkeeping (never serialised): whether this record is a
	// partial (basics + thumbPending, no derivation) vs a fully-processed record;
	// the Apple content id used to merge a Live Photo's still and video halves; a
	// flag marking a video record that was merged into a still; and a non-fatal
	// warning if the paired motion clip could not be stored.
	partial    bool
	contentID  string
	merged     bool
	motionWarn error
}

// MotionWarning reports a non-fatal problem storing this photo's paired motion
// clip (the still itself uploaded fine), or nil if there was none.
func (r *PhotoRecord) MotionWarning() error { return r.motionWarn }

// SetPartial marks the record as a partial write (basics + thumbPending:true, no
// derivation) — the §8.1 CLI-floor shape with no plaintext egress.
func (r *PhotoRecord) SetPartial(p bool) { r.partial = p }

// LatLngFloat parses the dec-string lat/lng back to floats (both nil when either
// is absent) for consumers that need numeric coordinates, e.g. exiftool --edited.
func (r *PhotoRecord) LatLngFloat() (*float64, *float64) {
	if r.Lat == nil || r.Lng == nil {
		return nil, nil
	}
	lat, err1 := strconv.ParseFloat(*r.Lat, 64)
	lng, err2 := strconv.ParseFloat(*r.Lng, 64)
	if err1 != nil || err2 != nil {
		return nil, nil
	}
	return &lat, &lng
}

// asMap renders the record as the exact key/value set the web writes, so
// canonical-JSON bytes (and thus shard hashes) match across clients. A partial
// record carries only the basics + thumbPending; a full record mirrors the
// post-/process `p` object in components/gallery.js.
func (r *PhotoRecord) asMap() map[string]any {
	m := map[string]any{
		"id":          r.ID,
		"originalRef": r.OriginalRef,
		"originalKey": r.OriginalKey,
		"name":        r.Name,
		"mime":        r.Mime,
		"size":        r.Size,
		"media_type":  r.MediaType,
		"sig":         r.Sig,
		"created":     r.Created,
	}
	if r.MotionRef != "" {
		m["motionRef"] = r.MotionRef
		m["motionKey"] = r.MotionKey
	}
	if r.Trashed != "" {
		m["trashed"] = r.Trashed
	}
	if r.partial {
		m["thumbPending"] = true
		return m
	}

	// Full record: renditions + meta pointer + promoted display fields, with the
	// web's explicit-null semantics for optional values.
	m["thumbRef"] = r.ThumbRef
	m["thumbKey"] = r.ThumbKey
	m["mediumRef"] = r.MediumRef
	m["mediumKey"] = r.MediumKey
	m["metaRef"] = r.MetaRef
	m["metaKey"] = r.MetaKey
	m["taken_at"] = r.TakenAt
	m["width"] = r.Width
	m["height"] = r.Height
	m["duration"] = intOrNull(r.Duration)
	m["lat"] = strOrNull(r.Lat)
	m["lng"] = strOrNull(r.Lng)
	m["geoChecked"] = r.GeoChecked
	m["camera"] = strOrNull(r.Camera)
	m["embModel"] = strOrNull(r.EmbModel)
	m["hasFaces"] = intOrNull(r.HasFaces)
	if r.FaceCropRefs == nil {
		m["faceCropRefs"] = []string{}
	} else {
		m["faceCropRefs"] = r.FaceCropRefs
	}
	m["mlPending"] = r.MlPending
	m["thumbPending"] = false
	return m
}

func intOrNull(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func strOrNull(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// metaBlob is the separately-encrypted, COLD metadata blob referenced by metaRef.
// It is per-photo, immutable and never hashed for dirty-detection, so it may
// carry floats (embeddings, scores, fractional duration) — it is serialised with
// plain JSON, not canonical JSON. Its shape matches the web's meta object.
type metaBlob struct {
	Exif      json.RawMessage `json:"exif"`
	Place     json.RawMessage `json:"place"`
	Embedding json.RawMessage `json:"embedding"`
	Phash     json.RawMessage `json:"phash"`
	EmbModel  *string         `json:"embModel"`
	Faces     []metaFace      `json:"faces"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	Duration  *float64        `json:"duration"`
	ContentID *string         `json:"content_id"`
}

// metaFace is one face inside the metadata blob (crop stored as its own blob).
type metaFace struct {
	Score     float64   `json:"score"`
	Box       []float64 `json:"box"`
	Embedding []float64 `json:"embedding"`
	CropRef   string    `json:"cropRef,omitempty"`
	CropKey   string    `json:"cropKey,omitempty"`
}

// newID mirrors sealed-store.js newId(): 16 random bytes as lowercase hex (a
// 128-bit CSPRNG id → even, deterministic shard-bucket distribution).
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// decodeCanonical decodes canonical JSON bytes (a loaded shard record) into a
// generic value for re-canonicalization alongside newly-added records.
func decodeCanonical(raw json.RawMessage) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// canonicalizeRecords renders a slice of record values as canonical JSON (§5.2).
func canonicalizeRecords(records []any) ([]byte, error) {
	return canonicaljson.Marshal(records)
}
