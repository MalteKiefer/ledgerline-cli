package api

import (
	"context"
	"strconv"
)

// FilesFolders lists all folders (no files). GET /files/folders.
func (c *Client) FilesFolders(ctx context.Context) ([]FileFolder, error) {
	var resp struct {
		Folders []FileFolder `json:"folders"`
	}
	if err := c.request(ctx, "GET", "/api/v1/files/folders", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Folders, nil
}

// CreateFolder creates a folder under parentID (nil = root). POST /files/folders.
func (c *Client) CreateFolder(ctx context.Context, name string, parentID *int64) (FileFolder, error) {
	body := map[string]any{"name": name}
	if parentID != nil {
		body["parent_id"] = *parentID
	}
	var resp struct {
		Folder FileFolder `json:"folder"`
	}
	if err := c.request(ctx, "POST", "/api/v1/files/folders", body, &resp); err != nil {
		return FileFolder{}, err
	}
	return resp.Folder, nil
}

// RenameFolder renames a folder. PUT /files/folders/{id}.
func (c *Client) RenameFolder(ctx context.Context, id int64, name string) (FileFolder, error) {
	body := map[string]any{"name": name}
	var resp struct {
		Folder FileFolder `json:"folder"`
	}
	path := "/api/v1/files/folders/" + strconv.FormatInt(id, 10)
	if err := c.request(ctx, "PUT", path, body, &resp); err != nil {
		return FileFolder{}, err
	}
	return resp.Folder, nil
}

// MoveFolder reparents a folder under parentID (nil = root). The server rejects
// a move that would create a cycle. POST /files/folders/{id}/move.
func (c *Client) MoveFolder(ctx context.Context, id int64, parentID *int64) (FileFolder, error) {
	body := map[string]any{"parent_id": parentID}
	var resp struct {
		Folder FileFolder `json:"folder"`
	}
	path := "/api/v1/files/folders/" + strconv.FormatInt(id, 10) + "/move"
	if err := c.request(ctx, "POST", path, body, &resp); err != nil {
		return FileFolder{}, err
	}
	return resp.Folder, nil
}

// DeleteFolder soft-deletes a folder and its whole subtree (to trash).
// DELETE /files/folders/{id}.
func (c *Client) DeleteFolder(ctx context.Context, id int64) error {
	return c.request(ctx, "DELETE", "/api/v1/files/folders/"+strconv.FormatInt(id, 10), nil, nil)
}

// RestoreFolder restores a trashed folder and its whole subtree + files.
// POST /files/folders/{id}/restore.
func (c *Client) RestoreFolder(ctx context.Context, id int64) (FileFolder, error) {
	var resp struct {
		Folder FileFolder `json:"folder"`
	}
	path := "/api/v1/files/folders/" + strconv.FormatInt(id, 10) + "/restore"
	if err := c.request(ctx, "POST", path, nil, &resp); err != nil {
		return FileFolder{}, err
	}
	return resp.Folder, nil
}

// ForceDeleteFolder permanently deletes a trashed folder subtree (folders +
// files + bytes). DELETE /files/folders/{id}/force.
func (c *Client) ForceDeleteFolder(ctx context.Context, id int64) error {
	return c.request(ctx, "DELETE", "/api/v1/files/folders/"+strconv.FormatInt(id, 10)+"/force", nil, nil)
}
