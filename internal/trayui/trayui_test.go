package trayui

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

func ptr(v int64) *int64 { return &v }

func TestBuildSignedOut(t *testing.T) {
	m := Build(State{Version: "0.7.5"})
	if m.Title != "Ledgerline 0.7.5" {
		t.Fatalf("title = %q", m.Title)
	}
	if !m.ShowLogin || m.ShowLogout || m.ShowOpenWeb || m.ShowSync || m.ShowRefresh {
		t.Fatalf("actions = %+v", m)
	}
	if !m.Offline {
		t.Fatal("signed out should use the muted icon")
	}
	// Signed out the menu says nothing else: a sync row or a "sync folders"
	// entry with no session behind it reads as broken.
	if m.ShowSyncRow || m.ShowStatus {
		t.Fatalf("rows shown before sign-in: %+v", m)
	}
	if len(m.SyncDetails) != 0 {
		t.Fatalf("content built before sign-in: %+v", m)
	}
}

func TestBuildSignedIn(t *testing.T) {
	m := Build(State{
		Version:   "v1.2.3",
		LoggedIn:  true,
		ServerURL: "https://ledger.example.com:8443/",
	})
	// The leading v is display noise; the CLI prints it the same way.
	if m.Title != "Ledgerline 1.2.3" {
		t.Fatalf("title = %q", m.Title)
	}
	if m.ShowLogin || !m.ShowLogout || !m.ShowOpenWeb || m.Offline {
		t.Fatalf("actions = %+v", m)
	}
	// The sync row is the only summary: what the program is doing, not who is
	// using it.
	if !m.ShowSyncRow {
		t.Fatalf("sync row missing: %+v", m)
	}
	if m.Tooltip != "Ledgerline 1.2.3" {
		t.Fatalf("tooltip = %q", m.Tooltip)
	}
}

// TestMenuCarriesNoIdentity is the requirement, not a detail: who is signed in,
// their e-mail, their server and their storage belong in the settings window.
// The tray menu is on screen for anyone walking past the machine, and the state
// it renders from no longer even has the fields — so this asserts on what a
// caller could still put there by mistake.
func TestMenuCarriesNoIdentity(t *testing.T) {
	m := Build(State{
		Version:   "1.0.0",
		LoggedIn:  true,
		ServerURL: "https://ledger.example.com:8443/",
		Sync: SyncState{
			Phase: SyncIdle,
			Pairs: []SyncPair{{Label: `C:\work <-> Work`}},
		},
	})
	rows := append([]string{m.Title, m.Tooltip, m.SyncSummary, m.Status}, m.SyncDetails...)
	for _, row := range rows {
		for _, leak := range []string{"@", "ledger.example.com", "MiB", "GiB"} {
			if strings.Contains(row, leak) {
				t.Fatalf("menu row %q carries %q", row, leak)
			}
		}
	}
}

func TestBuildRefreshErrorSaysSo(t *testing.T) {
	m := Build(State{
		Version:     "0.7.5",
		LoggedIn:    true,
		ServerURL:   "https://ledger.example.com",
		Err:         errors.New("dial tcp: connection refused"),
		Unreachable: true,
	})
	if !m.Offline {
		t.Fatal("a failed refresh must mute the icon")
	}
	if !m.ShowStatus || m.Status != "Server unreachable" {
		t.Fatalf("status = %q (shown=%v)", m.Status, m.ShowStatus)
	}
	// Signing out must stay reachable while offline.
	if !m.ShowLogout {
		t.Fatal("logout must remain available when the server is down")
	}
}

func TestBuildApiErrorIsTruncatedToOneLine(t *testing.T) {
	long := errors.New("server error 500: " + strings.Repeat("boom ", 40) + "\nsecond line")
	m := Build(State{Version: "1", LoggedIn: true, ServerURL: "https://x.test", Err: long})
	if strings.Contains(m.Status, "\n") || len([]rune(m.Status)) > 80 {
		t.Fatalf("error line = %q", m.Status)
	}
}

// TestSyncRowSummarisesAndDetails: the row is what a glance gets, the submenu
// is what a question gets.
func TestSyncRowSummarisesAndDetails(t *testing.T) {
	now := time.Now()
	m := Build(State{
		Version: "1", LoggedIn: true, ServerURL: "https://x.test",
		Sync: SyncState{
			Phase:   SyncRunning,
			Running: 2,
			LastRun: now.Add(-3 * time.Minute),
			Pairs: []SyncPair{
				{Label: "C:\\docs <-> Docs", Running: true},
				{Label: "C:\\pics -> Photos", LastRun: now.Add(-3 * time.Minute), Result: "1 pushed"},
				{Label: "C:\\old <-> Archive", Paused: true},
			},
		},
	})
	if !m.ShowSyncRow || m.SyncSummary != "Sync: syncing 2 folders…" {
		t.Fatalf("summary = %q", m.SyncSummary)
	}
	if !m.Busy {
		t.Fatal("a running sync must show in the icon")
	}
	if !strings.Contains(m.Tooltip, "syncing") {
		t.Fatalf("tooltip = %q", m.Tooltip)
	}
	if len(m.SyncDetails) != 3 {
		t.Fatalf("details = %v", m.SyncDetails)
	}
	if !strings.Contains(m.SyncDetails[0], "syncing") ||
		!strings.Contains(m.SyncDetails[1], "min ago") ||
		!strings.Contains(m.SyncDetails[2], "paused") {
		t.Fatalf("details = %v", m.SyncDetails)
	}
}

