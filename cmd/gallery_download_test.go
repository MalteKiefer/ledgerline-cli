package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGalleryDownloadAllGetsEditedVariantAndSkipsExistingFiles(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/gallery/data", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("limit"); got != "500" {
			t.Fatalf("limit = %q, want 500", got)
		}
		_, _ = w.Write([]byte(`{"photos":[{"id":11,"name":"new.jpg","size":3,"media_type":"image"},{"id":12,"name":"existing.jpg","size":3,"media_type":"image"}],"next_cursor":null}`))
	})
	downloads := 0
	mux.HandleFunc("/api/v1/gallery/11/download", func(w http.ResponseWriter, r *http.Request) {
		downloads++
		if got := r.URL.Query().Get("variant"); got != "edited" {
			t.Fatalf("variant = %q, want edited", got)
		}
		_, _ = w.Write([]byte("NEW"))
	})
	mux.HandleFunc("/api/v1/gallery/12/download", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("existing file must not be downloaded")
	})
	loggedInRoot(t, mux)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "existing.jpg"), []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := run(t, "gallery", "download", "--all", "--out", dir)

	got, err := os.ReadFile(filepath.Join(dir, "new.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW" || downloads != 1 {
		t.Fatalf("new.jpg = %q, downloads = %d", got, downloads)
	}
	existing, err := os.ReadFile(filepath.Join(dir, "existing.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(existing) != "OLD" {
		t.Fatalf("existing.jpg was overwritten: %q", existing)
	}
	for _, want := range []string{"downloaded new.jpg", "skip", "1 downloaded, 1 skipped, 0 failed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestGalleryDownloadAllContinuesAfterOneFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/gallery/data", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"photos":[{"id":21,"name":"bad.jpg","media_type":"image"},{"id":22,"name":"good.jpg","media_type":"image"}],"next_cursor":null}`))
	})
	mux.HandleFunc("/api/v1/gallery/21/download", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"broken"}`, http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/v1/gallery/22/download", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "GOOD")
	})
	loggedInRoot(t, mux)

	dir := t.TempDir()
	root := NewRootCommand()
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"gallery", "download", "--all", "--out", dir})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "1 photo(s) failed") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "bad.jpg")); !os.IsNotExist(statErr) {
		t.Fatalf("failed download left destination behind: %v", statErr)
	}
	if got, readErr := os.ReadFile(filepath.Join(dir, "good.jpg")); readErr != nil || string(got) != "GOOD" {
		t.Fatalf("good.jpg = %q, err = %v", got, readErr)
	}
}
