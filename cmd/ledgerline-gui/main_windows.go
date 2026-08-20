//go:build windows

// Command ledgerline-gui is the desktop companion to the CLI: a tray icon that
// shows who is signed in, which server, and how much storage is used, with
// sign-in and sign-out from the menu.
//
// It shares the CLI's session, certificate pins and API client, so both see the
// same credential and the same transport guarantees. Sign-in itself is handed
// to the CLI in a console window: pairing needs a one-time code the user pastes,
// which a tray menu cannot collect.
package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/clientset"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
	"github.com/MalteKiefer/ledgerline-cli/internal/trayui"
	"github.com/MalteKiefer/ledgerline-cli/internal/version"
)

// refreshInterval is how often the tray re-reads identity and storage. A tray
// is ambient, not a dashboard: often enough to be current after a web-side
// change, rare enough to be invisible in the server's request log.
const refreshInterval = 5 * time.Minute

// requestTimeout bounds one refresh so a hung server cannot wedge the menu.
const requestTimeout = 20 * time.Second

// statusSlots is how many non-clickable menu rows the tray reserves.
const statusSlots = 5

func main() {
	systray.Run(newApp().onReady, func() {})
}

// app owns the menu items and the goroutine that refreshes them.
type app struct {
	mu    sync.Mutex
	state trayui.State

	title   *systray.MenuItem
	lines   []*systray.MenuItem
	login   *systray.MenuItem
	logout  *systray.MenuItem
	openWeb *systray.MenuItem
	refresh *systray.MenuItem
	quit    *systray.MenuItem

	// avatarApplied guards against re-encoding the same avatar on every refresh.
	avatarApplied bool
	// loginBusy keeps a second sign-in dialog from opening over the first.
	loginBusy atomic.Bool
}

func newApp() *app { return &app{state: trayui.State{Version: version.Version}} }

func (a *app) onReady() {
	systray.SetIcon(trayui.BrandIcon(false))
	systray.SetTitle("Ledgerline")
	systray.SetTooltip("Ledgerline")

	a.title = systray.AddMenuItem("Ledgerline", "")
	a.title.Disable()
	systray.AddSeparator()

	// Status slots are allocated up front: systray cannot remove items, so the
	// renderer shows or hides fixed slots instead of rebuilding the menu. Five
	// covers account, server, files, gallery and the total.
	for range statusSlots {
		item := systray.AddMenuItem("", "")
		item.Disable()
		item.Hide()
		a.lines = append(a.lines, item)
	}
	systray.AddSeparator()

	a.login = systray.AddMenuItem("Sign in…", "Pair this computer with a Ledgerline server")
	a.openWeb = systray.AddMenuItem("Open web app", "Open the server in your browser")
	a.logout = systray.AddMenuItem("Sign out", "Revoke this device's token")
	systray.AddSeparator()
	a.refresh = systray.AddMenuItem("Refresh", "Re-read identity and storage now")
	a.quit = systray.AddMenuItem("Quit", "Close the tray icon")

	go a.loop()
}

