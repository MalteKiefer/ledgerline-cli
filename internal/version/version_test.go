package version

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpToDate(t *testing.T) {
	cases := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"equal", "1.2.3", "1.2.3", true},
		{"equal with v prefix", "1.2.3", "v1.2.3", true},
		{"newer patch available", "1.2.3", "1.2.4", false},
		{"newer minor available", "1.2.3", "1.3.0", false},
		{"newer major available", "1.2.3", "2.0.0", false},
		{"ahead of latest", "1.3.0", "1.2.9", true},
		{"prerelease suffix ignored", "1.2.3-rc1", "1.2.3", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := Version
			Version = tc.current
			defer func() { Version = orig }()
			if got := UpToDate(tc.latest); got != tc.want {
				t.Fatalf("UpToDate(%q) with Version=%q = %v, want %v", tc.latest, tc.current, got, tc.want)
			}
		})
	}
}

func TestUpToDateDevIsNeverCurrent(t *testing.T) {
	orig := Version
	Version = "dev"
	defer func() { Version = orig }()
	if UpToDate("0.0.1") {
		t.Fatal("a dev build must never report as up to date")
	}
}

func TestLatestRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.4.0","html_url":"https://example/releases/v1.4.0"}`))
	}))
	defer srv.Close()

	// Point the package at the test server by swapping the HTTP client's base
	// via a custom RoundTripper that rewrites the request URL.
	client := &http.Client{Transport: rewriteHost(srv.URL)}
	rel, err := LatestRelease(context.Background(), client)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if rel.TagName != "v1.4.0" {
		t.Fatalf("TagName = %q, want v1.4.0", rel.TagName)
	}
}

// rewriteHost redirects every request to base, letting the test intercept the
// hard-coded GitHub URL without changing production code.
func rewriteHost(base string) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		u, _ := req.URL.Parse(base)
		req.URL.Scheme = u.Scheme
		req.URL.Host = u.Host
		return http.DefaultTransport.RoundTrip(req)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
