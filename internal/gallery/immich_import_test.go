package gallery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// mockImmich emulates the subset of a self-hosted Immich server the importer
// speaks to: the health ping, the paginated search/metadata enumeration, and the
// original-asset download. Every handler asserts the x-api-key header so the test
// also proves the key is sent (and, by never logging it, never leaked).
type mockImmich struct {
	srv     *httptest.Server
	apiKey  string
	pings   int
	lastReq map[string]any // decoded body of the most recent search/metadata POST
	assets  map[string][]byte
}

func newMockImmich(t *testing.T, apiKey string) *mockImmich {
	t.Helper()
	m := &mockImmich{apiKey: apiKey, assets: map[string][]byte{}}

	requireKey := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("x-api-key") != m.apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"Invalid API key"}`))
			return false
		}
		return true
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/server/ping", func(w http.ResponseWriter, r *http.Request) {
		if !requireKey(w, r) {
			return
		}
		m.pings++
		json.NewEncoder(w).Encode(map[string]string{"res": "pong"})
	})
	mux.HandleFunc("/api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		if !requireKey(w, r) {
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.lastReq = body
		// Two-page library: page 1 → nextPage "2", page 2 → nextPage null.
		page, _ := body["page"].(float64)
		var items []map[string]any
		var next any
		switch int(page) {
		case 1:
			items = []map[string]any{
				{"id": "a1", "originalFileName": "IMG_1.jpg", "checksum": "c1", "type": "IMAGE", "exifInfo": map[string]any{"make": "Apple"}},
				{"id": "a2", "originalFileName": "IMG_2.jpg", "checksum": "c2", "type": "IMAGE"},
			}
			next = "2"
		default:
			items = []map[string]any{
				{"id": "a3", "originalFileName": "clip.mov", "checksum": "c3", "type": "VIDEO"},
			}
			next = nil
		}
		json.NewEncoder(w).Encode(map[string]any{
			"assets": map[string]any{"items": items, "nextPage": next},
		})
	})
	mux.HandleFunc("/api/assets/", func(w http.ResponseWriter, r *http.Request) {
		if !requireKey(w, r) {
			return
		}
		// Path is /api/assets/{id}/original.
		id := r.URL.Path[len("/api/assets/"):]
		if i := len(id) - len("/original"); i >= 0 && id[i:] == "/original" {
			id = id[:i]
		}
		data, ok := m.assets[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(data)
	})

	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockImmich) client(t *testing.T) *ImmichClient {
	t.Helper()
	c, err := NewImmichClient(m.srv.URL, m.apiKey, m.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestImmichNewClientValidation covers URL validation: a bad scheme or a missing
// host is rejected before any network call.
func TestImmichNewClientValidation(t *testing.T) {
	if _, err := NewImmichClient("ftp://host:2283", "k", nil); err == nil {
		t.Fatal("expected a non-http(s) scheme to be rejected")
	}
	if _, err := NewImmichClient("http://", "k", nil); err == nil {
		t.Fatal("expected a hostless URL to be rejected")
	}
	if _, err := NewImmichClient("http://host:2283/", "k", nil); err != nil {
		t.Fatalf("a valid URL should build: %v", err)
	}
}

// TestImmichPing exercises the health check against the mock's /api/server/ping.
func TestImmichPing(t *testing.T) {
	m := newMockImmich(t, "secret-key")
	c := m.client(t)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if m.pings != 1 {
		t.Fatalf("server saw %d pings, want 1", m.pings)
	}
}

// TestImmichSearchPagePaging drives the nextPage loop exactly as the importer
// does: page 1 reports a next page of 2, page 2 reports done (0). The request body
// must carry withExif=true, the caller's withPeople, and the fixed timeline/
// no-deleted markers.
func TestImmichSearchPagePaging(t *testing.T) {
	m := newMockImmich(t, "secret-key")
	c := m.client(t)
	ctx := context.Background()

	var all []ImmichAsset
	page := 1
	for page != 0 {
		assets, next, err := c.SearchPage(ctx, page, 250, SearchOptions{WithPeople: true})
		if err != nil {
			t.Fatalf("SearchPage(%d): %v", page, err)
		}
		all = append(all, assets...)
		page = next
	}

	// Every asset across both pages was collected, in order.
	if len(all) != 3 {
		t.Fatalf("want 3 assets across the paged library, got %d", len(all))
	}
	if all[0].ID != "a1" || all[1].ID != "a2" || all[2].ID != "a3" {
		t.Fatalf("assets out of order or missing: %+v", all)
	}
	// exifInfo mapped where present, nil where absent.
	if all[0].Exif == nil || all[0].Exif.Make != "Apple" {
		t.Fatalf("asset a1 exif not decoded: %+v", all[0].Exif)
	}
	if all[1].Exif != nil {
		t.Fatalf("asset a2 has no exifInfo; want nil, got %+v", all[1].Exif)
	}
	if all[2].Type != "VIDEO" {
		t.Fatalf("asset a3 type = %q, want VIDEO", all[2].Type)
	}

	// The last request body reflects the SearchPage contract.
	if m.lastReq["withExif"] != true {
		t.Fatalf("withExif not set true: %v", m.lastReq["withExif"])
	}
	if m.lastReq["withPeople"] != true {
		t.Fatalf("withPeople should mirror opts: %v", m.lastReq["withPeople"])
	}
	if m.lastReq["visibility"] != "timeline" || m.lastReq["withDeleted"] != false {
		t.Fatalf("fixed markers wrong: %v", m.lastReq)
	}
}

// TestImmichSearchPageWithPeopleOff confirms the flag propagates when disabled.
func TestImmichSearchPageWithPeopleOff(t *testing.T) {
	m := newMockImmich(t, "secret-key")
	c := m.client(t)
	if _, _, err := c.SearchPage(context.Background(), 1, 250, SearchOptions{WithPeople: false}); err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if m.lastReq["withPeople"] != false {
		t.Fatalf("withPeople should be false: %v", m.lastReq["withPeople"])
	}
}

// TestImmichDownloadOriginal streams an octet-stream asset to a temp file created
// 0600, and asserts the bytes round-trip.
func TestImmichDownloadOriginal(t *testing.T) {
	m := newMockImmich(t, "secret-key")
	original := []byte("ORIGINAL-ASSET-BYTES-\x00\x01\x02\xff")
	m.assets["a1"] = original
	c := m.client(t)

	dest := filepath.Join(t.TempDir(), "a1.jpg")
	if err := c.DownloadOriginal(context.Background(), "a1", dest); err != nil {
		t.Fatalf("DownloadOriginal: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("downloaded bytes = %q, want %q", got, original)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dest)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("downloaded original perm = %o, want 600", perm)
		}
	}
}

// TestImmichDownloadOriginalNotFound maps a missing asset (404) to a clear error
// and leaves no partial file behind.
func TestImmichDownloadOriginalNotFound(t *testing.T) {
	m := newMockImmich(t, "secret-key")
	c := m.client(t)
	dest := filepath.Join(t.TempDir(), "missing.jpg")
	if err := c.DownloadOriginal(context.Background(), "nope", dest); err == nil {
		t.Fatal("expected an error for a 404 asset")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("a failed download must leave no file, stat err = %v", err)
	}
}

// TestImmichUnauthorized covers the 401 path: a wrong key is rejected on every
// endpoint with an error, and never silently treated as an empty library.
func TestImmichUnauthorized(t *testing.T) {
	m := newMockImmich(t, "the-real-key")
	// Build a client with the WRONG key so the server answers 401.
	c, err := NewImmichClient(m.srv.URL, "wrong-key", m.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := c.Ping(ctx); err == nil {
		t.Fatal("Ping with a bad key must error")
	}
	if _, _, err := c.SearchPage(ctx, 1, 250, SearchOptions{}); err == nil {
		t.Fatal("SearchPage with a bad key must error")
	}
	m.assets["a1"] = []byte("x")
	dest := filepath.Join(t.TempDir(), "a1.jpg")
	if err := c.DownloadOriginal(ctx, "a1", dest); err == nil {
		t.Fatal("DownloadOriginal with a bad key must error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("a rejected download must leave no file, stat err = %v", err)
	}
}
