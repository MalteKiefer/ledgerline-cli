package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFilesFolders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/folders" || r.Method != "GET" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"folders":[{"id":1,"name":"docs"}]}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	folders, err := c.FilesFolders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Name != "docs" {
		t.Fatalf("folders = %+v", folders)
	}
}

func TestRenameFolder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/api/v1/files/folders/3" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "new-name" {
			t.Fatalf("body = %v", body)
		}
		_, _ = w.Write([]byte(`{"folder":{"id":3,"name":"new-name"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	f, err := c.RenameFolder(context.Background(), 3, "new-name")
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "new-name" {
		t.Fatalf("folder = %+v", f)
	}
}

func TestMoveFolderToRoot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/files/folders/3/move" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["parent_id"]; !ok || v != nil {
			t.Fatalf("expected parent_id: null, got %v (present=%v)", v, ok)
		}
		_, _ = w.Write([]byte(`{"folder":{"id":3,"name":"docs","parent_id":null}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	f, err := c.MoveFolder(context.Background(), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.ParentID != nil {
		t.Fatalf("folder = %+v", f)
	}
}

func TestDeleteRestoreForceDeleteFolder(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/files/folders/3/restore":
			_, _ = w.Write([]byte(`{"folder":{"id":3,"name":"docs"}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	c := testClient(t, srv)
	if err := c.DeleteFolder(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RestoreFolder(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if err := c.ForceDeleteFolder(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"DELETE /api/v1/files/folders/3",
		"POST /api/v1/files/folders/3/restore",
		"DELETE /api/v1/files/folders/3/force",
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
