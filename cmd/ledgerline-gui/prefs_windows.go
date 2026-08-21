//go:build windows

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/clientset"
	"github.com/MalteKiefer/ledgerline-cli/internal/deskintegrate"
	"github.com/MalteKiefer/ledgerline-cli/internal/deskprefs"
	"github.com/MalteKiefer/ledgerline-cli/internal/win32ui"
)

// prefsView is what the General tab renders: this computer's preferences, plus
// the one account setting that belongs on the same page because it is what a
// user looks for there.
type prefsView struct {
	LaunchAtLogin bool     `json:"launchAtLogin"`
	Language      string   `json:"language"`
	Theme         string   `json:"theme"`
	Notify        string   `json:"notify"`
	Paused        bool     `json:"paused"`
	OnBattery     bool     `json:"pauseOnBattery"`
	OnMetered     bool     `json:"pauseOnMetered"`
	ExplorerMenu  bool     `json:"explorerMenu"`
	Exclude       []string `json:"exclude"`
	UploadKBps    int      `json:"uploadKbps"`
	DownloadKBps  int      `json:"downloadKbps"`
	CameraFolder  string   `json:"cameraFolder"`
	CameraDelete  bool     `json:"cameraDelete"`

	// MaxVersions is the server's per-account cap on retained file versions.
	// Zero means the client could not read it — offline, or an older server —
	// and the page then shows the control as unavailable rather than as "none".
	MaxVersions int    `json:"maxVersions"`
	Error       string `json:"error"`
}

func (s *settings) prefs() (prefsView, error) {
	p, err := deskprefs.Load()
	v := prefsView{
		// The registry is the truth for autostart, not the file: a user can
		// remove the Run entry with any tool, and a settings page that then
		// still shows a switch turned on is lying.
		LaunchAtLogin: deskintegrate.LaunchAtLogin(),
		Language:      p.Language,
		Theme:         string(p.Theme),
		Notify:        string(p.Notify),
		Paused:        p.Paused,
		OnBattery:     p.PauseOnBattery,
		OnMetered:     p.PauseOnMetered,
		ExplorerMenu:  deskintegrate.ExplorerMenuRegistered(),
		Exclude:       p.Exclude,
		UploadKBps:    p.UploadKBps,
		DownloadKBps:  p.DownloadKBps,
		CameraFolder:  p.CameraFolder,
		CameraDelete:  p.CameraDelete,
	}
	if err != nil {
		v.Error = err.Error()
		return v, nil
	}
	v.MaxVersions = s.readMaxVersions()
	return v, nil
}

// readMaxVersions asks the server for the version cap. It is best effort: the
// General tab must open on a laptop with no network.
func (s *settings) readMaxVersions() int {
	client, _, err := clientset.Authenticated()
	if err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	got, err := client.Settings(ctx)
	if err != nil || got.FileMaxVersions == nil {
		return 0
	}
	return *got.FileMaxVersions
}

