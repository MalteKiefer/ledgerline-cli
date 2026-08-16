package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

// newFilesSearchCommand full-text/OCR-searches the user's files.
func newFilesSearchCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text/OCR search over filenames and extracted content",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			matches, err := client.FilesSearch(cmd.Context(), strings.Join(args, " "))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(matches) == 0 {
				fmt.Fprintln(out, "No matches.")
				return nil
			}
			for _, f := range matches {
				fmt.Fprintf(out, "%d  %s  %s\n", f.ID, humanBytes(f.Size), f.Name)
			}
			return nil
		},
	}
}

// newFilesStatsCommand shows storage usage by mime type + suspected duplicates.
func newFilesStatsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Storage usage by type + suspected duplicates (by sha256)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			stats, err := client.GetFilesStats(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Used: %s\n", humanBytes(stats.Used))
			for mime, n := range stats.ByType {
				fmt.Fprintf(out, "  %-30s %s\n", mime, humanBytes(n))
			}
			if len(stats.Duplicates) > 0 {
				fmt.Fprintf(out, "\n%d suspected duplicate group(s).\n", len(stats.Duplicates))
			}
			return nil
		},
	}
}

// newFilesActivityCommand shows the recent Files activity feed, or one file's
// history with --file.
func newFilesActivityCommand() *cobra.Command {
	var fileID int64
	cmd := &cobra.Command{
		Use:   "activity",
		Short: "Recent Files activity (--file for one file's history)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if cmd.Flags().Changed("file") {
				rows, err := client.FileActivityFor(cmd.Context(), fileID)
				if err != nil {
					return err
				}
				printActivity(out, rows)
				return nil
			}
			rows, err := client.FilesActivity(cmd.Context())
			if err != nil {
				return err
			}
			printActivity(out, rows)
			return nil
		},
	}
	cmd.Flags().Int64Var(&fileID, "file", 0, "show activity for one file id")
	return cmd
}

func printActivity(w io.Writer, rows []api.FileActivity) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No activity.")
		return
	}
	for _, a := range rows {
		name := ""
		if a.FileName != nil {
			name = *a.FileName
		}
		actor := ""
		if a.Actor != nil {
			actor = " by " + *a.Actor
		}
		fmt.Fprintf(w, "%s  %-9s %s%s\n", a.CreatedAt, a.Action, name, actor)
	}
}
