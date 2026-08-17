package files

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

func TestSyncKeepLocalVersionsSnapshotsBeforeOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("LOCAL-OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	ss := &syncServer{
		files: []api.FileEntry{{ID: 3, Name: "c.txt", Sha256: strptr(sha("REMOTE-NEW")), UpdatedAt: strptr("2099-01-01T00:00:00Z")}},
		raw:   map[int64]string{3: "REMOTE-NEW"},
	}
	srv := httptest.NewServer(ss.handler(t))
	defer srv.Close()

	res, err := Sync(context.Background(), newClient(t, srv), dir,
		SyncOptions{Conflict: ConflictNewest, KeepLocalVersions: true}, io.Discard, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pulled != 1 {
		t.Fatalf("expected the newer remote to be pulled, res=%+v", res)
	}

	got, err := os.ReadFile(filepath.Join(dir, "c.txt"))
	if err != nil || string(got) != "REMOTE-NEW" {
		t.Fatalf("local file = %q err=%v, want REMOTE-NEW", got, err)
	}

	versionDir := filepath.Join(dir, versionsDirName)
	entries, err := os.ReadDir(versionDir)
	if err != nil {
		t.Fatalf("expected a %s directory: %v", versionsDirName, err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one snapshot, got %v", entries)
	}
	snap, err := os.ReadFile(filepath.Join(versionDir, entries[0].Name()))
	if err != nil || string(snap) != "LOCAL-OLD" {
		t.Fatalf("snapshot content = %q err=%v, want LOCAL-OLD", snap, err)
	}
}

func TestSyncWithoutKeepLocalVersionsWritesNoSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("LOCAL-OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	ss := &syncServer{
		files: []api.FileEntry{{ID: 3, Name: "c.txt", Sha256: strptr(sha("REMOTE-NEW")), UpdatedAt: strptr("2099-01-01T00:00:00Z")}},
		raw:   map[int64]string{3: "REMOTE-NEW"},
	}
	srv := httptest.NewServer(ss.handler(t))
	defer srv.Close()

	if _, err := Sync(context.Background(), newClient(t, srv), dir, SyncOptions{Conflict: ConflictNewest}, io.Discard, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, versionsDirName)); !os.IsNotExist(err) {
		t.Fatal("expected no .ledgerline-versions directory without --keep-versions")
	}
}

func TestScanLocalExcludesVersionsDirEvenWithHidden(t *testing.T) {
	dir := t.TempDir()
	versionDir := filepath.Join(dir, versionsDirName)
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(versionDir, "c~20260101-000000.txt"), "old")
	mustWrite(t, filepath.Join(dir, "keep.txt"), "k")

	entries, err := scanLocal(dir, true /* includeHidden */, &ignoreMatcher{})
	if err != nil {
		t.Fatal(err)
	}
	for rel := range entries {
		if rel == versionsDirName+"/c~20260101-000000.txt" {
			t.Fatalf(".ledgerline-versions must never be scanned/synced, got entries=%v", entries)
		}
	}
	if _, ok := entries["keep.txt"]; !ok {
		t.Fatalf("expected keep.txt to still be scanned, got %v", entries)
	}
}

func TestPruneVersionsKeepsOnlyMaxVersions(t *testing.T) {
	dir := t.TempDir()
	// Distinct synthetic timestamps, bypassing snapshotLocalVersion's
	// real-clock naming (two snapshots in the same wall-clock second would
	// otherwise collide on one second-resolution filename) so this test
	// exercises pruneVersions' selection logic in isolation.
	stamps := []string{
		"20260101-000001", "20260101-000002", "20260101-000003",
		"20260101-000004", "20260101-000005", "20260101-000006", "20260101-000007",
	}
	for _, s := range stamps {
		mustWrite(t, filepath.Join(dir, "c~"+s+".txt"), "x")
	}

	if err := pruneVersions(dir, "c", ".txt", 3); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected exactly 3 snapshots to survive pruning, got %d: %v", len(entries), entries)
	}
	for _, want := range stamps[4:] { // the 3 most recent
		found := false
		for _, e := range entries {
			if e.Name() == "c~"+want+".txt" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected the most recent snapshot c~%s.txt to survive, got %v", want, entries)
		}
	}
}
