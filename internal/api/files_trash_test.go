package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFilesTrash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/trash" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"files":[{"id":1,"name":"a.txt"}],"folders":[{"id":2,"name":"docs"}]}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	files, folders, err := c.FilesTrash(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || len(folders) != 1 {
		t.Fatalf("files=%+v folders=%+v", files, folders)
	}
}

func TestRestoreForceDeleteEmptyTrash(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/api/v1/files/entries/1/restore" {
			_, _ = w.Write([]byte(`{"file":{"id":1,"name":"a.txt"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	if _, err := c.RestoreFile(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := c.ForceDeleteFile(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := c.EmptyFilesTrash(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /api/v1/files/entries/1/restore",
		"DELETE /api/v1/files/entries/1/force",
		"POST /api/v1/files/entries/trash/empty",
	}
	if len(seen) != len(want) {
		t.Fatalf("seen = %v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("seen[%d] = %q, want %q", i, seen[i], want[i])
		}
	}
}
