package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListPhotosDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/gallery/data" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"photos":[{"id":7,"name":"a.jpg","size":123,"media_type":"image"}]}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	photos, err := c.ListPhotos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 1 || photos[0].ID != 7 || photos[0].Name != "a.jpg" {
		t.Fatalf("photos = %+v", photos)
	}
}

func TestUploadPhotoSendsMultipartAndReportsDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		dup    bool
	}{
		{"created", 201, `{"photo":{"id":1,"name":"a.jpg"}}`, false},
		{"duplicate", 200, `{"photo":{"id":1,"name":"a.jpg"},"duplicate":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/gallery" {
					t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
				}
				if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data") {
					t.Fatalf("content-type = %q", ct)
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Fatal(err)
				}
				f, hdr, err := r.FormFile("file")
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if hdr.Filename != "a.jpg" {
					t.Fatalf("filename = %q", hdr.Filename)
				}
				got, _ := io.ReadAll(f)
				if string(got) != "PHOTOBYTES" {
					t.Fatalf("body = %q", got)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := testClient(t, srv)
			open := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("PHOTOBYTES")), nil }
			photo, dup, err := c.UploadPhoto(context.Background(), "a.jpg", open)
			if err != nil {
				t.Fatal(err)
			}
			if photo.ID != 1 || dup != tc.dup {
				t.Fatalf("photo=%+v dup=%v want dup=%v", photo, dup, tc.dup)
			}
		})
	}
}

func TestDownloadPhotoStreamsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/gallery/9/download" || r.URL.Query().Get("variant") != "original" {
			t.Fatalf("unexpected %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte("RAWBYTES"))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	var buf bytes.Buffer
	if err := c.DownloadPhoto(context.Background(), 9, "original", &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "RAWBYTES" {
		t.Fatalf("body = %q", buf.String())
	}
}

func TestDeletePhoto(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/gallery/5" {
			hit = true
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	if err := c.DeletePhoto(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("DELETE /gallery/5 not seen")
	}
}

func TestBulkDeletePhotos(t *testing.T) {
	var got map[string][]int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/gallery/bulk-destroy" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	if err := c.BulkDeletePhotos(context.Background(), []int64{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if len(got["ids"]) != 3 {
		t.Fatalf("ids = %v", got["ids"])
	}
}
