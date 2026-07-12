package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/version"
)

// newStatusCommand reports build metadata and whether a newer release exists.
func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show version, build metadata, and update availability",
		Long: "Print the repository, the installed version (with commit hash and build\n" +
			"date), and whether this build is up to date against the latest release on\n" +
			"GitHub. The update check is a single unauthenticated request and degrades\n" +
			"gracefully when offline.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Current()
			out := cmd.OutOrStdout()

			fmt.Fprintf(out, "Repository:  %s\n", version.Repository)
			fmt.Fprintf(out, "Version:     %s\n", info.Version)
			fmt.Fprintf(out, "Commit:      %s\n", info.Commit)
			fmt.Fprintf(out, "Built:       %s\n", info.BuildDate)
			fmt.Fprintf(out, "Go:          %s\n", info.GoVersion)
			fmt.Fprintf(out, "Platform:    %s\n", info.Platform)

			ctx, cancel := context.WithTimeout(cmd.Context(), 8*time.Second)
			defer cancel()

			rel, err := version.LatestRelease(ctx, nil)
			if err != nil {
				fmt.Fprintf(out, "Update:      could not check (%v)\n", err)
				return nil
			}

			switch {
			case info.Version == "dev":
				fmt.Fprintf(out, "Update:      development build; latest release is %s\n", rel.TagName)
			case version.UpToDate(rel.TagName):
				fmt.Fprintf(out, "Update:      up to date (latest %s)\n", rel.TagName)
			default:
				fmt.Fprintf(out, "Update:      %s available — see %s\n", rel.TagName, rel.HTMLURL)
				fmt.Fprintf(out, "             download the new binary from the releases page to update.\n")
			}
			return nil
		},
	}
}
