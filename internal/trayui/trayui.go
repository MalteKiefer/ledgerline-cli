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
	AvatarPNG   []byte // empty when the account has none or the fetch failed
	Err         error  // last refresh error, shown instead of stale numbers
	Unreachable bool   // the server could not be reached (offline, DNS, TLS)
}

// Model is the rendered menu: a title, a status block and the actions that make
// sense right now. The systray wiring maps this onto menu items 1:1, so a
// change in what the user sees is a change in this struct, not in UI plumbing.
type Model struct {
	Title       string // window/tooltip title, e.g. "Ledgerline 0.7.5"
	Tooltip     string
	Lines       []string // non-clickable status lines, top to bottom
	ShowLogin   bool
	ShowLogout  bool
	ShowOpenWeb bool
	Offline     bool // drives the muted tray icon
}

// Build renders the menu model for a state. It never returns an error: a tray
// that cannot describe its own state is worse than one that says "unavailable".
func Build(s State) Model {
	m := Model{
		Title:   "Ledgerline " + displayVersion(s.Version),
		Tooltip: "Ledgerline " + displayVersion(s.Version),
	}

	if !s.LoggedIn {
		m.Lines = []string{"Not signed in"}
		m.ShowLogin = true
		m.Offline = true
		m.Tooltip += " — not signed in"
		return m
	}

	host := ServerHost(s.ServerURL)
	m.ShowLogout = true
	m.ShowOpenWeb = s.ServerURL != ""

	// A failed refresh must not be dressed up as fresh data: say so, keep the
	// identity we know, and drop the numbers we cannot vouch for.
	if s.Err != nil {
		m.Offline = true
		status := "Server unreachable"
		if !s.Unreachable {
			status = "Error: " + firstLine(s.Err.Error())
		}
		m.Lines = []string{nameLine(s.UserName, s.UserEmail), host, status}
		m.Tooltip += " — " + status
		return m
	}

	m.Lines = []string{
		nameLine(s.UserName, s.UserEmail),
		host,
		"Storage: " + StorageLine(s.Usage),
	}
	m.Tooltip += " — " + nameLine(s.UserName, s.UserEmail) + " @ " + host
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

// StorageLine renders usage as "1.4 GiB of 10.0 GiB (14%)", or without the
// quota part when the account is unlimited. Files and gallery share one quota
// on the server, so they are summed here too.
func StorageLine(u api.Usage) string {
	used := u.Files + u.Gallery
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
