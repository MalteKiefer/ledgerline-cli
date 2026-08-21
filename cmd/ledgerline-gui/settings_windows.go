//go:build windows

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/applog"
	"github.com/MalteKiefer/ledgerline-cli/internal/clientset"
	"github.com/MalteKiefer/ledgerline-cli/internal/config"
	"github.com/MalteKiefer/ledgerline-cli/internal/deskui"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
	"github.com/MalteKiefer/ledgerline-cli/internal/syncconfig"
	"github.com/MalteKiefer/ledgerline-cli/internal/trayui"
	"github.com/MalteKiefer/ledgerline-cli/internal/version"
	"github.com/MalteKiefer/ledgerline-cli/internal/win32ui"
)

// runSettingsWindow opens the settings window and blocks until it closes.
//
// Everything the tray used to show in its menu lives here — the avatar, the
// storage figures, the folder list — because a tray menu is read at a glance and
// a settings page is read on purpose. The menu keeps two summary rows and hands
// the rest over.
func runSettingsWindow(log *applog.Logger, onSignOut func()) {
	s := &settings{log: log, onSignOut: onSignOut}
	if err := deskui.Run(deskui.Options{
		Title:    "Ledgerline settings",
		Width:    820,
		Height:   660,
		Body:     settingsBody,
		Script:   settingsScript,
		Bindings: s.bindings(),
	}); err != nil && log != nil {
		log.Printf("settings window: %v", err)
	}
}

type settings struct {
	log       *applog.Logger
	onSignOut func()
}

func (s *settings) bindings() []deskui.Binding {
	return []deskui.Binding{
		{Name: "loadProfile", Func: s.profile},
		{Name: "signOut", Func: s.signOut},
		{Name: "loadPairs", Func: s.pairs},
		{Name: "savePair", Func: s.savePair},
		{Name: "removePair", Func: s.removePair},
		{Name: "togglePair", Func: s.togglePair},
		{Name: "runPairs", Func: s.runPairs},
		{Name: "pickLocalFolder", Func: s.pickLocal},
		{Name: "remoteFolderList", Func: remoteFolders},
		{Name: "loadAbout", Func: s.about},
		{Name: "openPath", Func: s.openPath},
		{Name: "openWebApp", Func: s.openWebApp},

		// General tab.
		{Name: "loadPrefs", Func: s.prefs},
		{Name: "setPref", Func: s.setPref},
		{Name: "addExclude", Func: s.addExclude},
		{Name: "removeExclude", Func: s.removeExclude},
		{Name: "pickCameraFolder", Func: s.pickCameraFolder},

		// Photos tab.
		{Name: "loadGallery", Func: s.gallery},
		{Name: "pickPhotos", Func: s.pickPhotos},
		{Name: "pickPhotoFolder", Func: s.pickPhotoFolder},
		{Name: "sendPhotos", Func: s.sendPhotos},
		{Name: "sendPhotoFolder", Func: s.sendPhotoFolder},
	}
}

// ---------------------------------------------------------------- profile --

