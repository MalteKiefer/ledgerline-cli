package files

import (
	"mime"
	"path/filepath"
	"sort"
	"strings"
)

// isJunkName reports OS/VCS metadata files that must never be synced.
func isJunkName(name string) bool {
	switch name {
	case ".DS_Store", "Thumbs.db", "desktop.ini", ".localized", ".nomedia":
		return true
	}
	return false
}

// mimeTypeByExt guesses a MIME type from a path's extension.
func mimeTypeByExt(rel string) string {
	if t := mime.TypeByExtension(filepath.Ext(rel)); t != "" {
		if i := strings.IndexByte(t, ';'); i >= 0 {
			t = t[:i]
		}
		return t
	}
	return "application/octet-stream"
}

// sortStrings sorts in place (small wrapper for readability).
func sortStrings(s []string) { sort.Strings(s) }
