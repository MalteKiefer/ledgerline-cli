package cmd

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":  true,
		"127.0.0.53": true,
		"::1":        true,
		"localhost":  true,
		// "all interfaces" is reachable from the network, so it is not loopback.
		"":            false,
		"0.0.0.0":     false,
		"::":          false,
		"192.0.2.10":  false,
		"example.com": false,
	}
	for host, want := range cases {
		if got := isLoopbackHost(host); got != want {
			t.Fatalf("isLoopbackHost(%q) = %t, want %t", host, got, want)
		}
	}
}

func TestBasicAuthGate(t *testing.T) {
	reached := false
	handler := basicAuth("ledgerline", "s3cret", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))

	// No credentials at all.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate challenge")
	}

	// Right user, wrong password.
	rec = httptest.NewRecorder()
	bad := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	bad.SetBasicAuth("ledgerline", "guess")
	handler.ServeHTTP(rec, bad)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, want 401", rec.Code)
	}

	// Wrong user, right password.
	rec = httptest.NewRecorder()
	bad = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	bad.SetBasicAuth("someone", "s3cret")
	handler.ServeHTTP(rec, bad)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong user = %d, want 401", rec.Code)
	}
	if reached {
		t.Fatal("handler reached without valid credentials")
	}

	rec = httptest.NewRecorder()
	good := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	good.SetBasicAuth("ledgerline", "s3cret")
	handler.ServeHTTP(rec, good)
	if !reached || rec.Code != http.StatusOK {
		t.Fatalf("valid credentials: reached=%t code=%d", reached, rec.Code)
	}
}

func TestRandomPasswordIsUniqueAndLongEnough(t *testing.T) {
	seen := map[string]bool{}
	for range 32 {
		p, err := randomPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 32 { // 16 random bytes, hex-encoded
			t.Fatalf("password length = %d", len(p))
		}
		if seen[p] {
			t.Fatalf("duplicate password %q", p)
		}
		seen[p] = true
	}
}

func TestMountHintMatchesPlatform(t *testing.T) {
	hint := mountHint("http://127.0.0.1:9800/", "ledgerline")
	var want string
	switch runtime.GOOS {
	case "windows":
		want = "net use"
	case "darwin":
		want = "mount_webdav"
	default:
		want = "davfs"
	}
	if !strings.Contains(hint, want) {
		t.Fatalf("hint for %s = %q, want it to mention %q", runtime.GOOS, hint, want)
	}
	if !strings.Contains(hint, "127.0.0.1:9800") {
		t.Fatalf("hint does not carry the address: %q", hint)
	}
}
