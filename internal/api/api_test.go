package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewRejectsCleartextForRemoteHosts(t *testing.T) {
	if _, err := New("http://ledger.example.com"); err == nil {
		t.Fatal("expected http:// to be rejected for a non-loopback host")
	}
	if _, err := New("https://ledger.example.com"); err != nil {
		t.Fatalf("https should be accepted: %v", err)
	}
	if _, err := New("http://localhost:8080"); err != nil {
		t.Fatalf("http on loopback should be accepted: %v", err)
	}
	if _, err := New("://nonsense"); err == nil {
		t.Fatal("expected an invalid URL to be rejected")
	}
}

func TestNewTrimsTrailingSlash(t *testing.T) {
	c, err := New("https://ledger.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL() != "https://ledger.example.com" {
		t.Fatalf("BaseURL = %q", c.BaseURL())
	}
}

func TestClaimPair(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/pair" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing XHR header")
		}
		w.Write([]byte(`{"status":"pending"}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	if err := c.ClaimPair(context.Background(), "abc", "ledgerline-cli@host"); err != nil {
		t.Fatalf("ClaimPair: %v", err)
	}
}

func TestClaimPairExpiredCodeIsGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	c := testClient(t, srv)
	err := c.ClaimPair(context.Background(), "expired", "dev")
	if Status(err) != http.StatusGone {
		t.Fatalf("expected 410, got %v (status %d)", err, Status(err))
	}
}

func TestPollPairPendingThenApproved(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("code"); got != "the code" {
			t.Errorf("code query = %q", got)
		}
		calls++
		if calls == 1 {
			w.Write([]byte(`{"status":"pending"}`))
			return
		}
		w.Write([]byte(`{"status":"approved","token":"tok-123","user":{"id":7,"name":"Ada","email":"ada@example.com"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)

	status, result, err := c.PollPair(context.Background(), "the code")
	if err != nil || status != PairPending || result != nil {
		t.Fatalf("first poll = (%v, %v, %v), want pending", status, result, err)
	}

	status, result, err = c.PollPair(context.Background(), "the code")
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if status != PairApproved || result == nil {
		t.Fatalf("second poll status = %v", status)
	}
	if result.Token != "tok-123" || result.User.ID != 7 || result.User.Email != "ada@example.com" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestMeSendsBearerAndDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-xyz" {
			t.Errorf("Authorization = %q", got)
		}
		w.Write([]byte(`{"user":{"id":1,"name":"Bob","email":"b@x.io"},"usage":{"files":2048,"gallery":4096}}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, WithToken("tok-xyz"), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	user, usage, err := c.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if user.Name != "Bob" || usage.Files != 2048 || usage.Gallery != 4096 {
		t.Fatalf("unexpected: user=%+v usage=%+v", user, usage)
	}
}

func TestMeUnauthorizedMapsTo401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Unauthenticated."}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	_, _, err := c.Me(context.Background())
	if Status(err) != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %v", err)
	}
}

func TestValidationErrorExposesFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"invalid","errors":{"code":["The code field is required."]}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	err := c.ClaimPair(context.Background(), "", "dev")
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != 422 || len(apiErr.Fields["code"]) != 1 {
		t.Fatalf("unexpected APIError: %+v", apiErr)
	}
}

// testClient builds a client pointed at srv using its TLS-aware HTTP client.
func testClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(srv.URL, WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
