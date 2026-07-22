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

// SaveFilesStore writes the sealed Files root at the expected version. On a 409
// it returns ErrVersionConflict; the new server version is returned on success.
func (c *Client) SaveFilesStore(ctx context.Context, ciphertext string, version int64) (int64, error) {
	body := map[string]any{"ciphertext": ciphertext, "version": version}
	var out struct {
		Version int64 `json:"version"`
	}
	if err := c.request(ctx, "PUT", "/api/v1/files/store", body, &out); err != nil {
		if Status(err) == http.StatusConflict {
			return 0, ErrVersionConflict
		}
		return 0, err
	}
	return out.Version, nil
}
