package webdavfs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

// tree is the canned GET /files/data payload the tests run against: one folder
// "docs" holding "note.txt", plus "root.bin" at the root.
const tree = `{
  "folders": [{"id": 7, "parent_id": null, "name": "docs", "version": 1}],
  "files": [
    {"id": 11, "file_folder_id": 7, "name": "note.txt", "size": 5, "version": 3},
    {"id": 12, "file_folder_id": null, "name": "root.bin", "size": 2, "version": 1}
  ],
  "usage": {"used": 7, "quota": null}
}`

// newFS wires a filesystem against a test server built from mux.
func newFS(t *testing.T, mux *http.ServeMux, readOnly bool) (*FS, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client, err := api.New(srv.URL, api.WithToken("tok"))
	if err != nil {
		t.Fatal(err)
	}
	return New(client, readOnly), srv
}

func dataMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/files/data", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tree))
	})
	return mux
}

func TestStatResolvesFoldersAndFiles(t *testing.T) {
	fs, _ := newFS(t, dataMux(t), false)
	ctx := context.Background()

	root, err := fs.Stat(ctx, "/")
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if !root.IsDir() {
		t.Fatal("root is not a directory")
	}

	dir, err := fs.Stat(ctx, "/docs")
	if err != nil || !dir.IsDir() {
		t.Fatalf("stat /docs = %v, %v", dir, err)
	}

	file, err := fs.Stat(ctx, "/docs/note.txt")
	if err != nil {
		t.Fatalf("stat nested file: %v", err)
	}
	if file.IsDir() || file.Size() != 5 || file.Name() != "note.txt" {
		t.Fatalf("file info = %+v", file)
	}

	if _, err := fs.Stat(ctx, "/nope"); !os.IsNotExist(err) {
		t.Fatalf("stat missing = %v, want not-exist", err)
	}
}

func TestReaddirListsChildren(t *testing.T) {
	fs, _ := newFS(t, dataMux(t), false)
	ctx := context.Background()

	root, err := fs.OpenFile(ctx, "/", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	infos, err := root.Readdir(0)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, i := range infos {
		names[i.Name()] = true
	}
	if !names["docs"] || !names["root.bin"] || len(infos) != 2 {
		t.Fatalf("root children = %v", names)
	}

	docs, err := fs.OpenFile(ctx, "/docs", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer docs.Close()
	nested, err := docs.Readdir(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(nested) != 1 || nested[0].Name() != "note.txt" {
		t.Fatalf("docs children = %+v", nested)
	}
}

func TestReadDownloadsBodyOnce(t *testing.T) {
	mux := dataMux(t)
	var downloads int
	mux.HandleFunc("/api/v1/files/entries/11/raw", func(w http.ResponseWriter, _ *http.Request) {
		downloads++
		_, _ = w.Write([]byte("hello"))
	})
	fs, _ := newFS(t, mux, false)

	f, err := fs.OpenFile(context.Background(), "/docs/note.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello" {
		t.Fatalf("body = %q", body)
	}
	// A seek-then-read must not fetch the body a second time.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(f); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 {
		t.Fatalf("downloads = %d, want 1", downloads)
	}
}

func TestWriteUploadsOnClose(t *testing.T) {
	mux := dataMux(t)
	var uploadBody, uploadName string
	mux.HandleFunc("/api/v1/files/entries", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		buf, _ := io.ReadAll(file)
		uploadBody, uploadName = string(buf), header.Filename
		_, _ = w.Write([]byte(`{"file":{"id":13,"name":"new.txt","version":1}}`))
	})
	fs, _ := newFS(t, mux, false)

	f, err := fs.OpenFile(context.Background(), "/new.txt", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("fresh bytes")); err != nil {
		t.Fatal(err)
	}
	// Nothing is uploaded until the handle closes.
	if uploadBody != "" {
		t.Fatal("uploaded before Close")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if uploadBody != "fresh bytes" || uploadName != "new.txt" {
		t.Fatalf("upload = %q as %q", uploadBody, uploadName)
	}
}

func TestWriteToExistingFileReplacesContent(t *testing.T) {
	mux := dataMux(t)
	var hit bool
	mux.HandleFunc("/api/v1/files/entries/11/content", func(w http.ResponseWriter, r *http.Request) {
		hit = true
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"file":{"id":11,"name":"note.txt","version":4}}`))
	})
	fs, _ := newFS(t, mux, false)

	f, err := fs.OpenFile(context.Background(), "/docs/note.txt", os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("replaced")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("replace-content endpoint not called")
	}
}

func TestTempFileIsRemovedAfterClose(t *testing.T) {
	mux := dataMux(t)
	mux.HandleFunc("/api/v1/files/entries/11/raw", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	})
	fs, _ := newFS(t, mux, false)

	f, err := fs.OpenFile(context.Background(), "/docs/note.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	handle, ok := f.(*fileHandle)
	if !ok {
		t.Fatalf("handle type = %T", f)
	}
	tempPath := handle.temp.Name()
	if _, err := io.ReadAll(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// No plaintext body may survive the handle.
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("temp file still present: %v", err)
	}
}

func TestReadOnlyRefusesEveryMutation(t *testing.T) {
	fs, _ := newFS(t, dataMux(t), true)
	ctx := context.Background()

	if err := fs.Mkdir(ctx, "/new", 0o755); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("mkdir = %v", err)
	}
	if err := fs.RemoveAll(ctx, "/docs"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("removeall = %v", err)
	}
	if err := fs.Rename(ctx, "/root.bin", "/other.bin"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("rename = %v", err)
	}
	if _, err := fs.OpenFile(ctx, "/x", os.O_CREATE|os.O_WRONLY, 0o644); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("create = %v", err)
	}
	// Reads still work.
	if _, err := fs.Stat(ctx, "/docs"); err != nil {
		t.Fatalf("stat under read-only: %v", err)
	}
}

func TestMkdirUsesParentFolder(t *testing.T) {
	mux := dataMux(t)
	var body map[string]any
	mux.HandleFunc("/api/v1/files/folders", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"folder":{"id":21,"name":"sub","parent_id":7}}`))
	})
	fs, _ := newFS(t, mux, false)

	if err := fs.Mkdir(context.Background(), "/docs/sub", 0o755); err != nil {
		t.Fatal(err)
	}
	if body["name"] != "sub" {
		t.Fatalf("body = %v", body)
	}
	if parent, ok := body["parent_id"].(float64); !ok || int64(parent) != 7 {
		t.Fatalf("parent_id = %v", body["parent_id"])
	}
	// An existing folder must not be created twice.
	if err := fs.Mkdir(context.Background(), "/docs", 0o755); !os.IsExist(err) {
		t.Fatalf("mkdir existing = %v, want exist", err)
	}
	// A missing parent is a 404 for the client, not a silent root create.
	if err := fs.Mkdir(context.Background(), "/ghost/sub", 0o755); !os.IsNotExist(err) {
		t.Fatalf("mkdir orphan = %v, want not-exist", err)
	}
}

