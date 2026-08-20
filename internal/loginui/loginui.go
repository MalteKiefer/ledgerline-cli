// Package loginui serves the graphical sign-in dialog for the desktop client as
// a tiny page on loopback, opened in the user's browser.
//
// Why a browser page instead of a native window: pairing needs a one-time code
// that the user copies from the web app, so they are already in a browser, and
// the clipboard, IME and accessibility all work without hand-rolling a Win32
// dialog. Why not a console: a terminal window popping out of a tray icon is
// jarring, and the console cannot be styled or localised.
//
// Security posture, matching `files webdav`: bound to 127.0.0.1 on an ephemeral
// port, reachable only under an unguessable per-run path token, refusing
// cross-origin form posts, and shut down as soon as the flow finishes. No secret
// is ever rendered back into the page — not the code, not the bearer.
package loginui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/pairflow"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
)

// Phase is where a sign-in attempt currently stands.
type Phase string

const (
	PhaseIdle      Phase = "idle"    // the form has not been submitted yet
	PhaseWaiting   Phase = "waiting" // code accepted, waiting for web approval
	PhaseDone      Phase = "done"    // token issued, verified and stored
	PhaseError     Phase = "error"   // the attempt failed; the form is offered again
	PhaseCancelled Phase = "cancelled"
)

// Server hosts the dialog. One Server serves one sign-in at a time.
type Server struct {
	deviceName   string
	pollInterval time.Duration

	mu      sync.Mutex
	phase   Phase
	message string
	who     string // "Name <email>" once signed in, for the success page

	token     string
	listener  net.Listener
	http      *http.Server
	done      chan struct{}
	closeOnce sync.Once
}

// New creates a dialog server. deviceName prefills the device-name field, which
// is what the account owner sees in the web app's device list.
func New(deviceName string) *Server {
	return &Server{
		deviceName:   deviceName,
		pollInterval: pairflow.DefaultPollInterval,
		phase:        PhaseIdle,
		done:         make(chan struct{}),
	}
}

// Start binds the loopback listener and begins serving. It returns the URL to
// open, which carries the path token.
func (s *Server) Start(ctx context.Context) (string, error) {
	tok, err := randomToken()
	if err != nil {
		return "", err
	}
	s.token = tok

	lc := &net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	s.listener = ln
	s.http = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.http.Serve(ln) }()

	return fmt.Sprintf("http://%s/%s/", ln.Addr().String(), tok), nil
}

// Handler exposes the routes, so tests can drive them without a real listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	return mux
}

// Wait blocks until the flow finishes (success, failure the user gave up on, or
// ctx expiring) and returns the stored session on success.
func (s *Server) Wait(ctx context.Context) (Phase, string) {
	select {
	case <-s.done:
	case <-ctx.Done():
		s.set(PhaseCancelled, "cancelled", "")
	}
	return s.snapshot()
}

// Close shuts the dialog down. Safe to call more than once.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		if s.http != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = s.http.Shutdown(shutdownCtx)
		}
		if s.listener != nil {
			_ = s.listener.Close()
		}
	})
}

// route dispatches on the token-prefixed path. An unknown or mismatched token is
// a flat 404: the dialog does not confirm its own existence to a guesser.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(parts) == 0 || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(s.token)) != 1 {
		http.NotFound(w, r)
		return
	}
	rest := ""
	if len(parts) == 2 {
		rest = parts[1]
	}

	switch {
	case rest == "" && r.Method == http.MethodGet:
		s.renderForm(w)
	case rest == "start" && r.Method == http.MethodPost:
		s.start(w, r)
	case rest == "status" && r.Method == http.MethodGet:
		s.status(w)
	default:
		http.NotFound(w, r)
	}
}

// start validates the form and kicks the pairing off in the background so the
// browser is never left hanging on a long request.
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	// A browser only sends Origin on cross-origin posts; anything but our own
	// address means some other page is driving this dialog.
	if origin := r.Header.Get("Origin"); origin != "" && !s.sameOrigin(origin, r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	server := strings.TrimSpace(r.PostFormValue("server"))
	code := strings.TrimSpace(r.PostFormValue("code"))
	device := strings.TrimSpace(r.PostFormValue("device"))
	if device == "" {
		device = s.deviceName
	}
	if server == "" || code == "" {
		s.set(PhaseError, "Enter both the server URL and the one-time code.", "")
		s.writeJSON(w, http.StatusOK)
		return
	}
	if p, _ := s.snapshot(); p == PhaseWaiting {
		s.writeJSON(w, http.StatusOK) // a double submit must not start a second flow
		return
	}

	s.set(PhaseWaiting, "Waiting for you to approve this device in the web app…", "")
	s.writeJSON(w, http.StatusOK)

	go s.run(server, code, device)
}

// run performs the pairing and records the outcome for the status endpoint.
func (s *Server) run(server, code, device string) {
	// Generous but bounded: approval is a human step, and the dialog must not
	// wait for ever if the user walks away.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	sess, err := pairflow.Run(ctx, pairflow.Options{
		Server:       server,
		Code:         code,
		DeviceName:   device,
		PollInterval: s.pollInterval,
	})
	if err != nil {
		s.set(PhaseError, firstLine(err.Error()), "")
		return
	}
	s.set(PhaseDone, "Signed in.", describe(sess))
	s.finish()
}

func (s *Server) status(w http.ResponseWriter) { s.writeJSON(w, http.StatusOK) }

func (s *Server) writeJSON(w http.ResponseWriter, code int) {
	phase, message := s.snapshot()
	s.mu.Lock()
	who := s.who
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"phase":   string(phase),
		"message": message,
		"account": who,
	})
}

func (s *Server) set(phase Phase, message, who string) {
	s.mu.Lock()
	s.phase, s.message = phase, message
	if who != "" {
		s.who = who
	}
	s.mu.Unlock()
}

func (s *Server) snapshot() (Phase, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase, s.message
}

// finish releases Wait. The listener stays up briefly so the browser can fetch
// the success state before the caller closes it.
func (s *Server) finish() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// sameOrigin reports whether the Origin header names this dialog's own address.
func (s *Server) sameOrigin(origin string, r *http.Request) bool {
	want := "http://" + r.Host
	return strings.EqualFold(strings.TrimSuffix(origin, "/"), want)
}

// PollIntervalForTests shortens the approval poll; production uses the default.
func (s *Server) PollIntervalForTests(d time.Duration) { s.pollInterval = d }

func describe(sess session.Session) string {
	name := sess.UserName
	if name == "" {
		name = sess.UserEmail
	}
	if sess.UserEmail != "" && name != sess.UserEmail {
		return fmt.Sprintf("%s <%s>", name, sess.UserEmail)
	}
	return name
}

func randomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	const max = 200
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

// ErrCancelled reports that the dialog was closed before finishing.
var ErrCancelled = errors.New("loginui: sign-in cancelled")
