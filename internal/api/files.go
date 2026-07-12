package api

import "context"

// UploadFileBlob uploads opaque file-content bytes (encrypted + padded) and
// returns the server-assigned blob id.
func (c *Client) UploadFileBlob(ctx context.Context, data []byte) (string, error) {
	return c.uploadBlob(ctx, "/api/v1/files/upload", data)
}

// GetFileBlob downloads a file blob's raw (still-encrypted) bytes.
func (c *Client) GetFileBlob(ctx context.Context, id string) ([]byte, error) {
	return c.getBlob(ctx, "/api/v1/files/raw/"+id)
}

// DeleteFileBlob deletes a file blob (idempotent).
func (c *Client) DeleteFileBlob(ctx context.Context, id string) error {
	return c.deleteBlob(ctx, "/api/v1/files/blob/"+id)
}
