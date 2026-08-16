package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// FilesZip streams a ZIP of the given file ids and/or a folder subtree
// (folderID) to w. Capped server-side at 5000 files / 2 GiB total (413 over
// either budget). At least one of ids/folderID should be set.
// POST /files/zip.
func (c *Client) FilesZip(ctx context.Context, ids []int64, folderID *int64, w io.Writer) error {
	body := map[string]any{}
	if len(ids) > 0 {
		body["ids"] = ids
	}
	if folderID != nil {
		body["folder_id"] = *folderID
	}
	return c.postStream(ctx, "/api/v1/files/zip", body, w)
}

// postStream POSTs a JSON body to path and copies the response body to w,
// bypassing JSON decoding. Used for binary export endpoints (zip download).
func (c *Client) postStream(ctx context.Context, path string, body any, w io.Writer) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.retriableDo(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint(path), bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		c.setHeaders(req, true)
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	return err
}

// CreateArchiveInput is the request body for CreateArchive.
type CreateArchiveInput struct {
	IDs            []int64 // archive this selection...
	FolderID       *int64  // ...or this folder's subtree instead
	TargetFolderID **int64 // where to save the archive file (nil outer = default: root/source folder)
	Format         string  // zip, tar.gz, tar.xz, 7z
	Level          *int    // 0 (store) .. 9 (max)
	Password       *string // zip/7z only
	Name           *string
}

// CreateArchive archives a selection of files or a folder subtree and SAVES
// the result as a new file (download it via its normal FileEntry raw URL).
// Same caps as FilesZip. POST /files/archive.
func (c *Client) CreateArchive(ctx context.Context, in CreateArchiveInput) (FileEntry, error) {
	body := map[string]any{"format": in.Format}
	if len(in.IDs) > 0 {
		body["ids"] = in.IDs
	}
	if in.FolderID != nil {
		body["folder_id"] = *in.FolderID
	}
	if in.TargetFolderID != nil {
		body["target_folder_id"] = *in.TargetFolderID
	}
	if in.Level != nil {
		body["level"] = *in.Level
	}
	if in.Password != nil {
		body["password"] = *in.Password
	}
	if in.Name != nil {
		body["name"] = *in.Name
	}
	var resp struct {
		File FileEntry `json:"file"`
	}
	if err := c.request(ctx, "POST", "/api/v1/files/archive", body, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}

// ExtractedFolder is the destination folder of an ExtractArchive call, or nil
// when the archive was extracted straight into the target folder / root.
type ExtractedFolder struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ParentID *int64 `json:"parent_id"`
}

// ExtractArchive queues a server-side worker job to extract an archive file
// (zip/7z/rar/tar(.gz/.xz/.bz2/.zst)/gz/bz2/xz/zst) into a new or existing
// folder. Returns the destination folder immediately; the extraction itself
// completes asynchronously (outcome arrives as a server notification, not
// reflected in this return value). POST /files/entries/{id}/extract.
func (c *Client) ExtractArchive(ctx context.Context, id int64, password *string, targetFolderID **int64, intoNewFolder *bool) (*ExtractedFolder, error) {
	body := map[string]any{}
	if password != nil {
		body["password"] = *password
	}
	if targetFolderID != nil {
		body["target_folder_id"] = *targetFolderID
	}
	if intoNewFolder != nil {
		body["into_new_folder"] = *intoNewFolder
	}
	var resp struct {
		Folder *ExtractedFolder `json:"folder"`
	}
	path := "/api/v1/files/entries/" + strconv.FormatInt(id, 10) + "/extract"
	if err := c.request(ctx, "POST", path, body, &resp); err != nil {
		return nil, err
	}
	return resp.Folder, nil
}
