package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
)

// newFilesKeysCommand lists the server-side keyring: the account's own PGP/S-MIME
// keys (usable for --key) and the recipient public keys (usable for --recipient).
// Key generation/import and recipient management stay web-only (§2) — this client
// only reads the keyring to resolve ids.
func newFilesKeysCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "keys",
		Short: "List own encryption keys and known recipients",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			keys, recipients, err := client.Keyring(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(keys) == 0 {
				fmt.Fprintln(out, "No own keys. Create or import one in the web app first.")
			} else {
				fmt.Fprintln(out, "Own keys:")
				for _, k := range keys {
					fmt.Fprintf(out, "  %d  %-5s  %s", k.ID, k.Type, k.Label)
					if k.Fingerprint != nil {
						fmt.Fprintf(out, "  %s", *k.Fingerprint)
					}
					if !k.HasPrivate {
						fmt.Fprint(out, "  (public only — cannot decrypt)")
					}
					fmt.Fprintln(out)
				}
			}
			if len(recipients) > 0 {
				fmt.Fprintln(out, "Recipients:")
				for _, r := range recipients {
					fmt.Fprintf(out, "  %d  %-5s  %s", r.ID, r.Type, r.Label)
					if r.Fingerprint != nil {
						fmt.Fprintf(out, "  %s", *r.Fingerprint)
					}
					fmt.Fprintln(out)
				}
			}
			return nil
		},
	}
}

// newFilesEncryptCommand encrypts a file, or a whole folder subtree (--folder),
// server-side with a public key. The owner's own key is always included, so the
// result stays decryptable by this account.
func newFilesEncryptCommand() *cobra.Command {
	var key, folder int64
	var recipients []int64
	cmd := &cobra.Command{
		Use:   "encrypt [file-id]",
		Short: "Encrypt a file, or a folder subtree (--folder), with --key",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			usesFolder := cmd.Flags().Changed("folder")
			if (len(args) == 1) == usesFolder {
				return errors.New("pass exactly one target: a file id argument or --folder <id>")
			}
			if !cmd.Flags().Changed("key") {
				return errors.New("--key <id> is required (see 'files keys')")
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			if usesFolder {
				f, ferr := client.EncryptFolder(cmd.Context(), folder, key, recipients)
				if ferr != nil {
					return ferr
				}
				auditLog().Log(audit.Event{Event: "files.encrypt.folder", Outcome: audit.OutcomeOK, Count: 1})
				fmt.Fprintf(cmd.OutOrStdout(), "Encrypted folder %d to %s (id %d).\n", folder, f.Name, f.ID)
				return nil
			}
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			f, err := client.EncryptFile(cmd.Context(), ids[0], key, recipients)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.encrypt", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Encrypted to %s (id %d).\n", f.Name, f.ID)
			return nil
		},
	}
	cmd.Flags().Int64Var(&key, "key", 0, "own key id to encrypt with (required)")
	cmd.Flags().Int64Var(&folder, "folder", 0, "encrypt this folder subtree instead of a file")
	cmd.Flags().Int64SliceVar(&recipients, "recipient", nil, "extra recipient key id (repeatable)")
	return cmd
}

// newFilesDecryptCommand decrypts a stored encrypted file back to plaintext.
// The passphrase is read from a flag only when the key needs one; it is never
// logged or written to the audit trail.
func newFilesDecryptCommand() *cobra.Command {
	var key int64
	var passStdin bool
	cmd := &cobra.Command{
		Use:   "decrypt <file-id>",
		Short: "Decrypt an encrypted file with --key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("key") {
				return errors.New("--key <id> is required (see 'files keys')")
			}
			// A private-key passphrase is the highest-value secret this command
			// touches, so it has no argv path at all — stdin only.
			var passPtr *string
			if passStdin {
				pass, perr := readSecretStdin(cmd)
				if perr != nil {
					return perr
				}
				passPtr = &pass
			}
			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			f, err := client.DecryptFile(cmd.Context(), ids[0], key, passPtr)
			if err != nil {
				return err
			}
			auditLog().Log(audit.Event{Event: "files.decrypt", Outcome: audit.OutcomeOK, Count: 1})
			fmt.Fprintf(cmd.OutOrStdout(), "Decrypted to %s (id %d).\n", f.Name, f.ID)
			return nil
		},
	}
	cmd.Flags().Int64Var(&key, "key", 0, "own key id to decrypt with (required)")
	cmd.Flags().BoolVar(&passStdin, "passphrase-stdin", false, "read the key passphrase from stdin (never from argv)")
	return cmd
}
