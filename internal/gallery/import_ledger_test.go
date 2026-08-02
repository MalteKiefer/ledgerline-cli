package gallery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// newTestLedger opens a fresh ledger for a unique base URL under an isolated
// config dir, and registers Close. The caller passes a distinct baseURL to key
// the per-server file.
func newTestLedger(t *testing.T, baseURL string) *ImportLedger {
	t.Helper()
	l, err := OpenImportLedger(baseURL)
	if err != nil {
		t.Fatalf("OpenImportLedger(%q): %v", baseURL, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// TestImportLedgerMarkAndHas covers the basic Has/Mark contract: an unmarked id
// is absent, Mark makes it present, and a re-mark of the same id is a no-op that
// does not error or duplicate.
func TestImportLedgerMarkAndHas(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	l := newTestLedger(t, "http://immich.example:2283")

	if l.Has("asset-1") {
		t.Fatal("Has(asset-1) = true before any Mark")
	}
	if err := l.Mark("asset-1", "chk-1", "rec-1"); err != nil {
		t.Fatalf("Mark(asset-1): %v", err)
	}
	if !l.Has("asset-1") {
		t.Fatal("Has(asset-1) = false after Mark")
	}
	if l.Has("asset-2") {
		t.Fatal("Has(asset-2) = true without a Mark")
	}
	// A re-mark of an already-seen id must be a no-op (no error).
	if err := l.Mark("asset-1", "chk-1", "rec-1"); err != nil {
		t.Fatalf("re-Mark(asset-1): %v", err)
	}
}

// TestImportLedgerPersistence checks that marks survive a Close+reopen against
// the same base URL, and that a different base URL keys a separate ledger.
func TestImportLedgerPersistence(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	const baseURL = "http://immich.example:2283"

	l := newTestLedger(t, baseURL)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("asset-%d", i)
		if err := l.Mark(id, "chk-"+id, "rec-"+id); err != nil {
			t.Fatalf("Mark(%s): %v", id, err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen the same server's ledger: every marked id must reload.
	l2 := newTestLedger(t, baseURL)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("asset-%d", i)
		if !l2.Has(id) {
			t.Errorf("after reopen, Has(%s) = false; want true", id)
		}
	}
	if l2.Has("asset-99") {
		t.Error("after reopen, Has(asset-99) = true for an id never marked")
	}

	// A different base URL is a different ledger and shares nothing.
	other := newTestLedger(t, "http://other.example:2283")
	if other.Has("asset-0") {
		t.Error("a different base URL's ledger sees the first server's ids")
	}
}

// TestImportLedgerPersistenceReloadFile confirms persistence goes through the
// on-disk file (not a lingering in-memory set): the reopened ledger's backing
// file exists under the immich-import dir and holds one line per marked asset.
func TestImportLedgerPersistenceReloadFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", dir)

	l := newTestLedger(t, "http://immich.example:2283")
	if err := l.Mark("a", "chk-a", "rec-a"); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The per-server .jsonl file must exist in the immich-import subdir.
	glob := filepath.Join(dir, "immich-import", "*.jsonl")
	matches, err := filepath.Glob(glob)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly one ledger file at %s, got %v", glob, matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ledger file is empty after a Mark")
	}

	// The persisted line must round-trip the full ledger contract (design §4.3:
	// assetId → {checksum, ledgerlineRecordId, importedAt}), not just the assetId
	// that resume keys on. load() only reads back AssetID, so these fields would
	// otherwise be serialized but never verified.
	line := bytes.TrimRight(data, "\n")
	if bytes.ContainsRune(line, '\n') {
		t.Fatalf("want exactly one ledger line after a single Mark, got %q", data)
	}
	var e ledgerEntry
	if err := json.Unmarshal(line, &e); err != nil {
		t.Fatalf("unmarshal ledger line %q: %v", line, err)
	}
	if e.AssetID != "a" {
		t.Errorf("persisted AssetID = %q; want %q", e.AssetID, "a")
	}
	if e.Checksum != "chk-a" {
		t.Errorf("persisted Checksum = %q; want %q", e.Checksum, "chk-a")
	}
	if e.RecordID != "rec-a" {
		t.Errorf("persisted RecordID = %q; want %q", e.RecordID, "rec-a")
	}
	if e.ImportedAt == "" {
		t.Error("persisted ImportedAt is empty; want an RFC3339 timestamp")
	}
}

// TestImportLedgerConcurrentMark hammers Mark and Has from many goroutines to
// prove the mutex serialization is race-clean (run with -race) and that every
// distinct id ends up recorded and reloads after a reopen.
func TestImportLedgerConcurrentMark(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	const baseURL = "http://immich.example:2283"
	l := newTestLedger(t, baseURL)

	const workers = 16
	const perWorker = 40

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := fmt.Sprintf("w%d-a%d", w, i)
				if err := l.Mark(id, "chk", "rec"); err != nil {
					t.Errorf("Mark(%s): %v", id, err)
					return
				}
				// Concurrent read against concurrent writers.
				_ = l.Has(id)
			}
		}(w)
	}
	wg.Wait()

	// Every id written by every worker must be present.
	for w := 0; w < workers; w++ {
		for i := 0; i < perWorker; i++ {
			id := fmt.Sprintf("w%d-a%d", w, i)
			if !l.Has(id) {
				t.Fatalf("Has(%s) = false after concurrent Mark", id)
			}
		}
	}

	// And all of them must survive a reopen (append+flush per Mark).
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	l2 := newTestLedger(t, baseURL)
	for w := 0; w < workers; w++ {
		for i := 0; i < perWorker; i++ {
			id := fmt.Sprintf("w%d-a%d", w, i)
			if !l2.Has(id) {
				t.Fatalf("after reopen, Has(%s) = false", id)
			}
		}
	}
}
