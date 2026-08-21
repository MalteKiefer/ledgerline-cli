package trayui

import (
	"fmt"
	"time"
)

// SyncPhase is what the folder sync is doing right now. It drives both the tray
// icon and the one line the menu spends on it.
type SyncPhase string

const (
	SyncNone    SyncPhase = ""        // no folders configured
	SyncIdle    SyncPhase = "idle"    // configured, nothing running
	SyncRunning SyncPhase = "running" // at least one pair is syncing
	SyncFailed  SyncPhase = "failed"  // the last run of some pair failed
)

// SyncPair is one configured folder pair, as the menu shows it.
type SyncPair struct {
	Label   string // "C:\docs <-> Docs"
	Running bool
	LastRun time.Time
	Result  string // the last run's summary, or the failure
	Failed  bool
	Paused  bool
}

// SyncState is the whole picture: what is running, and what happened last.
type SyncState struct {
	Phase   SyncPhase
	Running int // pairs syncing right now
	Pairs   []SyncPair
	LastRun time.Time // the most recent finish across all pairs
}

// SyncLine is the single row the tray spends on sync. Everything else lives in
// its submenu, because a tray menu that lists every folder is unreadable at the
// moment you actually need it.
func (s SyncState) SyncLine() string {
	switch s.Phase {
	case SyncNone:
		return "Sync: no folders"
	case SyncRunning:
		if s.Running == 1 {
			return "Sync: syncing 1 folder…"
		}
		return fmt.Sprintf("Sync: syncing %d folders…", s.Running)
	case SyncFailed:
		return "Sync: last run failed"
	default:
		if s.LastRun.IsZero() {
			return "Sync: waiting"
		}
		return "Sync: up to date (" + Ago(s.LastRun) + ")"
	}
}

// SyncDetails is the submenu: one row per pair, newest state first.
func (s SyncState) SyncDetails() []string {
	if len(s.Pairs) == 0 {
		return []string{"No folders configured"}
	}
	rows := make([]string, 0, len(s.Pairs))
	for _, p := range s.Pairs {
		rows = append(rows, syncPairLine(p))
	}
	return rows
}

func syncPairLine(p SyncPair) string {
	switch {
	case p.Running:
		return p.Label + ": syncing…"
	case p.Paused:
		return p.Label + ": paused"
	case p.Failed:
		return p.Label + ": failed, " + firstLine(p.Result)
	case p.LastRun.IsZero():
		return p.Label + ": not run yet"
	default:
		return p.Label + ": " + Ago(p.LastRun)
	}
}

// Ago renders a timestamp the way a person reads a tray: "just now", "4 min
// ago", "2 h ago", then the date once it stops being about today.
func Ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now" // clock skew; claiming the future is worse
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return t.Local().Format("2 Jan 15:04")
	}
}
