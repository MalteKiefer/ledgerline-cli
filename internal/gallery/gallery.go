// Package gallery holds the thin local-side helpers for the gallery commands:
// selecting media files to upload and naming downloads. All server interaction
// goes through internal/api; there is no local crypto or derivation (the server
// computes thumbnails, EXIF and ML on the plaintext bytes).
package gallery

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// mediaExts is the set of file extensions the gallery accepts (images + videos),
// mirroring the server's upload contract. Lowercase, leading dot.
var mediaExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true,
	".heic": true, ".heif": true,
	".mp4": true, ".mov": true, ".m4v": true, ".webm": true, ".mkv": true, ".avi": true,
}

// IsMedia reports whether a file extension (with or without the leading dot,
// any case) is an uploadable gallery media type.
func IsMedia(ext string) bool {
	ext = strings.ToLower(ext)
	if ext != "" && ext[0] != '.' {
		ext = "." + ext
	}
	return mediaExts[ext]
}

// WalkMedia expands the given paths into a sorted, de-duplicated list of media
// files: a file path is kept when it is a media type; a directory is walked
// recursively for media files. Non-media files are skipped. Paths that do not
// exist surface as an error from the walk.
func WalkMedia(paths []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] && IsMedia(filepath.Ext(p)) {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			add(p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				add(path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// GuessName returns the base file name used as the upload name for a path.
func GuessName(path string) string { return filepath.Base(path) }
