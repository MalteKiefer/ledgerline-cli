//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/MalteKiefer/ledgerline-cli/internal/applog"
	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// singleInstance takes a session-wide named mutex and reports whether this
// process is the first one holding it.
//
// Why: the tray can be started from the Start menu, from autostart, and by hand
// while one is already running. Each start adds another icon to the
// notification area, and each runs its own background sync loop over the same
// configuration file — two processes racing on the same folders. The mutex name
// is per user (Local\), so two accounts on one machine still get one tray each.
//
// The handle is intentionally never released: it lives as long as the process,
// and Windows drops it when the process exits, including a crash.
func singleInstance() bool {
	name, err := windows.UTF16PtrFromString("Local\\ledgerline-gui")
	if err != nil {
		return true // cannot name the mutex: do not block the app over it
	}
	if _, err := windows.CreateMutex(nil, false, name); err != nil {
		return !isAlreadyExists(err)
	}
	return true
}

func isAlreadyExists(err error) bool {
	var errno windows.Errno
	if ok := asErrno(err, &errno); !ok {
		return false
	}
	return errno == windows.ERROR_ALREADY_EXISTS
}

func asErrno(err error, out *windows.Errno) bool {
	if e, ok := err.(windows.Errno); ok { //nolint:errorlint // syscall returns the concrete type
		*out = e
		return true
	}
	return false
}

// openLog starts the diary. The installed layout puts it next to the programs,
// in a "logs" directory the installer creates and grants write access to, since
// that is where someone looks for it. A copy running from a build tree or a
// locked-down machine falls back to the per-user configuration directory rather
// than logging nowhere.
func openLog() *applog.Logger {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "logs"))
	}
	if dir, err := config.Dir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "logs"))
	}
	l, _ := applog.OpenFirstWritable(candidates...)
	return l
}

// logDir is where the diary ended up, for the "Open log folder" menu entry.
func logDir(l *applog.Logger) string {
	p := l.Path()
	if strings.TrimSpace(p) == "" {
		return ""
	}
	return filepath.Dir(p)
}
