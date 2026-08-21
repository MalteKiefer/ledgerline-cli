package api

import (
	"context"
	"errors"
	"io"
	"strconv"
)

// User is the authenticated identity returned by the API (MeUser schema).
type User struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Email  string   `json:"email"`
	Locale string   `json:"locale"`
	Groups []string `json:"groups"`
	// Modules the account may use (per-user/group toggles); a client hides tabs
	// for modules not listed. Enum: dashboard, files, gallery, passwords, notes,
	// todos, bookmarks, contacts, finance, health, explore.
	Modules []string `json:"modules"`
	// HasAvatar reports whether a non-secret avatar is stored (fetch via Avatar).
	HasAvatar bool `json:"has_avatar"`
	// Preferences are the non-secret display prefs (units + clock format) the GUI
	// applies to its own rendering; canonical storage stays metric/ISO.
	Preferences DisplayPreferences `json:"preferences"`
	// Theme is the current UI theme (light|dark|system).
	Theme string `json:"theme"`
}

// DisplayPreferences are the global non-secret display preferences from GET /me
// (settable via POST /api/v1/preferences). Presentation only — canonical data
// stays in metres/kg/°C/mg-dL and 24h.
type DisplayPreferences struct {
	Distance   string `json:"distance,omitempty"`    // km | mi
	Elevation  string `json:"elevation,omitempty"`   // m | ft
	Weight     string `json:"weight,omitempty"`      // kg | lb
	Temp       string `json:"temp,omitempty"`        // c | f
	Glucose    string `json:"glucose,omitempty"`     // mgdl | mmoll
	TimeFormat string `json:"time_format,omitempty"` // 24h | 12h
}

// Usage is the per-user storage footprint reported by /me. Quota is the combined
// files+gallery byte limit; nil means unlimited (the server sends null whenever
// either dimension is uncapped).
type Usage struct {
	// Used is the combined figure the quota applies to. The server sends this
	// one; reading `files`/`gallery` instead is what made the tray report 0 B
	// against a real account.
	Used  int64  `json:"used"`
	Quota *int64 `json:"quota"`

	// Files and Gallery are the per-module breakdown. Older servers do not send
	// them, so they are pointers: absent is "unknown", not "zero" — a tray must
	// not claim an empty gallery it was never told about.
	Files   *int64 `json:"files"`
	Gallery *int64 `json:"gallery"`
}

// Total is the combined usage, falling back to the parts when a server reports
// only the breakdown.
func (u Usage) Total() int64 {
	if u.Used > 0 {
		return u.Used
	}
	var sum int64
	if u.Files != nil {
		sum += *u.Files
	}
	if u.Gallery != nil {
		sum += *u.Gallery
	}
	return sum
}

// HasBreakdown reports whether the server told us how the usage splits.
func (u Usage) HasBreakdown() bool { return u.Files != nil || u.Gallery != nil }

// Device is one connected device (Sanctum token) from GET /api/v1/devices.
type Device struct {
	ID            int64  `json:"id"`      // Sanctum token id; used in the revoke/wipe path
	Current       bool   `json:"current"` // true for the device making the request
	Name          string `json:"name"`
	Meta          string `json:"meta"`      // "IP · last used 3 hours ago"
	Version       string `json:"version"`   // non-secret app/OS build, may be ""
	InstallID     string `json:"installId"` // last 6 chars of the install id, may be ""
	Syncing       bool   `json:"syncing"`
	SyncDetail    string `json:"syncDetail"`
	SyncSeen      string `json:"syncSeen"`
	WipeRequested bool   `json:"wipeRequested"`
}

// PairStatus is the state reported by the pairing endpoints.
type PairStatus string

const (
	// PairPending means the code is claimed but not yet approved by the owner.
	PairPending PairStatus = "pending"
	// PairApproved means the owner approved and a token was issued.
	PairApproved PairStatus = "approved"
)

// pairPollResponse is the shape of POST /api/v1/auth/pair/collect.
type pairPollResponse struct {
	Status PairStatus `json:"status"`
	Token  string     `json:"token"`
	User   User       `json:"user"`
}

// PairResult carries an approved pairing's token and identity.
type PairResult struct {
	Token string
	User  User
}

// ClaimPair submits a pasted one-time code and a device name, moving the pairing
// to pending-approval. The server answers {status: "pending"}. A 410 means the
// code is expired, unknown, or already claimed.
func (c *Client) ClaimPair(ctx context.Context, code, deviceName string) error {
	body := map[string]string{"code": code, "device_name": deviceName}
	return c.request(ctx, "POST", "/api/v1/auth/pair", body, nil)
}

