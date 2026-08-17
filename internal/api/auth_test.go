package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAvatarStreamsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/avatar" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("PNGBYTES"))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	var buf bytes.Buffer
	if err := c.Avatar(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "PNGBYTES" {
		t.Fatalf("body = %q", buf.String())
	}
}

func TestWebDavStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/account/webdav" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"enabled":true,"username":"max@example.com","url":"https://ledger.example.com/dav/"}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	got, err := c.WebDavStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := WebDavAccess{Enabled: true, Username: "max@example.com", URL: "https://ledger.example.com/dav/"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSetWebDavPasswordSendsBody(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/api/v1/account/webdav" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"enabled":true,"username":"max@example.com","url":"https://ledger.example.com/dav/"}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	access, err := c.SetWebDavPassword(context.Background(), "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	if !access.Enabled {
		t.Fatalf("expected Enabled=true, got %+v", access)
	}
	if gotBody["webdav_password"] != "a-long-enough-password" {
		t.Fatalf("unexpected request body: %+v", gotBody)
	}
}

func TestClearWebDavPassword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" || r.URL.Path != "/api/v1/account/webdav" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"enabled":false,"username":"max@example.com","url":"https://ledger.example.com/dav/"}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	access, err := c.ClearWebDavPassword(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if access.Enabled {
		t.Fatalf("expected Enabled=false after clear, got %+v", access)
	}
}

func TestAvatarNotFoundIsAPIError404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no avatar"}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	var buf bytes.Buffer
	err := c.Avatar(context.Background(), &buf)
	if Status(err) != 404 {
		t.Fatalf("Status(err) = %d, want 404 (err=%v)", Status(err), err)
	}
}
