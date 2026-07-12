// Package cmd wires up the ledgerline-cli command tree.
//
// The surface is intentionally shallow and extensible: a small set of top-level
// groups (auth, gallery, status) each own their subcommands, and all shared
// concerns — the HTTP client, the stored session, and I/O streams — are reached
// through helpers here so new commands stay consistent.
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/version"
)

// NewRootCommand builds the root command and attaches every subcommand.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "ledgerline-cli",
		Short: "Command-line client for Ledgerline",
		Long: "ledgerline-cli is a console client for a self-hosted Ledgerline server.\n\n" +
			"Authenticate once with `auth login` (a copy/paste code from the web\n" +
			"profile), then run commands such as `gallery upload`. Authentication is\n" +
			"zero-knowledge: the stored token proves identity only.",
		Version:       version.Current().Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// A single, uniform version line (`--version`).
	root.SetVersionTemplate("ledgerline-cli {{.Version}}\n")

	root.AddCommand(
		newStatusCommand(),
		newAuthCommand(),
		newGalleryCommand(),
		newFilesCommand(),
		newTodoCommand(),
	)
	return root
}
