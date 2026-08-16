package gallery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsMedia(t *testing.T) {
	cases := map[string]bool{
		"jpg": true, ".JPG": true, ".png": true, ".heic": true, ".mp4": true, ".mov": true,
		".txt": false, "": false, ".pdf": false,
	}
	for ext, want := range cases {
		if got := IsMedia(ext); got != want {
			t.Errorf("IsMedia(%q) = %v, want %v", ext, got, want)
		}
	}
}

func TestWalkMediaFiltersAndRecurses(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	aJpg := write("a.jpg")
	write("b.txt")
	cPng := write("sub/c.png")

	got, err := WalkMedia([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{aJpg, cPng}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("WalkMedia = %v, want %v", got, want)
	}
}
