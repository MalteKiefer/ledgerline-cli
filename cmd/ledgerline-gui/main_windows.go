//go:build windows

// Command ledgerline-gui is the desktop companion to the CLI: a tray icon that
// shows who is signed in, which server, and how much storage is used, with
// sign-in and sign-out from the menu.
//
// It shares the CLI's session, certificate pins and API client, so both see the
// same credential and the same transport guarantees. Sign-in and the folder-sync
// list are native windows of its own; nothing is handed to a console.
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
	"github.com/MalteKiefer/ledgerline-cli/internal/applog"
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

// detailSlots is how many rows each submenu reserves. systray cannot remove an
// item once added, so the renderer fills fixed slots and hides the rest.
// Account needs e-mail, server, files, gallery and total; sync needs one per
// folder pair, and five is more folders than a tray menu should list before the
// window is the better answer.
const detailSlots = 5

func main() {
	// One tray per user. Two instances mean two icons and two background sync
	// loops racing on the same configuration file.
	if !singleInstance() {
		return
	}

	log := openLog()
	defer log.Close()
	log.Printf("tray started, version %s", version.Version)

	a := newApp(log)
	systray.Run(a.onReady, func() { log.Printf("tray stopped") })
}

// app owns the menu items and the goroutine that refreshes them.
type app struct {
	mu    sync.Mutex
	state trayui.State

	title    *systray.MenuItem
	account  *systray.MenuItem
	details  []*systray.MenuItem
	syncRow  *systray.MenuItem
	syncSub  []*systray.MenuItem
	status   *systray.MenuItem
	login    *systray.MenuItem
	settings *systray.MenuItem
	logout   *systray.MenuItem
	openWeb  *systray.MenuItem
	openLog  *systray.MenuItem
	refresh  *systray.MenuItem
	quit     *systray.MenuItem

	log *applog.Logger

	// loginBusy keeps a second sign-in dialog from opening over the first.
	loginBusy atomic.Bool
	// syncBusy does the same for the folder window, which writes the same file.
	syncBusy atomic.Bool
}

func newApp(log *applog.Logger) *app {
	return &app{state: trayui.State{Version: version.Version}, log: log}
}

