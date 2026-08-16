package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEncryptDecryptFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/entries/5/encrypt":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["key_id"].(float64) != 1 {
				t.Fatalf("body = %v", body)
			}
			_, _ = w.Write([]byte(`{"file":{"id":6,"name":"a.txt.gpg"}}`))
		case "/api/v1/files/entries/6/decrypt":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["key_id"].(float64) != 1 {
				t.Fatalf("body = %v", body)
			}
			_, _ = w.Write([]byte(`{"file":{"id":5,"name":"a.txt"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := testClient(t, srv)
	f, err := c.EncryptFile(context.Background(), 5, 1, nil)
	if err != nil || f.ID != 6 {
		t.Fatalf("f=%+v err=%v", f, err)
	}
	f, err = c.DecryptFile(context.Background(), 6, 1, nil)
	if err != nil || f.ID != 5 {
		t.Fatalf("f=%+v err=%v", f, err)
	}
}

func TestEncryptFolder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/files/folders/3/encrypt" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"file":{"id":9,"name":"docs.tar.gz.gpg"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	f, err := c.EncryptFolder(context.Background(), 3, 1, []int64{2, 3})
	if err != nil || f.ID != 9 {
		t.Fatalf("f=%+v err=%v", f, err)
	}
}

func TestKeyring(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/crypto/keyring" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"keys":[{"id":1,"type":"pgp","label":"me","is_own":true,"has_private":true}],"recipients":[{"id":2,"type":"pgp","label":"friend"}]}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	keys, recipients, err := c.Keyring(context.Background())
	if err != nil || len(keys) != 1 || !keys[0].IsOwn || len(recipients) != 1 {
		t.Fatalf("keys=%+v recipients=%+v err=%v", keys, recipients, err)
	}
}
