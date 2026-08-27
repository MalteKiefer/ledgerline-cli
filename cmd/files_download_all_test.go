package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesDownloadAllPreservesFoldersAndSkipsExistingFiles(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/files/data", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"folders":[{"id":1,"name":"Documents","parent_id":null},{"id":2,"name":"Reports","parent_id":1}],"files":[{"id":31,"name":"new.txt","file_folder_id":2},{"id":32,"name":"existing.txt","file_folder_id":1}],"usage":{"used":6}}`))
	})
	mux.HandleFunc("/api/v1/files/entries/31/raw", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("download"); got != "1" {
			t.Fatalf("download = %q, want 1", got)
		}
		_, _ = fmt.Fprint(w, "NEW")
	})
	mux.HandleFunc("/api/v1/files/entries/32/raw", func(http.ResponseWriter, *http.Request) {
		t.Fatal("existing file must not be downloaded")
	})
	loggedInRoot(t, mux)

	dir := t.TempDir()
	existingPath := filepath.Join(dir, "Documents", "existing.txt")
	if err := os.MkdirAll(filepath.Dir(existingPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existingPath, []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := run(t, "files", "download", "--all", "--out", dir)

	newPath := filepath.Join(dir, "Documents", "Reports", "new.txt")
	if got, err := os.ReadFile(newPath); err != nil || string(got) != "NEW" {
		t.Fatalf("new file = %q, err = %v", got, err)
	}
	if got, err := os.ReadFile(existingPath); err != nil || string(got) != "OLD" {
		t.Fatalf("existing file = %q, err = %v", got, err)
	}
	for _, want := range []string{"downloaded Documents/Reports/new.txt", "skip", "1 downloaded, 1 skipped, 0 failed"} {
		if !strings.Contains(filepath.ToSlash(out), want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestFilesDownloadAllContinuesAndLeavesNoPartialDestination(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/files/data", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"folders":[],"files":[{"id":41,"name":"bad.txt"},{"id":42,"name":"good.txt"}],"usage":{"used":4}}`))
	})
	mux.HandleFunc("/api/v1/files/entries/41/raw", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"broken"}`, http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/v1/files/entries/42/raw", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "GOOD")
	})
	loggedInRoot(t, mux)

	dir := t.TempDir()
	root := NewRootCommand()
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"files", "download", "--all", "--out", dir})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "1 file(s) failed") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "bad.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("failed download left destination behind: %v", statErr)
	}
	if got, readErr := os.ReadFile(filepath.Join(dir, "good.txt")); readErr != nil || string(got) != "GOOD" {
		t.Fatalf("good.txt = %q, err = %v", got, readErr)
	}
}

func TestSafeRemoteNameStripsPathComponents(t *testing.T) {
	for input, want := range map[string]string{
		"../../secret.txt": "secret.txt",
		`..\..\secret.txt`: "secret.txt",
		"normal.txt":       "normal.txt",
	} {
		if got := safeRemoteName(input, "fallback"); got != want {
			t.Fatalf("safeRemoteName(%q) = %q, want %q", input, got, want)
		}
	}
}
