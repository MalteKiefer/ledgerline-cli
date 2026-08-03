package gallery

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// maxSearchBytes bounds a single search/metadata response. A page caps at ~1000
// assets, each a small metadata record; this leaves ample headroom while still
// refusing a hostile server that streams an unbounded body (§3/§7 of the design).
const maxSearchBytes = 64 << 20 // 64 MiB

// immichCallTimeout bounds a single metadata/ping request. It caps TIME the way
// maxSearchBytes caps BYTES: a hostile or slow server can slow-drip a body that
// stays under the byte ceiling and hold enumeration open indefinitely otherwise
// (the caller's context is only cancelled on SIGINT). It is deliberately NOT
// applied to DownloadOriginal, whose lifetime must stay governed by the caller's
// context so a large multi-GB original is not cut off mid-stream.
const immichCallTimeout = 60 * time.Second

// ImmichExif is the subset of an Immich asset's exifInfo the importer maps into
// an ImportedMeta. Numeric/GPS fields are pointers so an absent value stays nil
// (distinct from a real zero) — the meta blob is cold and float-tolerant.
type ImmichExif struct {
	Make             string     `json:"make"`
	Model            string     `json:"model"`
	LensModel        string     `json:"lensModel"`
	DateTimeOriginal *time.Time `json:"dateTimeOriginal"`
	Latitude         *float64   `json:"latitude"`
	Longitude        *float64   `json:"longitude"`
	ExifImageWidth   int        `json:"exifImageWidth"`
	ExifImageHeight  int        `json:"exifImageHeight"`
}

// ImmichAsset is the subset of an Immich search result the importer needs. Type
// is "IMAGE" or "VIDEO"; LivePhotoVideoID (when set) is the paired motion clip's
// asset id, and Checksum is the base64 SHA-1 of the original (a natural dedup
// key). Exif is nil unless the search was made with withExif=true.
type ImmichAsset struct {
	ID               string         `json:"id"`
	OriginalFileName string         `json:"originalFileName"`
	Checksum         string         `json:"checksum"`
	Type             string         `json:"type"`
	FileCreatedAt    time.Time      `json:"fileCreatedAt"`
	LocalDateTime    time.Time      `json:"localDateTime"`
	Duration         immichDuration `json:"duration"` // "HH:MM:SS.ffffff", a bare number of seconds, or null
	LivePhotoVideoID string         `json:"livePhotoVideoId"`
	IsFavorite       bool           `json:"isFavorite"`
	Exif             *ImmichExif    `json:"exifInfo"`
}

// immichDuration tolerates Immich returning an asset's duration as either a
// JSON string ("HH:MM:SS.ffffff") or a bare number of seconds — both occur
// across Immich versions/asset types. It stores the raw scalar as a string;
// parseImmichDuration interprets the two forms.
type immichDuration string

func (d *immichDuration) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*d = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*d = immichDuration(s)
		return nil
	}
	*d = immichDuration(b) // a bare number of seconds
	return nil
}

// SearchOptions tunes a search/metadata page.
type SearchOptions struct {
	// WithPeople asks Immich to attach face/person data (only worth requesting
	// when ML is on and faces are wanted); it enlarges the response otherwise.
	WithPeople bool
	// TakenBefore, when non-nil, bounds the page to assets whose taken-time is at
	// or before it (inclusive) — the cursor that drives descending taken-time
	// window enumeration (design §6). Nil requests the newest, unbounded window.
	TakenBefore *time.Time
}

// ImmichClient is a thin, typed client for a self-hosted Immich server, in the
// same posture as internal/api: a TLS 1.3 floor for https, bounded response
// reads (hostile-server rule), and an x-api-key that is never logged. It is safe
// for sequential use; create one per import run.
type ImmichClient struct {
	baseURL *url.URL
	apiKey  string
	hc      *http.Client
}

