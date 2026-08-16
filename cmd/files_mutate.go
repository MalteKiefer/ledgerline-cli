package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
)

// newFilesRenameCommand renames a single file.
func newFilesRenameCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <id> <new-name>",
		Short: "Rename a file",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args[:1])
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			current, err := client.FileShow(cmd.Context(), ids[0])
			if err != nil {
				return fmt.Errorf("look up file %d: %w", ids[0], err)
			}
			name := args[1]
			f, err := client.UpdateFile(cmd.Context(), ids[0], api.FileUpdate{Name: &name}, current.Version)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.rename", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Renamed to %q.\n", f.Name)
			return nil
		},
	}
}

// newFilesMvCommand moves one or more files into a folder (or to the root).
func newFilesMvCommand() *cobra.Command {
	var toFolder int64
	var toRoot bool
	cmd := &cobra.Command{
		Use:   "mv <id...>",
		Short: "Move files into a folder (--to) or to the root (--root)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !toRoot && !cmd.Flags().Changed("to") {
				return fmt.Errorf("specify --to <folder-id> or --root")
			}
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			var folderPtr *int64
			if !toRoot {
				folderPtr = &toFolder
			}
			for _, id := range ids {
				current, err := client.FileShow(cmd.Context(), id)
				if err != nil {
					return fmt.Errorf("look up file %d: %w", id, err)
				}
				if _, err := client.UpdateFile(cmd.Context(), id, api.FileUpdate{FolderID: &folderPtr}, current.Version); err != nil {
					return fmt.Errorf("move %d: %w", id, err)
				}
			}
			auditLog().Log(audit.Event{Event: "files.mv", Outcome: audit.OutcomeOK, Count: len(ids)})
			fmt.Fprintf(cmd.OutOrStdout(), "Moved %d file(s).\n", len(ids))
			return nil
		},
	}
	cmd.Flags().Int64Var(&toFolder, "to", 0, "destination folder id")
	cmd.Flags().BoolVar(&toRoot, "root", false, "move to the root")
	return cmd
}

// newFilesCopyCommand duplicates a file, optionally into a different folder.
func newFilesCopyCommand() *cobra.Command {
	var toFolder int64
	cmd := &cobra.Command{
		Use:   "copy <id>",
		Short: "Duplicate a file (--to for a different destination folder)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			var folderPtr *int64
			if cmd.Flags().Changed("to") {
				folderPtr = &toFolder
			}
			f, err := client.CopyFile(cmd.Context(), ids[0], folderPtr)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.copy", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Copied to %q (id %d).\n", f.Name, f.ID)
			return nil
		},
	}
	cmd.Flags().Int64Var(&toFolder, "to", 0, "destination folder id (default: the file's own folder)")
	return cmd
}
