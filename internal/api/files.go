package api

import "context"

// UploadFileBlob uploads opaque file-content bytes (encrypted + padded) and
// returns the server-assigned blob id.
func (c *Client) UploadFileBlob(ctx context.Context, data []byte) (string, error) {
	return c.uploadBlob(ctx, "/api/v1/files/upload", data)
}

// UploadFileBlobProgress is UploadFileBlob with a progress callback fired as the
// body streams to the server. See uploadBlobProgress for callback semantics.
func (c *Client) UploadFileBlobProgress(ctx context.Context, data []byte, onProgress func(sent, total int64)) (string, error) {
	return c.uploadBlobProgress(ctx, "/api/v1/files/upload", data, onProgress)
}

// GetFileBlob downloads a file blob's raw (still-encrypted) bytes.
func (c *Client) GetFileBlob(ctx context.Context, id string) ([]byte, error) {
	return c.getBlob(ctx, "/api/v1/files/raw/"+id)
}

// DeleteFileBlob deletes a file blob (idempotent).
func (c *Client) DeleteFileBlob(ctx context.Context, id string) error {
	return c.deleteBlob(ctx, "/api/v1/files/blob/"+id)
}
