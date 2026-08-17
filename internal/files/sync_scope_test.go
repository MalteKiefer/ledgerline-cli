package files

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

// scopeServer is like syncServer but also serves a folder tree and records
// which parent id a created folder/uploaded file was placed under, so scoping
// tests can assert new content lands inside the scoped remote folder rather
// than at the true account root.
type scopeServer struct {
	folders       []api.FileFolder
	files         []api.FileEntry
	raw           map[int64]string
	nextFolderID  int64
	uploadedUnder []struct {
		name   string
		folder *int64
	}
}

func (s *scopeServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/files/data", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"folders": s.folders, "files": s.files, "usage": api.FilesUsage{},
		})
	})
	mux.HandleFunc("/api/v1/files/folders", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name     string `json:"name"`
			ParentID *int64 `json:"parent_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.nextFolderID++
		f := api.FileFolder{ID: s.nextFolderID, Name: body.Name, ParentID: body.ParentID}
		s.folders = append(s.folders, f)
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"folder": f})
	})
	mux.HandleFunc("/api/v1/files/entries", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		_, hdr, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		var folderID *int64
		if v := r.FormValue("file_folder_id"); v != "" {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			folderID = &id
		}
		s.uploadedUnder = append(s.uploadedUnder, struct {
			name   string
			folder *int64
		}{hdr.Filename, folderID})
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"file": api.FileEntry{ID: 999, Name: hdr.Filename}})
	})
	mux.HandleFunc("/api/v1/files/entries/", func(w http.ResponseWriter, r *http.Request) {
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

func TestSyncRemoteFolderScopePushesIntoScopedFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	scopeID := int64(42)
	ss := &scopeServer{
		folders:      []api.FileFolder{{ID: scopeID, Name: "Photos", ParentID: nil}},
		raw:          map[int64]string{},
		nextFolderID: scopeID,
	}
	srv := httptest.NewServer(ss.handler(t))
	defer srv.Close()

	res, err := Sync(context.Background(), newClient(t, srv), dir,
		SyncOptions{RemoteFolder: &scopeID}, io.Discard, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pushed != 1 || len(ss.uploadedUnder) != 1 {
		t.Fatalf("res=%+v uploaded=%v", res, ss.uploadedUnder)
	}
	got := ss.uploadedUnder[0]
	if got.name != "a.txt" || got.folder == nil || *got.folder != scopeID {
		t.Fatalf("expected a.txt uploaded under folder %d, got %+v", scopeID, got)
	}
}

func TestSyncRemoteFolderScopeIgnoresFilesOutsideScope(t *testing.T) {
	dir := t.TempDir()
	scopeID := int64(7)
	ss := &scopeServer{
		folders: []api.FileFolder{{ID: scopeID, Name: "Scoped", ParentID: nil}},
		files: []api.FileEntry{
			{ID: 1, Name: "outside.txt", FileFolderID: nil, Sha256: strptr(sha("x"))},
		},
		raw: map[int64]string{1: "x"},
	}
	srv := httptest.NewServer(ss.handler(t))
	defer srv.Close()

	res, err := Sync(context.Background(), newClient(t, srv), dir,
		SyncOptions{RemoteFolder: &scopeID}, io.Discard, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pulled != 0 {
		t.Fatalf("expected the out-of-scope file to be ignored, res=%+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("out-of-scope file should not have been pulled")
	}
}

func TestSyncUnknownRemoteFolderErrors(t *testing.T) {
	dir := t.TempDir()
	ss := &scopeServer{}
	srv := httptest.NewServer(ss.handler(t))
	defer srv.Close()

	missing := int64(999)
	_, err := Sync(context.Background(), newClient(t, srv), dir,
		SyncOptions{RemoteFolder: &missing}, io.Discard, false)
	if err == nil {
		t.Fatal("expected an error for a nonexistent scope folder")
	}
}

func TestScanLocalSkipsHiddenByDefault(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "visible.txt"), "v")
	mustWrite(t, filepath.Join(dir, ".hidden.txt"), "h")
	if err := os.Mkdir(filepath.Join(dir, ".hiddendir"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, ".hiddendir", "inside.txt"), "i")

	entries, err := scanLocal(dir, false, &ignoreMatcher{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries["visible.txt"]; !ok {
		t.Fatalf("expected visible.txt in %v", entries)
	}
	if _, ok := entries[".hidden.txt"]; ok {
		t.Fatalf("did not expect .hidden.txt in %v", entries)
	}
	if _, ok := entries[".hiddendir/inside.txt"]; ok {
		t.Fatalf("did not expect .hiddendir/inside.txt in %v", entries)
	}
}

func TestScanLocalIncludesHiddenWhenRequested(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, ".hidden.txt"), "h")

	entries, err := scanLocal(dir, true, &ignoreMatcher{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries[".hidden.txt"]; !ok {
		t.Fatalf("expected .hidden.txt with includeHidden=true, got %v", entries)
	}
}

func TestScanLocalNeverIncludesTheIgnoreFileItself(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, ignoreFileName), "*.tmp\n")

	entries, err := scanLocal(dir, true, loadIgnore(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries[ignoreFileName]; ok {
		t.Fatalf("the ignore file itself must never be synced, got %v", entries)
	}
}

func TestScanLocalAppliesIgnorePatterns(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, ignoreFileName), "*.tmp\nbuild/\n# a comment\n")
	mustWrite(t, filepath.Join(dir, "keep.txt"), "k")
	mustWrite(t, filepath.Join(dir, "scratch.tmp"), "s")
	if err := os.Mkdir(filepath.Join(dir, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "build", "output.bin"), "o")

	entries, err := scanLocal(dir, false, loadIgnore(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries["keep.txt"]; !ok {
		t.Fatalf("expected keep.txt, got %v", entries)
	}
	if _, ok := entries["scratch.tmp"]; ok {
		t.Fatalf("scratch.tmp should be excluded by *.tmp, got %v", entries)
	}
	if _, ok := entries["build/output.bin"]; ok {
		t.Fatalf("build/output.bin should be excluded by build/, got %v", entries)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
