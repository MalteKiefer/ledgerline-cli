package api

import (
	"context"
	"net/http"
)

// ModuleStore fetches a single module's sealed store row (Store v3 split: each
// module — notes, todos, bookmarks, contacts, invoices, passwords, health,
// sharing — has its own opaque row at /store/{module}) and its version.
func (c *Client) ModuleStore(ctx context.Context, module string) (SealedStore, error) {
	var out SealedStore
	if err := c.request(ctx, "GET", "/api/v1/store/"+module, nil, &out); err != nil {
		return SealedStore{}, err
	}
	return out, nil
}

// SaveModuleStore writes a module's sealed row at the expected version. A 409
// becomes ErrVersionConflict so the caller reloads, re-applies and retries; the
// new server version is returned on success.
func (c *Client) SaveModuleStore(ctx context.Context, module, ciphertext string, version int64) (int64, error) {
	body := map[string]any{"ciphertext": ciphertext, "version": version}
	var out struct {
		Version int64 `json:"version"`
	}
	if err := c.request(ctx, "PUT", "/api/v1/store/"+module, body, &out); err != nil {
		if Status(err) == http.StatusConflict {
			return 0, ErrVersionConflict
		}
		return 0, err
	}
	return out.Version, nil
}

// FilesStore fetches the sealed Files sharded-store root (Store v3: Files has its
// own sharded store at /files/store, like the gallery) and its version.
func (c *Client) FilesStore(ctx context.Context) (SealedStore, error) {
	var out SealedStore
	if err := c.request(ctx, "GET", "/api/v1/files/store", nil, &out); err != nil {
		return SealedStore{}, err
	}
	return out, nil
}

// SaveFilesStore writes the sealed Files root at the expected version. shards is
// the live blob refs the new root points at (record shards + the folders
// collection blob); the server rejects the write (422 missing_shard) if any ref
// has no stored blob. counts is an optional per-slice record-count map
// ({"files","fileFolders"}) feeding the server's anomaly-scan (silent data-loss
// detection); pass nil to omit it entirely — a caller must NEVER send a
// partial/incomplete map, since a missing key reads as a false 0 count and can
// trigger a false data-loss alarm when interleaved with another client's writes.
// On a 409 it returns ErrVersionConflict; on a missing_shard 422, ErrMissingShard.
// The new server version is returned on success.
func (c *Client) SaveFilesStore(ctx context.Context, ciphertext string, version int64, shards []string, counts map[string]int) (int64, error) {
	body := map[string]any{"ciphertext": ciphertext, "version": version, "shards": shards}
	if counts != nil {
		body["counts"] = counts
	}
	var out struct {
		Version int64 `json:"version"`
	}
	if err := c.request(ctx, "PUT", "/api/v1/files/store", body, &out); err != nil {
		if Status(err) == http.StatusConflict {
			return 0, ErrVersionConflict
		}
		if isMissingShard(err) {
			return 0, ErrMissingShard
		}
		return 0, err
	}
	return out.Version, nil
}
