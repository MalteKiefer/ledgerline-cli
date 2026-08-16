package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFilesActivityAndFileActivityFor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/activity":
			_, _ = w.Write([]byte(`{"activity":[{"id":1,"action":"upload","created_at":"2026-08-16T00:00:00Z"}]}`))
		case "/api/v1/files/entries/5/activity":
			_, _ = w.Write([]byte(`{"activity":[{"id":2,"action":"rename","created_at":"2026-08-16T00:00:00Z"}]}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := testClient(t, srv)
	all, err := c.FilesActivity(context.Background())
	if err != nil || len(all) != 1 || all[0].Action != "upload" {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	one, err := c.FileActivityFor(context.Background(), 5)
	if err != nil || len(one) != 1 || one[0].Action != "rename" {
		t.Fatalf("one=%+v err=%v", one, err)
	}
}

func TestFileShow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/entries/5/show" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"file":{"id":5,"name":"a.txt"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	f, err := c.FileShow(context.Background(), 5)
	if err != nil || f.ID != 5 {
		t.Fatalf("f=%+v err=%v", f, err)
	}
}

func TestGetFileInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/entries/5/info" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"sha256":"abc","version":2,"versions":3,"path":"/docs/a.txt","metadata":{"kind":"image","fields":{"width":"100"}}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	info, err := c.GetFileInfo(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if info.Sha256 == nil || *info.Sha256 != "abc" || info.Versions != 3 || info.Metadata == nil || info.Metadata.Fields["width"] != "100" {
		t.Fatalf("info = %+v", info)
	}
}

func TestFilesSearchEscapesQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/search" || r.URL.Query().Get("q") != "a b&c" {
			t.Fatalf("unexpected %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"files":[{"id":1,"name":"a.txt"}]}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	files, err := c.FilesSearch(context.Background(), "a b&c")
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
}

func TestGetFilesStats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/files/stats" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"used":100,"by_type":{"image/png":40},"duplicates":[[{"id":1,"name":"a.txt"},{"id":2,"name":"b.txt"}]]}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	stats, err := c.GetFilesStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Used != 100 || stats.ByType["image/png"] != 40 || len(stats.Duplicates) != 1 || len(stats.Duplicates[0]) != 2 {
		t.Fatalf("stats = %+v", stats)
	}
}
