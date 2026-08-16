package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
)

// newFilesLabelsCommand builds the `files labels` group: coloured, user-defined
// labels, many-to-many with files.
func newFilesLabelsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "labels",
		Short: "List, create, remove or assign coloured labels",
	}
	cmd.AddCommand(
		newFilesLabelsLsCommand(),
		newFilesLabelsCreateCommand(),
		newFilesLabelsRmCommand(),
		newFilesLabelsSetCommand(),
	)
	return cmd
}

func newFilesLabelsLsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List the user's labels",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			labels, err := client.FilesLabels(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(labels) == 0 {
				fmt.Fprintln(out, "No labels.")
				return nil
			}
			for _, l := range labels {
				fmt.Fprintf(out, "%d  %s  %s\n", l.ID, l.Color, l.Name)
			}
			return nil
		},
	}
}

func newFilesLabelsCreateCommand() *cobra.Command {
	var color string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a coloured label (--color #rrggbb)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			l, err := client.CreateLabel(cmd.Context(), args[0], color)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.labels.create", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Created label %q (id %d).\n", l.Name, l.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&color, "color", "", `hex colour, e.g. "#ff0000" (default: server default)`)
	return cmd
}

func newFilesLabelsRmCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id...>",
		Short: "Delete labels by id",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := client.DeleteLabel(cmd.Context(), id); err != nil {
					return fmt.Errorf("delete label %d: %w", id, err)
				}
			}
			auditLog().Log(audit.Event{Event: "files.labels.rm", Outcome: audit.OutcomeOK, Count: len(ids)})
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %d label(s).\n", len(ids))
			return nil
		},
	}
}

func newFilesLabelsSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <file-id> <label-id...>",
		Short: "Replace a file's whole label set (no ids clears it)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			f, err := client.SetFileLabels(cmd.Context(), ids[0], ids[1:])
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.labels.set", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "%q now has %d label(s).\n", f.Name, len(f.Labels))
			return nil
		},
	}
}
