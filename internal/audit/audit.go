// Package audit writes a local, append-only JSON-lines audit trail of every
// operation the CLI performs. It is LOCAL ONLY — never shipped, never phoned
// home — and records operation metadata, never secrets: no token, credential, or
// file content ever reaches it.
//
// The log is one JSON object per line at <config>/audit.log, 0600 in a 0700 dir,
// size-rotated to a single .1 backup so it stays bounded. Logging is best-effort:
// a logging failure never breaks the command that triggered it.
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// maxSize is the byte threshold at which audit.log is rotated to audit.log.1.
// One backup is kept, bounding the trail to ~2×maxSize on disk.
const maxSize = 5 << 20 // 5 MiB

// Outcome is the result of an audited operation.
const (
	OutcomeStart = "start"
	OutcomeOK    = "ok"
	OutcomeError = "error"
)

// Event is one audited operation. Every field is operation METADATA — the struct
// deliberately has no field that could carry a secret (key/token/passphrase) or
// decrypted content. Callers must never place such material in Detail or Error.
type Event struct {
	Time     string `json:"ts"`                    // RFC3339 UTC
	Event    string `json:"event"`                 // stable key, e.g. "gallery.upload", "cmd:auth login"
	Outcome  string `json:"outcome"`               // start | ok | error
	Target   string `json:"target,omitempty"`      // coarse target (module / server host) — no secrets
	Count    int    `json:"count,omitempty"`       // item count, when meaningful
	Duration int64  `json:"duration_ms,omitempty"` // wall-clock ms for a completed op
	Detail   string `json:"detail,omitempty"`      // short, generic note — no secrets
	Error    string `json:"error,omitempty"`       // generic error text — no crypto internals
	PID      int    `json:"pid"`
}

// Logger appends events to a rotated JSONL file. A nil *Logger is valid and
// silently discards every event, so callers never nil-check.
type Logger struct {
	mu   sync.Mutex
	path string
}

// New opens (creating the 0700 dir if needed) an audit logger at <dir>/audit.log.
// It never fails hard: on any error it returns a nil Logger that discards events,
// so auditing can never break a command.
func New(dir string) *Logger {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	return &Logger{path: filepath.Join(dir, "audit.log")}
}

// nowUTC is a var so tests can pin the clock.
var nowUTC = func() time.Time { return time.Now().UTC() }

// Log appends one event (best-effort). It stamps the time and pid, rotates the
// file if it has grown past maxSize, and writes a single JSON line. Any failure
// is swallowed — the audit trail must never break the operation it records.
func (l *Logger) Log(e Event) {
	if l == nil {
		return
	}
	e.Time = nowUTC().Format(time.RFC3339)
	e.PID = os.Getpid()

	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	l.rotateIfNeeded(len(line))

	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(line)
}

// rotateIfNeeded renames audit.log to audit.log.1 (overwriting the previous
// backup) when the next write would push it past maxSize. Caller holds l.mu.
func (l *Logger) rotateIfNeeded(next int) {
	info, err := os.Stat(l.path)
	if err != nil || info.Size()+int64(next) <= maxSize {
		return
	}
	_ = os.Rename(l.path, l.path+".1")
}

// Purge removes the audit log and its rotated backup (used by logout / a purge
// command). Missing files are not an error.
func Purge(dir string) error {
	if dir == "" {
		return nil
	}
	base := filepath.Join(dir, "audit.log")
	_ = os.Remove(base + ".1")
	if err := os.Remove(base); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
