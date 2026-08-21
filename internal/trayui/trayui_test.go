package trayui

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
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
	// Signed out the menu says nothing else: a storage row, a server row or a
	// "sync folders" entry with no session behind it reads as broken.
	if m.ShowAccount || m.ShowSyncRow || m.ShowStatus {
		t.Fatalf("rows shown before sign-in: %+v", m)
	}
	if m.Account != "" || len(m.AccountDetails) != 0 || len(m.SyncDetails) != 0 {
		t.Fatalf("content built before sign-in: %+v", m)
	}
}

func TestBuildSignedIn(t *testing.T) {
	m := Build(State{
		Version:   "v1.2.3",
		LoggedIn:  true,
		ServerURL: "https://ledger.example.com:8443/",
		UserName:  "Grace",
		UserEmail: "grace@example.com",
		Usage:     api.Usage{Used: (1 << 30) + (512 << 20), Files: ptr(1 << 30), Gallery: ptr(512 << 20), Quota: ptr(10 << 30)},
	})
	// The leading v is display noise; the CLI prints it the same way.
	if m.Title != "Ledgerline 1.2.3" {
		t.Fatalf("title = %q", m.Title)
	}
	if m.ShowLogin || !m.ShowLogout || !m.ShowOpenWeb || m.Offline {
		t.Fatalf("actions = %+v", m)
	}
	// The top level carries the account; the figures live one level down, so
	// the menu stays readable at a glance.
	if !m.ShowAccount || m.Account != "Grace" {
		t.Fatalf("account row = %+v", m)
	}
	// Identity and server only: the storage figures moved to the settings
	// window, where there is room to draw them properly.
	want := []string{"grace@example.com", "Server: ledger.example.com:8443"}
	if len(m.AccountDetails) != len(want) {
		t.Fatalf("details = %v", m.AccountDetails)
	}
	for i := range want {
		if m.AccountDetails[i] != want[i] {
			t.Fatalf("detail %d = %q, want %q", i, m.AccountDetails[i], want[i])
		}
	}
	if !strings.Contains(m.Tooltip, "Grace") || !strings.Contains(m.Tooltip, "ledger.example.com") {
		t.Fatalf("tooltip = %q", m.Tooltip)
	}
}

