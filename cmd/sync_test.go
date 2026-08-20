package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/MalteKiefer/ledgerline-cli/internal/syncconfig"
)

// syncEnv gives the test its own configuration directory and a signed-in
// session pointing at srv.
func syncEnv(t *testing.T, srv *httptest.Server) {
	t.Helper()
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	keyring.MockInit()

	login := NewRootCommand()
	var out bytes.Buffer
	login.SetOut(&out)
	login.SetErr(&out)
	login.SetArgs([]string{"auth", "pair", "--server", srv.URL, "--code", "pasted-code", "--device-name", "ci-runner"})
	if err := login.Execute(); err != nil {
		t.Fatalf("sign in: %v\n%s", err, out.String())
	}
}

// syncAPI serves just enough of the Files API for one pair to run: an empty
// remote tree plus an upload endpoint.
func syncAPI(t *testing.T, uploaded *[]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/pair", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	})
	mux.HandleFunc("/api/v1/auth/pair/collect", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"approved","token":"tok","user":{"id":1,"name":"Ada","email":"ada@example.com"}}`))
	})
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"user":{"id":1,"name":"Ada","email":"ada@example.com"},"usage":{"files":0,"gallery":0}}`))
	})
	mux.HandleFunc("/api/v1/files/data", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"folders": []any{}, "files": []any{}, "usage": map[string]any{}})
	})
	mux.HandleFunc("/api/v1/files/folders", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"folder":{"id":42,"name":"Docs"}}`))
	})
	mux.HandleFunc("/api/v1/files/entries", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		if _, hdr, err := r.FormFile("file"); err == nil {
			*uploaded = append(*uploaded, hdr.Filename)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"file":{"id":7,"name":"x"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// The package already has run/runErr helpers over the same root command; these
// tests use those.

func TestSyncAddListRemove(t *testing.T) {
	var uploaded []string
	srv := syncAPI(t, &uploaded)
	syncEnv(t, srv)

	dir := t.TempDir()
	out := run(t, "sync", "add", dir, "--remote", "Docs", "--interval", "30m")
	if !strings.Contains(out, "Added pair 1") {
		t.Fatalf("add output:\n%s", out)
	}

	out = run(t, "sync", "ls")
	if !strings.Contains(out, "Docs") || !strings.Contains(out, "30m") {
		t.Fatalf("ls output:\n%s", out)
	}

	// Pausing is not deleting: the pair stays in the list.
	run(t, "sync", "set", "1", "--disable")
	if out = run(t, "sync", "ls"); !strings.Contains(out, "off") {
		t.Fatalf("expected a paused pair:\n%s", out)
	}

	out = run(t, "sync", "rm", "1")
	if !strings.Contains(out, "No files were deleted") {
		t.Fatalf("rm output:\n%s", out)
	}
	if out = run(t, "sync", "ls"); !strings.Contains(out, "No folder pairs") {
		t.Fatalf("pair survived removal:\n%s", out)
	}
}

// TestSyncRunUploadsIntoTheConfiguredRemote is the end-to-end shape: a pair is
// configured once, and `sync run` uses it without being told again.
func TestSyncRunUploadsIntoTheConfiguredRemote(t *testing.T) {
	var uploaded []string
	srv := syncAPI(t, &uploaded)
	syncEnv(t, srv)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "sync", "add", dir, "--remote", "Docs")

	out := run(t, "sync", "run", "1")
	if !strings.Contains(out, "1 pushed") {
		t.Fatalf("run output:\n%s", out)
	}
	if len(uploaded) != 1 || uploaded[0] != "note.txt" {
		t.Fatalf("uploaded = %v", uploaded)
	}

	// The outcome is recorded, so a pair that starts failing is visible.
	p, err := syncconfig.Get("1")
	if err != nil {
		t.Fatal(err)
	}
	if p.LastRun.IsZero() || !strings.Contains(p.LastResult, "1 pushed") || p.LastFailure != "" {
		t.Fatalf("pair after run = %+v", p)
	}
}

func TestSyncRunAllSkipsDisabledPairs(t *testing.T) {
	var uploaded []string
	srv := syncAPI(t, &uploaded)
	syncEnv(t, srv)

	on, off := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(on, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(off, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "sync", "add", on, "--remote", "On")
	run(t, "sync", "add", off, "--remote", "Off", "--disabled")

	run(t, "sync", "run", "--all")
	if len(uploaded) != 1 || uploaded[0] != "a.txt" {
		t.Fatalf("uploaded = %v, want only the enabled pair", uploaded)
	}
}

func TestSyncRunNeedsATarget(t *testing.T) {
	var uploaded []string
	srv := syncAPI(t, &uploaded)
	syncEnv(t, srv)

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"sync", "run"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("err = %v", err)
	}
}

func TestSyncSetRejectsAnInvalidValue(t *testing.T) {
	var uploaded []string
	srv := syncAPI(t, &uploaded)
	syncEnv(t, srv)
	run(t, "sync", "add", t.TempDir())

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"sync", "set", "1", "--direction", "sideways"})
	if err := root.Execute(); err == nil {
		t.Fatal("an invalid direction was accepted")
	}
}