// setPref applies one preference. One binding for the whole page rather than
// one per switch: the page already knows which key it changed, and twenty
// near-identical bindings is twenty places for them to drift apart.
//
// The returned string is a message for the user, empty on success.
func (s *settings) setPref(key string, value string) (string, error) {
	switch key {
	case "launchAtLogin":
		on := value == "true"
		if err := deskintegrate.SetLaunchAtLogin(on); err != nil {
			return "Could not change the startup entry: " + err.Error(), nil
		}
		_, err := deskprefs.Update(func(p *deskprefs.Prefs) { p.LaunchAtLogin = on })
		return errText(err), nil

	case "explorerMenu":
		on := value == "true"
		if err := s.applyExplorerMenu(on); err != nil {
			return "Could not change the Explorer menu: " + err.Error(), nil
		}
		_, err := deskprefs.Update(func(p *deskprefs.Prefs) { p.ExplorerMenu = on })
		return errText(err), nil

	case "language":
		_, err := deskprefs.Update(func(p *deskprefs.Prefs) { p.Language = value })
		if err != nil {
			return errText(err), nil
		}
		// The shell menu's labels are strings in the registry, so they only
		// follow a language change if they are rewritten.
		if deskintegrate.ExplorerMenuRegistered() {
			_ = s.applyExplorerMenu(true)
		}
		return "", nil

	case "theme":
		_, err := deskprefs.Update(func(p *deskprefs.Prefs) { p.Theme = deskprefs.Theme(value) })
		return errText(err), nil
	case "notify":
		_, err := deskprefs.Update(func(p *deskprefs.Prefs) { p.Notify = deskprefs.Notify(value) })
		return errText(err), nil
	case "paused":
		_, err := deskprefs.Update(func(p *deskprefs.Prefs) { p.Paused = value == "true" })
		return errText(err), nil
	case "pauseOnBattery":
		_, err := deskprefs.Update(func(p *deskprefs.Prefs) { p.PauseOnBattery = value == "true" })
		return errText(err), nil
	case "pauseOnMetered":
		_, err := deskprefs.Update(func(p *deskprefs.Prefs) { p.PauseOnMetered = value == "true" })
		return errText(err), nil
	case "cameraDelete":
		_, err := deskprefs.Update(func(p *deskprefs.Prefs) { p.CameraDelete = value == "true" })
		return errText(err), nil

	case "uploadKbps", "downloadKbps":
		n, err := parseRate(value)
		if err != nil {
			return err.Error(), nil
		}
		_, err = deskprefs.Update(func(p *deskprefs.Prefs) {
			if key == "uploadKbps" {
				p.UploadKBps = n
			} else {
				p.DownloadKBps = n
			}
		})
		return errText(err), nil

	case "cameraFolder":
		_, err := deskprefs.Update(func(p *deskprefs.Prefs) { p.CameraFolder = strings.TrimSpace(value) })
		return errText(err), nil

	case "maxVersions":
		return s.setMaxVersions(value), nil
	}
	return "Unknown setting: " + key, nil
}

// applyExplorerMenu registers or removes the shell menu in the current UI
// language.
func (s *settings) applyExplorerMenu(on bool) error {
	if !on {
		return deskintegrate.UnregisterExplorerMenu()
	}
	files, dirs := explorerMenus()
	return deskintegrate.RegisterExplorerMenu(files, dirs)
}

// setMaxVersions changes the account's version cap on the server.
func (s *settings) setMaxVersions(value string) string {
	n, err := parseRate(value)
	if err != nil {
		return err.Error()
	}
	// The server's own bounds. Rejecting here means a clear sentence instead of
	// a 422 rendered as a stack of JSON.
	if n < 1 || n > 200 {
		return "Keep between 1 and 200 versions."
	}
	client, _, err := clientset.Authenticated()
	if err != nil {
		return "Sign in first: this setting lives with your account, not on this computer."
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	if _, err := client.UpdateSettings(ctx, api.UserSettings{FileMaxVersions: &n}); err != nil {
		return "Could not save it on the server: " + firstLine(err.Error())
	}
	return ""
}

// addExclude and removeExclude edit the skip list.
func (s *settings) addExclude(pattern string) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", nil
	}
	// A pattern with a separator in it would never match, because matching is
	// on the base name. Saying so beats silently keeping a dead entry.
	if strings.ContainsAny(pattern, `\/`) {
		return "Patterns match a file or folder name, so they cannot contain a path separator.", nil
	}
	_, err := deskprefs.Update(func(p *deskprefs.Prefs) { p.Exclude = append(p.Exclude, pattern) })
	return errText(err), nil
}

func (s *settings) removeExclude(pattern string) (string, error) {
	_, err := deskprefs.Update(func(p *deskprefs.Prefs) {
		kept := make([]string, 0, len(p.Exclude))
		for _, e := range p.Exclude {
			if !strings.EqualFold(e, pattern) {
				kept = append(kept, e)
			}
		}
		p.Exclude = kept
	})
	return errText(err), nil
}

// pickCameraFolder chooses the folder watched for new photos.
func (s *settings) pickCameraFolder() (string, error) {
	return win32ui.PickFolder(0, "Choose the folder to upload photos from"), nil
}

// parseRate reads a non-negative whole number, treating blank as zero so
// clearing a field means "no limit" rather than an error.
func parseRate(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	var n int
	if _, err := fmt.Sscanf(value, "%d", &n); err != nil || n < 0 {
		return 0, fmt.Errorf("enter a whole number, or leave it empty for no limit")
	}
	return n, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
