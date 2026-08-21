//go:build !windows

package cmd

import "github.com/spf13/cobra"

// platformCommands is empty away from Windows: the Explorer context menu and the
// per-user startup entry are shell integration that only exists there. The Linux
// and macOS equivalents (a .desktop action, a Finder extension) are separate
// work with their own shapes, and pretending one command covers all three would
// only produce a command that fails on two of them.
func platformCommands() []*cobra.Command { return nil }
