package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/MalteKiefer/ledgerline-cli/internal/session"
)

// TestAuthedClientRemoteWipe verifies the kill switch: when /me reports a pending
// wipe, authedClient erases all local state and stops.
func TestAuthedClientRemoteWipe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", dir)
	keyring.MockInit()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user":{"id":1,"name":"T"},"usage":{"files":0,"gallery":0},"wipe":true}`))
	}))
	defer srv.Close()

	if _, err := session.Save(session.Session{ServerURL: srv.URL, UserID: 1, Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	// Leave a stray sync-state file to prove the whole dir is wiped.
	if err := os.MkdirAll(filepath.Join(dir, "sync-state"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "sync-state", "x.json"), []byte("{}"), 0o600)

	_, err := authedClient(context.Background())
	if err == nil || !strings.Contains(err.Error(), "wiped") {
		t.Fatalf("expected a remote-wipe error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("config.json survived the remote wipe")
	}
	if _, err := os.Stat(filepath.Join(dir, "sync-state")); !os.IsNotExist(err) {
		t.Fatal("sync-state survived the remote wipe")
	}
}
