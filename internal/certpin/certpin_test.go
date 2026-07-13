package certpin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTOFUPinning(t *testing.T) {
	dir := t.TempDir()
	keyA := []byte{1, 2, 3, 4}
	keyB := []byte{9, 9, 9, 9}

	s := NewStore(dir)
	if err := s.Check("ledger.example.com", keyA); err != nil {
		t.Fatalf("first use should establish the pin: %v", err)
	}
	if err := s.Check("ledger.example.com", keyA); err != nil {
		t.Fatalf("same key should match: %v", err)
	}
	if err := s.Check("ledger.example.com", keyB); err == nil {
		t.Fatal("a changed key must be refused")
	}
	// A different host is pinned independently.
	if err := s.Check("other.example.com", keyB); err != nil {
		t.Fatalf("new host should establish its own pin: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, pinsFile)); err != nil {
		t.Fatalf("pins file not written: %v", err)
	}

	// A fresh store loads the persisted pins.
	s2 := NewStore(dir)
	if err := s2.Check("ledger.example.com", keyA); err != nil {
		t.Fatalf("persisted pin should still match: %v", err)
	}
	if err := s2.Check("ledger.example.com", keyB); err == nil {
		t.Fatal("persisted pin should still refuse a changed key")
	}
}

func TestMissingFileIsEmptyStore(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Check("h", []byte{1}); err != nil {
		t.Fatalf("empty store should accept first pin: %v", err)
	}
}
