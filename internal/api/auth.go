package api

import (
	"context"
	"net/url"
)

// User is the authenticated identity returned by the API.
type User struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Email  string   `json:"email"`
	Locale string   `json:"locale"`
	Groups []string `json:"groups"`
}

// Usage is the per-user storage footprint reported by /me.
type Usage struct {
	Files   int64 `json:"files"`
	Gallery int64 `json:"gallery"`
}

// PairStatus is the state reported by the pairing endpoints.
type PairStatus string

const (
	// PairPending means the code is claimed but not yet approved by the owner.
	PairPending PairStatus = "pending"
	// PairApproved means the owner approved and a token was issued.
	PairApproved PairStatus = "approved"
)

// pairPollResponse is the shape of GET /api/v1/auth/pair.
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
	var resp pairPollResponse
	if err := c.request(ctx, "GET", "/api/v1/auth/pair?code="+url.QueryEscape(code), nil, &resp); err != nil {
		return "", nil, err
	}
	if resp.Status != PairApproved || resp.Token == "" {
		return PairPending, nil, nil
	}
	return PairApproved, &PairResult{Token: resp.Token, User: resp.User}, nil
}

// Me returns the authenticated user and storage usage for the bearer in use.
// A 401 *APIError indicates the token is missing, invalid, or revoked.
func (c *Client) Me(ctx context.Context) (User, Usage, error) {
	var resp struct {
		User  User  `json:"user"`
		Usage Usage `json:"usage"`
	}
	if err := c.request(ctx, "GET", "/api/v1/me", nil, &resp); err != nil {
		return User{}, Usage{}, err
	}
	return resp.User, resp.Usage, nil
}

// Logout revokes the bearer currently in use (server-side), ending the session.
func (c *Client) Logout(ctx context.Context) error {
	return c.request(ctx, "DELETE", "/api/v1/auth/session", nil, nil)
}
