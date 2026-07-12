package api

import (
	"context"
	"net/http"
)

// Store fetches the sealed workspace manifest (notes, bookmarks, todos, files,
// contacts — all modules share this one opaque store) and its version.
func (c *Client) Store(ctx context.Context) (SealedStore, error) {
	var out SealedStore
	if err := c.request(ctx, "GET", "/api/v1/store", nil, &out); err != nil {
		return SealedStore{}, err
	}
	return out, nil
}

// SaveStore writes the sealed workspace manifest at the expected version. A 409
// becomes ErrVersionConflict so the caller reloads, re-applies and retries; the
// new server version is returned on success.
func (c *Client) SaveStore(ctx context.Context, ciphertext string, version int64) (int64, error) {
	body := map[string]any{"ciphertext": ciphertext, "version": version}
	var out struct {
		Version int64 `json:"version"`
	}
	if err := c.request(ctx, "PUT", "/api/v1/store", body, &out); err != nil {
		if Status(err) == http.StatusConflict {
			return 0, ErrVersionConflict
		}
		return 0, err
	}
	return out.Version, nil
}
