package api

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
)

// ChunkSession is an open chunked-upload session: upload parts of exactly
// PartSize bytes (the final part may be shorter) via FilesChunkPart, in any
// order, then call FilesChunkComplete to assemble them.
type ChunkSession struct {
	ID       string `json:"id"`
	PartSize int64  `json:"partSize"`
}

// FilesChunkInit begins a chunked upload session for a file of the given
// total size. POST /files/upload/chunk/init.
func (c *Client) FilesChunkInit(ctx context.Context, name string, size int64, folderID *int64) (ChunkSession, error) {
	body := map[string]any{"name": name, "size": size}
	if folderID != nil {
		body["file_folder_id"] = *folderID
	}
	var session ChunkSession
	if err := c.request(ctx, "POST", "/api/v1/files/upload/chunk/init", body, &session); err != nil {
		return ChunkSession{}, err
	}
	return session, nil
}

// FilesChunkPart uploads one part (0-based index) of a chunked-upload session.
// open must return a FRESH reader each call so the request can be replayed
// across the transport's retry/backoff. POST /files/upload/chunk/part.
func (c *Client) FilesChunkPart(ctx context.Context, sessionID string, index int, open func() (io.ReadCloser, error)) error {
	fields := map[string]string{
		"id":    sessionID,
		"index": strconv.Itoa(index),
	}
	_, err := c.uploadMultipart(ctx, "/api/v1/files/upload/chunk/part", "file", "part", fields, open, nil)
	return err
}

// FilesChunkComplete assembles a chunked-upload session's parts into the final
// file. POST /files/upload/chunk/complete.
func (c *Client) FilesChunkComplete(ctx context.Context, sessionID string) (FileEntry, error) {
	body := map[string]any{"id": sessionID}
	var resp struct {
		File FileEntry `json:"file"`
	}
	if err := c.request(ctx, "POST", "/api/v1/files/upload/chunk/complete", body, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}

// FilesChunkAbort discards an in-progress chunked-upload session's parts.
// POST /files/upload/chunk/abort.
func (c *Client) FilesChunkAbort(ctx context.Context, sessionID string) error {
	body := map[string]any{"id": sessionID}
	return c.request(ctx, "POST", "/api/v1/files/upload/chunk/abort", body, nil)
}

// UploadFileChunked uploads a local file in parts via the chunked-upload
// session endpoints, sized by the server's chosen ChunkSession.PartSize. It is
// the large-file counterpart to UploadFile (which streams the whole body in
// one multipart request); a sync client should prefer it once a file exceeds a
// few hundred MB, since a failed part only costs re-sending that part.
// progress, if non-nil, is called after each part with bytes sent so far.
// On any part failure the session is aborted best-effort before returning err.
func (c *Client) UploadFileChunked(ctx context.Context, path, name string, folderID *int64, progress func(sent, total int64)) (FileEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileEntry{}, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return FileEntry{}, err
	}
	size := stat.Size()

	session, err := c.FilesChunkInit(ctx, name, size, folderID)
	if err != nil {
		return FileEntry{}, err
	}
	partSize := session.PartSize
	if partSize <= 0 {
		partSize = 8 << 20
	}

	var sent int64
	for index := 0; sent < size || (size == 0 && index == 0); index++ {
		n := partSize
		if remaining := size - sent; remaining < n {
			n = remaining
		}
		section := io.NewSectionReader(f, sent, n)
		buf := make([]byte, n)
		if _, rerr := io.ReadFull(section, buf); rerr != nil && rerr != io.EOF {
			_ = c.FilesChunkAbort(ctx, session.ID)
			return FileEntry{}, rerr
		}
		open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf)), nil }
		if perr := c.FilesChunkPart(ctx, session.ID, index, open); perr != nil {
			_ = c.FilesChunkAbort(ctx, session.ID)
			return FileEntry{}, perr
		}
		sent += n
		if progress != nil {
			progress(sent, size)
		}
		if size == 0 {
			break
		}
	}

	return c.FilesChunkComplete(ctx, session.ID)
}
