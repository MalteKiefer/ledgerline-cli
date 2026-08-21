//go:build windows

package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/deskintegrate"
)

// newShellMenuCommand registers or removes the Explorer context menu.
//
// It exists as a command rather than only as a button because the registration
// is machine-wide and therefore needs an administrator: the installer calls it
// during setup, the uninstaller on removal, and the settings window re-launches
// it elevated when the switch is flipped. One implementation, three callers.
func newShellMenuCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "shell-menu",
		Short: "Add or remove the Explorer right-click menu (needs administrator)",
		Long: "Registers this computer's Explorer context menu for Ledgerline, or removes it.\n\n" +
			"The entries are machine-wide registry verbs, so this needs administrator\n" +
			"rights. The installer runs it for you; use it by hand after moving the\n" +
			"program, or to repair a registration another tool removed.",
	}
	c.AddCommand(newShellMenuInstallCommand(), newShellMenuRemoveCommand(), newShellMenuStatusCommand())
	return c
}

func newShellMenuInstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Register the Explorer menu",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			files, dirs := deskintegrate.DefaultMenus()
			if err := deskintegrate.RegisterExplorerMenu(files, dirs); err != nil {
				return elevationHint(err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Explorer menu registered.")
			return nil
		},
	}
}

func newShellMenuRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove",
		Short: "Remove the Explorer menu",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := deskintegrate.UnregisterExplorerMenu(); err != nil {
				return elevationHint(err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Explorer menu removed.")
			return nil
		},
	}
}

func newShellMenuStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report whether the Explorer menu is registered",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if deskintegrate.ExplorerMenuRegistered() {
				fmt.Fprintln(cmd.OutOrStdout(), "registered")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "not registered")
			return nil
		},
	}
}

// elevationHint turns the "needs administrator" case into an instruction rather
// than a bare refusal.
func elevationHint(err error) error {
	if errors.Is(err, deskintegrate.ErrNeedsElevation) {
		return errors.New("this needs administrator rights: run it from an elevated " +
			"terminal, or use the switch in Settings, which asks for elevation itself")
	}
	return err
}

// platformCommands are the subcommands that only exist on this platform.
func platformCommands() []*cobra.Command {
	return []*cobra.Command{newShellMenuCommand()}
}
