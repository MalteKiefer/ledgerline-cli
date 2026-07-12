package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// SaveGalleryStore writes the sealed manifest at the expected version. On a 409
// it returns ErrVersionConflict; the caller reloads and retries. It returns the
// new server version on success.
func (c *Client) SaveGalleryStore(ctx context.Context, ciphertext string, version int64) (int64, error) {
	body := map[string]any{"ciphertext": ciphertext, "version": version}
	var out struct {
		Version int64 `json:"version"`
	}
	err := c.request(ctx, "PUT", "/api/v1/gallery/store", body, &out)
	if err != nil {
		if Status(err) == http.StatusConflict {
			return 0, ErrVersionConflict
		}
		return 0, err
	}
	return out.Version, nil
}

// UploadGalleryBlob uploads opaque blob bytes (already encrypted + padded) and
// returns the server-assigned blob id.
func (c *Client) UploadGalleryBlob(ctx context.Context, data []byte) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", "blob.enc")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint("/api/v1/gallery/upload"), &buf)
	if err != nil {
		return "", err
	}
	c.applyAuth(req)
	req.Header.Set("Content-Type", w.FormDataContentType())

	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(req, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// GetGalleryBlob downloads an opaque blob's bytes (still encrypted).
func (c *Client) GetGalleryBlob(ctx context.Context, id string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", c.endpoint("/api/v1/gallery/raw/"+id), nil)
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeError(resp)
	}
	return io.ReadAll(resp.Body)
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
	MediaType string          `json:"media_type"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	Duration  *float64        `json:"duration"`
	ContentID *string         `json:"content_id"`
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

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint("/api/v1/gallery/process"), &buf)
	if err != nil {
		return ProcessResult{}, err
	}
	c.applyAuth(req)
	req.Header.Set("Content-Type", w.FormDataContentType())

	var out ProcessResult
	if err := c.do(req, &out); err != nil {
		return ProcessResult{}, err
	}
	return out, nil
}

// boolField renders a Laravel-friendly boolean form value.
func boolField(b bool) string { return strconv.Itoa(map[bool]int{true: 1, false: 0}[b]) }

// do executes a prepared request and decodes a 2xx JSON body into out.
func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
