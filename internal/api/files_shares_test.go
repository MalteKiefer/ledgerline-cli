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

func TestFilesSharesAndCreate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/files/rel-shares":
			_, _ = w.Write([]byte(`{"shares":[{"id":1,"token":"tok","kind":"file","name":"a.txt"}]}`))
		case r.Method == "POST" && r.URL.Path == "/api/v1/files/rel-shares":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["kind"] != "file" || body["file_id"].(float64) != 5 {
				t.Fatalf("body = %v", body)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"share":{"id":2,"token":"tok2","kind":"file","file_id":5,"version":0}}`))
		case r.Method == "PUT" && r.URL.Path == "/api/v1/files/rel-shares/2":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["version"].(float64) != 0 {
				t.Fatalf("body = %v", body)
			}
			_, _ = w.Write([]byte(`{"share":{"id":2,"token":"tok2","allow_download":false,"version":1}}`))
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/files/rel-shares/2":
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := testClient(t, srv)
	shares, err := c.FilesShares(context.Background())
	if err != nil || len(shares) != 1 || shares[0].Name != "a.txt" {
		t.Fatalf("shares=%+v err=%v", shares, err)
	}
	fileID := int64(5)
	share, err := c.CreateFileShare(context.Background(), CreateFileShareInput{Kind: "file", FileID: &fileID})
	if err != nil || share.ID != 2 {
		t.Fatalf("share=%+v err=%v", share, err)
	}
	allow := false
	share, err = c.UpdateFileShare(context.Background(), 2, UpdateFileShareInput{AllowDownload: &allow}, 0)
	if err != nil || share.AllowDownload {
		t.Fatalf("share=%+v err=%v", share, err)
	}
	if err := c.DeleteFileShare(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
}

func TestFolderShares(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/files/folder-shares":
			_, _ = w.Write([]byte(`{"shares":[{"id":1,"kind":"folder","file_folder_id":3,"members":[{"id":1,"user_id":9,"role":"viewer"}]}]}`))
		case r.Method == "POST" && r.URL.Path == "/api/v1/files/folder-shares":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["email"] != "a@b.com" || body["role"] != "editor" {
				t.Fatalf("body = %v", body)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"share":{"id":2,"kind":"folder","file_folder_id":3}}`))
		case r.Method == "PUT" && r.URL.Path == "/api/v1/files/folder-shares/2/members":
			_, _ = w.Write([]byte(`{"share":{"id":2,"kind":"folder"}}`))
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/files/folder-shares/2/members":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["user_id"].(float64) != 9 {
				t.Fatalf("body = %v", body)
			}
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/files/folder-shares/2":
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := testClient(t, srv)
	shares, err := c.FilesFolderShares(context.Background())
	if err != nil || len(shares) != 1 || shares[0].Members[0].Role != "viewer" {
		t.Fatalf("shares=%+v err=%v", shares, err)
	}
	folderID := int64(3)
	share, err := c.CreateFolderShare(context.Background(), "folder", &folderID, nil, "a@b.com", "editor")
	if err != nil || share.ID != 2 {
		t.Fatalf("share=%+v err=%v", share, err)
	}
	if _, err := c.UpdateFolderShareMember(context.Background(), 2, 9, "viewer"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveFolderShareMember(context.Background(), 2, 9); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteFolderShare(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
}

func TestUploadLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/files/upload-links":
			_, _ = w.Write([]byte(`{"links":[{"id":1,"token":"tok"}]}`))
		case r.Method == "POST" && r.URL.Path == "/api/v1/files/upload-links":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["file_folder_id"].(float64) != 3 || body["expires_at"] != "2027-01-01T00:00:00Z" {
				t.Fatalf("body = %v", body)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"link":{"id":2,"token":"tok2"}}`))
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/files/upload-links/2":
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := testClient(t, srv)
	links, err := c.FilesUploadLinks(context.Background())
	if err != nil || len(links) != 1 {
		t.Fatalf("links=%+v err=%v", links, err)
	}
	link, err := c.CreateUploadLink(context.Background(), 3, nil, "2027-01-01T00:00:00Z", nil)
	if err != nil || link.ID != 2 {
		t.Fatalf("link=%+v err=%v", link, err)
	}
	if err := c.DeleteUploadLink(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
}

func TestSharedWithMeFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/shared-with-me":
			_, _ = w.Write([]byte(`{"shares":[{"id":1,"kind":"folder","folder_name":"shared-docs","role":"editor"}]}`))
		case r.Method == "GET" && r.URL.Path == "/api/v1/shared-with-me/1":
			_, _ = w.Write([]byte(`{"share_id":1,"role":"editor","kind":"folder","root_id":9,"folders":[],"files":[{"id":5,"name":"a.txt"}]}`))
		case r.Method == "GET" && r.URL.Path == "/api/v1/shared-with-me/1/files/5/raw":
			_, _ = w.Write([]byte("BYTES"))
		case r.Method == "POST" && r.URL.Path == "/api/v1/shared-with-me/1/upload":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"file":{"id":6,"name":"b.txt"}}`))
		case r.Method == "PUT" && r.URL.Path == "/api/v1/shared-with-me/1/files/5":
			_, _ = w.Write([]byte(`{"file":{"id":5,"name":"renamed.txt"}}`))
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/shared-with-me/1/files/5":
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := testClient(t, srv)
	items, err := c.SharedWithMe(context.Background())
	if err != nil || len(items) != 1 || items[0].Role != "editor" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	result, err := c.SharedWithMeBrowse(context.Background(), 1)
	if err != nil || result.RootID == nil || *result.RootID != 9 || len(result.Files) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var buf bytes.Buffer
	if err := c.SharedWithMeDownload(context.Background(), 1, 5, &buf); err != nil || buf.String() != "BYTES" {
		t.Fatalf("buf=%q err=%v", buf.String(), err)
	}
	open := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("X")), nil }
	f, err := c.SharedWithMeUpload(context.Background(), 1, "b.txt", nil, open)
	if err != nil || f.ID != 6 {
		t.Fatalf("f=%+v err=%v", f, err)
	}
	f, err = c.SharedWithMeRename(context.Background(), 1, 5, "renamed.txt")
	if err != nil || f.Name != "renamed.txt" {
		t.Fatalf("f=%+v err=%v", f, err)
	}
	if err := c.SharedWithMeDelete(context.Background(), 1, 5); err != nil {
		t.Fatal(err)
	}
}
