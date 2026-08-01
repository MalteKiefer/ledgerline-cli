package cmd

import (
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/settings"
)

// TestMappingFromFlagsPreservesUnsetPolicy proves that re-registering an
// existing mapping via `files sync --map X:Y --service` WITHOUT repeating a
// policy flag leaves the saved per-mapping override intact (rather than
// resetting it to the command default). Only flags the user actually passed
// are written into the persisted mapping.
func TestMappingFromFlagsPreservesUnsetPolicy(t *testing.T) {
	var cfg settings.Settings

	// First run: user explicitly sets --conflict skip for /backup/aa.
	cmd1 := newFilesSyncCommand()
	if err := cmd1.Flags().Set("conflict", "skip"); err != nil {
		t.Fatalf("set conflict: %v", err)
	}
	fl1 := syncFlags{conflict: "skip", delete: "both"} // delete left at default (not Changed)
	cfg.Upsert(mappingFromFlags(cmd1, fl1, settings.Mapping{Remote: "X", Local: "/backup/aa"}))

	if got := cfg.Sync[0].Conflict; got != "skip" {
		t.Fatalf("after first upsert: want Conflict=skip, got %q", got)
	}
	if got := cfg.Sync[0].Delete; got != "" {
		t.Fatalf("after first upsert: want Delete unset (default resolves), got %q", got)
	}

	// Second run: SAME local path, no --conflict this time (fresh command, flag
	// not Changed). The saved skip override must survive. This mirrors the
	// runFilesSyncService call site: seed the base from the existing saved
	// mapping before applying flags.
	cmd2 := newFilesSyncCommand()
	fl2 := syncFlags{conflict: "newest", delete: "both"} // defaults; nothing Changed
	parsed := settings.Mapping{Remote: "X", Local: "/backup/aa"}
	base := parsed
	if existing, ok := findMapping(cfg, parsed.Local); ok {
		base = existing
		base.Remote = parsed.Remote
		base.Local = parsed.Local
	}
	cfg.Upsert(mappingFromFlags(cmd2, fl2, base))

	if len(cfg.Sync) != 1 {
		t.Fatalf("want 1 mapping (upsert by local), got %d", len(cfg.Sync))
	}
	if got := cfg.Sync[0].Conflict; got != "skip" {
		t.Fatalf("second upsert clobbered saved override: want Conflict=skip, got %q", got)
	}
}
