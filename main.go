// Command ledgerline-cli is a console client for a self-hosted Ledgerline
// server. See `ledgerline-cli --help` for usage.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/MalteKiefer/ledgerline-cli/cmd"
)

func main() {
	// Cancel in-flight requests cleanly on Ctrl-C / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := cmd.Execute(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
