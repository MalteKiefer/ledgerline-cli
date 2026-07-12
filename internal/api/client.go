// Package api is a thin, typed client for the Ledgerline mobile/CLI API
// (the /api/v1 surface shared with the Android app). It handles transport
// security, bearer authentication, and decoding the server's JSON error shape.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/version"
)

// DefaultTimeout bounds a single request. Pairing polls are short; uploads set
// their own longer timeouts.
const DefaultTimeout = 30 * time.Second

// Client talks to one Ledgerline server. It is safe for sequential use; create
// one per command invocation.
type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

// Option customises a Client.
type Option func(*Client)

// WithToken authenticates requests with the given Sanctum bearer.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithHTTPClient overrides the underlying HTTP client (used by tests).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// New builds a client for baseURL. The URL must be absolute and use https,
// except for loopback hosts where http is allowed to ease local development.
func New(baseURL string, opts ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("invalid server URL: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("server URL must include a host, e.g. https://ledger.example.com")
	}
	if err := validateScheme(u); err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/")

	c := &Client{
		baseURL:    u,
		httpClient: &http.Client{Timeout: DefaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// BaseURL returns the normalised server URL the client targets.
func (c *Client) BaseURL() string { return c.baseURL.String() }

// validateScheme enforces https for non-loopback hosts, matching the app's
// transport posture (it refuses cleartext in production).
func validateScheme(u *url.URL) error {
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && isLoopback(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("server URL must use https (got %q)", u.Scheme)
}

// isLoopback reports whether host is a local address for which http is tolerated.
func isLoopback(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// APIError is a structured error decoded from a non-2xx JSON response. It
// exposes the HTTP status so callers can branch on 410 (expired code), 429
// (rate limited), 401 (bad/revoked token), etc.
type APIError struct {
	StatusCode int
	Message    string
	Fields     map[string][]string
	RetryAfter time.Duration
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("server error %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("server error %d", e.StatusCode)
}

// Status returns the HTTP status code, or 0 if err is not an *APIError.
func Status(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// request performs an HTTP request, JSON-encoding body (if any) and decoding a
// 2xx JSON response into out (if any). Non-2xx responses become *APIError.
func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL.String()+path, reader)
	if err != nil {
		return err
	}
	c.setHeaders(req, body != nil)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}

	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// endpoint joins the base URL with a request path.
func (c *Client) endpoint(path string) string { return c.baseURL.String() + path }

// applyAuth sets the headers common to every request: JSON accept, the XHR
// marker (so Laravel answers with JSON, never an HTML redirect), the bearer, and
// a UA carrying the version for server-side diagnostics. It does not set a
// Content-Type, so multipart callers can set their own.
func (c *Client) applyAuth(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", "ledgerline-cli/"+version.Version)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// setHeaders applies the common headers plus a JSON Content-Type when a body is
// present.
func (c *Client) setHeaders(req *http.Request, hasBody bool) {
	c.applyAuth(req)
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
}

// decodeError turns a non-2xx response into an *APIError, best-effort parsing
// Laravel's {message, errors} envelope and the Retry-After header.
func decodeError(resp *http.Response) error {
	apiErr := &APIError{StatusCode: resp.StatusCode}

	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := time.ParseDuration(ra + "s"); err == nil {
			apiErr.RetryAfter = secs
		}
	}

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var envelope struct {
		Message string              `json:"message"`
		Errors  map[string][]string `json:"errors"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil {
		apiErr.Message = envelope.Message
		apiErr.Fields = envelope.Errors
	}
	return apiErr
}
