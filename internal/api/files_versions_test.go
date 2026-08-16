package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFileVersions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/entries/5/versions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"versions":[{"id":1,"file_id":5,"size":10}]}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	versions, err := c.FileVersions(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Size != 10 {
		t.Fatalf("versions = %+v", versions)
	}
}

func TestDownloadFileVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/entries/5/versions/2/raw" || r.URL.Query().Get("download") != "1" {
			t.Fatalf("unexpected %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte("OLDBYTES"))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	var buf bytes.Buffer
	if err := c.DownloadFileVersion(context.Background(), 5, 2, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "OLDBYTES" {
		t.Fatalf("body = %q", buf.String())
	}
}

func TestRestoreFileVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/files/entries/5/versions/2/restore" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"file":{"id":5,"name":"a.txt"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	f, err := c.RestoreFileVersion(context.Background(), 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != 5 {
		t.Fatalf("file = %+v", f)
	}
}
