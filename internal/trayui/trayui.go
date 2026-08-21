// Package trayui holds the platform-independent half of the tray application:
// what the menu should say for a given session state, and how an avatar becomes
// a menu icon. Keeping it out of the systray wiring means the behaviour is unit
// testable — a tray app is otherwise only verifiable by looking at a screen.
package trayui

import (
	"net/url"
	"strings"
)

// State is everything the tray renders from: whether there is a session, where
// the server is, and whether the last refresh failed.
//
// It carries no name, no e-mail and no storage figure, because the menu shows
// none of those. The refresh that would have supplied them still happens — it
// is how a revoked device and a remote wipe are noticed — but its identity half
// stops here rather than travelling to a renderer that would drop it.
type State struct {
	Version     string
	LoggedIn    bool
	ServerURL   string
	Err         error // last refresh error
	Unreachable bool  // the server could not be reached (offline, DNS, TLS)

	Sync SyncState // what the folder sync is doing
}

// Model is the rendered menu. The systray wiring maps it onto menu items 1:1,
// so a change in what the user sees is a change here, not in UI plumbing.
//
// It says what the program is doing, not who is using it. Identity — the name,
// the e-mail, the server, the storage — belongs in the settings window, which
// can show it as a profile rather than as rows of text behind a hover; a tray
// menu is read at a glance while something else has the user's attention, and
// it is also on screen for anyone walking past the machine.
type Model struct {
	Title   string // window/tooltip title, e.g. "Ledgerline 0.7.5"
	Tooltip string

	SyncSummary string   // the row: what the folder sync is doing
	SyncDetails []string // its submenu: one line per folder pair

	Status string // a failure, e.g. "Server unreachable"

	ShowSyncRow bool
	ShowStatus  bool
	ShowLogin   bool
	ShowLogout  bool
	ShowOpenWeb bool
	ShowSync    bool // the folder window needs a session to be useful
	ShowRefresh bool

	Offline bool // drives the muted tray icon
	Busy    bool // a sync is running: the icon says so
}

// Build renders the menu model for a state. It never returns an error: a tray
// that cannot describe its own state is worse than one that says "unavailable".
func Build(s State) Model {
	m := Model{
		Title:   "Ledgerline " + displayVersion(s.Version),
		Tooltip: "Ledgerline " + displayVersion(s.Version),
	}

	// Signed out, the menu offers exactly one thing. Storage figures, the server
	// row, "sync folders", "open web app" — none of them mean anything without a
	// session, and a menu full of dead entries reads as broken rather than as
	// waiting.
	if !s.LoggedIn {
		m.ShowLogin = true
		m.Offline = true
		m.Tooltip += " — not signed in"
		return m
	}

	m.ShowLogout = true
	m.ShowSync = true
	m.ShowRefresh = true
	m.ShowOpenWeb = s.ServerURL != ""
	m.Busy = s.Sync.Phase == SyncRunning

	// The sync row is shown whenever signed in, including with no folders: "no
	// folders" is the answer to "is anything syncing", and hiding the row makes
	// the feature invisible to someone who has not found it yet.
	m.ShowSyncRow = true
	m.SyncSummary = s.Sync.SyncLine()
	m.SyncDetails = s.Sync.SyncDetails()

	// A failed refresh must not be dressed up as fresh data: say so, and keep it
	// to what went wrong rather than to who it went wrong for.
	if s.Err != nil {
		m.Offline = true
		m.ShowStatus = true
		m.Status = "Server unreachable"
		if !s.Unreachable {
			m.Status = "Error: " + firstLine(s.Err.Error())
		}
		m.Tooltip += " — " + m.Status
		return m
	}

	if m.Busy {
		m.Tooltip += " — syncing"
	}
	return m
}

// displayVersion normalises the build stamp for display; an unstamped dev build
// reports "dev" rather than an empty string.
func displayVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "dev"
	}
	return strings.TrimPrefix(v, "v")
}

// ServerHost reduces a server URL to the host (with port when non-default), the
// part a user recognises. An unparsable value is shown verbatim rather than
// hidden.
func ServerHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "(no server)"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.TrimSuffix(raw, "/")
	}
	return u.Host
}

// firstLine keeps a menu entry to one line: an API error can carry a long body.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	const max = 60
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}
