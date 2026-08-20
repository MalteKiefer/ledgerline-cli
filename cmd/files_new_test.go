package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// loggedInRoot logs in against a server built from mux (which must include a
// GET /api/v1/me handler — authedClient calls it before every command) and
// returns an output buffer for the caller to attach to subsequent commands.
// It also registers the auth pairing routes on mux, so the caller's own
// handlers only need to cover the files endpoints under test.
func loggedInRoot(t *testing.T, mux *http.ServeMux) *bytes.Buffer {
	t.Helper()
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	keyring.MockInit()

	mux.HandleFunc("/api/v1/auth/pair", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	})
	mux.HandleFunc("/api/v1/auth/pair/collect", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"approved","token":"tok-live","user":{"id":9,"name":"Grace","email":"grace@example.com"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	login := NewRootCommand()
	var out bytes.Buffer
	login.SetOut(&out)
	login.SetErr(&out)
	login.SetArgs([]string{"auth", "pair", "--server", srv.URL, "--code", "pasted-code", "--device-name", "ci-runner"})
	if err := login.Execute(); err != nil {
		t.Fatalf("login: %v\noutput:\n%s", err, out.String())
	}
	out.Reset()
	return &out
}

// run executes a files subcommand against the already-authenticated session
// loggedInRoot set up, returning combined stdout+stderr.
func run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runErr(t, args...)
	if err != nil {
		t.Fatalf("%v: %v\noutput:\n%s", args, err, out)
	}
	return out
}

// runErr is run for the cases where the command is EXPECTED to fail (flag
// validation, refused binds): it returns the output and the error instead of
// failing the test.
func runErr(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runStdinErr(t, "", args...)
}

// runStdin executes a subcommand with stdin wired to input (for the
// --*-stdin secret paths), failing the test on error.
func runStdin(t *testing.T, input string, args ...string) string {
	t.Helper()
	out, err := runStdinErr(t, input, args...)
	if err != nil {
		t.Fatalf("%v: %v\noutput:\n%s", args, err, out)
	}
	return out
}

func runStdinErr(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(input))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// readFile is a tiny helper for asserting on a file a command wrote.
func readFile(path string) (string, error) {
	buf, err := os.ReadFile(path)
	return string(buf), err
}