// loop owns every mutation of the menu: clicks and the periodic refresh arrive
// on the same goroutine, so no menu item is written from two places at once.
func (a *app) loop() {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	a.reload()

	for {
		select {
		case <-a.login.ClickedCh:
			a.startLogin()
		case <-a.logout.ClickedCh:
			a.doLogout()
		case <-a.openWeb.ClickedCh:
			a.openBrowser()
		case <-a.refresh.ClickedCh:
			a.reload()
		case <-ticker.C:
			a.reload()
		case <-a.quit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// reload re-reads the session and, when there is one, the account state.
func (a *app) reload() {
	state := trayui.State{Version: version.Version}

	client, sess, err := clientset.Authenticated()
	switch {
	case errors.Is(err, clientset.ErrNotAuthenticated):
		a.setState(state)
		return
	case err != nil:
		state.Err = err
		a.setState(state)
		return
	}

	state.LoggedIn = true
	state.ServerURL = sess.ServerURL
	state.UserName = sess.UserName
	state.UserEmail = sess.UserEmail

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	user, usage, wipe, err := client.Me(ctx)
	switch {
	case err != nil && api.Status(err) == 401:
		// The device was revoked from the web, or the token expired: drop the
		// local credential so the tray stops claiming a session it lost.
		_ = session.Clear()
		a.setState(trayui.State{Version: version.Version})
		return
	case err != nil:
		state.Err = err
		state.Unreachable = api.Status(err) == 0 // no HTTP status: transport failed
		a.setState(state)
		return
	}
	if wipe {
		// Remote kill switch, same contract as the CLI.
		_ = session.WipeLocal()
		a.setState(trayui.State{Version: version.Version})
		return
	}

	state.UserName = user.Name
	state.UserEmail = user.Email
	state.Usage = usage
	if user.HasAvatar {
		state.AvatarPNG = fetchAvatar(ctx, client)
	}
	a.setState(state)
}

// fetchAvatar is best effort: a missing or broken avatar just means initials.
func fetchAvatar(ctx context.Context, client *api.Client) []byte {
	var buf limitedBuffer
	if err := client.Avatar(ctx, &buf); err != nil {
		return nil
	}
	return buf.data
}

// setState stores the state and repaints the menu from it.
func (a *app) setState(s trayui.State) {
	a.mu.Lock()
	previousAvatar := len(a.state.AvatarPNG)
	a.state = s
	a.mu.Unlock()

	m := trayui.Build(s)
	systray.SetIcon(trayui.BrandIcon(!m.Offline))
	systray.SetTooltip(m.Tooltip)
	a.title.SetTitle(m.Title)

	for i, item := range a.lines {
		if i < len(m.Lines) {
			item.SetTitle(m.Lines[i])
			item.Show()
			continue
		}
		item.Hide()
	}
	setVisible(a.login, m.ShowLogin)
	setVisible(a.logout, m.ShowLogout)
	setVisible(a.openWeb, m.ShowOpenWeb)

	// The account line carries the avatar. Re-encode only when it changed, since
	// systray writes a temp file per SetIcon call.
	if len(m.Lines) > 0 {
		changed := len(s.AvatarPNG) != previousAvatar || !a.avatarApplied
		if len(s.AvatarPNG) > 0 && changed {
			if ico, err := trayui.ICOFromAvatar(s.AvatarPNG); err == nil {
				a.lines[0].SetIcon(ico)
				a.avatarApplied = true
			}
		}
		if len(s.AvatarPNG) == 0 {
			a.avatarApplied = false
		}
	}
}

func setVisible(item *systray.MenuItem, show bool) {
	if show {
		item.Show()
		return
	}
	item.Hide()
}

// startLogin opens the sign-in window: a real dialog, not a browser page and
// not a console. The user picks the method — e-mail and password with the
// account's second factor, or a one-time code approved in the web app — because
// both are legitimate: the password route is fewer steps, the code route never
// types the password into a desktop program.
//
// The dialog runs on its own OS thread with its own message loop, so it cannot
// block the tray, and only one may be open at a time: two would race on the
// stored credential.
func (a *app) startLogin() {
	if !a.loginBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.loginBusy.Store(false)

		// Prefill the server from a previous session so re-signing in after a
		// revoked token is one field shorter.
		var lastServer string
		if sess, err := session.Load(); err == nil {
			lastServer = sess.ServerURL
		}

		// Errors are shown in the dialog itself while it is open; whatever the
		// outcome, the tray simply re-reads the credential afterwards.
		runLoginDialog(lastServer)
		a.reload()
	}()
}

// deviceName is what the account owner sees in the web app's device list.
func deviceName() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "Windows desktop"
	}
	return host
}

// doLogout revokes server-side and clears the local credential in-process: it
// needs no input, so it does not deserve a console window.
func (a *app) doLogout() {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	if client, _, err := clientset.Authenticated(); err == nil {
		// A revoke that fails (server down) must still clear the local token:
		// the user asked to be signed out on this machine.
		_ = client.Logout(ctx)
	}
	_ = session.Clear()
	a.reload()
}

// openURL hands a local or configured URL to the shell. rundll32 avoids
// `cmd /c start`'s argument mangling of URLs containing &. Background context:
// the handoff outlives this click, and cancelling it mid-launch would just fail
// to open the browser.
func openURL(target string) {
	_ = exec.CommandContext(context.Background(), "rundll32", "url.dll,FileProtocolHandler", target).Start()
}

// openBrowser opens the configured server in the default browser.
func (a *app) openBrowser() {
	a.mu.Lock()
	target := a.state.ServerURL
	a.mu.Unlock()
	// Only ever hand the shell an http(s) URL: the value comes from the local
	// config file, and a file:// or ms-settings: value there must not become a
	// shell action just because the tray was clicked.
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return
	}
	openURL(u.String())
}

// limitedBuffer collects an avatar with a hard ceiling, so a hostile or broken
// server cannot make the tray allocate without bound.
type limitedBuffer struct{ data []byte }

const maxAvatarBytes = 4 << 20

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > maxAvatarBytes {
		return 0, errors.New("avatar too large")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
