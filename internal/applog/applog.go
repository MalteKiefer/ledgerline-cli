// Package applog writes the desktop client's diary: what it refreshed, what it
// synced, and what went wrong when nothing visible happened.
//
// A tray application fails quietly by nature. There is no console to print to,
// and a menu can only carry one line of status, so without a file on disk a
// failed background sync at 03:00 leaves no trace at all. This is that file.
//
// Where it goes: the directory the caller names — the installer creates
// "logs" beside the programs and grants the users on the machine write access
// there, because that is where people look first. If that directory cannot be
// written (a copy unpacked into Program Files by hand, a locked-down machine),
// logging falls back to the per-user configuration directory rather than
// failing the operation it was meant to record.
//
// What it must never contain: the bearer token, a password, or a two-factor
// code. Callers pass what happened, not what was sent.
package applog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxSize is when the current file is rotated, and keep is how many old files
// survive. Small on purpose: this is a diary, not an archive, and it sits in a
// directory the user did not ask to grow.
const (
	maxSize = 2 << 20 // 2 MiB
	keep    = 3
)

// Logger appends timestamped lines to a file, rotating it when it grows.
// The zero value is a no-op logger, so a caller that never opened one can log
// unconditionally.
type Logger struct {
	mu   sync.Mutex
	file *os.File
	path string
	size int64
}

// Open creates dir if needed and opens (or continues) ledgerline.log inside it.
// On failure it returns a no-op logger and the error: a client that cannot
// write its diary must still run.
func Open(dir string) (*Logger, error) {
	if strings.TrimSpace(dir) == "" {
		return &Logger{}, fmt.Errorf("applog: no directory given")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &Logger{}, err
	}
	path := filepath.Join(dir, "ledgerline.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return &Logger{}, err
	}
	var size int64
	if info, serr := f.Stat(); serr == nil {
		size = info.Size()
	}
	return &Logger{file: f, path: path, size: size}, nil
}

// OpenFirstWritable tries each directory in order and returns a logger for the
// first one that accepts a file, plus the directory it settled on. It never
// returns nil.
func OpenFirstWritable(dirs ...string) (*Logger, string) {
	var lastErr error
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		l, err := Open(dir)
		if err == nil {
			return l, dir
		}
		lastErr = err
	}
	_ = lastErr // nothing to report to: logging is the thing that failed
	return &Logger{}, ""
}

// Path is where the log is being written, or "" for a no-op logger.
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Printf appends one line. A nil or unopened logger discards it.
func (l *Logger) Printf(format string, args ...any) {
	if l == nil || l.file == nil {
		return
	}
	line := fmt.Sprintf(format, args...)
	line = strings.ReplaceAll(strings.TrimRight(line, "\r\n"), "\n", " ")
	stamped := time.Now().Format("2006-01-02 15:04:05") + "  " + line + "\n"

	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := io.WriteString(l.file, stamped)
	if err != nil {
		return
	}
	l.size += int64(n)
	if l.size >= maxSize {
		l.rotate()
	}
}

// Close releases the file.
func (l *Logger) Close() {
	if l == nil || l.file == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.file.Close()
	l.file = nil
}

// rotate renames the current file aside and starts a new one. Called with the
// lock held. Any failure leaves the existing file in place: losing the log is
// better than losing the process.
func (l *Logger) rotate() {
	_ = l.file.Close()
	l.file = nil

	stamp := time.Now().Format("20060102-150405")
	rotated := strings.TrimSuffix(l.path, ".log") + "-" + stamp + ".log"
	if err := os.Rename(l.path, rotated); err != nil {
		// Could not rename (file locked by a viewer, for instance): reopen and
		// keep appending rather than dropping every later line.
		if f, oerr := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); oerr == nil {
			l.file = f
		}
		return
	}
	pruneOld(filepath.Dir(l.path))

	if f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		l.file = f
		l.size = 0
	}
}

// pruneOld keeps the newest `keep` rotated files and deletes the rest.
func pruneOld(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var rotated []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "ledgerline-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		rotated = append(rotated, name)
	}
	if len(rotated) <= keep {
		return
	}
	sort.Strings(rotated) // the timestamp suffix sorts chronologically
	for _, name := range rotated[:len(rotated)-keep] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}
