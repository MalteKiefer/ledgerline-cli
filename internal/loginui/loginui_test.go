package loginui

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/MalteKiefer/ledgerline-cli/internal/session"
)

// fakeLedgerline stands in for the server's pairing endpoints: the code is
// accepted, then approved after approveAfter polls.
func fakeLedgerline(t *testing.T, approveAfter int) *httptest.Server {
	t.Helper()
	polls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/pair", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "good-code") {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Invalid code."}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	})
	mux.HandleFunc("/api/v1/auth/pair/collect", func(w http.ResponseWriter, _ *http.Request) {
		polls++
		if polls <= approveAfter {
			_, _ = w.Write([]byte(`{"status":"pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"approved","token":"tok-live","user":{"id":7,"name":"Ada","email":"ada@example.com"}}`))
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
	return srv
}

// dialog wires a Server to a local test transport and returns it with its base
// path, isolating the credential store per test.
func dialog(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	keyring.MockInit()

	s := New("test-device")
	// Use the real generator rather than a fixed literal: the tests then cover
	// token generation too, and no constant in this file looks like a secret.
	tok, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	s.token = tok
	s.PollIntervalForTests(10 * time.Millisecond)
	ui := httptest.NewServer(s.Handler())
	t.Cleanup(ui.Close)
	return s, ui, ui.URL + "/" + s.token
}

// get and post are context-carrying helpers; the linter rightly refuses the
// bare http.Get/PostForm shortcuts even in tests.
func get(t *testing.T, target string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func post(t *testing.T, base string, form url.Values) map[string]string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/start",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestFormIsServedUnderTheTokenPath(t *testing.T) {
	s, ui, base := dialog(t)

	resp := get(t, base+"/")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	page := string(body)
	for _, want := range []string{"Sign in to Ledgerline", `name="server"`, `name="code"`, "two-factor"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page is missing %q", want)
		}
	}
	// The dialog must not leak its own path token into the markup: the page is
	// reached by it, it does not need to repeat it.
	if strings.Contains(page, s.token) {
		t.Fatal("page echoes the path token")
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("csp = %q", csp)
	}

	// A wrong token is a flat 404 anywhere in the tree.
	for _, p := range []string{"/", "/deadbeef/", "/deadbeef/status"} {
		r := get(t, ui.URL+p)
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", p, r.StatusCode)
		}
	}
}

func TestSignInStoresTheSession(t *testing.T) {
	s, _, base := dialog(t)
	server := fakeLedgerline(t, 1) // approved on the second poll

	got := post(t, base, url.Values{"server": {server.URL}, "code": {"good-code"}})
	if got["phase"] != string(PhaseWaiting) {
		t.Fatalf("first response = %v", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phase, message := s.Wait(ctx)
	if phase != PhaseDone {
		t.Fatalf("phase = %s (%s)", phase, message)
	}

	// The credential must actually be stored, with the token the server issued.
	sess, err := session.Load()
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if sess.Token != "tok-live" || sess.UserEmail != "ada@example.com" || sess.ServerURL != server.URL {
		t.Fatalf("session = %+v", sess)
	}

	// The status endpoint reports the account for the success page, and still
	// never the token.
	resp := get(t, base+"/status")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ada@example.com") {
		t.Fatalf("status body = %s", body)
	}
	if strings.Contains(string(body), "tok-live") {
		t.Fatal("status endpoint leaks the bearer token")
	}
}

func TestRejectedCodeIsReportedAndRetryable(t *testing.T) {
	s, _, base := dialog(t)
	server := fakeLedgerline(t, 0)

	post(t, base, url.Values{"server": {server.URL}, "code": {"wrong"}})

	// The failure arrives asynchronously; poll the status like the page does.
	deadline := time.Now().Add(3 * time.Second)
	var phase Phase
	var message string
	for time.Now().Before(deadline) {
		phase, message = s.snapshot()
		if phase == PhaseError {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if phase != PhaseError {
		t.Fatalf("phase = %s (%s)", phase, message)
	}
	if !strings.Contains(message, "rejected") {
		t.Fatalf("message = %q", message)
	}
	if _, err := session.Load(); err == nil {
		t.Fatal("a rejected code stored a session")
	}

	// The user can correct the code without restarting the dialog.
	got := post(t, base, url.Values{"server": {server.URL}, "code": {"good-code"}})
	if got["phase"] != string(PhaseWaiting) {
		t.Fatalf("retry response = %v", got)
	}
}

func TestMissingFieldsAreRejectedWithoutTouchingTheNetwork(t *testing.T) {
	s, _, base := dialog(t)

	got := post(t, base, url.Values{"server": {""}, "code": {"good-code"}})
	if got["phase"] != string(PhaseError) || !strings.Contains(got["message"], "server URL") {
		t.Fatalf("response = %v", got)
	}
	if p, _ := s.snapshot(); p != PhaseError {
		t.Fatalf("phase = %s", p)
	}
}

func TestCrossOriginPostIsRefused(t *testing.T) {
	_, _, base := dialog(t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/start",
		strings.NewReader("server=https://x.test&code=abc"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestDoubleSubmitDoesNotStartASecondFlow(t *testing.T) {
	s, _, base := dialog(t)
	server := fakeLedgerline(t, 100) // never approves within the test

	post(t, base, url.Values{"server": {server.URL}, "code": {"good-code"}})
	second := post(t, base, url.Values{"server": {server.URL}, "code": {"good-code"}})
	if second["phase"] != string(PhaseWaiting) {
		t.Fatalf("second submit = %v", second)
	}
	if p, _ := s.snapshot(); p != PhaseWaiting {
		t.Fatalf("phase = %s", p)
	}
}

func TestStartBindsLoopbackOnly(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	keyring.MockInit()

	s := New("dev")
	target, err := s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("dialog listens on %s, want loopback", u.Host)
	}
	// The URL carries a per-run token, not a fixed path.
	if len(strings.Trim(u.Path, "/")) != 32 {
		t.Fatalf("path = %q, want a 32-char token", u.Path)
	}
}
