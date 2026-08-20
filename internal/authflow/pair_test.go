package authflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/MalteKiefer/ledgerline-cli/internal/session"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	keyring.MockInit()
}

// TestNoSessionIsStoredWhenTheTokenDoesNotWork is the reason verification exists
// at all: storing a credential that cannot call /me would leave every later
// command failing for a reason the user cannot see.
func TestNoSessionIsStoredWhenTheTokenDoesNotWork(t *testing.T) {
	isolate(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/pair", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	})
	mux.HandleFunc("/api/v1/auth/pair/collect", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"approved","token":"stale","user":{"id":1}}`))
	})
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := Run(context.Background(), Options{Server: srv.URL, Code: "abc", DeviceName: "dev", PollInterval: time.Millisecond})
	if err == nil {
		t.Fatal("a token that fails verification was accepted")
	}
	if !strings.Contains(err.Error(), "verification") {
		t.Fatalf("error = %v", err)
	}
	if _, lerr := session.Load(); lerr == nil {
		t.Fatal("session stored despite failed verification")
	}
}

func TestMissingInputIsRejectedBeforeAnyRequest(t *testing.T) {
	isolate(t)
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	if _, err := Run(context.Background(), Options{Server: srv.URL}); err == nil {
		t.Fatal("empty code accepted")
	}
	if _, err := Run(context.Background(), Options{Code: "abc"}); err == nil {
		t.Fatal("empty server accepted")
	}
	if called {
		t.Fatal("a request was sent for an incomplete form")
	}
}

func TestOnWaitingFiresOnceTheCodeIsAccepted(t *testing.T) {
	isolate(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/pair", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	})
	// Never approves: the test only cares that the waiting callback ran.
	mux.HandleFunc("/api/v1/auth/pair/collect", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	waiting := false
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, _ = Run(ctx, Options{
		Server: srv.URL, Code: "abc", DeviceName: "dev",
		PollInterval: 20 * time.Millisecond,
		OnWaiting:    func() { waiting = true },
	})
	if !waiting {
		t.Fatal("OnWaiting never fired after the code was accepted")
	}
}

func TestClaimAndPollErrorsAreTranslated(t *testing.T) {
	isolate(t)
	// A rejected code and an expired pairing must read as something the user can
	// act on, not as a bare status code.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/pair", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := Run(context.Background(), Options{Server: srv.URL, Code: "nope", PollInterval: time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("claim error = %v", err)
	}
}
