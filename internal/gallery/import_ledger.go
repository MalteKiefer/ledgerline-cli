package gallery

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// ImportLedger is a per-Immich-server, append-only record of which assets have
// already been imported into the gallery, so a re-run resumes where it stopped
// and never re-downloads an asset. It lives under the CLI config dir at
// <config>/immich-import/<sha256(baseURL)[:16]>.jsonl (0600 in a 0700 dir), one
// JSON line per imported asset. The hashed base URL keys it per server so
// multiple Immich servers never collide. It is NON-secret — asset ids, SHA-1
// checksums, and Ledgerline record ids only — but is stored 0600 like the rest
// of the config dir.
type ImportLedger struct {
	path string

	mu   sync.Mutex
	seen map[string]bool
	f    *os.File
}

// ledgerEntry is one line of the ledger: an imported asset and where it landed.
type ledgerEntry struct {
	AssetID    string `json:"assetId"`
	Checksum   string `json:"checksum,omitempty"`
	RecordID   string `json:"recordId,omitempty"`
	ImportedAt string `json:"importedAt"` // RFC3339 UTC
}

// OpenImportLedger opens (creating the immich-import dir if needed) the ledger
// for the given Immich base URL, loads the ids it already holds into memory, and
// leaves the file open for appends. A corrupt/partial trailing line is tolerated:
// every well-formed entry up to it is loaded and further appends continue after
// it. Call Close when done.
func OpenImportLedger(baseURL string) (*ImportLedger, error) {
	base, err := config.Dir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "immich-import")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	sum := sha256.Sum256([]byte(baseURL))
	path := filepath.Join(dir, hex.EncodeToString(sum[:])[:16]+".jsonl")

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}

	l := &ImportLedger{path: path, seen: make(map[string]bool), f: f}
	if err := l.load(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return l, nil
}

// load reads existing entries into l.seen and positions the file at its end for
// appends. A malformed line (e.g. a torn write from a prior crash) stops the
// scan without erroring — earlier valid ids are kept.
func (l *ImportLedger) load() error {
	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	sc := bufio.NewScanner(l.f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e ledgerEntry
		if err := json.Unmarshal(line, &e); err != nil || e.AssetID == "" {
			break // torn trailing line — stop; earlier ids are already loaded
		}
		l.seen[e.AssetID] = true
	}
	// Ignore sc.Err(): a read error just leaves the file at its current offset,
	// which the following seek-to-end corrects for appends.
	if _, err := l.f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	return nil
}

// Has reports whether assetID has already been imported. Concurrency-safe.
func (l *ImportLedger) Has(assetID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen[assetID]
}

// Mark records assetID as imported (with its checksum and the resulting
// Ledgerline record id) by appending one line and flushing it to disk, then
// updates the in-memory set. Concurrency-safe. A re-mark of an id already seen
// is a no-op. The append+fsync makes the ledger crash-consistent per batch.
func (l *ImportLedger) Mark(assetID, checksum, recordID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return errors.New("import ledger is closed")
	}
	if l.seen[assetID] {
		return nil
	}

	line, err := json.Marshal(ledgerEntry{
		AssetID:    assetID,
		Checksum:   checksum,
		RecordID:   recordID,
		ImportedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	line = append(line, '\n')

	if _, err := l.f.Write(line); err != nil {
		return err
	}
	if err := l.f.Sync(); err != nil {
		return err
	}
	l.seen[assetID] = true
	return nil
}

// Close flushes and closes the underlying file. It is safe to call more than
// once. After Close, Has still answers from the loaded set but Mark fails.
func (l *ImportLedger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
