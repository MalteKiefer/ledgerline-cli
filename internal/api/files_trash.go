package api

import (
	"context"
	"strconv"
)

// FilesTrash lists soft-deleted files + folders. GET /files/trash.
func (c *Client) FilesTrash(ctx context.Context) (files []FileEntry, folders []FileFolder, err error) {
	var resp struct {
		Files   []FileEntry  `json:"files"`
		Folders []FileFolder `json:"folders"`
	}
	if err = c.request(ctx, "GET", "/api/v1/files/trash", nil, &resp); err != nil {
		return nil, nil, err
	}
	return resp.Files, resp.Folders, nil
}

// RestoreFile restores a soft-deleted file from trash. POST /files/entries/{id}/restore.
func (c *Client) RestoreFile(ctx context.Context, id int64) (FileEntry, error) {
	var resp struct {
		File FileEntry `json:"file"`
	}
	path := "/api/v1/files/entries/" + strconv.FormatInt(id, 10) + "/restore"
	if err := c.request(ctx, "POST", path, nil, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}

// ForceDeleteFile permanently deletes a file (removes bytes + version history).
// DELETE /files/entries/{id}/force.
func (c *Client) ForceDeleteFile(ctx context.Context, id int64) error {
	return c.request(ctx, "DELETE", "/api/v1/files/entries/"+strconv.FormatInt(id, 10)+"/force", nil, nil)
}

// EmptyFilesTrash permanently deletes every trashed file and folder.
// POST /files/entries/trash/empty.
func (c *Client) EmptyFilesTrash(ctx context.Context) error {
	return c.request(ctx, "POST", "/api/v1/files/entries/trash/empty", nil, nil)
}
