package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// uploadBlob POSTs opaque bytes to a module's blob upload endpoint and returns
// the server-assigned blob id. Shared by gallery and files (same contract).
func (c *Client) uploadBlob(ctx context.Context, path string, data []byte) (string, error) {
	return c.uploadBlobProgress(ctx, path, data, nil)
}

// uploadBlobProgress is uploadBlob with an optional progress callback invoked as
// the request body is streamed to the server (sent/total are bytes of the whole
// multipart body, slightly larger than len(data)). The callback may fire many
// times per second and, on a retry, restart from zero — callers should throttle
// and tolerate a reset.
func (c *Client) uploadBlobProgress(ctx context.Context, path string, data []byte, onProgress func(sent, total int64)) (string, error) {
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

	bodyBytes := buf.Bytes()
	total := int64(len(bodyBytes))
	contentType := w.FormDataContentType()

	var out struct {
		ID string `json:"id"`
	}
	err = c.do(ctx, func() (*http.Request, error) {
		// Fresh reader per attempt so a retry re-sends (and re-reports) cleanly.
		var body io.Reader = bytes.NewReader(bodyBytes)
		if onProgress != nil {
			body = &countingReader{r: body, total: total, cb: onProgress}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint(path), body)
		if err != nil {
			return nil, err
		}
		req.ContentLength = total // wrapping the body hides the length; set it back
		c.applyAuth(req)
		req.Header.Set("Content-Type", contentType)
		return req, nil
	}, &out)
	if err != nil {
		return "", err
	}
	return out.ID, nil
}

// countingReader forwards Read and reports cumulative bytes via cb.
type countingReader struct {
	r     io.Reader
	n     int64
	total int64
	cb    func(sent, total int64)
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.n += int64(n)
		r.cb(r.n, r.total)
	}
	return n, err
}

// getBlob downloads a module blob's raw (still-encrypted) bytes.
func (c *Client) getBlob(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	resp, err := c.retriableDo(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", c.endpoint(path), nil)
		if err != nil {
			return nil, err
		}
		c.applyAuth(req)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Bound the body so a hostile/broken server can't stream an unbounded blob
	// and OOM the client. The cap sits above the largest legitimate media +
	// Padmé padding; an over-long body is truncated and will fail to decrypt.
	return io.ReadAll(io.LimitReader(resp.Body, maxBlobBytes))
}

// maxBlobBytes caps a single downloaded blob (encrypted, Padmé-padded).
const maxBlobBytes = 4 << 30 // 4 GiB

// batchMaxIDs is the server's per-request cap on raw-batch ids (openapi maxItems).
const batchMaxIDs = 512

// maxBatchBytes caps one raw-batch response body. raw-batch is used for the small
// record shards; the cap bounds a hostile/broken server (§31) while sitting well
// above a realistic shard batch.
const maxBatchBytes = 512 << 20 // 512 MiB

// getBlobsBatch fetches many blobs in one round-trip via a module's raw-batch
// endpoint, returning ref->ciphertext for the blobs that were present (the server
// silently skips unknown/foreign/missing ids — 404-hiding — so a caller must
// treat an absent ref as "fetch individually / fail", never as empty). ids are
// chunked to the server's per-request cap.
func (c *Client) getBlobsBatch(ctx context.Context, path string, ids []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(ids))
	for start := 0; start < len(ids); start += batchMaxIDs {
		end := start + batchMaxIDs
		if end > len(ids) {
			end = len(ids)
		}
		if err := c.getBlobsBatchChunk(ctx, path, ids[start:end], out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *Client) getBlobsBatchChunk(ctx context.Context, path string, ids []string, out map[string][]byte) error {
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{"blobs": ids})
	if err != nil {
		return err
	}
	resp, err := c.retriableDo(ctx, func() (*http.Request, error) {
		req, rerr := http.NewRequestWithContext(ctx, "POST", c.endpoint(path), bytes.NewReader(body))
		if rerr != nil {
			return nil, rerr
		}
		c.applyAuth(req)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/octet-stream")
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBatchBytes))
	if err != nil {
		return err
	}
	return parseBatchStream(data, out)
}

// parseBatchStream decodes the raw-batch wire format into out, one entry per blob:
// [u32le idLen][id utf8][u32le dataLen][ciphertext]. Every length is bounded
// before use so a hostile stream cannot drive an unbounded allocation (§31).
func parseBatchStream(data []byte, out map[string][]byte) error {
	const maxIDLen = 1024
	off := 0
	for off < len(data) {
		if off+4 > len(data) {
			return fmt.Errorf("api: raw-batch truncated id length")
		}
		idLen := int(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		if idLen <= 0 || idLen > maxIDLen || off+idLen > len(data) {
			return fmt.Errorf("api: raw-batch bad id length %d", idLen)
		}
		id := string(data[off : off+idLen])
		off += idLen

		if off+4 > len(data) {
			return fmt.Errorf("api: raw-batch truncated data length")
		}
		dataLen := int(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		if dataLen < 0 || off+dataLen > len(data) {
			return fmt.Errorf("api: raw-batch bad data length %d", dataLen)
		}
		blob := make([]byte, dataLen)
		copy(blob, data[off:off+dataLen])
		out[id] = blob
		off += dataLen
	}
	return nil
}

// GetGalleryBlobsBatch fetches many gallery blobs in one round-trip.
func (c *Client) GetGalleryBlobsBatch(ctx context.Context, ids []string) (map[string][]byte, error) {
	return c.getBlobsBatch(ctx, "/api/v1/gallery/raw-batch", ids)
}

// GetFilesBlobsBatch fetches many files blobs in one round-trip.
func (c *Client) GetFilesBlobsBatch(ctx context.Context, ids []string) (map[string][]byte, error) {
	return c.getBlobsBatch(ctx, "/api/v1/files/raw-batch", ids)
}

// deleteBlob removes a module blob (idempotent server-side).
func (c *Client) deleteBlob(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	return c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "DELETE", c.endpoint(path), nil)
		if err != nil {
			return nil, err
		}
		c.applyAuth(req)
		return req, nil
	}, nil)
}
