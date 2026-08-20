package webdavfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"time"

	"golang.org/x/net/webdav"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

// parseTime turns a server timestamp into a time.Time, falling back to the zero
// value: a WebDAV client tolerates a missing mtime, but not a parse panic.
func parseTime(s *string) time.Time {
	if s == nil {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, *s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// folderInfo adapts a folder row to fs.FileInfo.
type folderInfo struct{ folder api.FileFolder }

func (i folderInfo) Name() string { return i.folder.Name }
func (i folderInfo) Size() int64  { return 0 }
func (i folderInfo) Mode() fs.FileMode {
	return fs.ModeDir | 0o755
}
func (i folderInfo) ModTime() time.Time { return parseTime(i.folder.UpdatedAt) }
func (i folderInfo) IsDir() bool        { return true }
func (i folderInfo) Sys() any           { return i.folder }

// fileInfo adapts a file row to fs.FileInfo.
type fileInfo struct{ file api.FileEntry }

func (i fileInfo) Name() string       { return i.file.Name }
func (i fileInfo) Size() int64        { return i.file.Size }
func (i fileInfo) Mode() fs.FileMode  { return 0o644 }
func (i fileInfo) ModTime() time.Time { return parseTime(i.file.UpdatedAt) }
func (i fileInfo) IsDir() bool        { return false }
func (i fileInfo) Sys() any           { return i.file }

// dirHandle is a read-only directory handle: PROPFIND reads it, nothing writes it.
type dirHandle struct {
	info    folderInfo
	entries []fs.FileInfo
	offset  int
}

func (d *dirHandle) Close() error              { return nil }
func (d *dirHandle) Read([]byte) (int, error)  { return 0, errIsDir }
func (d *dirHandle) Write([]byte) (int, error) { return 0, errIsDir }
func (d *dirHandle) Seek(int64, int) (int64, error) {
	return 0, errIsDir
}
func (d *dirHandle) Stat() (fs.FileInfo, error) { return d.info, nil }

// Readdir returns the next count children (all of them when count <= 0), which
// is the contract webdav.Handler's PROPFIND walk expects.
func (d *dirHandle) Readdir(count int) ([]fs.FileInfo, error) {
	if count <= 0 {
		rest := d.entries[d.offset:]
		d.offset = len(d.entries)
		return rest, nil
	}
	if d.offset >= len(d.entries) {
		return nil, io.EOF
	}
	end := d.offset + count
	if end > len(d.entries) {
		end = len(d.entries)
	}
	batch := d.entries[d.offset:end]
	d.offset = end
	return batch, nil
}

var errIsDir = errors.New("webdav: is a directory")

// fileHandle is a temp-file-backed file handle. Reads download the body once;
// writes accumulate locally and are uploaded when the handle closes, because the
// API has no partial-write endpoint — a WebDAV PUT is one whole-body upload.
type fileHandle struct {
	fs     *FS
	ctx    context.Context
	name   string // full mount path
	entry  api.FileEntry
	exists bool

	temp   *os.File
	loaded bool // body already downloaded into temp
	dirty  bool // written to; needs an upload on Close
	closed bool
	size   int64
}

// openFile prepares a handle. A truncating or brand-new write starts from an
// empty temp file (no download); any other mode downloads lazily on first use.
func (f *FS) openFile(ctx context.Context, name string, entry api.FileEntry, exists, writing bool, flag int) (webdav.File, error) {
	temp, err := os.CreateTemp("", "ledgerline-dav-*")
	if err != nil {
		return nil, err
	}
	h := &fileHandle{
		fs:     f,
		ctx:    ctx,
		name:   name,
		entry:  entry,
		exists: exists,
		temp:   temp,
		size:   entry.Size,
	}
	if !exists || flag&os.O_TRUNC != 0 {
		h.loaded = true
		h.size = 0
		if writing {
			h.dirty = true // an empty PUT must still create/truncate the file
		}
	}
	return h, nil
}

// ensureLoaded downloads the current body into the temp file once.
func (h *fileHandle) ensureLoaded() error {
	if h.loaded {
		return nil
	}
	if err := h.fs.client.DownloadFile(h.ctx, h.entry.ID, h.temp); err != nil {
		return err
	}
	if _, err := h.temp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h.loaded = true
	return nil
}

func (h *fileHandle) Read(p []byte) (int, error) {
	if err := h.ensureLoaded(); err != nil {
		return 0, err
	}
	return h.temp.Read(p)
}

func (h *fileHandle) Seek(offset int64, whence int) (int64, error) {
	if err := h.ensureLoaded(); err != nil {
		return 0, err
	}
	return h.temp.Seek(offset, whence)
}

func (h *fileHandle) Write(p []byte) (int, error) {
	if h.fs.readOnly {
		return 0, ErrReadOnly
	}
	if err := h.ensureLoaded(); err != nil {
		return 0, err
	}
	n, err := h.temp.Write(p)
	if n > 0 {
		h.dirty = true
	}
	return n, err
}

// Readdir satisfies webdav.File; a regular file is never a directory.
func (h *fileHandle) Readdir(int) ([]fs.FileInfo, error) {
	return nil, errors.New("webdav: not a directory")
}

func (h *fileHandle) Stat() (fs.FileInfo, error) {
	if h.dirty {
		// Report what the client just wrote, not the stale server size.
		if info, err := h.temp.Stat(); err == nil {
			entry := h.entry
			entry.Name = path.Base(h.name)
			entry.Size = info.Size()
			return fileInfo{entry}, nil
		}
	}
	if h.exists {
		return fileInfo{h.entry}, nil
	}
	entry := h.entry
	entry.Name = path.Base(h.name)
	entry.Size = h.size
	return fileInfo{entry}, nil
}

// Close uploads a dirty handle and always removes the temp file, so a mount
// never leaves plaintext bodies behind on the local disk.
func (h *fileHandle) Close() error {
	if h.closed {
		return nil
	}
	h.closed = true
	tempPath := h.temp.Name()
	defer func() {
		_ = h.temp.Close()
		_ = os.Remove(tempPath)
	}()

	if !h.dirty {
		return nil
	}
	if _, err := h.temp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// Re-open the temp file per attempt: the upload helper may read the body more
	// than once (redirect/retry), and a consumed handle would send zero bytes.
	open := func() (io.ReadCloser, error) { return os.Open(tempPath) }
	base := path.Base(h.name)

	var err error
	if h.exists {
		_, err = h.fs.client.ReplaceFileContent(h.ctx, h.entry.ID, base, open)
	} else {
		folderID, ok := h.fs.folderIDFor(path.Dir(h.name))
		if !ok {
			return os.ErrNotExist
		}
		_, err = h.fs.client.UploadFile(h.ctx, base, folderID, open)
	}
	if err != nil {
		return err
	}
	h.fs.invalidate()
	return nil
}
