package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// syncServer is a minimal in-memory Files API for sync tests.
type syncServer struct {
	files    []api.FileEntry
	raw      map[int64]string // file id -> bytes
	uploaded []string         // names POSTed to /files/entries
}

func (s *syncServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/files/data", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"folders": []api.FileFolder{}, "files": s.files, "usage": api.FilesUsage{},
		})
	})
	mux.HandleFunc("/api/v1/files/entries", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		_, hdr, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		s.uploaded = append(s.uploaded, hdr.Filename)
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"file": api.FileEntry{ID: 999, Name: hdr.Filename}})
	})
	mux.HandleFunc("/api/v1/files/entries/", func(w http.ResponseWriter, r *http.Request) {
		// /api/v1/files/entries/{id}/raw
		var id int64
		fmt.Sscanf(r.URL.Path, "/api/v1/files/entries/%d/raw", &id)
		if body, ok := s.raw[id]; ok {
			_, _ = io.WriteString(w, body)
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

func newClient(t *testing.T, srv *httptest.Server) *api.Client {
	c, err := api.New(srv.URL, api.WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSyncPushesNewLocalFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	ss := &syncServer{raw: map[int64]string{}}
	srv := httptest.NewServer(ss.handler(t))
	defer srv.Close()

	res, err := Sync(context.Background(), newClient(t, srv), dir, SyncOptions{}, io.Discard, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pushed != 1 || len(ss.uploaded) != 1 || ss.uploaded[0] != "new.txt" {
		t.Fatalf("res=%+v uploaded=%v", res, ss.uploaded)
	}
}

func TestSyncPullsRemoteOnlyFile(t *testing.T) {
	dir := t.TempDir()
	ss := &syncServer{
		files: []api.FileEntry{{ID: 5, Name: "remote.txt", Sha256: strptr(sha("R"))}},
		raw:   map[int64]string{5: "R"},
	}
	srv := httptest.NewServer(ss.handler(t))
	defer srv.Close()

	res, err := Sync(context.Background(), newClient(t, srv), dir, SyncOptions{}, io.Discard, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "remote.txt"))
	if err != nil || string(got) != "R" {
		t.Fatalf("pulled file = %q err=%v", got, err)
	}
	if res.Pulled != 1 {
		t.Fatalf("res=%+v", res)
	}
}

func TestSyncNoOpOnEqualContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "same.txt"), []byte("X"), 0o644); err != nil {
		t.Fatal(err)
	}
	ss := &syncServer{
		files: []api.FileEntry{{ID: 8, Name: "same.txt", Sha256: strptr(sha("X"))}},
		raw:   map[int64]string{8: "X"},
	}
	srv := httptest.NewServer(ss.handler(t))
	defer srv.Close()

	res, err := Sync(context.Background(), newClient(t, srv), dir, SyncOptions{}, io.Discard, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pushed != 0 || res.Pulled != 0 || len(ss.uploaded) != 0 {
		t.Fatalf("expected no-op, got res=%+v uploaded=%v", res, ss.uploaded)
	}
}

func TestSyncConflictSkip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("LOCAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	ss := &syncServer{
		files: []api.FileEntry{{ID: 3, Name: "c.txt", Sha256: strptr(sha("REMOTE"))}},
		raw:   map[int64]string{3: "REMOTE"},
	}
	srv := httptest.NewServer(ss.handler(t))
	defer srv.Close()

	res, err := Sync(context.Background(), newClient(t, srv), dir, SyncOptions{Conflict: ConflictSkip}, io.Discard, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflicts != 1 || res.Skipped != 1 || len(ss.uploaded) != 0 {
		t.Fatalf("res=%+v uploaded=%v", res, ss.uploaded)
	}
	// Local file must be untouched.
	got, _ := os.ReadFile(filepath.Join(dir, "c.txt"))
	if string(got) != "LOCAL" {
		t.Fatalf("local clobbered: %q", got)
	}
}

func strptr(s string) *string { return &s }
