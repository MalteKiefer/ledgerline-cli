package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestFilesChunkLifecycle(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/files/upload/chunk/init":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "big.bin" || body["size"].(float64) != 20 {
				t.Fatalf("body = %v", body)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"sess-1","partSize":10}`))
		case "/api/v1/files/upload/chunk/part":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("id") != "sess-1" {
				t.Fatalf("id = %q", r.FormValue("id"))
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/v1/files/upload/chunk/complete":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["id"] != "sess-1" {
				t.Fatalf("body = %v", body)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"file":{"id":9,"name":"big.bin"}}`))
		case "/api/v1/files/upload/chunk/abort":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := testClient(t, srv)
	session, err := c.FilesChunkInit(context.Background(), "big.bin", 20, nil)
	if err != nil || session.ID != "sess-1" || session.PartSize != 10 {
		t.Fatalf("session=%+v err=%v", session, err)
	}
	if err := c.FilesChunkAbort(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("seen = %v", seen)
	}
}

func TestUploadFileChunkedSplitsIntoParts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	data := make([]byte, 25)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var partIndexes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/upload/chunk/init":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"sess-2","partSize":10}`))
		case "/api/v1/files/upload/chunk/part":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			partIndexes = append(partIndexes, r.FormValue("index"))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/v1/files/upload/chunk/complete":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"file":{"id":9,"name":"big.bin","size":25}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := testClient(t, srv)
	var lastSent int64
	f, err := c.UploadFileChunked(context.Background(), path, "big.bin", nil, func(sent, total int64) {
		lastSent = sent
		if total != 25 {
			t.Fatalf("total = %d", total)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != 9 {
		t.Fatalf("file = %+v", f)
	}
	if lastSent != 25 {
		t.Fatalf("lastSent = %d", lastSent)
	}
	if len(partIndexes) != 3 { // 25 bytes / 10-byte parts = 3 parts (10, 10, 5)
		t.Fatalf("partIndexes = %v", partIndexes)
	}
}