func TestRenameMovesAndRenames(t *testing.T) {
	mux := dataMux(t)
	var put string
	mux.HandleFunc("/api/v1/files/entries/11", func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		put = string(buf)
		_, _ = w.Write([]byte(`{"file":{"id":11,"name":"moved.txt","version":4}}`))
	})
	fs, _ := newFS(t, mux, false)

	if err := fs.Rename(context.Background(), "/docs/note.txt", "/moved.txt"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(put, `"name":"moved.txt"`) {
		t.Fatalf("PUT body = %q", put)
	}
	// Moving to the root must send an explicit null folder, not omit the field.
	if !strings.Contains(put, `"file_folder_id":null`) {
		t.Fatalf("PUT body = %q", put)
	}
	// The optimistic version of the known row is sent along.
	if !strings.Contains(put, `"version":3`) {
		t.Fatalf("PUT body = %q", put)
	}
}

func TestRemoveAllTrashesFileAndFolder(t *testing.T) {
	mux := dataMux(t)
	var deletedFile, deletedFolder bool
	mux.HandleFunc("/api/v1/files/entries/12", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletedFile = true
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/files/folders/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletedFolder = true
		}
		w.WriteHeader(http.StatusNoContent)
	})
	fs, _ := newFS(t, mux, false)
	ctx := context.Background()

	if err := fs.RemoveAll(ctx, "/root.bin"); err != nil {
		t.Fatal(err)
	}
	if err := fs.RemoveAll(ctx, "/docs"); err != nil {
		t.Fatal(err)
	}
	if !deletedFile || !deletedFolder {
		t.Fatalf("deleted file=%t folder=%t", deletedFile, deletedFolder)
	}
	// The mount root itself is never deletable.
	if err := fs.RemoveAll(ctx, "/"); err == nil {
		t.Fatal("deleting the root succeeded")
	}
}

func TestTrashedRowsAreNotMounted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/files/data", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
		  "folders": [{"id": 7, "parent_id": null, "name": "docs", "version": 1, "deleted_at": "2026-08-01T00:00:00Z"}],
		  "files": [{"id": 12, "file_folder_id": null, "name": "gone.bin", "size": 2, "version": 1, "deleted_at": "2026-08-01T00:00:00Z"}],
		  "usage": {"used": 0, "quota": null}
		}`))
	})
	fs, _ := newFS(t, mux, false)
	ctx := context.Background()

	if _, err := fs.Stat(ctx, "/docs"); !os.IsNotExist(err) {
		t.Fatalf("trashed folder visible: %v", err)
	}
	if _, err := fs.Stat(ctx, "/gone.bin"); !os.IsNotExist(err) {
		t.Fatalf("trashed file visible: %v", err)
	}
}

func TestCleanNormalisesPaths(t *testing.T) {
	cases := map[string]string{
		"":              "/",
		"/":             "/",
		"docs":          "/docs",
		"/docs/":        "/docs",
		"/docs/../docs": "/docs",
		`\docs\a.txt`:   "/docs/a.txt",
	}
	for in, want := range cases {
		if got := clean(in); got != want {
			t.Fatalf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}
