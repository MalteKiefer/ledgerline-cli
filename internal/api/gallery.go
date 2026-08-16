package api

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
)

// GalleryPhoto is a photo row from the plaintext gallery API (GalleryPhoto
// schema). Bytes are never in this struct; download them via DownloadPhoto.
// Nullable columns are pointers so "absent" is distinct from a zero value.
type GalleryPhoto struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Mime      *string  `json:"mime"`
	Width     *int     `json:"width"`
	Height    *int     `json:"height"`
	Size      int64    `json:"size"`
	Favorite  bool     `json:"favorite"`
	Thumb     bool     `json:"thumb"`
	Preview   bool     `json:"preview"`
	Motion    bool     `json:"motion"`
	MediaType string   `json:"media_type"`
	Status    string   `json:"status"`
	Duration  *int     `json:"duration"`
	Rotation  int      `json:"rotation"`
	FlipH     bool     `json:"flip_h"`
	TakenAt   *string  `json:"taken_at"`
	Camera    *string  `json:"camera"`
	Place     *string  `json:"place"`
	Lat       *float64 `json:"lat"`
	Lng       *float64 `json:"lng"`
	Version   int      `json:"version"`
	CreatedAt *string  `json:"created_at"`
}

// ListPhotos returns every photo for the current user (newest capture first),
// without bytes. GET /gallery/data.
func (c *Client) ListPhotos(ctx context.Context) ([]GalleryPhoto, error) {
	var resp struct {
		Photos []GalleryPhoto `json:"photos"`
	}
	if err := c.request(ctx, "GET", "/api/v1/gallery/data", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Photos, nil
}

// UploadPhoto uploads one image/video whole (multipart POST /gallery). The
// returned bool is true when the server de-duplicated by sha256 (HTTP 200 with
// the existing photo) rather than creating a new row (HTTP 201).
func (c *Client) UploadPhoto(ctx context.Context, fileName string, open func() (io.ReadCloser, error)) (GalleryPhoto, bool, error) {
	var resp struct {
		Photo     GalleryPhoto `json:"photo"`
		Duplicate bool         `json:"duplicate"`
	}
	status, err := c.uploadMultipart(ctx, "/api/v1/gallery", "file", fileName, nil, open, &resp)
	if err != nil {
		return GalleryPhoto{}, false, err
	}
	return resp.Photo, status == 200 || resp.Duplicate, nil
}

// DownloadPhoto streams a photo's bytes to w. variant is "original" (default) or
// "edited" (rotation/flip baked in). GET /gallery/{id}/download.
func (c *Client) DownloadPhoto(ctx context.Context, id int64, variant string, w io.Writer) error {
	path := "/api/v1/gallery/" + strconv.FormatInt(id, 10) + "/download"
	if variant != "" {
		path += "?" + url.Values{"variant": {variant}}.Encode()
	}
	return c.getStream(ctx, path, w)
}

// DeletePhoto moves one photo to the trash (soft delete). DELETE /gallery/{id}.
func (c *Client) DeletePhoto(ctx context.Context, id int64) error {
	return c.request(ctx, "DELETE", "/api/v1/gallery/"+strconv.FormatInt(id, 10), nil, nil)
}

// BulkDeletePhotos moves many photos to the trash in one call.
// POST /gallery/bulk-destroy.
func (c *Client) BulkDeletePhotos(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return fmt.Errorf("no photo ids given")
	}
	return c.request(ctx, "POST", "/api/v1/gallery/bulk-destroy", map[string][]int64{"ids": ids}, nil)
}
