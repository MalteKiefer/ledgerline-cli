// Package uploadledger records the sha256 of every file already uploaded to a
// server so repeated `gallery upload` / `files upload` runs skip duplicates
// WITHOUT re-sending the bytes. It is a local, per-server content index (the
// server also de-duplicates by sha256, but that still costs a full upload; this
// avoids the transfer entirely). It is safe for concurrent use by an upload
// worker pool.
package uploadledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sync"
)

// Ledger is a per-server set of uploaded content hashes with a dirty counter so
// callers can checkpoint periodically (`--batch`).
type Ledger struct {
	mu     sync.Mutex
	path   string
	server string
	seen   map[string]bool
	dirty  int
}

// diskState is the on-disk JSON shape.
type diskState struct {
	Server string   `json:"server"`
	Sha    []string `json:"sha"`
}

// Open loads the ledger at path for server. A ledger stored for a DIFFERENT
// server (or a missing/corrupt file) starts empty — the hash set is per-server.
// A path of "" yields a functional in-memory-only ledger (Save is a no-op).
func Open(path, server string) (*Ledger, error) {
	l := &Ledger{path: path, server: server, seen: map[string]bool{}}
	if path == "" {
		return l, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, err
	}
	var st diskState
	if json.Unmarshal(data, &st) == nil && st.Server == server {
		for _, s := range st.Sha {
			l.seen[s] = true
		}
	}
	return l, nil
}

// Has reports whether sha was already uploaded.
func (l *Ledger) Has(sha string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen[sha]
}

// Add records sha as uploaded and marks the ledger dirty.
func (l *Ledger) Add(sha string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.seen[sha] {
		l.seen[sha] = true
		l.dirty++
	}
}

// MaybeCheckpoint writes the ledger when at least `every` new hashes have
// accumulated since the last write (every <= 0 disables periodic checkpointing).
func (l *Ledger) MaybeCheckpoint(every int) error {
	if every <= 0 {
		return nil
	}
	l.mu.Lock()
	due := l.dirty >= every
	l.mu.Unlock()
	if !due {
		return nil
	}
	return l.Save()
}

// Save atomically writes the ledger to disk (0600) and resets the dirty counter.
// A ledger with no path is a no-op.
func (l *Ledger) Save() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path == "" {
		return nil
	}
	shas := make([]string, 0, len(l.seen))
	for s := range l.seen {
		shas = append(shas, s)
	}
	data, err := json.MarshalIndent(diskState{Server: l.server, Sha: shas}, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return err
	}
	l.dirty = 0
	return nil
}

// HashFile returns the hex sha256 of a file's contents.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
