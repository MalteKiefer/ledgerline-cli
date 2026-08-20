package webdavfs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/webdav"
)

// TestWebdavHandlerServesRealProtocol drives the filesystem through the actual
// webdav.Handler with real PROPFIND/GET/PUT/MKCOL/DELETE requests — the same
// verbs an OS mount client sends — rather than only calling the FileSystem
// methods directly. A mount that fails at the protocol level (a wrong Readdir
// contract, a handle that reports the wrong size) would pass the unit tests and
// still be unusable.
func TestWebdavHandlerServesRealProtocol(t *testing.T) {
	mux := dataMux(t)
	mux.HandleFunc("/api/v1/files/entries/11/raw", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	})
	var uploaded string
	mux.HandleFunc("/api/v1/files/entries", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		buf, _ := io.ReadAll(file)
		uploaded = string(buf)
		_, _ = w.Write([]byte(`{"file":{"id":14,"name":"put.txt","version":1}}`))
	})
	var madeFolder bool
	mux.HandleFunc("/api/v1/files/folders", func(w http.ResponseWriter, _ *http.Request) {
		madeFolder = true
		_, _ = w.Write([]byte(`{"folder":{"id":22,"name":"fresh","parent_id":null}}`))
	})
	var trashed bool
	mux.HandleFunc("/api/v1/files/entries/12", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			trashed = true
		}
		w.WriteHeader(http.StatusNoContent)
	})

	fs, _ := newFS(t, mux, false)
	dav := httptest.NewServer(&webdav.Handler{FileSystem: fs, LockSystem: webdav.NewMemLS()})
	t.Cleanup(dav.Close)

	do := func(method, path, body string, headers map[string]string) *http.Response {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(t.Context(), method, dav.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := dav.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	// PROPFIND on the root must list both children.
	resp := do("PROPFIND", "/", "", map[string]string{"Depth": "1"})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND / = %d, want 207:\n%s", resp.StatusCode, body)
	}
	listing := string(body)
	if !strings.Contains(listing, "docs") || !strings.Contains(listing, "root.bin") {
		t.Fatalf("root listing = %s", listing)
	}

	// GET must stream the body the server holds.
	resp = do(http.MethodGet, "/docs/note.txt", "", nil)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Fatalf("GET = %d %q", resp.StatusCode, body)
	}

	// PUT of a new file must reach the upload endpoint.
	resp = do(http.MethodPut, "/put.txt", "written through webdav", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT = %d", resp.StatusCode)
	}
	if uploaded != "written through webdav" {
		t.Fatalf("uploaded = %q", uploaded)
	}

	// MKCOL creates a folder.
	resp = do("MKCOL", "/fresh", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL = %d", resp.StatusCode)
	}
	if !madeFolder {
		t.Fatal("folder endpoint not called")
	}

	// DELETE trashes rather than erasing.
	resp = do(http.MethodDelete, "/root.bin", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE = %d", resp.StatusCode)
	}
	if !trashed {
		t.Fatal("delete endpoint not called")
	}
}

// TestWebdavHandlerReadOnlyRejectsWrites proves a read-only mount answers a
// write attempt at the protocol level instead of silently accepting it.
func TestWebdavHandlerReadOnlyRejectsWrites(t *testing.T) {
	fs, _ := newFS(t, dataMux(t), true)
	dav := httptest.NewServer(&webdav.Handler{FileSystem: fs, LockSystem: webdav.NewMemLS()})
	t.Cleanup(dav.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, dav.URL+"/nope.txt", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := dav.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("PUT on a read-only mount = %d, want an error status", resp.StatusCode)
	}
}
