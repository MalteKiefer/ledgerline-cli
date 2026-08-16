package api

import (
	"context"
	"net/url"
	"strconv"
)

// FileActivity is one Files activity-feed entry (owner-scoped). Actor is the
// display name of whoever performed it, or an upload-link label for an
// anonymous external upload.
type FileActivity struct {
	ID           int64          `json:"id"`
	Action       string         `json:"action"` // upload, external_upload, rename, move, version, trash, restore, delete, share
	FileID       *int64         `json:"file_id"`
	FileName     *string        `json:"file_name"`
	FileFolderID *int64         `json:"file_folder_id"`
	Actor        *string        `json:"actor"`
	Meta         map[string]any `json:"meta"`
	CreatedAt    string         `json:"created_at"`
}

// FilesActivity returns the recent Files activity feed for the current user
// (newest first). GET /files/activity.
func (c *Client) FilesActivity(ctx context.Context) ([]FileActivity, error) {
	var resp struct {
		Activity []FileActivity `json:"activity"`
	}
	if err := c.request(ctx, "GET", "/api/v1/files/activity", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Activity, nil
}

// FileActivityFor returns the activity history for one file.
// GET /files/entries/{id}/activity.
func (c *Client) FileActivityFor(ctx context.Context, id int64) ([]FileActivity, error) {
	var resp struct {
		Activity []FileActivity `json:"activity"`
	}
	path := "/api/v1/files/entries/" + strconv.FormatInt(id, 10) + "/activity"
	if err := c.request(ctx, "GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Activity, nil
}

// FileShow returns a single file row (deep-open from a global search result).
// GET /files/entries/{id}/show.
func (c *Client) FileShow(ctx context.Context, id int64) (FileEntry, error) {
	var resp struct {
		File FileEntry `json:"file"`
	}
	path := "/api/v1/files/entries/" + strconv.FormatInt(id, 10) + "/show"
	if err := c.request(ctx, "GET", path, nil, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}

// FileInfoMetadata is per-filetype extracted metadata (image EXIF, PDF info,
// audio/video, STL geometry, text/archive stats).
type FileInfoMetadata struct {
	Kind   string            `json:"kind"`
	Fields map[string]string `json:"fields"`
}

// FileInfoShare is the file's public-share status, if any.
type FileInfoShare struct {
	ExpiresAt     *string `json:"expires_at"`
	AllowDownload bool    `json:"allow_download"`
	Protected     bool    `json:"protected"`
}

// FileInfoDuplicate is a same-checksum sibling file.
type FileInfoDuplicate struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// FileInfo is the rich info-panel aggregate for one file: checksum, dates,
// version count, folder path, extracted metadata, a content snippet, sharing
// status, same-checksum duplicates and recent activity.
type FileInfo struct {
	Sha256     *string             `json:"sha256"`
	CreatedAt  *string             `json:"created_at"`
	UpdatedAt  *string             `json:"updated_at"`
	Version    int                 `json:"version"`
	Versions   int                 `json:"versions"`
	Path       string              `json:"path"`
	Metadata   *FileInfoMetadata   `json:"metadata"`
	Snippet    *string             `json:"snippet"`
	Share      *FileInfoShare      `json:"share"`
	Duplicates []FileInfoDuplicate `json:"duplicates"`
	Activity   []FileActivity      `json:"activity"`
}

// GetFileInfo returns the rich info panel for one file.
// GET /files/entries/{id}/info.
func (c *Client) GetFileInfo(ctx context.Context, id int64) (FileInfo, error) {
	var info FileInfo
	path := "/api/v1/files/entries/" + strconv.FormatInt(id, 10) + "/info"
	if err := c.request(ctx, "GET", path, nil, &info); err != nil {
		return FileInfo{}, err
	}
	return info, nil
}

// FilesSearch full-text/OCR-searches the user's files (filename + extracted
// content). An empty query returns an empty match set.
// GET /files/search.
func (c *Client) FilesSearch(ctx context.Context, query string) ([]FileEntry, error) {
	var resp struct {
		Files []FileEntry `json:"files"`
	}
	path := "/api/v1/files/search?q=" + url.QueryEscape(query)
	if err := c.request(ctx, "GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Files, nil
}

// FilesStats is storage usage by mime type plus suspected duplicate groups (by
// sha256). Each entry in Duplicates is a group of file rows (loosely typed:
// id/name/path, shape not otherwise fixed by the API).
type FilesStats struct {
	Used       int64              `json:"used"`
	ByType     map[string]int64   `json:"by_type"`
	Duplicates [][]map[string]any `json:"duplicates"`
}

// GetFilesStats returns storage stats: size by mime type + suspected
// duplicates by sha256. GET /files/stats.
func (c *Client) GetFilesStats(ctx context.Context) (FilesStats, error) {
	var stats FilesStats
	if err := c.request(ctx, "GET", "/api/v1/files/stats", nil, &stats); err != nil {
		return FilesStats{}, err
	}
	return stats, nil
}
