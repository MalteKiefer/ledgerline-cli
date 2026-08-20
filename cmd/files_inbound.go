package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
)

// newFilesUploadLinkCommand builds `files upload-link`: token links that let an
// external, unauthenticated person drop files into one of the caller's folders.
// Only the owner side lives here — consuming a link is a public no-auth flow for
// the uploader's browser, deliberately outside this client (§2).
func newFilesUploadLinkCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upload-link",
		Short: "Inbound upload links (let outsiders drop files into a folder)",
	}
	cmd.AddCommand(
		newFilesUploadLinkLsCommand(),
		newFilesUploadLinkCreateCommand(),
		newFilesUploadLinkRmCommand(),
	)
	return cmd
}

func newFilesUploadLinkLsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List the caller's inbound upload links",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			links, err := client.FilesUploadLinks(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(links) == 0 {
				fmt.Fprintln(out, "No upload links.")
				return nil
			}
			for _, l := range links {
				label := ""
				if l.Label != nil {
					label = *l.Label
				}
				folder := ""
				if l.FolderName != nil {
					folder = *l.FolderName
				}
				fmt.Fprintf(out, "%d  %s  -> %s  token=%s", l.ID, label, folder, l.Token)
				if l.NeedsPassword {
					fmt.Fprint(out, "  password")
				}
				if l.ExpiresAt != nil {
					fmt.Fprintf(out, "  expires=%s", *l.ExpiresAt)
				}
				fmt.Fprintln(out)
			}
			return nil
		},
	}
}

func newFilesUploadLinkCreateCommand() *cobra.Command {
	var folder int64
	var label, expires, password string
	var passStdin bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an inbound upload link into a folder (--folder, --expires required)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The server requires both a destination folder and an expiry (no
			// root dumps, no immortal links); fail locally rather than as a 422.
			if !cmd.Flags().Changed("folder") {
				return errors.New("--folder <id> is required (an upload link always targets a folder)")
			}
			if expires == "" {
				return errors.New("--expires is required (the server refuses a link without an expiry)")
			}
			var labelPtr *string
			if cmd.Flags().Changed("label") {
				labelPtr = &label
			}
			passwordPtr, err := secretFlag(cmd, "password", password, passStdin)
			if err != nil {
				return err
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			link, err := client.CreateUploadLink(cmd.Context(), folder, labelPtr, expires, passwordPtr)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.upload-link.create", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Created upload link %d (token %s).\n", link.ID, link.Token)
			return nil
		},
	}
	cmd.Flags().Int64Var(&folder, "folder", 0, "destination folder id (required)")
	cmd.Flags().StringVar(&label, "label", "", "label shown on the public upload page")
	cmd.Flags().StringVar(&expires, "expires", "", "expiry timestamp (required, e.g. 2026-12-31T23:59:59Z)")
	cmd.Flags().StringVar(&password, "password", "", "gate the upload page behind a password (visible in argv)")
	cmd.Flags().BoolVar(&passStdin, "password-stdin", false, "read the password from stdin instead")
	return cmd
}

func newFilesUploadLinkRmCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id...>",
		Short: "Revoke inbound upload links by id",
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
				if derr := client.DeleteUploadLink(cmd.Context(), id); derr != nil {
					return fmt.Errorf("delete upload link %d: %w", id, derr)
				}
			}
			auditLog().Log(audit.Event{Event: "files.upload-link.rm", Outcome: audit.OutcomeOK, Count: len(ids)})
			fmt.Fprintf(cmd.OutOrStdout(), "Revoked %d upload link(s).\n", len(ids))
			return nil
		},
	}
}
