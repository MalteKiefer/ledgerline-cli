// Package version exposes build-time metadata about the running binary and a
// helper to compare it against the latest published GitHub release.
//
// The three exported variables are populated at build time via -ldflags (see
// the Makefile). When the binary is built with `go run` or `go build` without
// those flags they fall back to sensible development defaults.
package version

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// Build metadata, injected at link time with -ldflags "-X ...".
var (
	// Version is the semantic version of this build (e.g. "1.2.3"). "dev" for
	// un-stamped local builds.
	Version = "dev"
	// Commit is the short git commit hash the binary was built from.
	Commit = "none"
	// BuildDate is the RFC 3339 UTC timestamp of the build.
	BuildDate = "unknown"
)

// Repository is the canonical source repository for this project.
const Repository = "https://github.com/MalteKiefer/ledgerline-cli"

// releasesAPI is the GitHub REST endpoint for the latest published release.
const releasesAPI = "https://api.github.com/repos/MalteKiefer/ledgerline-cli/releases/latest"

// Info is a snapshot of the running binary's build metadata.
type Info struct {
	Version   string
	Commit    string
	BuildDate string
	GoVersion string
	Platform  string
}

// Current returns the metadata for the running binary.
func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// String renders the version alone, e.g. "ledgerline-cli 1.2.3".
func (i Info) String() string {
	return fmt.Sprintf("ledgerline-cli %s", i.Version)
}

// Release describes the latest release as reported by GitHub.
type Release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// LatestRelease fetches the most recent published release from GitHub. It never
// sends credentials and is safe to call unauthenticated. The context bounds the
// network call so `status` stays responsive when offline.
func LatestRelease(ctx context.Context, client *http.Client) (Release, error) {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ledgerline-cli/"+Version)

	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("github returned status %d", resp.StatusCode)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, err
	}
	if rel.TagName == "" {
		return Release{}, fmt.Errorf("no release tag reported")
	}
	return rel, nil
}

// UpToDate reports whether the running Version is at least as new as latest.
// Both values are compared as semantic versions with an optional leading "v".
// A "dev" build is always considered not up to date so developers notice.
func UpToDate(latest string) bool {
	if Version == "dev" {
		return false
	}
	return compareSemver(Version, latest) >= 0
}

// compareSemver returns -1, 0 or 1 comparing dotted numeric versions a and b.
// Non-numeric or pre-release suffixes are ignored beyond the first three
// numeric fields, which is sufficient for our MAJOR.MINOR.PATCH tags.
func compareSemver(a, b string) int {
	pa := parseSemver(a)
	pb := parseSemver(b)
	for i := 0; i < 3; i++ {
		switch {
		case pa[i] < pb[i]:
			return -1
		case pa[i] > pb[i]:
			return 1
		}
	}
	return 0
}

// parseSemver extracts up to three leading numeric fields from a version
// string, tolerating a "v" prefix and a trailing "-pre"/"+build" suffix.
func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, field := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n := 0
		for _, r := range field {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		out[i] = n
	}
	return out
}
