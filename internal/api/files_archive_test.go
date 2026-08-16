package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFilesZipStreamsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/files/zip" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		ids := body["ids"].([]any)
		if len(ids) != 2 {
			t.Fatalf("body = %v", body)
		}
		_, _ = w.Write([]byte("ZIPBYTES"))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	var buf bytes.Buffer
	if err := c.FilesZip(context.Background(), []int64{1, 2}, nil, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "ZIPBYTES" {
		t.Fatalf("body = %q", buf.String())
	}
}

func TestCreateArchive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/files/archive" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["format"] != "zip" || body["folder_id"].(float64) != 3 {
			t.Fatalf("body = %v", body)
		}
		_, _ = w.Write([]byte(`{"file":{"id":9,"name":"archive.zip"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	folder := int64(3)
	f, err := c.CreateArchive(context.Background(), CreateArchiveInput{FolderID: &folder, Format: "zip"})
	if err != nil || f.ID != 9 {
		t.Fatalf("f=%+v err=%v", f, err)
	}
}

func TestExtractArchive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/files/entries/5/extract" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"folder":{"id":11,"name":"archive"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	folder, err := c.ExtractArchive(context.Background(), 5, nil, nil, nil)
	if err != nil || folder == nil || folder.ID != 11 {
		t.Fatalf("folder=%+v err=%v", folder, err)
	}
}

func TestExtractArchiveNilFolderWhenNotIntoNewFolder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"folder":null}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	into := false
	folder, err := c.ExtractArchive(context.Background(), 5, nil, nil, &into)
	if err != nil || folder != nil {
		t.Fatalf("folder=%+v err=%v", folder, err)
	}
}
