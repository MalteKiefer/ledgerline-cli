package files

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ignoreFileName is the marker file read from the root of a synced directory.
// It is never itself synced (in either direction), regardless of --hidden.
const ignoreFileName = ".ledgerline-ignore"

// ignoreMatcher excludes paths from a sync pass per the synced directory's
// .ledgerline-ignore file: one glob pattern per line, "#" comments and blank
// lines skipped. This is a deliberately small subset of gitignore syntax —
// no negation (!pattern), no "**", a leading "/" anchors to the sync root
// (otherwise the pattern also matches a file/directory's base name anywhere
// in the tree), and a trailing "/" restricts a pattern to directories. A nil
// *ignoreMatcher (or one loaded from a missing file) matches nothing.
type ignoreMatcher struct {
	patterns []string
}

// loadIgnore reads <root>/.ledgerline-ignore. A missing file is not an error —
// the ignore list is optional — and yields a matcher with no patterns.
func loadIgnore(root string) *ignoreMatcher {
	data, err := os.ReadFile(filepath.Join(root, ignoreFileName))
	if err != nil {
		return &ignoreMatcher{}
	}
	m := &ignoreMatcher{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m.patterns = append(m.patterns, line)
	}
	return m
}

// Match reports whether relPath — slash-separated, relative to the synced
// root, with a trailing "/" for a directory — should be excluded.
func (m *ignoreMatcher) Match(relPath string) bool {
	if m == nil {
		return false
	}
	isDir := strings.HasSuffix(relPath, "/")
	trimmed := strings.TrimSuffix(relPath, "/")
	base := path.Base(trimmed)

	for _, pat := range m.patterns {
		dirOnly := strings.HasSuffix(pat, "/")
		p := strings.TrimSuffix(pat, "/")
		if dirOnly && !isDir {
			continue
		}
		anchored := strings.HasPrefix(p, "/")
		p = strings.TrimPrefix(p, "/")
		if ok, _ := path.Match(p, trimmed); ok {
			return true
		}
		if !anchored {
			if ok, _ := path.Match(p, base); ok {
				return true
			}
		}
	}
	return false
}