// NewImmichClient builds a client for baseURL (e.g. http://host:2283). The URL
// must be http or https and include a host. hc may be nil, in which case a
// hardened client is built (TLS 1.3 floor for https). The API key is sent as the
// x-api-key header on every request and is never logged or placed in a URL.
func NewImmichClient(baseURL, apiKey string, hc *http.Client) (*ImmichClient, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("invalid Immich URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("Immich URL must use http or https (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("Immich URL must include a host, e.g. http://host:2283")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if hc == nil {
		hc = hardenedImmichClient()
	}
	return &ImmichClient{baseURL: u, apiKey: apiKey, hc: hc}, nil
}

// hardenedImmichClient is the default HTTP client: it clones the standard
// transport (keeping its dial/handshake timeouts) and pins a TLS 1.3 floor for
// https. The request lifetime is governed by the caller's context rather than a
// blunt client timeout, so a large original download is not cut off mid-stream.
func hardenedImmichClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	return &http.Client{Transport: transport}
}

// endpoint joins the base URL with an /api request path.
func (c *ImmichClient) endpoint(path string) string { return c.baseURL.String() + path }

// newRequest builds a request carrying the x-api-key header (and, for a JSON
// body, the matching Content-Type). The key lives only in the header — never in
// the URL, argv, or a log line.
func (c *ImmichClient) newRequest(ctx context.Context, method, path string, body io.Reader, jsonBody bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	if jsonBody {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// immichError turns a non-2xx response into an error, giving 401/403/404 a clear
// hint. It reads only a bounded snippet of the body and never echoes the key.
func immichError(resp *http.Response) error {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10)) //nolint:errcheck // drain for reuse
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("Immich rejected the request (%d) — check --immich-url and the API key/permissions", resp.StatusCode)
	case http.StatusNotFound:
		return fmt.Errorf("Immich returned 404 — check --immich-url (is /api reachable?) or the asset id")
	default:
		return fmt.Errorf("Immich returned status %d", resp.StatusCode)
	}
}

// Ping verifies the base URL and key are usable by calling the server's
// unauthenticated health endpoint (GET /api/server/ping), failing fast on a bad
// URL before any enumeration or download.
func (c *ImmichClient) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, immichCallTimeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, "/api/server/ping", nil, false)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("reaching Immich: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return immichError(resp)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10)) //nolint:errcheck // drain, response is a tiny {"res":"pong"}
	return nil
}

