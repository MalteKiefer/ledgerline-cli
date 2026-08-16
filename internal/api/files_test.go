package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFilesDataDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/data" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"folders":[{"id":1,"name":"docs"}],"files":[{"id":2,"name":"a.txt","size":5}],"usage":{"used":5,"quota":null}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	folders, files, usage, err := c.FilesData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Name != "docs" {
		t.Fatalf("folders = %+v", folders)
	}
	if len(files) != 1 || files[0].ID != 2 {
		t.Fatalf("files = %+v", files)
	}
	if usage.Used != 5 || usage.Quota != nil {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestUploadFileSendsMultipartWithFolder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/entries" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("file_folder_id") != "3" {
			t.Fatalf("file_folder_id = %q", r.FormValue("file_folder_id"))
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if hdr.Filename != "a.txt" {
			t.Fatalf("filename = %q", hdr.Filename)
		}
		got, _ := io.ReadAll(f)
		if string(got) != "HELLO" {
			t.Fatalf("body = %q", got)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"file":{"id":9,"name":"a.txt"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	folder := int64(3)
	open := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("HELLO")), nil }
	f, err := c.UploadFile(context.Background(), "a.txt", &folder, open)
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != 9 {
		t.Fatalf("file = %+v", f)
	}
}

func TestDownloadFileStreamsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/entries/4/raw" || r.URL.Query().Get("download") != "1" {
			t.Fatalf("unexpected %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte("FILEBYTES"))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	var buf bytes.Buffer
	if err := c.DownloadFile(context.Background(), 4, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "FILEBYTES" {
		t.Fatalf("body = %q", buf.String())
	}
}

func TestCreateFolderSendsParent(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/folders" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"folder":{"id":11,"name":"sub","parent_id":2}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	parent := int64(2)
	folder, err := c.CreateFolder(context.Background(), "sub", &parent)
	if err != nil {
		t.Fatal(err)
	}
	if folder.ID != 11 || got["name"] != "sub" || got["parent_id"].(float64) != 2 {
		t.Fatalf("folder=%+v body=%v", folder, got)
	}
}

func TestUpdateFileSendsVersionAndFields(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/api/v1/files/entries/5" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"file":{"id":5,"name":"renamed.txt","version":2}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	name := "renamed.txt"
	var folder *int64 // move to root: JSON null
	f, err := c.UpdateFile(context.Background(), 5, FileUpdate{Name: &name, FolderID: &folder}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "renamed.txt" || f.Version != 2 {
		t.Fatalf("file = %+v", f)
	}
	if got["version"].(float64) != 1 || got["name"] != "renamed.txt" {
		t.Fatalf("body = %v", got)
	}
	if v, ok := got["file_folder_id"]; !ok || v != nil {
		t.Fatalf("expected file_folder_id: null in body, got %v (present=%v)", v, ok)
	}
}

func TestUpdateFileVersionConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"version_conflict","version":7}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	name := "x"
	_, err := c.UpdateFile(context.Background(), 5, FileUpdate{Name: &name}, 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "version_conflict" || apiErr.Version != 7 {
		t.Fatalf("err = %v", err)
	}
}

func TestToggleFileFavorite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/files/entries/5/toggle" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["field"] != "favorite" || body["value"] != true {
			t.Fatalf("body = %v", body)
		}
		_, _ = w.Write([]byte(`{"file":{"id":5,"favorite":true}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	f, err := c.ToggleFileFavorite(context.Background(), 5, true)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Favorite {
		t.Fatalf("file = %+v", f)
	}
}

func TestCopyFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/files/entries/5/copy" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["file_folder_id"].(float64) != 9 {
			t.Fatalf("body = %v", body)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"file":{"id":6,"name":"a (copy).txt"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	folder := int64(9)
	f, err := c.CopyFile(context.Background(), 5, &folder)
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != 6 {
		t.Fatalf("file = %+v", f)
	}
}

func TestDeleteFile(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/files/entries/7" {
			hit = true
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	if err := c.DeleteFile(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("DELETE /files/entries/7 not seen")
	}
}
