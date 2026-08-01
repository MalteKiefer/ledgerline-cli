package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"time"
)

// uploadTimeout bounds a single blob upload or process call; media can be large.
const uploadTimeout = 5 * time.Minute

// ErrVersionConflict is returned by SaveGalleryStore when the server's manifest
// version has moved on and the caller must reload, re-apply and retry.
var ErrVersionConflict = fmt.Errorf("gallery store version conflict")

// ErrMissingShard is returned when the server rejects a store PUT (422
// missing_shard) because the new root references a blob with no ledger row — i.e.
// a shard upload never durably landed. The write is refused to prevent persisting
// a root that dangles at a missing shard (the sharded-store data-loss failure
// mode). The caller must NOT drop the ref; retry after the upload settles.
var ErrMissingShard = fmt.Errorf("store rejected: root references a shard with no stored blob (missing_shard)")

// isMissingShard reports whether an APIError carries the missing_shard code.
func isMissingShard(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == "missing_shard"
}

// SealedStore is the opaque manifest envelope: ciphertext plus a monotonic
// version for optimistic concurrency.
type SealedStore struct {
	Ciphertext string `json:"ciphertext"`
	Version    int64  `json:"version"`
}

// GalleryStore fetches the sealed gallery manifest and its version.
func (c *Client) GalleryStore(ctx context.Context) (SealedStore, error) {
	var out SealedStore
	if err := c.request(ctx, "GET", "/api/v1/gallery/store", nil, &out); err != nil {
		return SealedStore{}, err
	}
	return out, nil
}

// SaveGalleryStore writes the sealed manifest at the expected version. shards is
// the live blob refs the new root points at (record shards + collection blobs);
// the server rejects the write (422 missing_shard) if any ref has no stored blob,
// preventing a dangling-shard save. counts is an optional per-slice record-count
// map ({"photos","albums","people"}) feeding the server's anomaly-scan (silent
// data-loss detection); pass nil to omit it entirely — a caller must NEVER send
// a partial/incomplete map, since a missing key reads as a false 0 count and can
// trigger a false data-loss alarm when interleaved with another client's writes.
// On a 409 it returns ErrVersionConflict; on a missing_shard 422, ErrMissingShard.
// It returns the new server version on success.
func (c *Client) SaveGalleryStore(ctx context.Context, ciphertext string, version int64, shards []string, counts map[string]int) (int64, error) {
	body := map[string]any{"ciphertext": ciphertext, "version": version, "shards": shards}
	if counts != nil {
		body["counts"] = counts
	}
	var out struct {
		Version int64 `json:"version"`
	}
	err := c.request(ctx, "PUT", "/api/v1/gallery/store", body, &out)
	if err != nil {
		if Status(err) == http.StatusConflict {
			return 0, ErrVersionConflict
		}
		if isMissingShard(err) {
			return 0, ErrMissingShard
		}
		return 0, err
	}
	return out.Version, nil
}

// UploadGalleryBlob uploads opaque blob bytes (already encrypted + padded) and
// returns the server-assigned blob id.
func (c *Client) UploadGalleryBlob(ctx context.Context, data []byte) (string, error) {
	return c.uploadBlob(ctx, "/api/v1/gallery/upload", data)
}

// GetGalleryBlob downloads an opaque blob's bytes (still encrypted).
func (c *Client) GetGalleryBlob(ctx context.Context, id string) ([]byte, error) {
	return c.getBlob(ctx, "/api/v1/gallery/raw/"+id)
}

// ProcessFace is one detected face in a ProcessResult.
type ProcessFace struct {
	Score     float64   `json:"score"`
	Box       []float64 `json:"box"`
	Embedding []float64 `json:"embedding"`
	Crop      string    `json:"crop"` // base64 JPEG, may be empty
}

// ProcessExif is the extracted EXIF subset.
type ProcessExif struct {
	TakenAt string   `json:"taken_at"`
	Lat     *float64 `json:"lat"`
	Lon     *float64 `json:"lon"`
	Camera  *string  `json:"camera"`
}

// ProcessResult is the JSON returned by POST /api/v1/gallery/process. Binary
// derivations (thumb/medium/motion, face crops) are base64-encoded.
type ProcessResult struct {
	MediaType string   `json:"media_type"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	Duration  *float64 `json:"duration"`
	ContentID *string  `json:"content_id"`
	// Model is the CLIP model the server produced the embedding with (server
	// config gallery.ml_clip_model). Native clients tag the record's embModel
	// from this so semantic search only compares same-model embeddings (§8.5).
	// Empty on an older server or a fast (no-ML) pass.
	Model     string          `json:"model"`
	Exif      ProcessExif     `json:"exif"`
	Place     json.RawMessage `json:"place"`
	Embedding json.RawMessage `json:"embedding"`
	Phash     json.RawMessage `json:"phash"`
	Faces     []ProcessFace   `json:"faces"`
	Thumb     string          `json:"thumb"`  // base64 JPEG
	Medium    string          `json:"medium"` // base64 JPEG
	Motion    string          `json:"motion"` // base64 video, may be empty
}

// ProcessPhoto sends a plaintext original to the transient-plaintext transform
// endpoint and returns its derived data. The mime is declared on the file part
// because the server keys "is this a video?" off the client-declared type.
// withML enables the CLIP embedding and face detection (requires the server's ML
// sidecar); false is a fast pass.
func (c *Client) ProcessPhoto(ctx context.Context, filename, mime string, data []byte, withML bool) (ProcessResult, error) {
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	if mime == "" {
		mime = "application/octet-stream"
	}
	header.Set("Content-Type", mime)
	part, err := w.CreatePart(header)
	if err != nil {
		return ProcessResult{}, err
	}
	if _, err := part.Write(data); err != nil {
		return ProcessResult{}, err
	}
	if err := w.WriteField("ml", boolField(withML)); err != nil {
		return ProcessResult{}, err
	}
	if err := w.Close(); err != nil {
		return ProcessResult{}, err
	}

	bodyBytes := buf.Bytes()
	contentType := w.FormDataContentType()

	var out ProcessResult
	err = c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint("/api/v1/gallery/process"), bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		c.applyAuth(req)
		req.Header.Set("Content-Type", contentType)
		return req, nil
	}, &out)
	if err != nil {
		return ProcessResult{}, err
	}
	return out, nil
}

// boolField renders a Laravel-friendly boolean form value.
func boolField(b bool) string { return strconv.Itoa(map[bool]int{true: 1, false: 0}[b]) }

// do runs a request (built by newReq, so its body can be replayed on a retry)
// through the shared 429/503 backoff and decodes a 2xx JSON body into out.
func (c *Client) do(ctx context.Context, newReq func() (*http.Request, error), out any) error {
	resp, err := c.retriableDo(ctx, newReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return readJSON(resp, out, 64<<20)
}