// profileView is what the Profile tab renders. Every field is optional on
// purpose: being offline is not the same as being signed out, so the page shows
// what the stored session knows and says why the rest is missing.
type profileView struct {
	SignedIn bool   `json:"signedIn"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Server   string `json:"server"`
	Avatar   string `json:"avatar"` // data: URI, or empty for initials
	Initials string `json:"initials"`

	Used    int64  `json:"used"`
	Quota   int64  `json:"quota"` // 0 means unlimited
	Files   int64  `json:"files"`
	Gallery int64  `json:"gallery"`
	Split   bool   `json:"split"` // whether the server reported the breakdown
	Error   string `json:"error"`
}

func (s *settings) profile() (profileView, error) {
	client, sess, err := clientset.Authenticated()
	if err != nil {
		return profileView{Error: "Not signed in. Use “Sign in…” in the tray menu."}, nil
	}

	v := profileView{
		SignedIn: true,
		Name:     sess.UserName,
		Email:    sess.UserEmail,
		Server:   trayui.ServerHost(sess.ServerURL),
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	user, usage, _, err := client.Me(ctx)
	if err != nil {
		v.Error = "Could not reach the server: " + firstLine(err.Error())
		v.Initials = initials(v.Name, v.Email)
		return v, nil
	}

	v.Name = user.Name
	if strings.TrimSpace(v.Name) == "" {
		v.Name = user.Email
	}
	v.Email = user.Email
	v.Initials = initials(v.Name, v.Email)
	v.Used = usage.Total()
	if usage.Quota != nil {
		v.Quota = *usage.Quota
	}
	if usage.Files != nil {
		v.Files = *usage.Files
	}
	if usage.Gallery != nil {
		v.Gallery = *usage.Gallery
	}
	v.Split = usage.HasBreakdown()
	v.Avatar = avatarDataURI(ctx, client, user)
	return v, nil
}

// avatarDataURI fetches the avatar and inlines it. The page cannot reach the
// network — its content policy forbids it — so the bytes come through here or
// not at all, and the sniffed type keeps a mislabelled response from being
// declared as something it is not.
func avatarDataURI(ctx context.Context, client *api.Client, user api.User) string {
	if !user.HasAvatar {
		return ""
	}
	data := fetchAvatar(ctx, client)
	if len(data) == 0 {
		return ""
	}
	kind := http.DetectContentType(data)
	if !strings.HasPrefix(kind, "image/") {
		return ""
	}
	return "data:" + kind + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// initials is the fallback mark: the first letters of the name, or of the
// address when there is no name.
func initials(name, email string) string {
	source := strings.TrimSpace(name)
	if source == "" {
		source = strings.TrimSpace(email)
	}
	fields := strings.FieldsFunc(source, func(r rune) bool {
		return r == ' ' || r == '.' || r == '_' || r == '-' || r == '@'
	})
	out := make([]rune, 0, 2)
	for _, f := range fields {
		if len(out) == 2 {
			break
		}
		out = append(out, []rune(strings.ToUpper(f))[0])
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

func (s *settings) signOut() (string, error) {
	sess, err := session.Load()
	if err != nil || sess.Token == "" {
		return "You are not signed in.", nil
	}
	if err := session.Clear(); err != nil {
		return "Could not sign out: " + err.Error(), nil
	}
	if s.log != nil {
		s.log.Printf("signed out from the settings window")
	}
	if s.onSignOut != nil {
		s.onSignOut()
	}
	return "", nil
}

// ------------------------------------------------------------------ pairs --

// pairView is one row of the folder list, already rendered for display so the
// page has no rules of its own to keep in step with the CLI's.
type pairView struct {
	ID        string `json:"id"`
	Local     string `json:"local"`
	Remote    string `json:"remote"`
	Direction string `json:"direction"`
	Conflict  string `json:"conflict"`
	Interval  int    `json:"interval"`
	Watch     bool   `json:"watch"`
	Enabled   bool   `json:"enabled"`
	Schedule  string `json:"schedule"`
	Last      string `json:"last"`
	Failed    bool   `json:"failed"`
	Running   bool   `json:"running"`
}

func (s *settings) pairs() ([]pairView, error) {
	f, err := syncconfig.Load()
	if err != nil {
		return nil, err
	}
	out := make([]pairView, 0, len(f.Pairs))
	for _, p := range f.Pairs {
		v := pairView{
			ID: p.ID, Local: p.Local, Remote: p.Remote,
			Direction: p.Direction, Conflict: p.Conflict,
			Interval: p.IntervalMinutes, Watch: p.Watch, Enabled: p.Enabled,
			Schedule: scheduleLine(p), Running: isRunning(p.ID),
		}
		switch {
		case p.LastFailure != "":
			v.Last, v.Failed = firstLine(p.LastFailure), true
		default:
			v.Last = p.LastResult
		}
		out = append(out, v)
	}
	return out, nil
}

// scheduleLine says when a pair runs, in words rather than in the CLI's compact
// notation: the list is read, not parsed.
func scheduleLine(p syncconfig.Pair) string {
	var parts []string
	if p.IntervalMinutes > 0 {
		parts = append(parts, fmt.Sprintf("every %s", plural(p.IntervalMinutes, "minute", "minutes")))
	}
	if p.Watch {
		parts = append(parts, "on change")
	}
	if len(parts) == 0 {
		return "manual only"
	}
	return strings.Join(parts, " and ")
}

// savePair adds or updates one pair. An empty id means a new one.
func (s *settings) savePair(id, local, remote, direction, conflict string, interval int, watch bool) (string, error) {
	if interval < 0 {
		return "The interval must be zero or more minutes.", nil
	}
	if interval == 0 && !watch {
		return "With no interval and no change detection this folder would never sync on its own.", nil
	}

	draft := syncconfig.Pair{
		ID:              id,
		Local:           strings.TrimSpace(local),
		Remote:          strings.TrimSpace(remote),
		Direction:       direction,
		Conflict:        conflict,
		IntervalMinutes: interval,
		Watch:           watch,
		Enabled:         true,
	}
	if _, err := syncconfig.Normalise(draft); err != nil {
		return err.Error(), nil
	}

	if id == "" {
		if _, err := syncconfig.Add(draft); err != nil {
			return err.Error(), nil
		}
		return "", nil
	}
	_, err := syncconfig.Update(id, func(t *syncconfig.Pair) {
		t.Remote = draft.Remote
		t.Direction = draft.Direction
		t.Conflict = draft.Conflict
		t.IntervalMinutes = draft.IntervalMinutes
		t.Watch = draft.Watch
	})
	if err != nil {
		return err.Error(), nil
	}
	return "", nil
}

func (s *settings) removePair(id string) (string, error) {
	if err := syncconfig.Remove(id); err != nil {
		return err.Error(), nil
	}
	return "", nil
}

func (s *settings) togglePair(id string) (string, error) {
	if _, err := syncconfig.Update(id, func(t *syncconfig.Pair) { t.Enabled = !t.Enabled }); err != nil {
		return err.Error(), nil
	}
	return "", nil
}

// runPairs syncs the given pairs, or every enabled one when the list is empty.
// It blocks: the page shows a spinner and the promise resolving is the signal
// that it is done.
func (s *settings) runPairs(ids []string) (string, error) {
	f, err := syncconfig.Load()
	if err != nil {
		return err.Error(), nil
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	var due []syncconfig.Pair
	for _, p := range f.Pairs {
		if len(wanted) == 0 && !p.Enabled {
			continue
		}
		if len(wanted) > 0 && !wanted[p.ID] {
			continue
		}
		due = append(due, p)
	}
	if len(due) == 0 {
		return "Nothing to sync.", nil
	}
	return syncPairs(context.Background(), due), nil
}

// pickLocal opens the shell's folder picker. It is the one piece of native UI
// left: reimplementing the file dialog in a page would be worse in every way.
func (s *settings) pickLocal(current string) (string, error) {
	_ = current // the shell picker has no notion of a starting folder here
	return win32ui.PickFolder(0, "Choose a folder to keep in sync"), nil
}

// ------------------------------------------------------------------ about --

type aboutView struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	Built      string `json:"built"`
	Go         string `json:"go"`
	ConfigDir  string `json:"configDir"`
	LogDir     string `json:"logDir"`
	Executable string `json:"executable"`
}

func (s *settings) about() (aboutView, error) {
	v := version.Current()
	a := aboutView{
		Version: v.Version,
		Commit:  v.Commit,
		Built:   v.BuildDate,
		Go:      runtime.Version(),
		LogDir:  logDir(s.log),
	}
	if dir, err := config.Dir(); err == nil {
		a.ConfigDir = dir
	}
	if exe, err := executablePath(); err == nil {
		a.Executable = filepath.Dir(exe)
	}
	return a, nil
}

// openPath opens a folder in Explorer or a URL in the browser. The page cannot
// navigate anywhere itself, so every outward jump comes through here — which is
// also the only place that decides what counts as a safe target.
func (s *settings) openPath(kind, target string) error {
	switch kind {
	case "folder":
		openFolder(target)
	case "url":
		openBrowserURL(target)
	}
	return nil
}

// openWebApp opens the signed-in server in the browser. The page has no idea
// what the server is until the profile loads, so the target is resolved here.
func (s *settings) openWebApp(module string) error {
	sess, err := session.Load()
	if err != nil || strings.TrimSpace(sess.ServerURL) == "" {
		return nil
	}
	url := strings.TrimSuffix(sess.ServerURL, "/")
	// Only the module names this client knows: passing a path through from the
	// page would make the button a way to open any URL.
	switch module {
	case "gallery", "files", "settings":
		url += "/" + module
	}
	openBrowserURL(url)
	return nil
}

// openFolder shows a directory in Explorer.
func openFolder(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	_ = startProcess("explorer.exe", dir)
}

// remoteFolders returns every folder path on the server, sorted, so the picker
// can show the tree as a flat list of paths.
func remoteFolders() ([]string, error) {
	client, _, err := clientset.Authenticated()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	folders, _, _, err := client.FilesData(ctx)
	if err != nil {
		return nil, err
	}

	byID := make(map[int64]api.FileFolder, len(folders))
	for _, f := range folders {
		byID[f.ID] = f
	}
	paths := make([]string, 0, len(folders))
	for _, f := range folders {
		paths = append(paths, folderPath(byID, f.ID))
	}
	sort.Strings(paths)
	return paths, nil
}

// folderPath walks parent links to build a slash path.
func folderPath(byID map[int64]api.FileFolder, id int64) string {
	var parts []string
	seen := map[int64]bool{}
	for id != 0 && !seen[id] {
		seen[id] = true
		f, ok := byID[id]
		if !ok {
			break
		}
		parts = append([]string{f.Name}, parts...)
		if f.ParentID == nil {
			break
		}
		id = *f.ParentID
	}
	return strings.Join(parts, "/")
}

// firstLine trims a multi-line error to something a single row can hold.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	const max = 90
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

// executablePath is os.Executable with the symlink resolved, so About shows
// where the program actually lives rather than where it was invoked from.
func executablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}
