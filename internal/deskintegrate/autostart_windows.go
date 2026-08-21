//go:build windows

// Package deskintegrate wires the desktop client into the shell: launch at
// login, and the Explorer context menu.
//
// Everything here writes under HKEY_CURRENT_USER. That is a deliberate limit,
// not an oversight: a per-user registration needs no elevation, cannot affect
// anyone else who signs in to the machine, and is removed by uninstalling for
// that user. A machine-wide equivalent would need an administrator every time
// somebody ticked a checkbox.
package deskintegrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// runKey is where Windows looks for per-user programs to start at sign-in.
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// runValue is our entry's name. Stable, so toggling the setting replaces the
// entry rather than accumulating one per version.
const runValue = "Ledgerline"

// SetLaunchAtLogin adds or removes this executable from the per-user Run key.
//
// The path is quoted: Program Files has a space in it, and an unquoted path is
// the classic way a Run entry silently launches the wrong thing.
func SetLaunchAtLogin(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key: %w", err)
	}
	defer k.Close()

	if !on {
		err := k.DeleteValue(runValue)
		if err != nil && !strings.Contains(err.Error(), "cannot find") {
			return fmt.Errorf("remove Run entry: %w", err)
		}
		return nil
	}

	exe, err := trayExecutable()
	if err != nil {
		return err
	}
	if err := k.SetStringValue(runValue, `"`+exe+`"`); err != nil {
		return fmt.Errorf("write Run entry: %w", err)
	}
	return nil
}

// LaunchAtLogin reports whether the Run entry is present and points at this
// installation. A stale entry left by a copy that was moved or deleted counts
// as off, because that is what it does in practice.
func LaunchAtLogin() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()

	value, _, err := k.GetStringValue(runValue)
	if err != nil {
		return false
	}
	target := strings.Trim(strings.TrimSpace(value), `"`)
	if _, err := os.Stat(target); err != nil {
		return false
	}
	exe, err := trayExecutable()
	if err != nil {
		return true // it points at something that exists; assume it is us
	}
	return strings.EqualFold(target, exe)
}

// trayExecutable is the tray program's path.
//
// It resolves from whatever is running, because both the tray and the CLI may
// be the caller — the CLI toggling the setting must still register the tray,
// not itself.
func trayExecutable() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if strings.EqualFold(filepath.Base(self), trayName) {
		return self, nil
	}
	tray := filepath.Join(filepath.Dir(self), trayName)
	if _, err := os.Stat(tray); err != nil {
		return "", fmt.Errorf("%s is not next to %s: %w", trayName, filepath.Base(self), err)
	}
	return tray, nil
}

const trayName = "ledgerline-gui.exe"
