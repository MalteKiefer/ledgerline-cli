// Package trayui holds the platform-independent half of the tray application:
// what the menu should say for a given session state, and how an avatar becomes
// a menu icon. Keeping it out of the systray wiring means the behaviour is unit
// testable — a tray app is otherwise only verifiable by looking at a screen.
package trayui

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/ui"
)

// State is everything the tray renders from: who we are, where the server is,
// how much storage is used, and whether the last refresh failed.
type State struct {
	Version     string
	LoggedIn    bool
	ServerURL   string
	UserName    string
	UserEmail   string
	Usage       api.Usage
	Err         error // last refresh error, shown instead of stale numbers
	Unreachable bool  // the server could not be reached (offline, DNS, TLS)

	Sync SyncState // what the folder sync is doing
}

// Model is the rendered menu. The systray wiring maps it onto menu items 1:1,
// so a change in what the user sees is a change here, not in UI plumbing.
//
// The shape is two summary rows, each with a submenu. A tray menu is read at a
// glance while something else has the user's attention; five stacked figures at
// the top level is a wall of text, and the details are one hover away.
type Model struct {
	Title   string // window/tooltip title, e.g. "Ledgerline 0.7.5"
	Tooltip string

	Account        string   // the row: who is signed in
	AccountDetails []string // its submenu: e-mail, server, storage

	SyncSummary string   // the row: what the folder sync is doing
	SyncDetails []string // its submenu: one line per folder pair

	Status string // a failure that replaces the figures, e.g. "Server unreachable"

	ShowAccount bool
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

	host := ServerHost(s.ServerURL)
	name := nameLine(s.UserName, s.UserEmail)

	m.ShowLogout = true
	m.ShowSync = true
	m.ShowRefresh = true
	m.ShowOpenWeb = s.ServerURL != ""
	m.ShowAccount = true
	m.Account = name
	m.Busy = s.Sync.Phase == SyncRunning

	// The sync row is shown whenever signed in, including with no folders: "no
	// folders" is the answer to "is anything syncing", and hiding the row makes
	// the feature invisible to someone who has not found it yet.
	m.ShowSyncRow = true
	m.SyncSummary = s.Sync.SyncLine()
	m.SyncDetails = s.Sync.SyncDetails()

	// A failed refresh must not be dressed up as fresh data: say so, keep the
	// identity we know, and drop the numbers we cannot vouch for.
	if s.Err != nil {
		m.Offline = true
		m.ShowStatus = true
		m.Status = "Server unreachable"
		if !s.Unreachable {
			m.Status = "Error: " + firstLine(s.Err.Error())
		}
		m.AccountDetails = detailRows(s.UserEmail, name, host)
		m.Tooltip += " — " + m.Status
		return m
	}

	// Storage lives in the settings window, not here. A tray menu is read at a
	// glance, and four figures behind a hover are four things nobody reads; the
	// window can draw the same numbers as a bar with the split beside it.
	m.AccountDetails = detailRows(s.UserEmail, name, host)
	m.Tooltip += " — " + name + " @ " + host
	if m.Busy {
		m.Tooltip += " — syncing"
	}
	return m
}

// detailRows is the fixed head of the account submenu: the e-mail (when it adds
// something the row does not already say) and the server.
func detailRows(email, shown, host string) []string {
	rows := make([]string, 0, 4)
	if email != "" && email != shown {
		rows = append(rows, email)
	}
	return append(rows, "Server: "+host)
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

// nameLine prefers the display name and falls back to the e-mail, so the menu
// always identifies the account even for a profile without a name.
func nameLine(name, email string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	if strings.TrimSpace(email) != "" {
		return email
	}
	return "Signed in"
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

// StorageLine renders the combined usage as "1.4 GiB of 10.0 GiB (14%)", or
// without the quota part when the account is unlimited.
func StorageLine(u api.Usage) string {
	used := u.Total()
	if u.Quota == nil || *u.Quota <= 0 {
		return ui.HumanBytes(used) + " used"
	}
	pct := float64(used) / float64(*u.Quota) * 100
	if pct > 999 {
		pct = 999
	}
	return fmt.Sprintf("%s of %s (%.0f%%)", ui.HumanBytes(used), ui.HumanBytes(*u.Quota), pct)
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

// ErrNoAvatar reports that no usable avatar image was available.
var ErrNoAvatar = errors.New("trayui: no avatar")
