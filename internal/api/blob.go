package api

import (
	"bytes"
	"context"
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
