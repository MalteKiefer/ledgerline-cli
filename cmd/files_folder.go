package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
)

// newFilesFolderCommand builds the `files folder` group: rename/move/remove/
// restore, complementing the top-level `files mkdir`.
func newFilesFolderCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "folder",
		Short: "Rename, move, remove or restore folders",
	}
	cmd.AddCommand(
		newFilesFolderRenameCommand(),
		newFilesFolderMvCommand(),
		newFilesFolderRmCommand(),
		newFilesFolderRestoreCommand(),
	)
	return cmd
}

func newFilesFolderRenameCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <id> <new-name>",
		Short: "Rename a folder",
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
			f, err := client.RenameFolder(cmd.Context(), ids[0], args[1])
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.folder.rename", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Renamed to %q.\n", f.Name)
			return nil
		},
	}
}

func newFilesFolderMvCommand() *cobra.Command {
	var toParent int64
	var toRoot bool
	cmd := &cobra.Command{
		Use:   "mv <id>",
		Short: "Move a folder under another (--to) or to the root (--root)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !toRoot && !cmd.Flags().Changed("to") {
				return fmt.Errorf("specify --to <parent-id> or --root")
			}
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			var parentPtr *int64
			if !toRoot {
				parentPtr = &toParent
			}
			f, err := client.MoveFolder(cmd.Context(), ids[0], parentPtr)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.folder.mv", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Moved %q.\n", f.Name)
			return nil
		},
	}
	cmd.Flags().Int64Var(&toParent, "to", 0, "destination parent folder id")
	cmd.Flags().BoolVar(&toRoot, "root", false, "move to the root")
	return cmd
}

func newFilesFolderRmCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rm <id...>",
		Short: "Move folders (and their whole subtree) to the trash (--force to permanently delete)",
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
				if force {
					err = client.ForceDeleteFolder(cmd.Context(), id)
				} else {
					err = client.DeleteFolder(cmd.Context(), id)
				}
				if err != nil {
					return fmt.Errorf("remove folder %d: %w", id, err)
				}
			}
			auditLog().Log(audit.Event{Event: "files.folder.rm", Outcome: audit.OutcomeOK, Count: len(ids)})
			verb := "Trashed"
			if force {
				verb = "Permanently deleted"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %d folder(s).\n", verb, len(ids))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "permanently delete instead of trashing (must already be trashed)")
	return cmd
}

func newFilesFolderRestoreCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <id...>",
		Short: "Restore trashed folders (and their whole subtree + files)",
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
				if _, err := client.RestoreFolder(cmd.Context(), id); err != nil {
					return fmt.Errorf("restore folder %d: %w", id, err)
				}
			}
			auditLog().Log(audit.Event{Event: "files.folder.restore", Outcome: audit.OutcomeOK, Count: len(ids)})
			fmt.Fprintf(cmd.OutOrStdout(), "Restored %d folder(s).\n", len(ids))
			return nil
		},
	}
}
