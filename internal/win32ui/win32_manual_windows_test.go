package win32ui

import (
	"os"
	"testing"
	"time"
)

// TestManualWindow puts a real window on screen with one of every control, so
// the toolkit can be looked at rather than only compiled. There is no way to
// assert "this looks right" from a test, and an automated run must not open
// windows on a build machine, so it is opt-in:
//
//	LEDGERLINE_WIN32UI_MANUAL=1 go test ./internal/win32ui/ -run TestManualWindow -v
//
// It closes itself after a few seconds.
func TestManualWindow(t *testing.T) {
	if os.Getenv("LEDGERLINE_WIN32UI_MANUAL") == "" {
		t.Skip("set LEDGERLINE_WIN32UI_MANUAL=1 to open a real window")
	}

	EnableDPIAwareness()
	w, err := NewWindow("Ledgerline — toolkit probe", 420, 250)
	if err != nil {
		t.Fatalf("create window: %v", err)
	}

	w.Label("Server", 16, 20, 110, 20)
	server := w.Edit("https://example.test", 130, 16, 270, 24, false)
	w.Label("Password", 16, 52, 110, 20)
	w.Edit("", 130, 48, 270, 24, true)

	list := w.List(16, 84, 384, 70)
	list.ListAdd("1   C:\\docs <-> Docs  [15m0s, on]")
	list.ListAdd("2   C:\\photos -> Photos  [manual, off]")
	list.ListSelect(0)

	status := w.Label("", 16, 162, 384, 20)
	w.Button("Probe", 300, 190, 100, 26, true, func() {
		status.SetText("clicked, server field says: " + server.Text())
	})

	// Read the field back while the window is still up: once it closes, its
	// child controls are destroyed and every handle reads empty.
	var readBack string
	go func() {
		time.Sleep(4 * time.Second)
		readBack = server.Text()
		w.Close()
	}()
	w.Run()

	// Reaching here means the message loop ran and the window closed cleanly.
	if readBack != "https://example.test" {
		t.Fatalf("field read back as %q while the window was open", readBack)
	}
}
