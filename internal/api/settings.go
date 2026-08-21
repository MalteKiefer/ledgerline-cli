package api

import "context"

// UserSettings are the per-user, non-display settings the server keeps: today
// just the personal cap on how many versions of a file it retains.
//
// The field is a pointer so a partial update means what it says. Sending
// {"file_max_versions": 0} would ask the server to keep no versions at all;
// sending nothing at all must leave the setting alone, and only a nil pointer
// can express that.
type UserSettings struct {
	FileMaxVersions *int `json:"file_max_versions,omitempty"`
}

// Settings reads the caller's per-user settings. GET /settings.
func (c *Client) Settings(ctx context.Context) (UserSettings, error) {
	var out UserSettings
	if err := c.request(ctx, "GET", "/api/v1/settings", nil, &out); err != nil {
		return UserSettings{}, err
	}
	return out, nil
}

// UpdateSettings changes a subset of the per-user settings and returns the
// stored result. Keys left nil are untouched. PUT /settings.
func (c *Client) UpdateSettings(ctx context.Context, in UserSettings) (UserSettings, error) {
	var out UserSettings
	if err := c.request(ctx, "PUT", "/api/v1/settings", in, &out); err != nil {
		return UserSettings{}, err
	}
	return out, nil
}
