package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
	"github.com/MalteKiefer/ledgerline-cli/internal/files"
)

// newFilesTrashCommand builds the `files trash` group: list/restore/purge for
// trashed files and folders (a soft-deleted file goes there via `files rm`; a
// soft-deleted folder via `files folder rm`).
func newFilesTrashCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trash",
		Short: "List, restore or permanently delete trashed files/folders",
	}
	cmd.AddCommand(
		newFilesTrashLsCommand(),
		newFilesTrashRestoreCommand(),
		newFilesTrashRmCommand(),
		newFilesTrashEmptyCommand(),
	)
	return cmd
}

func newFilesTrashLsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List trashed files and folders",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			entries, folders, err := client.FilesTrash(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(entries) == 0 && len(folders) == 0 {
				fmt.Fprintln(out, "Trash is empty.")
				return nil
			}
			files.BuildTree(folders, entries).Print(out)
			return nil
		},
	}
}

// isTrashedFile/isTrashedFolder let `trash restore`/`trash rm` dispatch an id
// to the right endpoint without the caller having to remember which namespace
// (file vs. folder) it came from.
func isTrashedFile(entries []api.FileEntry, id int64) bool {
	for _, e := range entries {
		if e.ID == id {
			return true
		}
	}
	return false
}

func isTrashedFolder(folders []api.FileFolder, id int64) bool {
	for _, f := range folders {
		if f.ID == id {
			return true
		}
	}
	return false
}

func newFilesTrashRestoreCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <id...>",
		Short: "Restore trashed files/folders by id",
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
			entries, folders, err := client.FilesTrash(cmd.Context())
			if err != nil {
				return err
			}
			for _, id := range ids {
				switch {
				case isTrashedFile(entries, id):
					if _, err := client.RestoreFile(cmd.Context(), id); err != nil {
						return fmt.Errorf("restore file %d: %w", id, err)
					}
				case isTrashedFolder(folders, id):
					if _, err := client.RestoreFolder(cmd.Context(), id); err != nil {
						return fmt.Errorf("restore folder %d: %w", id, err)
					}
				default:
					return fmt.Errorf("id %d is not in the trash", id)
				}
			}
			auditLog().Log(audit.Event{Event: "files.trash.restore", Outcome: audit.OutcomeOK, Count: len(ids)})
			fmt.Fprintf(cmd.OutOrStdout(), "Restored %d item(s).\n", len(ids))
			return nil
		},
	}
}

func newFilesTrashRmCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id...>",
		Short: "Permanently delete trashed files/folders by id (irreversible)",
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
			entries, folders, err := client.FilesTrash(cmd.Context())
			if err != nil {
				return err
			}
			for _, id := range ids {
				switch {
				case isTrashedFile(entries, id):
					if err := client.ForceDeleteFile(cmd.Context(), id); err != nil {
						return fmt.Errorf("delete file %d: %w", id, err)
					}
				case isTrashedFolder(folders, id):
					if err := client.ForceDeleteFolder(cmd.Context(), id); err != nil {
						return fmt.Errorf("delete folder %d: %w", id, err)
					}
				default:
					return fmt.Errorf("id %d is not in the trash", id)
				}
			}
			auditLog().Log(audit.Event{Event: "files.trash.rm", Outcome: audit.OutcomeOK, Count: len(ids)})
			fmt.Fprintf(cmd.OutOrStdout(), "Permanently deleted %d item(s).\n", len(ids))
			return nil
		},
	}
}

func newFilesTrashEmptyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "empty",
		Short: "Permanently delete everything in the trash (irreversible)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			if err := client.EmptyFilesTrash(cmd.Context()); err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.trash.empty", Outcome: audit.OutcomeOK})
			fmt.Fprintln(cmd.OutOrStdout(), "Trash emptied.")
			return nil
		},
	}
}