func (a *app) onReady() {
	systray.SetIcon(trayui.BrandIcon(false))
	systray.SetTitle("Ledgerline")
	systray.SetTooltip("Ledgerline")

	a.title = systray.AddMenuItem("Ledgerline", "")
	// The same mark as the tray icon and the executables, so the menu is
	// recognisably this program and not an unlabelled row of text.
	a.title.SetIcon(trayui.BrandIcon(true))
	systray.AddSeparator()

	// Two summary rows, each opening a submenu, instead of a stack of figures:
	// a tray menu is read at a glance, and the detail is one hover away.
	//
	// Nothing here is disabled. A disabled item renders grey, which Windows
	// means as "unavailable" — wrong for a row whose whole job is to be read.
	// They simply have no click handler.
	a.account = systray.AddMenuItem("", "Account and storage")
	for range detailSlots {
		item := a.account.AddSubMenuItem("", "")
		item.Hide()
		a.details = append(a.details, item)
	}
	a.syncRow = systray.AddMenuItem("", "Folder sync")
	for range detailSlots {
		item := a.syncRow.AddSubMenuItem("", "")
		item.Hide()
		a.syncSub = append(a.syncSub, item)
	}
	a.status = systray.AddMenuItem("", "")
	a.status.Hide()
	systray.AddSeparator()

	a.login = systray.AddMenuItem("Sign in…", "Sign in to a Ledgerline server")
	a.settings = systray.AddMenuItem("Settings…", "Profile, synced folders and program information")
	a.openWeb = systray.AddMenuItem("Open web app", "Open the server in your browser")
	a.logout = systray.AddMenuItem("Sign out", "Revoke this device's token")
	systray.AddSeparator()
	a.refresh = systray.AddMenuItem("Refresh", "Re-read identity and storage now")
	a.openLog = systray.AddMenuItem("Open log folder", "Show what this program has been doing")
	a.quit = systray.AddMenuItem("Quit", "Close the tray icon")

	// The folder pairs run whether or not their window is open; that is the
	// point of a tray application.
	startSyncRunner(context.Background(), a.log, a.syncChanged)

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
		case <-a.settings.ClickedCh:
			a.openSettingsWindow()
		case <-a.logout.ClickedCh:
			a.doLogout()
		case <-a.openWeb.ClickedCh:
			a.openBrowser()
		case <-a.refresh.ClickedCh:
			a.reload()
		case <-a.openLog.ClickedCh:
			a.showLogFolder()
		case <-ticker.C:
			a.reload()
		case <-a.quit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// reload re-reads the session and, when there is one, the account state.
// syncChanged repaints the menu from the current state after a pair starts or
// finishes, so "syncing…" appears while it is true rather than at the next
// scheduled refresh.
func (a *app) syncChanged() {
	a.mu.Lock()
	state := a.state
	a.mu.Unlock()
	a.setState(state)
}

func (a *app) reload() {
	state := trayui.State{Version: version.Version}

	client, sess, err := clientset.Authenticated()
	switch {
	case errors.Is(err, clientset.ErrNotAuthenticated):
		a.setState(state)
		return
	case err != nil:
		a.log.Printf("refresh failed before any request: %v", err)
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
		a.log.Printf("signed out: the server rejected this device's token")
		_ = session.Clear()
		a.setState(trayui.State{Version: version.Version})
		return
	case err != nil:
		a.log.Printf("refresh failed: %v", err)
		state.Err = err
		state.Unreachable = api.Status(err) == 0 // no HTTP status: transport failed
		a.setState(state)
		return
	}
	if wipe {
		// Remote kill switch, same contract as the CLI.
		a.log.Printf("remote wipe requested: clearing local state")
		_ = session.WipeLocal()
		a.setState(trayui.State{Version: version.Version})
		return
	}

	state.UserName = user.Name
	state.UserEmail = user.Email
	state.Usage = usage
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
	a.state = s
	a.mu.Unlock()

	// The folder-sync picture is read at paint time: it changes on its own
	// schedule, independently of the /me refresh this state came from.
	s.Sync = syncState()

	m := trayui.Build(s)
	systray.SetIcon(trayui.StateIcon(iconState(m)))
	systray.SetTooltip(m.Tooltip)
	a.title.SetTitle(m.Title)

	setVisible(a.account, m.ShowAccount)
	if m.ShowAccount {
		a.account.SetTitle(m.Account)
	}
	fillSlots(a.details, m.AccountDetails)

	setVisible(a.syncRow, m.ShowSyncRow)
	if m.ShowSyncRow {
		a.syncRow.SetTitle(m.SyncSummary)
	}
	fillSlots(a.syncSub, m.SyncDetails)

	setVisible(a.status, m.ShowStatus)
	if m.ShowStatus {
		a.status.SetTitle(m.Status)
	}

	setVisible(a.login, m.ShowLogin)
	setVisible(a.settings, m.ShowSync)
	setVisible(a.logout, m.ShowLogout)
	setVisible(a.openWeb, m.ShowOpenWeb)
	setVisible(a.refresh, m.ShowRefresh)

}

// iconState maps the rendered menu onto the three tray icons.
func iconState(m trayui.Model) trayui.IconState {
	switch {
	case m.Offline:
		return trayui.IconMuted
	case m.Busy:
		return trayui.IconBusy
	default:
		return trayui.IconIdle
	}
}

// fillSlots writes rows into the fixed submenu items and hides the remainder.
func fillSlots(slots []*systray.MenuItem, rows []string) {
	for i, item := range slots {
		if i < len(rows) {
			item.SetTitle(rows[i])
			item.Show()
			continue
		}
		item.Hide()
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

// openSyncWindow shows the folder list. Like the sign-in dialog it runs on its
// own OS thread, so a long list or a running sync never freezes the tray.
func (a *app) openSettingsWindow() {
	if !a.syncBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.syncBusy.Store(false)
		runSettingsWindow(a.log, a.reload)
	}()
}

// showLogFolder opens the directory the diary is being written to. When there
// is none — nothing writable was found — the menu entry says so rather than
// opening a window onto nothing.
func (a *app) showLogFolder() {
	dir := logDir(a.log)
	if dir == "" {
		a.setState(trayui.State{Version: version.Version, Err: errors.New("no writable log directory")})
		return
	}
	openFolder(dir)
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
	_ = startProcess("rundll32", "url.dll,FileProtocolHandler", target)
}

// startProcess launches a program with an argv array — no shell parses it, so a
// path or URL from the local configuration cannot become a command.
func startProcess(name string, args ...string) error {
	return exec.CommandContext(context.Background(), name, args...).Start()
}

// openBrowserURL opens an http(s) address. Anything else is refused: the value
// comes from a local config file, and a file:// or ms-settings: value there must
// not turn a button into a shell action.
func openBrowserURL(target string) {
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return
	}
	openURL(u.String())
}

// openBrowser opens the configured server in the default browser.
func (a *app) openBrowser() {
	a.mu.Lock()
	target := a.state.ServerURL
	a.mu.Unlock()
	openBrowserURL(target)
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