// PollPair polls the pairing with the given code. While the owner has not yet
// approved the device it returns (PairPending, nil, nil). Once approved it
// returns (PairApproved, result, nil) exactly once — the code is then spent and
// further polls yield a 410 *APIError.
func (c *Client) PollPair(ctx context.Context, code string) (PairStatus, *PairResult, error) {
	// POST so the one-time code travels in the request body, never in a URL/
	// query string (which would land in server access logs and proxies).
	var resp pairPollResponse
	if err := c.request(ctx, "POST", "/api/v1/auth/pair/collect", map[string]string{"code": code}, &resp); err != nil {
		return "", nil, err
	}
	if resp.Status != PairApproved || resp.Token == "" {
		return PairPending, nil, nil
	}
	return PairApproved, &PairResult{Token: resp.Token, User: resp.User}, nil
}

// LoginRequest is a password sign-in. Code and RecoveryCode are the account's
// second factor; exactly one of them is sent, and only when the server asked
// for it. InstallID makes the server register this installation as a device
// (revocable, wipeable, and replacing its own previous slot) instead of minting
// a bare token.
type LoginRequest struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	Code         string `json:"code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
	DeviceName   string `json:"device_name,omitempty"`
	InstallID    string `json:"install_id,omitempty"`
	AppVersion   string `json:"app_version,omitempty"`
	OSVersion    string `json:"os_version,omitempty"`
}

// LoginResult carries the issued bearer and the identity behind it.
type LoginResult struct {
	Token string
	User  User
}

// Login exchanges credentials for a device-scoped bearer token.
//
// Failure modes worth branching on, all *APIError:
//   - 422 with TwoFactor: the account has a confirmed second factor and none was
//     supplied (or it was wrong). Ask for it and retry the same call.
//   - 422 otherwise: wrong credentials — deliberately indistinguishable from a
//     blocked account, so do not guess which it was in the message.
//   - 403 with Code "verify-email": the address is unverified; no token exists
//     to hand out until it is.
func (c *Client) Login(ctx context.Context, req LoginRequest) (LoginResult, error) {
	var resp struct {
		Token string `json:"token"`
		User  User   `json:"user"`
	}
	if err := c.request(ctx, "POST", "/api/v1/auth/login", req, &resp); err != nil {
		return LoginResult{}, err
	}
	if resp.Token == "" {
		return LoginResult{}, errors.New("the server accepted the credentials but issued no token")
	}
	return LoginResult{Token: resp.Token, User: resp.User}, nil
}

// Me returns the authenticated user, storage usage, and whether the owner has
// requested a remote wipe of this client. A 401 *APIError indicates the token is
// missing, invalid, or revoked.
func (c *Client) Me(ctx context.Context) (User, Usage, bool, error) {
	var resp struct {
		User  User  `json:"user"`
		Usage Usage `json:"usage"`
		Wipe  bool  `json:"wipe"`
	}
	if err := c.request(ctx, "GET", "/api/v1/me", nil, &resp); err != nil {
		return User{}, Usage{}, false, err
	}
	return resp.User, resp.Usage, resp.Wipe, nil
}

// Avatar streams the account's profile picture (PNG/JPEG bytes) to w. Only
// meaningful when User.HasAvatar is true; a missing avatar is a 404, which the
// caller can treat as "show initials instead". The image is non-secret display
// data, like the display name. GET /avatar.
func (c *Client) Avatar(ctx context.Context, w io.Writer) error {
	return c.getStream(ctx, "/api/v1/avatar", w)
}

// Heartbeat reports this client's sync activity (state is "idle" or "syncing",
// detail is a short human summary) and returns whether a remote wipe is pending.
func (c *Client) Heartbeat(ctx context.Context, state, detail string) (wipe bool, err error) {
	body := map[string]string{"state": state, "detail": detail}
	var resp struct {
		Wipe bool `json:"wipe"`
	}
	if err := c.request(ctx, "POST", "/api/v1/device/heartbeat", body, &resp); err != nil {
		return false, err
	}
	return resp.Wipe, nil
}

// Logout revokes the bearer currently in use (server-side), ending the session.
func (c *Client) Logout(ctx context.Context) error {
	return c.request(ctx, "DELETE", "/api/v1/auth/session", nil, nil)
}

// Devices lists the caller's connected devices (Sanctum tokens), most-recently-
// used first.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	var resp struct {
		Devices []Device `json:"devices"`
	}
	if err := c.request(ctx, "GET", "/api/v1/devices", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Devices, nil
}

// RevokeDevice deletes a connected device's token by its id. The server refuses to
// revoke the current device (a client should also guard on Device.Current).
func (c *Client) RevokeDevice(ctx context.Context, id int64) error {
	return c.request(ctx, "DELETE", "/api/v1/devices/"+strconv.FormatInt(id, 10), nil, nil)
}

// WipeDevice flags a device for remote wipe: its next /me or /device/heartbeat
// returns wipe=true, prompting that client to erase local data and log out.
func (c *Client) WipeDevice(ctx context.Context, id int64) error {
	return c.request(ctx, "POST", "/api/v1/devices/"+strconv.FormatInt(id, 10)+"/wipe", nil, nil)
}