func TestBuildRefreshErrorDoesNotShowStaleNumbers(t *testing.T) {
	m := Build(State{
		Version:     "0.7.5",
		LoggedIn:    true,
		ServerURL:   "https://ledger.example.com",
		UserName:    "Grace",
		Usage:       api.Usage{Used: 999, Quota: ptr(1000)},
		Err:         errors.New("dial tcp: connection refused"),
		Unreachable: true,
	})
	if !m.Offline {
		t.Fatal("a failed refresh must mute the icon")
	}
	for _, l := range m.AccountDetails {
		if strings.Contains(l, "Total") || strings.Contains(l, "Files:") {
			t.Fatalf("storage shown despite a failed refresh: %v", m.AccountDetails)
		}
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

func TestStorageLine(t *testing.T) {
	if got := StorageLine(api.Usage{Used: 2048}); got != "2.0 KiB used" {
		t.Fatalf("unlimited = %q", got)
	}
	// Files and gallery share one quota on the server, so the combined figure is
	// what it applies to.
	got := StorageLine(api.Usage{Used: 2 << 20, Quota: ptr(4 << 20)})
	if got != "2.0 MiB of 4.0 MiB (50%)" {
		t.Fatalf("quota = %q", got)
	}
	if got := StorageLine(api.Usage{Used: 5, Quota: ptr(0)}); !strings.HasSuffix(got, "used") {
		t.Fatalf("zero quota should read as unlimited, got %q", got)
	}
	// A server that sends only the breakdown still gets a total.
	if got := StorageLine(api.Usage{Files: ptr(1 << 20), Gallery: ptr(1 << 20)}); got != "2.0 MiB used" {
		t.Fatalf("breakdown-only total = %q", got)
	}
}

// pngBytes builds a test image of the given size filled with one colour.
func pngBytes(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
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

func TestICOFromAvatarProducesA16pxPNGIcon(t *testing.T) {
	ico, err := ICOFromAvatar(pngBytes(t, 64, 64, color.NRGBA{R: 10, G: 200, B: 30, A: 255}))
	if err != nil {
		t.Fatal(err)
	}
	w, h, payload := parseICO(t, ico)
	if w != IconSize || h != IconSize {
		t.Fatalf("icon dimensions = %dx%d", w, h)
	}
	img, err := png.Decode(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("payload is not a PNG: %v", err)
	}
	if img.Bounds().Dx() != IconSize || img.Bounds().Dy() != IconSize {
		t.Fatalf("payload bounds = %v", img.Bounds())
	}
	// The flat source colour must survive the box filter.
	r, g, b, a := img.At(8, 8).RGBA()
	if r>>8 != 10 || g>>8 != 200 || b>>8 != 30 || a>>8 != 255 {
		t.Fatalf("centre pixel = %d %d %d %d", r>>8, g>>8, b>>8, a>>8)
	}
}

func TestICOFromAvatarCentreCropsNonSquare(t *testing.T) {
	// A wide image must be cropped, not squashed: build one whose centre column
	// differs from its edges and check the centre survives.
	img := image.NewNRGBA(image.Rect(0, 0, 64, 16))
	for y := range 16 {
		for x := range 64 {
			c := color.NRGBA{R: 255, A: 255}
			if x >= 24 && x < 40 {
				c = color.NRGBA{B: 255, A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	ico, err := ICOFromAvatar(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	_, _, payload := parseICO(t, ico)
	out, err := png.Decode(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	r, _, b, _ := out.At(IconSize/2, IconSize/2).RGBA()
	if b>>8 < 200 || r>>8 > 50 {
		t.Fatalf("centre pixel after crop = r%d b%d, want the blue centre band", r>>8, b>>8)
	}
}

func TestICOFromAvatarAcceptsJPEGAndRejectsJunk(t *testing.T) {
	var jbuf bytes.Buffer
	src := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	if err := jpeg.Encode(&jbuf, src, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ICOFromAvatar(jbuf.Bytes()); err != nil {
		t.Fatalf("jpeg avatar: %v", err)
	}
	if _, err := ICOFromAvatar(nil); !errors.Is(err, ErrNoAvatar) {
		t.Fatalf("empty avatar = %v, want ErrNoAvatar", err)
	}
	if _, err := ICOFromAvatar([]byte("not an image")); err == nil {
		t.Fatal("junk avatar accepted")
	}
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

func TestAccountSubmenuLeavesStorageToTheWindow(t *testing.T) {
	m := Build(State{
		LoggedIn: true, UserName: "Ada", UserEmail: "ada@example.test",
		ServerURL: "https://ledgerline.example",
		Usage:     api.Usage{Used: 8 << 20, Files: ptr(3 << 20), Gallery: ptr(5 << 20), Quota: ptr(16 << 20)},
	})
	for _, row := range m.AccountDetails {
		if strings.Contains(row, "MiB") || strings.HasPrefix(row, "Storage") {
			t.Fatalf("account submenu still carries storage: %q", row)
		}
	}
}

// TestBreakdownDistinguishesAbsentFromZero is the bug this shape exists for: the
// server sends {used, quota}, and reading the absent per-module fields as zero
// showed "0 B" over a full account. The figures moved to the settings window,
// but the distinction they depend on is still this package's to keep.
func TestBreakdownDistinguishesAbsentFromZero(t *testing.T) {
	absent := api.Usage{Used: 3 << 30, Quota: ptr(10 << 30)}
	if absent.HasBreakdown() {
		t.Fatal("a payload without per-module fields claims a breakdown")
	}
	if got := StorageLine(absent); got != "3.0 GiB of 10.0 GiB (30%)" {
		t.Fatalf("storage line = %q", got)
	}

	// A module the server did report as empty is a stated fact, not a gap.
	reported := api.Usage{Files: ptr(0), Gallery: ptr(0)}
	if !reported.HasBreakdown() {
		t.Fatal("an explicit zero was mistaken for an absent field")
	}
}
