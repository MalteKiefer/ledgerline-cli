package api

import (
	"context"
	"io"
	"strconv"
)

// FileVersion is a previous revision of a file's bytes, kept up to a per-user
// cap. Current bytes are the FileEntry itself, not a FileVersion.
type FileVersion struct {
	ID          int64   `json:"id"`
	FileID      int64   `json:"file_id"`
	StoragePath string  `json:"storage_path"`
	Size        int64   `json:"size"`
	Mime        *string `json:"mime"`
	Sha256      *string `json:"sha256"`
	CreatedAt   *string `json:"created_at"`
}

// FileVersions lists a file's archived version history (oldest bytes are
// archived here each time ReplaceFileContent replaces the current bytes).
// GET /files/entries/{id}/versions.
func (c *Client) FileVersions(ctx context.Context, id int64) ([]FileVersion, error) {
	var resp struct {
		Versions []FileVersion `json:"versions"`
	}
	path := "/api/v1/files/entries/" + strconv.FormatInt(id, 10) + "/versions"
	if err := c.request(ctx, "GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Versions, nil
}

// DownloadFileVersion streams one historical version's bytes to w.
// GET /files/entries/{id}/versions/{version}/raw.
func (c *Client) DownloadFileVersion(ctx context.Context, id, version int64, w io.Writer) error {
	path := "/api/v1/files/entries/" + strconv.FormatInt(id, 10) +
		"/versions/" + strconv.FormatInt(version, 10) + "/raw?download=1"
	return c.getStream(ctx, path, w)
}

// RestoreFileVersion restores a historical version as the file's current
// bytes (the previously-current bytes are archived as a new version).
// POST /files/entries/{id}/versions/{version}/restore.
func (c *Client) RestoreFileVersion(ctx context.Context, id, version int64) (FileEntry, error) {
	var resp struct {
		File FileEntry `json:"file"`
	}
	path := "/api/v1/files/entries/" + strconv.FormatInt(id, 10) +
		"/versions/" + strconv.FormatInt(version, 10) + "/restore"
	if err := c.request(ctx, "POST", path, nil, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}
