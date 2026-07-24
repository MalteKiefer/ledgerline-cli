package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsAudited covers the audit inclusion rule: real operations are audited;
// bare/help/status/audit paths are not (reading the trail must not write it).
func TestIsAudited(t *testing.T) {
	audited := []string{
		"ledgerline-cli auth login", "ledgerline-cli gallery upload",
		"ledgerline-cli files sync", "ledgerline-cli todo add",
	}
	unaudited := []string{
		"", "ledgerline-cli", "ledgerline-cli help",
		"ledgerline-cli status", "ledgerline-cli audit", "ledgerline-cli audit show",
	}
	for _, p := range audited {
		if !isAudited(p) {
			t.Fatalf("%q should be audited", p)
		}
	}
	for _, p := range unaudited {
		if isAudited(p) {
			t.Fatalf("%q should NOT be audited", p)
		}
	}
}

// TestAuditShowRendersTrail seeds an audit.log in the config dir and reads it back
// through `audit show`, confirming the command renders the JSONL trail.
func TestAuditShowRendersTrail(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", dir)

	lines := `{"ts":"2026-01-02T03:04:05Z","event":"gallery.upload","outcome":"ok","count":3,"duration_ms":42,"pid":1}
{"ts":"2026-01-02T03:05:00Z","event":"auth.login","outcome":"ok","target":"example.com","pid":1}
`
	if err := os.WriteFile(filepath.Join(dir, "audit.log"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	root := NewRootCommand()
	var buf strings.Builder
	root.SetOut(&buf)
	root.SetArgs([]string{"audit", "show"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"gallery.upload", "auth.login", "example.com", "×3", "42ms"} {
		if !strings.Contains(out, want) {
			t.Fatalf("audit show output missing %q:\n%s", want, out)
		}
	}
}

// TestAuditShowEmpty reports cleanly when there is no log yet.
func TestAuditShowEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", dir)

	root := NewRootCommand()
	var buf strings.Builder
	root.SetOut(&buf)
	root.SetArgs([]string{"audit", "show"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No audit log yet") {
		t.Fatalf("unexpected output: %q", buf.String())
	}
}
