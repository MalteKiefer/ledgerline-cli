package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
)

// archiveFormats are the formats the server can write. Password protection only
// exists for zip and 7z; the tar family has no encryption.
var archiveFormats = map[string]bool{"zip": true, "tar.gz": true, "tar.xz": true, "7z": true}

// newFilesZipCommand streams a ZIP of a selection (or a folder subtree) straight
// to a local file. Unlike `files archive create`, nothing is stored server-side.
func newFilesZipCommand() *cobra.Command {
	var folder int64
	var dest string
	cmd := &cobra.Command{
		Use:   "zip [file-id...]",
		Short: "Download a ZIP of files and/or a folder subtree (--folder) to disk",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			usesFolder := cmd.Flags().Changed("folder")
			if len(args) == 0 && !usesFolder {
				return errors.New("pass file ids and/or --folder <id>")
			}
			var ids []int64
			if len(args) > 0 {
				parsed, perr := parseIDs(args)
				if perr != nil {
					return perr
				}
				ids = parsed
			}
			var folderPtr *int64
			if usesFolder {
				folderPtr = &folder
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			f, err := os.Create(dest)
			if err != nil {
				return err
			}
			zerr := client.FilesZip(cmd.Context(), ids, folderPtr, f)
			closeErr := f.Close()
			if zerr != nil {
				// A failed export leaves a truncated/empty file behind; drop it so
				// the caller never mistakes it for a usable archive.
				_ = os.Remove(dest)
				return zerr
			}
			if closeErr != nil {
				return closeErr
			}
			auditLog().Log(audit.Event{Event: "files.zip", Outcome: audit.OutcomeOK, Count: len(ids)})
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s.\n", dest)
			return nil
		},
	}
	cmd.Flags().Int64Var(&folder, "folder", 0, "include this folder's subtree")
	cmd.Flags().StringVar(&dest, "out", "ledgerline-files.zip", "local path to write the ZIP to")
	return cmd
}

// newFilesArchiveCommand builds `files archive`: server-side archive creation
// (the result is stored as a normal file) and extraction of an uploaded archive.
func newFilesArchiveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "archive",
		Short: "Create a server-side archive, or extract one that is stored",
	}
	cmd.AddCommand(
		newFilesArchiveCreateCommand(),
		newFilesArchiveExtractCommand(),
	)
	return cmd
}

func newFilesArchiveCreateCommand() *cobra.Command {
	var folder, target int64
	var format, password, name string
	var level int
	var passStdin bool
	cmd := &cobra.Command{
		Use:   "create [file-id...]",
		Short: "Archive files and/or a folder subtree (--folder) into a stored file",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			usesFolder := cmd.Flags().Changed("folder")
			if len(args) == 0 && !usesFolder {
				return errors.New("pass file ids and/or --folder <id>")
			}
			if !archiveFormats[format] {
				return errors.New("--format must be zip, tar.gz, tar.xz or 7z")
			}
			if (cmd.Flags().Changed("password") || passStdin) && format != "zip" && format != "7z" {
				return errors.New("a password only works with zip or 7z")
			}
			in := api.CreateArchiveInput{Format: format}
			if len(args) > 0 {
				ids, perr := parseIDs(args)
				if perr != nil {
					return perr
				}
				in.IDs = ids
			}
			if usesFolder {
				in.FolderID = &folder
			}
			if cmd.Flags().Changed("into") {
				ptr := &target
				in.TargetFolderID = &ptr
			}
			if cmd.Flags().Changed("level") {
				in.Level = &level
			}
			pass, err := secretFlag(cmd, "password", password, passStdin)
			if err != nil {
				return err
			}
			in.Password = pass
			if cmd.Flags().Changed("name") {
				in.Name = &name
			}
			client, cerr := authedClient(cmd.Context())
			if cerr != nil {
				return cerr
			}
			f, err := client.CreateArchive(cmd.Context(), in)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{
				Event: "files.archive.create", Outcome: audit.OutcomeOK, Count: 1, Target: format,
			})
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s (id %d, %s).\n", f.Name, f.ID, humanBytes(f.Size))
			return nil
		},
	}
	cmd.Flags().Int64Var(&folder, "folder", 0, "archive this folder's subtree")
	cmd.Flags().Int64Var(&target, "into", 0, "folder id to save the archive in (default: server's choice)")
	cmd.Flags().StringVar(&format, "format", "zip", "zip, tar.gz, tar.xz or 7z")
	cmd.Flags().IntVar(&level, "level", 0, "compression level 0 (store) .. 9 (max)")
	cmd.Flags().StringVar(&password, "password", "", "encrypt the archive, zip/7z only (visible in argv)")
	cmd.Flags().BoolVar(&passStdin, "password-stdin", false, "read the archive password from stdin instead")
	cmd.Flags().StringVar(&name, "name", "", "archive filename (default: server's choice)")
	return cmd
}

func newFilesArchiveExtractCommand() *cobra.Command {
	var target int64
	var password string
	var here, passStdin bool
	cmd := &cobra.Command{
		Use:   "extract <file-id>",
		Short: "Extract a stored archive (server-side worker job)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			passwordPtr, err := secretFlag(cmd, "password", password, passStdin)
			if err != nil {
				return err
			}
			var targetPtr **int64
			if cmd.Flags().Changed("into") {
				ptr := &target
				targetPtr = &ptr
			}
			var intoNew *bool
			if here {
				// Default is a fresh folder named after the archive; --here extracts
				// straight into the target folder instead.
				value := false
				intoNew = &value
			}
			client, cerr := authedClient(cmd.Context())
			if cerr != nil {
				return cerr
			}
			folder, err := client.ExtractArchive(cmd.Context(), ids[0], passwordPtr, targetPtr, intoNew)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.archive.extract", Outcome: audit.OutcomeOK, Count: 1})
			out := cmd.OutOrStdout()
			if folder != nil {
				fmt.Fprintf(out, "Extracting into folder %q (id %d); the worker finishes in the background.\n", folder.Name, folder.ID)
			} else {
				fmt.Fprintln(out, "Extracting into the target folder; the worker finishes in the background.")
			}
			return nil
		},
	}
	cmd.Flags().Int64Var(&target, "into", 0, "folder id to extract into (default: root/source folder)")
	cmd.Flags().StringVar(&password, "password", "", "password for an encrypted archive (visible in argv)")
	cmd.Flags().BoolVar(&passStdin, "password-stdin", false, "read the archive password from stdin instead")
	cmd.Flags().BoolVar(&here, "here", false, "extract straight into the target folder, not a new subfolder")
	return cmd
}
