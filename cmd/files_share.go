package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
)

// newFilesShareCommand builds the `files share` group: public token links plus
// the internal (viewer/editor) shares handed to another account.
func newFilesShareCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "share",
		Short: "Create, list, update or revoke public links and internal shares",
	}
	cmd.AddCommand(
		newFilesShareLsCommand(),
		newFilesShareCreateCommand(),
		newFilesShareUpdateCommand(),
		newFilesShareRmCommand(),
		newFilesShareFolderCommand(),
	)
	return cmd
}

// shareTarget renders which file/folder a share points at.
func shareTarget(s api.FileShare) string {
	switch {
	case s.FileID != nil:
		return fmt.Sprintf("file %d", *s.FileID)
	case s.FileFolderID != nil:
		return fmt.Sprintf("folder %d", *s.FileFolderID)
	default:
		return s.Kind
	}
}

func newFilesShareLsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List the caller's public share links",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			shares, err := client.FilesShares(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(shares) == 0 {
				fmt.Fprintln(out, "No public share links.")
				return nil
			}
			for _, s := range shares {
				fmt.Fprintf(out, "%d  %s  %s  token=%s", s.ID, s.Kind, s.Name, s.Token)
				if s.NeedsPassword {
					fmt.Fprint(out, "  password")
				}
				if !s.AllowDownload {
					fmt.Fprint(out, "  view-only")
				}
				if s.ExpiresAt != nil {
					fmt.Fprintf(out, "  expires=%s", *s.ExpiresAt)
				}
				fmt.Fprintf(out, "  v%d\n", s.Version)
			}
			return nil
		},
	}
}

func newFilesShareCreateCommand() *cobra.Command {
	var folder int64
	var password, expires string
	var noDownload, passStdin bool
	cmd := &cobra.Command{
		Use:   "create [file-id]",
		Short: "Create a public link for a file, or for a folder subtree (--folder)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			usesFolder := cmd.Flags().Changed("folder")
			if (len(args) == 1) == usesFolder {
				return errors.New("pass exactly one target: a file id argument or --folder <id>")
			}
			in := api.CreateFileShareInput{Kind: "file"}
			if usesFolder {
				in.Kind = "folder"
				in.FileFolderID = &folder
			} else {
				ids, perr := parseIDs(args)
				if perr != nil {
					return perr
				}
				in.FileID = &ids[0]
			}
			pass, err := secretFlag(cmd, "password", password, passStdin)
			if err != nil {
				return err
			}
			in.Password = pass
			if cmd.Flags().Changed("expires") {
				in.ExpiresAt = &expires
			}
			if noDownload {
				allow := false
				in.AllowDownload = &allow
			}
			client, cerr := authedClient(cmd.Context())
			if cerr != nil {
				return cerr
			}
			share, err := client.CreateFileShare(cmd.Context(), in)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{
				Event: "files.share.create", Outcome: audit.OutcomeOK, Count: 1,
				Target: shareTarget(share),
			})
			fmt.Fprintf(cmd.OutOrStdout(), "Created share %d for %s (token %s).\n", share.ID, shareTarget(share), share.Token)
			return nil
		},
	}
	cmd.Flags().Int64Var(&folder, "folder", 0, "share this folder subtree instead of a file")
	cmd.Flags().StringVar(&password, "password", "", "gate the link behind a password (visible in argv)")
	cmd.Flags().BoolVar(&passStdin, "password-stdin", false, "read the password from stdin instead")
	cmd.Flags().StringVar(&expires, "expires", "", "expiry timestamp the server accepts (e.g. 2026-12-31T23:59:59Z)")
	cmd.Flags().BoolVar(&noDownload, "no-download", false, "view only: refuse ?download=1")
	return cmd
}

func newFilesShareUpdateCommand() *cobra.Command {
	var password, expires string
	var removePassword, clearExpires, allowDownload, noDownload, passStdin bool
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Change a public link's password, expiry or download permission",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			if allowDownload && noDownload {
				return errors.New("--allow-download and --no-download are mutually exclusive")
			}
			if (cmd.Flags().Changed("password") || passStdin) && removePassword {
				return errors.New("setting a password and --remove-password are mutually exclusive")
			}
			if cmd.Flags().Changed("expires") && clearExpires {
				return errors.New("--expires and --clear-expires are mutually exclusive")
			}
			var in api.UpdateFileShareInput
			pass, err := secretFlag(cmd, "password", password, passStdin)
			if err != nil {
				return err
			}
			in.Password = pass
			if removePassword {
				in.RemovePassword = &removePassword
			}
			if allowDownload || noDownload {
				allow := allowDownload
				in.AllowDownload = &allow
			}
			if cmd.Flags().Changed("expires") {
				at := expires
				ptr := &at
				in.ExpiresAt = &ptr
			}
			if clearExpires {
				var nilAt *string
				in.ExpiresAt = &nilAt
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			// The update is optimistic-concurrency; read the current version from
			// the share list instead of making the caller pass it.
			shares, err := client.FilesShares(cmd.Context())
			if err != nil {
				return err
			}
			version := -1
			for _, s := range shares {
				if s.ID == ids[0] {
					version = s.Version
					break
				}
			}
			if version < 0 {
				return fmt.Errorf("share %d not found", ids[0])
			}
			share, err := client.UpdateFileShare(cmd.Context(), ids[0], in, version)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.share.update", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Updated share %d (v%d).\n", share.ID, share.Version)
			return nil
		},
	}
	cmd.Flags().StringVar(&password, "password", "", "set a new password (visible in argv)")
	cmd.Flags().BoolVar(&passStdin, "password-stdin", false, "read the new password from stdin instead")
	cmd.Flags().BoolVar(&removePassword, "remove-password", false, "drop the password gate")
	cmd.Flags().StringVar(&expires, "expires", "", "set the expiry timestamp")
	cmd.Flags().BoolVar(&clearExpires, "clear-expires", false, "remove the expiry (link never expires)")
	cmd.Flags().BoolVar(&allowDownload, "allow-download", false, "allow downloads")
	cmd.Flags().BoolVar(&noDownload, "no-download", false, "view only")
	return cmd
}

func newFilesShareRmCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id...>",
		Short: "Revoke public share links by id",
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
				if derr := client.DeleteFileShare(cmd.Context(), id); derr != nil {
					return fmt.Errorf("delete share %d: %w", id, derr)
				}
			}
			auditLog().Log(audit.Event{Event: "files.share.rm", Outcome: audit.OutcomeOK, Count: len(ids)})
			fmt.Fprintf(cmd.OutOrStdout(), "Revoked %d share link(s).\n", len(ids))
			return nil
		},
	}
}

// newFilesShareFolderCommand builds `files share folder`: internal shares that
// grant another Ledgerline account viewer/editor access to a folder or file.
func newFilesShareFolderCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "folder",
		Short: "Internal shares granted to other accounts (viewer/editor)",
	}
	cmd.AddCommand(
		newFilesShareFolderLsCommand(),
		newFilesShareFolderAddCommand(),
		newFilesShareFolderRoleCommand(),
		newFilesShareFolderRemoveCommand(),
		newFilesShareFolderRmCommand(),
	)
	return cmd
}

func newFilesShareFolderLsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List internal shares the caller owns, with their members",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			shares, err := client.FilesFolderShares(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(shares) == 0 {
				fmt.Fprintln(out, "No internal shares.")
				return nil
			}
			for _, s := range shares {
				name := ""
				switch {
				case s.FolderName != nil:
					name = *s.FolderName
				case s.FileName != nil:
					name = *s.FileName
				}
				fmt.Fprintf(out, "%d  %s  %s\n", s.ID, s.Kind, name)
				for _, m := range s.Members {
					who := ""
					if m.Email != nil {
						who = *m.Email
					} else if m.Name != nil {
						who = *m.Name
					}
					fmt.Fprintf(out, "    user %d  %-6s  %s\n", m.UserID, m.Role, who)
				}
			}
			return nil
		},
	}
}

// validShareRole guards the two roles the server accepts, so a typo fails
// locally instead of as a 422.
func validShareRole(role string) error {
	if role == "viewer" || role == "editor" {
		return nil
	}
	return errors.New("role must be viewer or editor")
}

func newFilesShareFolderAddCommand() *cobra.Command {
	var folder, file int64
	var role string
	cmd := &cobra.Command{
		Use:   "add <email>",
		Short: "Share a folder (--folder) or file (--file) with an account by email",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			usesFolder := cmd.Flags().Changed("folder")
			usesFile := cmd.Flags().Changed("file")
			if usesFolder == usesFile {
				return errors.New("pass exactly one target: --folder <id> or --file <id>")
			}
			if err := validShareRole(role); err != nil {
				return err
			}
			kind := "folder"
			var folderPtr, filePtr *int64
			if usesFolder {
				folderPtr = &folder
			} else {
				kind = "file"
				filePtr = &file
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			share, err := client.CreateFolderShare(cmd.Context(), kind, folderPtr, filePtr, args[0], role)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.share.folder.add", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Shared %s as %s (share %d, %d member(s)).\n", kind, role, share.ID, len(share.Members))
			return nil
		},
	}
	cmd.Flags().Int64Var(&folder, "folder", 0, "folder id to share")
	cmd.Flags().Int64Var(&file, "file", 0, "file id to share")
	cmd.Flags().StringVar(&role, "role", "viewer", "access level: viewer or editor")
	return cmd
}

func newFilesShareFolderRoleCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "role <share-id> <user-id> <viewer|editor>",
		Short: "Change a member's access level",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args[:2])
			if err != nil {
				return err
			}
			role := args[2]
			if err := validShareRole(role); err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			if _, err := client.UpdateFolderShareMember(cmd.Context(), ids[0], ids[1], role); err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.share.folder.role", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "User %d is now %s on share %d.\n", ids[1], role, ids[0])
			return nil
		},
	}
}

func newFilesShareFolderRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <share-id> <user-id>",
		Short: "Revoke one member's access (the share itself stays)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			if err := client.RemoveFolderShareMember(cmd.Context(), ids[0], ids[1]); err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.share.folder.remove", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Removed user %d from share %d.\n", ids[1], ids[0])
			return nil
		},
	}
}

func newFilesShareFolderRmCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <share-id...>",
		Short: "Delete internal shares entirely (all members lose access)",
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
				if derr := client.DeleteFolderShare(cmd.Context(), id); derr != nil {
					return fmt.Errorf("delete share %d: %w", id, derr)
				}
			}
			auditLog().Log(audit.Event{Event: "files.share.folder.rm", Outcome: audit.OutcomeOK, Count: len(ids)})
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %d internal share(s).\n", len(ids))
			return nil
		},
	}
}
