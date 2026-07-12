package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/files"
)

// newFilesOpenCommand downloads a file to a temp location and opens it with the
// OS default application.
func newFilesOpenCommand() *cobra.Command {
	var wait bool

	cmd := &cobra.Command{
		Use:   "open <path>",
		Short: "Open a file with the default app (temporary decrypted copy)",
		Long: "Decrypt a file to a temporary location and open it with the operating\n" +
			"system's default application — without a normal download or sync.\n\n" +
			"  ledgerline-cli files open Docs/report.pdf\n\n" +
			"The temporary copy is DECRYPTED plaintext. It is written to a private temp\n" +
			"directory (0600). Without --wait it is left for the OS to reclaim; with\n" +
			"--wait the command blocks until the app closes and then deletes the copy.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFilesOpen(cmd, args[0], wait)
		},
	}
	cmd.Flags().BoolVarP(&wait, "wait", "w", false, "wait until the app closes, then delete the temp copy")
	return cmd
}

// runFilesOpen resolves the path to a file, decrypts it to a temp file and opens
// it with the OS handler.
func runFilesOpen(cmd *cobra.Command, path string, wait bool) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	client, err := authedClient(ctx)
	if err != nil {
		return err
	}
	vk, err := unlockVault(cmd, client)
	if err != nil {
		return err
	}

	store := files.NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		return err
	}
	fv, ok := files.FindFile(store, path)
	if !ok {
		return fmt.Errorf("no such file: %s", path)
	}

	data, err := files.NewDownloader(client, vk).Fetch(ctx, fv)
	if err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "ledgerline-open-")
	if err != nil {
		return err
	}
	dest := filepath.Join(dir, fv.Name)
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return err
	}

	fmt.Fprintf(w, "Opening %s …\n", fv.Name)
	if err := openInApp(ctx, dest, wait); err != nil {
		fmt.Fprintf(w, "Could not launch an opener (%v).\nDecrypted copy: %s\n", err, dest)
		return nil
	}

	if wait {
		_ = os.Remove(dest)
		_ = os.Remove(dir)
		fmt.Fprintln(w, "Closed; temporary copy deleted.")
		return nil
	}
	fmt.Fprintf(w, "Temporary decrypted copy: %s\n", dest)
	return nil
}

// openInApp launches the OS default application for a path. With wait it blocks
// until the application closes (best effort per platform).
func openInApp(ctx context.Context, path string, wait bool) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
		if wait {
			args = []string{"-W", path}
		} else {
			args = []string{path}
		}
	case "linux":
		name = "xdg-open"
		args = []string{path}
	default:
		return errors.New("unsupported platform")
	}

	if wait {
		return exec.CommandContext(ctx, name, args...).Run()
	}
	// Fire and forget so the CLI returns immediately.
	return exec.Command(name, args...).Start()
}
