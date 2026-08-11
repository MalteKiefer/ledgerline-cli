package api

import (
	"context"
	"io"
	"strconv"
)

// FileFolder is a folder row in the plaintext file tree. ParentID is nil at the
// root.
type FileFolder struct {
	ID        int64   `json:"id"`
	ParentID  *int64  `json:"parent_id"`
	Name      string  `json:"name"`
	Version   int     `json:"version"`
	DeletedAt *string `json:"deleted_at"`
	CreatedAt *string `json:"created_at"`
	UpdatedAt *string `json:"updated_at"`
}

// FileEntry is a file row (metadata only; bytes are fetched via DownloadFile).
type FileEntry struct {
	ID           int64       `json:"id"`
	FileFolderID *int64      `json:"file_folder_id"`
	Name         string      `json:"name"`
	Mime         *string     `json:"mime"`
	Size         int64       `json:"size"`
	Sha256       *string     `json:"sha256"`
	Tags         []string    `json:"tags"`
	Note         *string     `json:"note"`
	Favorite     bool        `json:"favorite"`
	Labels       []FileLabel `json:"labels"`
	Version      int         `json:"version"`
	DeletedAt    *string     `json:"deleted_at"`
	CreatedAt    *string     `json:"created_at"`
	UpdatedAt    *string     `json:"updated_at"`
}

// FileLabel is a coloured user-defined label.
type FileLabel struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// FilesUsage is the combined files+versions storage footprint vs. the quota
// (Quota nil = unlimited).
type FilesUsage struct {
	Used  int64  `json:"used"`
	Quota *int64 `json:"quota"`
}

// FilesData returns the whole file tree for the current user: folders, files and
// the usage snapshot. GET /files/data.
func (c *Client) FilesData(ctx context.Context) (folders []FileFolder, files []FileEntry, usage FilesUsage, err error) {
	var resp struct {
		Folders []FileFolder `json:"folders"`
		Files   []FileEntry  `json:"files"`
		Usage   FilesUsage   `json:"usage"`
	}
	if err = c.request(ctx, "GET", "/api/v1/files/data", nil, &resp); err != nil {
		return nil, nil, FilesUsage{}, err
	}
	return resp.Folders, resp.Files, resp.Usage, nil
}

// UploadFile uploads one file whole into folderID (nil = root).
// Multipart POST /files/entries.
func (c *Client) UploadFile(ctx context.Context, name string, folderID *int64, open func() (io.ReadCloser, error)) (FileEntry, error) {
	fields := map[string]string{}
	if name != "" {
		fields["name"] = name
	}
	if folderID != nil {
		fields["file_folder_id"] = strconv.FormatInt(*folderID, 10)
	}
	var resp struct {
		File FileEntry `json:"file"`
	}
	if _, err := c.uploadMultipart(ctx, "/api/v1/files/entries", "file", name, fields, open, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}

// ReplaceFileContent replaces an existing file's bytes, archiving the current
// bytes as a version. Multipart POST /files/entries/{id}/content.
func (c *Client) ReplaceFileContent(ctx context.Context, id int64, name string, open func() (io.ReadCloser, error)) (FileEntry, error) {
	var resp struct {
		File FileEntry `json:"file"`
	}
	path := "/api/v1/files/entries/" + strconv.FormatInt(id, 10) + "/content"
	if _, err := c.uploadMultipart(ctx, path, "file", name, nil, open, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}

// DownloadFile streams a file's bytes to w. GET /files/entries/{id}/raw.
func (c *Client) DownloadFile(ctx context.Context, id int64, w io.Writer) error {
	path := "/api/v1/files/entries/" + strconv.FormatInt(id, 10) + "/raw?download=1"
	return c.getStream(ctx, path, w)
}

// DeleteFile soft-deletes a file (to trash). DELETE /files/entries/{id}.
func (c *Client) DeleteFile(ctx context.Context, id int64) error {
	return c.request(ctx, "DELETE", "/api/v1/files/entries/"+strconv.FormatInt(id, 10), nil, nil)
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
