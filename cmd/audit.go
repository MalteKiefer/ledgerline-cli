package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// newAuditCommand builds the `audit` group: view and manage the local audit
// trail. The trail records LOCAL operation metadata only (never secrets, §18) and
// is never sent anywhere.
func newAuditCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "View and manage the local operation audit log",
		Long: "The CLI records every operation it performs to a local, append-only\n" +
			"JSON-lines log (one object per line) in the config directory. It holds\n" +
			"operation metadata only — never keys, tokens, passphrases, or content —\n" +
			"and is never sent anywhere.",
	}
	cmd.AddCommand(newAuditShowCommand(), newAuditPathCommand(), newAuditPurgeCommand())
	return cmd
}

// auditPath returns the audit log's path under the config directory.
func auditPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "audit.log"), nil
}

func newAuditShowCommand() *cobra.Command {
	var limit int
	var raw bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print recent audit entries (newest last)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuditShow(cmd, limit, raw)
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "show at most this many recent entries (0 = all)")
	cmd.Flags().BoolVar(&raw, "raw", false, "print the raw JSON lines instead of a formatted table")
	return cmd
}

func runAuditShow(cmd *cobra.Command, limit int, raw bool) error {
	w := cmd.OutOrStdout()
	path, err := auditPath()
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(w, "No audit log yet.")
			return nil
		}
		return err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	for _, ln := range lines {
		if raw {
			fmt.Fprintln(w, ln)
			continue
		}
		var e struct {
			Time     string `json:"ts"`
			Event    string `json:"event"`
			Outcome  string `json:"outcome"`
			Target   string `json:"target"`
			Count    int    `json:"count"`
			Duration int64  `json:"duration_ms"`
			Detail   string `json:"detail"`
		}
		if json.Unmarshal([]byte(ln), &e) != nil {
			continue
		}
		extra := ""
		if e.Target != "" {
			extra += "  " + e.Target
		}
		if e.Count > 0 {
			extra += fmt.Sprintf("  ×%d", e.Count)
		}
		if e.Duration > 0 {
			extra += fmt.Sprintf("  %dms", e.Duration)
		}
		if e.Detail != "" {
			extra += "  " + e.Detail
		}
		fmt.Fprintf(w, "%s  %-7s  %s%s\n", e.Time, e.Outcome, e.Event, extra)
	}
	return nil
}

func newAuditPathCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the audit log's file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := auditPath()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
}

func newAuditPurgeCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Delete the local audit log",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return fmt.Errorf("this permanently deletes the local audit trail; re-run with --yes to confirm")
			}
			purgeAudit()
			fmt.Fprintln(cmd.OutOrStdout(), "Audit log purged.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm deletion")
	return cmd
}
