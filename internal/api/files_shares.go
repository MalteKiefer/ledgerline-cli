package api

import (
	"context"
	"io"
	"strconv"
)

// FileShare is a public share link (optionally password-gated, with expiry) to
// a single file or a folder subtree. The password hash is never serialized.
type FileShare struct {
	ID            int64   `json:"id"`
	Token         string  `json:"token"`
	Kind          string  `json:"kind"` // file, folder
	FileID        *int64  `json:"file_id"`
	FileFolderID  *int64  `json:"file_folder_id"`
	NeedsPassword bool    `json:"needs_password"`
	AllowDownload bool    `json:"allow_download"`
	ExpiresAt     *string `json:"expires_at"`
	Version       int     `json:"version"`
}

// FileShareListItem is a FileShare plus the target's display name, as returned
// by FilesShares.
type FileShareListItem struct {
	FileShare
	Name string `json:"name"`
}

// FilesShares lists the caller's own public share links.
// GET /files/rel-shares.
func (c *Client) FilesShares(ctx context.Context) ([]FileShareListItem, error) {
	var resp struct {
		Shares []FileShareListItem `json:"shares"`
	}
	if err := c.request(ctx, "GET", "/api/v1/files/rel-shares", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Shares, nil
}

// CreateFileShareInput is the request body for CreateFileShare. Exactly one of
// FileID (kind=file) / FileFolderID (kind=folder) must be set.
type CreateFileShareInput struct {
	Kind          string // "file" or "folder"
	FileID        *int64
	FileFolderID  *int64
	Password      *string
	AllowDownload *bool // server defaults true when omitted
	ExpiresAt     *string
}

// CreateFileShare creates a public share link for a file or a folder subtree.
// POST /files/rel-shares.
func (c *Client) CreateFileShare(ctx context.Context, in CreateFileShareInput) (FileShare, error) {
	body := map[string]any{"kind": in.Kind}
	if in.FileID != nil {
		body["file_id"] = *in.FileID
	}
	if in.FileFolderID != nil {
		body["file_folder_id"] = *in.FileFolderID
	}
	if in.Password != nil {
		body["password"] = *in.Password
	}
	if in.AllowDownload != nil {
		body["allow_download"] = *in.AllowDownload
	}
	if in.ExpiresAt != nil {
		body["expires_at"] = *in.ExpiresAt
	}
	var resp struct {
		Share FileShare `json:"share"`
	}
	if err := c.request(ctx, "POST", "/api/v1/files/rel-shares", body, &resp); err != nil {
		return FileShare{}, err
	}
	return resp.Share, nil
}

// UpdateFileShareInput is the request body for UpdateFileShare; only non-nil
// fields are sent.
type UpdateFileShareInput struct {
	Password       *string
	RemovePassword *bool
	AllowDownload  *bool
	ExpiresAt      **string // nested pointer: non-nil outer + nil inner clears the expiry
}

// UpdateFileShare updates a share's password/expiry/download flag under
// optimistic concurrency (version must match FileShare.Version); a mismatch
// returns an *APIError with Code "version_conflict". PUT /files/rel-shares/{id}.
func (c *Client) UpdateFileShare(ctx context.Context, id int64, in UpdateFileShareInput, version int) (FileShare, error) {
	body := map[string]any{"version": version}
	if in.Password != nil {
		body["password"] = *in.Password
	}
	if in.RemovePassword != nil {
		body["remove_password"] = *in.RemovePassword
	}
	if in.AllowDownload != nil {
		body["allow_download"] = *in.AllowDownload
	}
	if in.ExpiresAt != nil {
		body["expires_at"] = *in.ExpiresAt
	}
	var resp struct {
		Share FileShare `json:"share"`
	}
	path := "/api/v1/files/rel-shares/" + strconv.FormatInt(id, 10)
	if err := c.request(ctx, "PUT", path, body, &resp); err != nil {
		return FileShare{}, err
	}
	return resp.Share, nil
}

// DeleteFileShare revokes a public share link. DELETE /files/rel-shares/{id}.
func (c *Client) DeleteFileShare(ctx context.Context, id int64) error {
	return c.request(ctx, "DELETE", "/api/v1/files/rel-shares/"+strconv.FormatInt(id, 10), nil, nil)
}

// FolderShareMember is one member of an internal (registered-user) share.
type FolderShareMember struct {
	ID     int64   `json:"id"`
	UserID int64   `json:"user_id"`
	Name   *string `json:"name"`
	Email  *string `json:"email"`
	Role   string  `json:"role"` // viewer, editor
}

// FolderShareOwnerView is an owner-visible internal share: the shared target
// (a folder subtree OR a single file, exactly one set) plus its member roster.
type FolderShareOwnerView struct {
	ID           int64               `json:"id"`
	Kind         string              `json:"kind"` // file, folder
	FileFolderID *int64              `json:"file_folder_id"`
	FolderName   *string             `json:"folder_name"`
	FileID       *int64              `json:"file_id"`
	FileName     *string             `json:"file_name"`
	Members      []FolderShareMember `json:"members"`
}

// FilesFolderShares lists internal (registered-user) shares the caller owns.
// GET /files/folder-shares.
func (c *Client) FilesFolderShares(ctx context.Context) ([]FolderShareOwnerView, error) {
	var resp struct {
		Shares []FolderShareOwnerView `json:"shares"`
	}
	if err := c.request(ctx, "GET", "/api/v1/files/folder-shares", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Shares, nil
}

// CreateFolderShare shares a folder subtree OR a single file with a registered
// user by email, as viewer or editor. kind selects the target ("file" or
// "folder"; defaults to folder server-side when empty).
// POST /files/folder-shares.
func (c *Client) CreateFolderShare(ctx context.Context, kind string, fileFolderID, fileID *int64, email, role string) (FolderShareOwnerView, error) {
	body := map[string]any{"email": email, "role": role}
	if kind != "" {
		body["kind"] = kind
	}
	if fileFolderID != nil {
		body["file_folder_id"] = *fileFolderID
	}
	if fileID != nil {
		body["file_id"] = *fileID
	}
	var resp struct {
		Share FolderShareOwnerView `json:"share"`
	}
	if err := c.request(ctx, "POST", "/api/v1/files/folder-shares", body, &resp); err != nil {
		return FolderShareOwnerView{}, err
	}
	return resp.Share, nil
}

// UpdateFolderShareMember changes a member's role (viewer/editor).
// PUT /files/folder-shares/{share}/members.
func (c *Client) UpdateFolderShareMember(ctx context.Context, shareID, userID int64, role string) (FolderShareOwnerView, error) {
	body := map[string]any{"user_id": userID, "role": role}
	var resp struct {
		Share FolderShareOwnerView `json:"share"`
	}
	path := "/api/v1/files/folder-shares/" + strconv.FormatInt(shareID, 10) + "/members"
	if err := c.request(ctx, "PUT", path, body, &resp); err != nil {
		return FolderShareOwnerView{}, err
	}
	return resp.Share, nil
}

// RemoveFolderShareMember revokes a member's access.
// DELETE /files/folder-shares/{share}/members (JSON body).
func (c *Client) RemoveFolderShareMember(ctx context.Context, shareID, userID int64) error {
	body := map[string]any{"user_id": userID}
	path := "/api/v1/files/folder-shares/" + strconv.FormatInt(shareID, 10) + "/members"
	return c.request(ctx, "DELETE", path, body, nil)
}

// DeleteFolderShare deletes an internal share outright.
// DELETE /files/folder-shares/{share}.
func (c *Client) DeleteFolderShare(ctx context.Context, shareID int64) error {
	return c.request(ctx, "DELETE", "/api/v1/files/folder-shares/"+strconv.FormatInt(shareID, 10), nil, nil)
}

// FileUploadLink is a public inbound upload link: external people upload INTO
// the owner's folder (write-only; the token is the capability).
type FileUploadLink struct {
	ID            int64   `json:"id"`
	Token         string  `json:"token"`
	Label         *string `json:"label"`
	FileFolderID  *int64  `json:"file_folder_id"`
	FolderName    *string `json:"folder_name"`
	NeedsPassword bool    `json:"needs_password"`
	ExpiresAt     *string `json:"expires_at"`
}

// FilesUploadLinks lists the owner's public inbound upload links.
// GET /files/upload-links.
func (c *Client) FilesUploadLinks(ctx context.Context) ([]FileUploadLink, error) {
	var resp struct {
		Links []FileUploadLink `json:"links"`
	}
	if err := c.request(ctx, "GET", "/api/v1/files/upload-links", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Links, nil
}

// CreateUploadLink creates a public inbound upload link into folderID, expiring
// at expiresAt (required, must be in the future; RFC3339). label and password
// are optional.
// POST /files/upload-links.
func (c *Client) CreateUploadLink(ctx context.Context, folderID int64, label *string, expiresAt string, password *string) (FileUploadLink, error) {
	body := map[string]any{"file_folder_id": folderID, "expires_at": expiresAt}
	if label != nil {
		body["label"] = *label
	}
	if password != nil {
		body["password"] = *password
	}
	var resp struct {
		Link FileUploadLink `json:"link"`
	}
	if err := c.request(ctx, "POST", "/api/v1/files/upload-links", body, &resp); err != nil {
		return FileUploadLink{}, err
	}
	return resp.Link, nil
}

// DeleteUploadLink revokes an upload link. DELETE /files/upload-links/{id}.
func (c *Client) DeleteUploadLink(ctx context.Context, id int64) error {
	return c.request(ctx, "DELETE", "/api/v1/files/upload-links/"+strconv.FormatInt(id, 10), nil, nil)
}

// SharedWithMeItem is a folder or file another user shared with the caller.
type SharedWithMeItem struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"` // file, folder
	FolderName string `json:"folder_name"`
	FileName   string `json:"file_name"`
	Role       string `json:"role"` // viewer, editor
	Owner      struct {
		ID    *int64  `json:"id"`
		Name  *string `json:"name"`
		Email *string `json:"email"`
	} `json:"owner"`
}

// SharedWithMe lists folders/files others have shared with the caller.
// GET /shared-with-me.
func (c *Client) SharedWithMe(ctx context.Context) ([]SharedWithMeItem, error) {
	var resp struct {
		Shares []SharedWithMeItem `json:"shares"`
	}
	if err := c.request(ctx, "GET", "/api/v1/shared-with-me", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Shares, nil
}

// SharedBrief is the lightweight file/folder row shape returned when browsing a
// share (a subset of the owner-side FileEntry/FileFolder fields).
type SharedBrief struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Mime         *string `json:"mime"`
	Size         int64   `json:"size"`
	ParentID     *int64  `json:"parent_id"`      // folders only
	FileFolderID *int64  `json:"file_folder_id"` // files only
	UpdatedAt    *string `json:"updated_at"`
}

// SharedWithMeBrowseResult is the content of a browsed share: for a folder
// share, the whole subtree (Folders/Files, rooted at RootID); for a file
// share, just File (Folders/Files are empty).
type SharedWithMeBrowseResult struct {
	ShareID int64         `json:"share_id"`
	Role    string        `json:"role"` // owner, viewer, editor
	Kind    string        `json:"kind"` // file, folder
	RootID  *int64        `json:"root_id"`
	File    *SharedBrief  `json:"file"`
	Folders []SharedBrief `json:"folders"`
	Files   []SharedBrief `json:"files"`
}

// SharedWithMeBrowse browses a shared folder subtree, or a lone shared file.
// GET /shared-with-me/{share}.
func (c *Client) SharedWithMeBrowse(ctx context.Context, shareID int64) (SharedWithMeBrowseResult, error) {
	var result SharedWithMeBrowseResult
	path := "/api/v1/shared-with-me/" + strconv.FormatInt(shareID, 10)
	if err := c.request(ctx, "GET", path, nil, &result); err != nil {
		return SharedWithMeBrowseResult{}, err
	}
	return result, nil
}

// SharedWithMeDownload streams a shared file's bytes to w.
// GET /shared-with-me/{share}/files/{file}/raw.
func (c *Client) SharedWithMeDownload(ctx context.Context, shareID, fileID int64, w io.Writer) error {
	path := "/api/v1/shared-with-me/" + strconv.FormatInt(shareID, 10) +
		"/files/" + strconv.FormatInt(fileID, 10) + "/raw?download=1"
	return c.getStream(ctx, path, w)
}

// SharedWithMeUpload uploads into a shared folder (editor role required;
// counts against the owner's quota). folderID nil = the share's root.
// POST /shared-with-me/{share}/upload.
func (c *Client) SharedWithMeUpload(ctx context.Context, shareID int64, name string, folderID *int64, open func() (io.ReadCloser, error)) (FileEntry, error) {
	fields := map[string]string{}
	if folderID != nil {
		fields["file_folder_id"] = strconv.FormatInt(*folderID, 10)
	}
	var resp struct {
		File FileEntry `json:"file"`
	}
	path := "/api/v1/shared-with-me/" + strconv.FormatInt(shareID, 10) + "/upload"
	if _, err := c.uploadMultipart(ctx, path, "file", name, fields, open, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}

// SharedWithMeRename renames a file within a shared folder subtree, or a lone
// shared file (editor role required).
// PUT /shared-with-me/{share}/files/{file}.
func (c *Client) SharedWithMeRename(ctx context.Context, shareID, fileID int64, name string) (FileEntry, error) {
	body := map[string]any{"name": name}
	var resp struct {
		File FileEntry `json:"file"`
	}
	path := "/api/v1/shared-with-me/" + strconv.FormatInt(shareID, 10) +
		"/files/" + strconv.FormatInt(fileID, 10)
	if err := c.request(ctx, "PUT", path, body, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}

// SharedWithMeDelete deletes a file within a shared folder subtree (editor
// role required; never permitted for a lone file share).
// DELETE /shared-with-me/{share}/files/{file}.
func (c *Client) SharedWithMeDelete(ctx context.Context, shareID, fileID int64) error {
	path := "/api/v1/shared-with-me/" + strconv.FormatInt(shareID, 10) +
		"/files/" + strconv.FormatInt(fileID, 10)
	return c.request(ctx, "DELETE", path, nil, nil)
}
