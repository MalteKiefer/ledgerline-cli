// Command gen-icon writes the application icon to a file.
//
// The mark is drawn in code (internal/trayui), so this exists to hand the same
// bytes to the things that need a file rather than a []byte: the Windows
// resource compiler that stamps the icon into the executables, and the NSIS
// installer. Generating it keeps one source of truth instead of a checked-in
// image that quietly stops matching the tray.
//
// Usage: go run ./cmd/gen-icon -o packaging/windows/ledgerline.ico
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MalteKiefer/ledgerline-cli/internal/trayui"
)

func main() {
	out := flag.String("o", "ledgerline.ico", "file to write")
	flag.Parse()

	raw, err := trayui.BrandICO()
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, raw, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %s (%d bytes, sizes %v)\n", *out, len(raw), trayui.AppIconSizes)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gen-icon:", err)
	os.Exit(1)
}
