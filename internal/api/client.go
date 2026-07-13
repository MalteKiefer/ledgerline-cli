// Package api is a thin, typed client for the Ledgerline mobile/CLI API
// (the /api/v1 surface shared with the Android app). It handles transport
// security, bearer authentication, and decoding the server's JSON error shape.
package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds a single request. Pairing polls are short; uploads set
// their own longer timeouts.
const DefaultTimeout = 30 * time.Second

// Retry policy for rate-limited (429) and temporarily-unavailable (503)
// responses. A parallel bulk upload is bursty and will periodically trip the
// server's rate limit; the client backs off (honouring Retry-After) and retries
// rather than failing the whole run.
const (
	maxRetries     = 8
	retryBaseDelay = 500 * time.Millisecond
	retryMaxDelay  = 30 * time.Second
)

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
		httpClient: hardenedClient(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// hardenedClient builds the default HTTP client: TLS 1.2+ and a redirect policy
// that refuses scheme downgrades and cross-host hops (the API is single-origin,
// so the bearer and any transient plaintext must never follow a redirect off it).
func hardenedClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &http.Client{
		Timeout:   DefaultTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 {
				return nil
			}
			if req.URL.Scheme != "https" && !isLoopback(req.URL.Hostname()) {
				return errors.New("refusing redirect to a non-https URL")
			}
			if req.URL.Host != via[0].URL.Host {
				return errors.New("refusing cross-host redirect")
			}
			return nil
		},
	}
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

	var bodyBytes []byte
	if reader != nil {
		bodyBytes, _ = io.ReadAll(reader)
	}
	resp, err := c.retriableDo(ctx, func() (*http.Request, error) {
		var r io.Reader
		if bodyBytes != nil {
			r = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), r)
		if err != nil {
			return nil, err
		}
		c.setHeaders(req, body != nil)
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return readJSON(resp, out, 8<<20)
}

// retriableDo runs requests built by newReq, retrying on a 429/503 with backoff
// (honouring Retry-After) so a bursty parallel upload rides out the server's
// rate limit instead of failing. newReq must produce a fresh request each call
// so its body can be replayed. It returns the first 2xx response with its body
// still open for the caller to read.
func (c *Client) retriableDo(ctx context.Context, newReq func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		apiErr := decodeError(resp)
		resp.Body.Close()
		var ae *APIError
		if attempt >= maxRetries || !errors.As(apiErr, &ae) || !retryable(ae.StatusCode) {
			return nil, apiErr
		}
		if werr := sleepBackoff(ctx, ae, attempt); werr != nil {
			return nil, werr
		}
	}
}

// readJSON decodes a bounded 2xx JSON body into out (a no-op when out is nil or
// the body is empty).
func readJSON(resp *http.Response, out any, limit int64) error {
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// retryable reports whether a status code is worth retrying after a wait.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
}

// sleepBackoff waits before the next attempt: the server's Retry-After if given,
// otherwise an exponential delay, both capped and jittered to avoid a thundering
// herd of parallel workers retrying in lockstep. It returns early if ctx ends.
func sleepBackoff(ctx context.Context, e *APIError, attempt int) error {
	delay := e.RetryAfter
	if delay <= 0 {
		delay = retryBaseDelay << attempt
	}
	if delay > retryMaxDelay {
		delay = retryMaxDelay
	}
	// Full jitter over [delay/2, delay].
	half := delay / 2
	delay = half + time.Duration(rand.Int63n(int64(half)+1))

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
	// A version-less UA to the server: the exact build is client-side metadata
	// that would only help fingerprint the user. (The GitHub update check, which
	// is not the server, still sends the version.)
	req.Header.Set("User-Agent", "ledgerline-cli")
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