// SearchPage fetches one page of the library via POST /api/search/metadata,
// requesting exif (and optionally people) in descending taken-time order with a
// stable timeline visibility and no deleted assets. When opts.TakenBefore is set
// it bounds the page to that window (design §6 keyset enumeration). It returns the
// page's assets and the next page number (0 when Immich reports no further page).
// The JSON body is bounded per the hostile-server rule.
func (c *ImmichClient) SearchPage(ctx context.Context, page, size int, opts SearchOptions) ([]ImmichAsset, int, int, error) {
	ctx, cancel := context.WithTimeout(ctx, immichCallTimeout)
	defer cancel()
	reqBody := map[string]any{
		"page":        page,
		"size":        size,
		"withExif":    true,
		"withPeople":  opts.WithPeople,
		"visibility":  "timeline",
		"withDeleted": false,
		"order":       "desc",
	}
	if opts.TakenBefore != nil {
		reqBody["takenBefore"] = opts.TakenBefore.UTC().Format(time.RFC3339Nano)
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, 0, 0, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/api/search/metadata", bytes.NewReader(body), true)
	if err != nil {
		return nil, 0, 0, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("searching Immich: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, 0, 0, immichError(resp)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchBytes))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("reading Immich search response: %w", err)
	}
	var out struct {
		Assets struct {
			Total    int           `json:"total"`
			Items    []ImmichAsset `json:"items"`
			NextPage *string       `json:"nextPage"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, 0, 0, fmt.Errorf("decoding Immich search response: %w", err)
	}

	// nextPage is null at the end of enumeration (the terminal signal) and a
	// stringified page number otherwise. A non-nil value that does NOT parse is a
	// misbehaving/hostile server, not a terminal page: surface it as an error so
	// enumeration aborts rather than silently truncating the sweep (the ledger
	// would otherwise record a partial run as complete).
	next := 0
	if out.Assets.NextPage != nil {
		n, err := strconv.Atoi(strings.TrimSpace(*out.Assets.NextPage))
		if err != nil {
			return nil, 0, 0, fmt.Errorf("Immich returned an unparseable nextPage %q", *out.Assets.NextPage)
		}
		next = n
	}
	return out.Assets.Items, next, out.Assets.Total, nil
}

// DownloadOriginal streams GET /api/assets/{id}/original to destPath (created
// 0600, truncated), size-bounded against a hostile server. A partial file from a
// mid-stream failure is removed so a truncated original is never left behind.
func (c *ImmichClient) DownloadOriginal(ctx context.Context, assetID, destPath string) error {
	return c.downloadTo(ctx, "/api/assets/"+url.PathEscape(assetID)+"/original", assetID, destPath)
}

// DownloadPreview streams Immich's own decodable JPEG preview rendition
// (GET /api/assets/{id}/thumbnail?size=preview) to destPath. The importer uses it
// as the thumbnail/medium and the local-ML input, so a HEIC/RAW/video original is
// analysed and thumbnailed WITHOUT round-tripping plaintext through the Ledgerline
// server's /process transform (which the Go client cannot decode and some servers
// reject).
func (c *ImmichClient) DownloadPreview(ctx context.Context, assetID, destPath string) error {
	return c.downloadTo(ctx, "/api/assets/"+url.PathEscape(assetID)+"/thumbnail?size=preview", assetID, destPath)
}

// downloadTo streams a bounded GET response to destPath (0600), shared by the
// original and preview downloads. A transient transport failure (an HTTP/2
// stream reset, a closed keep-alive connection, a timeout) is retried with
// backoff — large video originals over a busy link hit these intermittently and
// a whole-asset failure would otherwise leave holes a re-run has to sweep up.
func (c *ImmichClient) downloadTo(ctx context.Context, apiPath, assetID, destPath string) error {
	const attempts = 4
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if werr := immichBackoff(ctx, attempt); werr != nil {
				return werr
			}
		}
		err := c.downloadOnce(ctx, apiPath, assetID, destPath)
		if err == nil {
			return nil
		}
		// A definite HTTP status (401/403/404/…) will not change on a retry; only
		// a transport-level error is worth repeating.
		if ctx.Err() != nil || !isTransientNetErr(err) {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("asset %s: download failed after %d attempts: %w", assetID, attempts, lastErr)
}

// downloadOnce performs a single download attempt.
func (c *ImmichClient) downloadOnce(ctx context.Context, apiPath, assetID, destPath string) error {
	req, err := c.newRequest(ctx, http.MethodGet, apiPath, nil, false)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("downloading asset %s: %w", assetID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return immichError(resp)
	}

	f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxFileBytes))
	closeErr := f.Close()
	if copyErr != nil {
		os.Remove(destPath)
		return fmt.Errorf("writing asset %s: %w", assetID, copyErr)
	}
	if closeErr != nil {
		os.Remove(destPath)
		return closeErr
	}
	if n >= maxFileBytes {
		os.Remove(destPath)
		return fmt.Errorf("asset %s exceeds the %d-byte limit", assetID, int64(maxFileBytes))
	}
	return nil
}

// isTransientNetErr reports whether an error is a transport-level failure worth
// retrying: a net timeout/connection error, an EOF mid-stream, or an HTTP/2
// stream reset (which surfaces as a typed error whose message carries "stream
// error"/"INTERNAL_ERROR" but no clean Go type to match).
func isTransientNetErr(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"stream error", "INTERNAL_ERROR", "connection reset", "closed network connection", "unexpected EOF", "broken pipe"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// immichBackoff waits before a retry: a jitter-free exponential delay capped at
// a few seconds, returning early if ctx ends.
func immichBackoff(ctx context.Context, attempt int) error {
	d := time.Duration(attempt) * 500 * time.Millisecond
	if d > 4*time.Second {
		d = 4 * time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
