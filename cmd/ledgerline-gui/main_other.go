//go:build !windows

// The tray GUI currently ships for Windows only. This stub keeps
// `go build ./...` and the test suite meaningful on Linux and macOS while the
// other platforms' trays are built (Linux via StatusNotifierItem, macOS via
// Cocoa, each with its own autostart and packaging story).
package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/MalteKiefer/ledgerline-cli/internal/version"
)

func main() {
	fmt.Fprintf(os.Stderr,
		"ledgerline-gui %s: the tray application is Windows-only in this release (this is %s).\nUse the ledgerline-cli command instead.\n",
		version.Version, runtime.GOOS)
	os.Exit(1)
}
