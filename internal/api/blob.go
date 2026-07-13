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

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint(path), &buf)
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

// getBlob downloads a module blob's raw (still-encrypted) bytes.
func (c *Client) getBlob(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", c.endpoint(path), nil)
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

	req, err := http.NewRequestWithContext(ctx, "DELETE", c.endpoint(path), nil)
	if err != nil {
		return err
	}
	c.applyAuth(req)
	return c.do(req, nil)
}
