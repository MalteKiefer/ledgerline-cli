package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestAuthLoginEndToEnd drives the login command against a mock server that
// behaves like the real pairing API: claim → poll(approved) → me. It asserts the
// session is persisted and the identity is reported.
func TestAuthLoginEndToEnd(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	keyring.MockInit()

	var claimed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/pair":
			claimed = true
			w.Write([]byte(`{"status":"pending"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/pair/collect":
			if !claimed {
				t.Error("polled before claiming")
			}
			w.Write([]byte(`{"status":"approved","token":"tok-live","user":{"id":9,"name":"Grace","email":"grace@example.com"}}`))
		case r.URL.Path == "/api/v1/me":
			if r.Header.Get("Authorization") != "Bearer tok-live" {
				t.Errorf("me without bearer: %q", r.Header.Get("Authorization"))
			}
			w.Write([]byte(`{"user":{"id":9,"name":"Grace","email":"grace@example.com"},"usage":{"files":0,"gallery":0}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	// Non-interactive: pass server and code as flags so no prompt is needed.
	root.SetArgs([]string{"auth", "login", "--server", srv.URL, "--code", "pasted-code", "--device-name", "ci-runner"})

	if err := root.Execute(); err != nil {
		t.Fatalf("login: %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Logged in as Grace") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}

	// The session is now usable by `auth status`.
	out.Reset()
	root2 := NewRootCommand()
	root2.SetOut(&out)
	root2.SetErr(&out)
	root2.SetArgs([]string{"auth", "status"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "grace@example.com") {
		t.Fatalf("status did not show identity:\n%s", out.String())
	}
}

// TestAuthLoginExpiredCode asserts a 410 on claim yields a clear message.
func TestAuthLoginExpiredCode(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	keyring.MockInit()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"auth", "login", "--server", srv.URL, "--code", "old"})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected an expiry error, got %v", err)
	}
}
