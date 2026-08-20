package authflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/session"
)

// loginServer stands in for the API: it accepts one credential pair and,
// when twoFactor is set, refuses until the right code arrives.
func loginServer(t *testing.T, twoFactor bool) (*httptest.Server, *map[string]any) {
	t.Helper()
	last := map[string]any{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &last)

		if last["email"] != "ada@example.com" || last["password"] != "correct horse" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"These credentials do not match our records.","errors":{"email":["invalid"]}}`))
			return
		}
		if twoFactor && last["code"] != "123456" && last["recovery_code"] != "rescue-me" {
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
		_, _ = w.Write([]byte(`{"user":{"id":7,"name":"Ada","email":"ada@example.com"},"usage":{"files":1,"gallery":2}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &last
}

func TestPasswordStoresTheSessionAndRegistersTheDevice(t *testing.T) {
	isolate(t)
	srv, last := loginServer(t, false)

	sess, err := Password(context.Background(), PasswordOptions{
		Server: srv.URL, Email: "ada@example.com", Password: "correct horse",
		DeviceName: "workstation", AppVersion: "1.2.3",
	})
	if err != nil {
		t.Fatalf("password login: %v", err)
	}
	if sess.Token != "tok-live" || sess.UserEmail != "ada@example.com" {
		t.Fatalf("session = %+v", sess)
	}
	stored, err := session.Load()
	if err != nil || stored.Token != "tok-live" {
		t.Fatalf("stored session = %+v (%v)", stored, err)
	}

	// The install id is what makes the server treat this as a device it can
	// revoke and replace, instead of stacking a new entry on every sign-in.
	if id, _ := (*last)["install_id"].(string); len(id) != 32 {
		t.Fatalf("install_id = %q, want 32 hex characters", id)
	}
	if (*last)["device_name"] != "workstation" || (*last)["app_version"] != "1.2.3" {
		t.Fatalf("device metadata not sent: %+v", *last)
	}
}

// TestTwoFactorIsEnforcedByTheServer is the point of the whole exercise: going
// through the API instead of the web app must not skip the second factor.
func TestTwoFactorIsEnforcedByTheServer(t *testing.T) {
	isolate(t)
	srv, _ := loginServer(t, true)

	base := PasswordOptions{Server: srv.URL, Email: "ada@example.com", Password: "correct horse"}

	_, err := Password(context.Background(), base)
	if !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatalf("err = %v, want ErrTwoFactorRequired", err)
	}
	if _, lerr := session.Load(); lerr == nil {
		t.Fatal("a session was stored without the second factor")
	}

	wrong := base
	wrong.Code = "000000"
	if _, err := Password(context.Background(), wrong); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatalf("wrong code: err = %v", err)
	}

	right := base
	right.Code = "123456"
	if _, err := Password(context.Background(), right); err != nil {
		t.Fatalf("correct code rejected: %v", err)
	}
	if _, lerr := session.Load(); lerr != nil {
		t.Fatalf("session not stored after the factor was supplied: %v", lerr)
	}
}

func TestRecoveryCodeIsAccepted(t *testing.T) {
	isolate(t)
	srv, _ := loginServer(t, true)

	_, err := Password(context.Background(), PasswordOptions{
		Server: srv.URL, Email: "ada@example.com", Password: "correct horse",
		Recovery: "rescue-me",
	})
	if err != nil {
		t.Fatalf("recovery code rejected: %v", err)
	}
}

func TestBadCredentialsDoNotRevealWhichPartWasWrong(t *testing.T) {
	isolate(t)
	srv, _ := loginServer(t, false)

	_, err := Password(context.Background(), PasswordOptions{
		Server: srv.URL, Email: "ada@example.com", Password: "guess",
	})
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("err = %v, want ErrBadCredentials", err)
	}
	// The server deliberately answers the same for a blocked account, so the
	// message must not claim the password was the problem.
	if strings.Contains(err.Error(), "password is wrong") {
		t.Fatalf("message over-claims: %v", err)
	}
	if _, lerr := session.Load(); lerr == nil {
		t.Fatal("a rejected sign-in stored a session")
	}
}

func TestUnverifiedEmailIsItsOwnFailure(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":"verify-email"}`))
	}))
	defer srv.Close()

	_, err := Password(context.Background(), PasswordOptions{
		Server: srv.URL, Email: "ada@example.com", Password: "correct horse",
	})
	if !errors.Is(err, ErrEmailUnverified) {
		t.Fatalf("err = %v, want ErrEmailUnverified", err)
	}
}

func TestIncompleteInputIsRejectedBeforeAnyRequest(t *testing.T) {
	isolate(t)
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	for _, opts := range []PasswordOptions{
		{Email: "a@b.c", Password: "x"},
		{Server: srv.URL, Password: "x"},
		{Server: srv.URL, Email: "a@b.c"},
	} {
		if _, err := Password(context.Background(), opts); err == nil {
			t.Fatalf("accepted incomplete input: %+v", opts)
		}
	}
	if called {
		t.Fatal("a request was sent for an incomplete form")
	}
}

// TestTokenIsVerifiedBeforeItIsStored mirrors the pairing flow: a token that
// cannot call /me is worse than none, because every later command fails for a
// reason the user cannot see.
func TestTokenIsVerifiedBeforeItIsStored(t *testing.T) {
	isolate(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"stale","user":{"id":1}}`))
	})
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := Password(context.Background(), PasswordOptions{
		Server: srv.URL, Email: "a@b.c", Password: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "verification") {
		t.Fatalf("err = %v", err)
	}
	if _, lerr := session.Load(); lerr == nil {
		t.Fatal("session stored despite failed verification")
	}
}
