package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// passwordServer accepts one credential pair and demands a second factor.
func passwordServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)

		if body["password"] != "correct horse" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"These credentials do not match our records."}`))
			return
		}
		if body["code"] != "123456" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"two_factor":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"tok-live","user":{"id":7,"name":"Ada","email":"ada@example.com"}}`))
	})
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-live" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"user":{"id":7,"name":"Ada","email":"ada@example.com"},"usage":{"files":0,"gallery":0}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestAuthLoginWithPasswordAndOTP drives the whole command: the password
// arrives on stdin (never in argv) and the second factor as a flag.
func TestAuthLoginWithPasswordAndOTP(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	keyring.MockInit()
	srv := passwordServer(t)

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader("correct horse\n"))
	root.SetArgs([]string{
		"auth", "login", "--server", srv.URL, "--email", "ada@example.com",
		"--password-stdin", "--otp", "123456", "--device-name", "ci-runner",
	})

	if err := root.Execute(); err != nil {
		t.Fatalf("login: %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Signed in as Ada") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
}

// TestAuthLoginPromptsForTheSecondFactor covers the common shape: the user
// supplies no code, the server asks for one, and the command asks the user
// rather than failing.
func TestAuthLoginPromptsForTheSecondFactor(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	keyring.MockInit()
	srv := passwordServer(t)

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	// First line is consumed as the password, the second as the prompted code.
	root.SetIn(strings.NewReader("correct horse\n123456\n"))
	root.SetArgs([]string{
		"auth", "login", "--server", srv.URL, "--email", "ada@example.com", "--password-stdin",
	})

	if err := root.Execute(); err != nil {
		t.Fatalf("login: %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Two-factor code:") {
		t.Fatalf("no prompt for the factor:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Signed in as Ada") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
}

// TestAuthLoginWrongPasswordSaysSoWithoutGuessing checks the failure path keeps
// the server's deliberate ambiguity (wrong password and blocked account look
// the same) instead of inventing a cause.
func TestAuthLoginWrongPasswordSaysSoWithoutGuessing(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	keyring.MockInit()
	srv := passwordServer(t)

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader("guess\n"))
	root.SetArgs([]string{
		"auth", "login", "--server", srv.URL, "--email", "ada@example.com", "--password-stdin",
	})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "wrong e-mail or password") {
		t.Fatalf("err = %v", err)
	}
}

// TestAuthLoginRefusesPasswordInArgvAndStdinTogether keeps the two paths from
// silently disagreeing about which secret to use.
func TestAuthLoginRefusesPasswordInArgvAndStdinTogether(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	keyring.MockInit()

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader("x\n"))
	root.SetArgs([]string{
		"auth", "login", "--server", "https://example.test", "--email", "a@b.c",
		"--password", "x", "--password-stdin",
	})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v", err)
	}
}
