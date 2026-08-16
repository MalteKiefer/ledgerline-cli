package api

import (
	"context"
	"strconv"
)

// FilesLabels lists the user's coloured labels. GET /files/labels.
func (c *Client) FilesLabels(ctx context.Context) ([]FileLabel, error) {
	var resp struct {
		Labels []FileLabel `json:"labels"`
	}
	if err := c.request(ctx, "GET", "/api/v1/files/labels", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Labels, nil
}

// CreateLabel creates a coloured label. color is a "#rrggbb" hex string, or
// empty to use the server default. POST /files/labels.
func (c *Client) CreateLabel(ctx context.Context, name, color string) (FileLabel, error) {
	body := map[string]any{"name": name}
	if color != "" {
		body["color"] = color
	}
	var resp struct {
		Label FileLabel `json:"label"`
	}
	if err := c.request(ctx, "POST", "/api/v1/files/labels", body, &resp); err != nil {
		return FileLabel{}, err
	}
	return resp.Label, nil
}

// UpdateLabel renames/recolors a label. PUT /files/labels/{id}.
func (c *Client) UpdateLabel(ctx context.Context, id int64, name, color string) (FileLabel, error) {
	body := map[string]any{"name": name}
	if color != "" {
		body["color"] = color
	}
	var resp struct {
		Label FileLabel `json:"label"`
	}
	path := "/api/v1/files/labels/" + strconv.FormatInt(id, 10)
	if err := c.request(ctx, "PUT", path, body, &resp); err != nil {
		return FileLabel{}, err
	}
	return resp.Label, nil
}

// DeleteLabel deletes a label (pivot rows on files cascade).
// DELETE /files/labels/{id}.
func (c *Client) DeleteLabel(ctx context.Context, id int64) error {
	return c.request(ctx, "DELETE", "/api/v1/files/labels/"+strconv.FormatInt(id, 10), nil, nil)
}

// SetFileLabels replaces a file's whole label set. POST /files/entries/{id}/labels.
func (c *Client) SetFileLabels(ctx context.Context, fileID int64, labelIDs []int64) (FileEntry, error) {
	body := map[string]any{"label_ids": labelIDs}
	var resp struct {
		File FileEntry `json:"file"`
	}
	path := "/api/v1/files/entries/" + strconv.FormatInt(fileID, 10) + "/labels"
	if err := c.request(ctx, "POST", path, body, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}
