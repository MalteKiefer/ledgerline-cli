// Package cmd wires up the ledgerline-cli command tree.
//
// The surface is intentionally shallow and extensible: a small set of top-level
// groups (auth, gallery, status) each own their subcommands, and all shared
// concerns — the HTTP client, the stored session, and I/O streams — are reached
// through helpers here so new commands stay consistent.
package cmd

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
	"github.com/MalteKiefer/ledgerline-cli/internal/version"
)

// unauditedPrefixes are command-path prefixes NOT written to the audit trail:
// purely local, read-only, no-server operations of no security interest. Reading
// the trail must not itself write to it. Every other command (auth, gallery,
// files, todo, …) is audited.
var unauditedPrefixes = []string{
	"ledgerline-cli help",
	"ledgerline-cli status", // local build info + a public update check
	"ledgerline-cli audit",  // viewing/managing the trail is not itself audited
}

// isAudited reports whether a command path should be written to the audit trail.
func isAudited(path string) bool {
	if path == "" || path == "ledgerline-cli" {
		return false // bare invocation / help
	}
	for _, p := range unauditedPrefixes {
		if path == p || strings.HasPrefix(path, p+" ") {
			return false
		}
	}
	return true
}

// Execute runs the CLI and writes a uniform audit-trail entry for every operation
// it performs (§18): one "start" and one "ok"/"error" line per command, with the
// command path, duration and a generic outcome — never secrets. Returns the
// command's error unchanged.
func Execute(ctx context.Context) error {
	root := NewRootCommand()
	root.SetContext(ctx)

	start := time.Now()
	c, err := root.ExecuteC()

	path := "ledgerline-cli"
	if c != nil {
		path = c.CommandPath()
	}
	if isAudited(path) {
		ev := audit.Event{Event: "cmd:" + path, Outcome: audit.OutcomeOK, Duration: time.Since(start).Milliseconds()}
		if err != nil {
			ev.Outcome = audit.OutcomeError
			ev.Error = "command failed" // generic — the detailed error goes to stderr, not the trail
		}
		auditLog().Log(ev)
	}
	return err
}

// NewRootCommand builds the root command and attaches every subcommand.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "ledgerline-cli",
		Short: "Command-line client for Ledgerline",
		Long: "ledgerline-cli is a console client for a self-hosted Ledgerline server.\n\n" +
			"Authenticate once with `auth login` (a copy/paste code from the web\n" +
			"profile), then run commands such as `gallery upload` and `files sync`.",
		Version:       version.Current().Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// A single, uniform version line (`--version`).
	root.SetVersionTemplate("ledgerline-cli {{.Version}}\n")

	root.AddCommand(
		newStatusCommand(),
		newAuthCommand(),
		newAuditCommand(),
	)
	return root
}
