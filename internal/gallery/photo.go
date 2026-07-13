// Package gallery replicates the web client's zero-knowledge gallery: the
// sharded v2 manifest, the per-photo record schema, and the upload pipeline
// (encrypt original, derive thumbnails/metadata via the transient-plaintext
// process endpoint, seal everything). Photos written here are readable by the
// web and Android clients and vice versa.
package gallery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
)

// PhotoRecord is one entry inside a shard. The JSON field names and shape match
// resources/js/app.js exactly so the web client can read CLI-uploaded photos.
//
// Nullable display fields are emitted as explicit null (matching the web, which
// assigns them unconditionally); fields only set in some cases use omitempty.
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
	Duration     *float64 `json:"duration"`
	Lat          *float64 `json:"lat"`
	Lng          *float64 `json:"lng"`
	GeoChecked   bool     `json:"geoChecked,omitempty"`
	Camera       *string  `json:"camera"`
	HasFaces     *int     `json:"hasFaces"`
	FaceCropRefs []string `json:"faceCropRefs"`
	MlPending    bool     `json:"mlPending,omitempty"`
	Trashed      string   `json:"trashed,omitempty"` // soft-delete timestamp; set = in the trash

	// Session-only bookkeeping (never serialised): the Apple content id used to
	// merge a Live Photo's still and video halves, a flag marking a video record
	// that was merged into a still (so it is not written out), and a non-fatal
	// warning if this photo's paired motion clip could not be stored.
	contentID  string
	merged     bool
	motionWarn error
}

// MotionWarning reports a non-fatal problem storing this photo's paired motion
// clip (the still itself uploaded fine), or nil if there was none.
func (r *PhotoRecord) MotionWarning() error { return r.motionWarn }

// metaBlob is the separately-encrypted metadata blob referenced by metaRef. Its
// shape matches the web's meta object.
type metaBlob struct {
	Exif      json.RawMessage `json:"exif"`
	Place     json.RawMessage `json:"place"`
	Embedding json.RawMessage `json:"embedding"`
	Phash     json.RawMessage `json:"phash"`
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

// newID mirrors LLGalleryStore.newId(): 16 random bytes as lowercase hex.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
