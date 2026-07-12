package files

import (
	"path"
	"strings"
)

// Matcher tests paths against a set of gitignore-style patterns:
//   - a trailing "/" makes a pattern match a directory (any path segment)
//   - a pattern with a "/" is matched against the full relative path
//   - otherwise the pattern is matched against each path segment (basename)
//
// Glob metacharacters (*, ?, [..]) are supported via path.Match.
type Matcher struct {
	dirNames  map[string]bool // "node_modules/" -> node_modules
	segGlobs  []string        // patterns matched per segment
	pathGlobs []string        // patterns matched against the whole path
}

// NewMatcher compiles ignore patterns.
func NewMatcher(patterns []string) *Matcher {
	m := &Matcher{dirNames: map[string]bool{}}
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		if strings.HasSuffix(p, "/") {
			m.dirNames[strings.ToLower(strings.Trim(p, "/"))] = true
			continue
		}
		if strings.Contains(p, "/") {
			m.pathGlobs = append(m.pathGlobs, strings.TrimPrefix(p, "/"))
			continue
		}
		m.segGlobs = append(m.segGlobs, p)
	}
	return m
}

// Match reports whether a relative slash path is ignored.
func (m *Matcher) Match(rel string) bool {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return false
	}
	segs := strings.Split(rel, "/")

	// Directory-name patterns: ignore anything under a matching segment.
	for _, s := range segs {
		if m.dirNames[strings.ToLower(s)] {
			return true
		}
	}
	// Per-segment globs (match basename or any segment).
	for _, g := range m.segGlobs {
		for _, s := range segs {
			if ok, _ := path.Match(g, s); ok {
				return true
			}
		}
	}
	// Whole-path globs.
	for _, g := range m.pathGlobs {
		if ok, _ := path.Match(g, rel); ok {
			return true
		}
		if strings.HasPrefix(rel, strings.TrimSuffix(g, "/")+"/") {
			return true
		}
	}
	return false
}