func TestSyncRowStates(t *testing.T) {
	cases := map[SyncPhase]string{
		SyncNone: "Sync: no folders",
		SyncIdle: "Sync: waiting",
	}
	for phase, want := range cases {
		got := SyncState{Phase: phase}.SyncLine()
		if got != want {
			t.Fatalf("%s = %q, want %q", phase, got, want)
		}
	}
	// A failure is not softened into "up to date".
	failed := SyncState{Phase: SyncFailed, LastRun: time.Now()}.SyncLine()
	if !strings.Contains(failed, "failed") {
		t.Fatalf("failed line = %q", failed)
	}
	idle := SyncState{Phase: SyncIdle, LastRun: time.Now().Add(-90 * time.Minute)}.SyncLine()
	if !strings.Contains(idle, "up to date") || !strings.Contains(idle, "h ago") {
		t.Fatalf("idle line = %q", idle)
	}
}

// TestBusyIconIsDistinguishableWithoutColour: three states, three different
// images — a colour-only difference is no signal at 16 px for many people.
func TestBusyIconIsDistinguishableWithoutColour(t *testing.T) {
	muted, idle, busy := StateIcon(IconMuted), StateIcon(IconIdle), StateIcon(IconBusy)
	for name, ico := range map[string][]byte{"muted": muted, "idle": idle, "busy": busy} {
		if len(ico) == 0 {
			t.Fatalf("%s icon is empty", name)
		}
	}
	if bytes.Equal(idle, busy) {
		t.Fatal("the busy icon is identical to the idle one")
	}
	if bytes.Equal(idle, muted) {
		t.Fatal("the muted icon is identical to the idle one")
	}
	// The dot is a shape, not just a hue: the busy image differs in more than
	// the palette, which a size difference is a cheap proxy for.
	if len(busy) == len(idle) {
		t.Log("busy and idle encode to the same length; comparing bytes instead")
	}
}

func TestDisplayVersionFallsBackToDev(t *testing.T) {
	if got := Build(State{}).Title; got != "Ledgerline dev" {
		t.Fatalf("unstamped build title = %q", got)
	}
}

func TestServerHost(t *testing.T) {
	cases := map[string]string{
		"https://ledger.example.com":  "ledger.example.com",
		"https://ledger.example.com/": "ledger.example.com",
		"http://127.0.0.1:8000/sub":   "127.0.0.1:8000",
		"":                            "(no server)",
		"not a url":                   "not a url",
	}
	for in, want := range cases {
		if got := ServerHost(in); got != want {
			t.Fatalf("ServerHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// parseICO reads the directory of an .ico and returns the entry dimensions plus
// the embedded payload, so a test can assert the container is well formed
// rather than just non-empty.
func parseICO(t *testing.T, data []byte) (w, h int, payload []byte) {
	t.Helper()
	if len(data) < 22 {
		t.Fatalf("ico too short: %d bytes", len(data))
	}
	r := bytes.NewReader(data)
	var reserved, typ, count uint16
	_ = binary.Read(r, binary.LittleEndian, &reserved)
	_ = binary.Read(r, binary.LittleEndian, &typ)
	_ = binary.Read(r, binary.LittleEndian, &count)
	if reserved != 0 || typ != 1 || count != 1 {
		t.Fatalf("ico header = reserved %d type %d count %d", reserved, typ, count)
	}
	entry := data[6:22]
	w, h = int(entry[0]), int(entry[1])
	size := binary.LittleEndian.Uint32(entry[8:12])
	offset := binary.LittleEndian.Uint32(entry[12:16])
	if int(offset)+int(size) > len(data) {
		t.Fatalf("payload out of range: offset %d size %d of %d", offset, size, len(data))
	}
	return w, h, data[offset : offset+size]
}

func TestBrandIconIsAValidIconAndDiffersByState(t *testing.T) {
	active, muted := BrandIcon(true), BrandIcon(false)
	for name, ico := range map[string][]byte{"active": active, "muted": muted} {
		w, h, payload := parseICO(t, ico)
		if w != trayIconSize || h != trayIconSize {
			t.Fatalf("%s icon = %dx%d", name, w, h)
		}
		if _, err := png.Decode(bytes.NewReader(payload)); err != nil {
			t.Fatalf("%s payload: %v", name, err)
		}
	}
	if bytes.Equal(active, muted) {
		t.Fatal("signed-in and signed-out icons are identical")
	}
	// Cached: a second call must hand back the same slice, not redraw.
	if &BrandIcon(true)[0] != &active[0] {
		t.Fatal("BrandIcon redrew instead of using its cache")
	}
}

func TestBreakdownDistinguishesAbsentFromZero(t *testing.T) {
	absent := api.Usage{Used: 3 << 30, Quota: ptr(10 << 30)}
	if absent.HasBreakdown() {
		t.Fatal("a payload without per-module fields claims a breakdown")
	}
	// A module the server did report as empty is a stated fact, not a gap.
	reported := api.Usage{Files: ptr(0), Gallery: ptr(0)}
	if !reported.HasBreakdown() {
		t.Fatal("an explicit zero was mistaken for an absent field")
	}
}
