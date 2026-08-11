package uploadledger

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddHasPersistsPerServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "led.json")

	l, err := Open(path, "https://a.example")
	if err != nil {
		t.Fatal(err)
	}
	if l.Has("deadbeef") {
		t.Fatal("empty ledger should not have the hash")
	}
	l.Add("deadbeef")
	if err := l.Save(); err != nil {
		t.Fatal(err)
	}

	// Reopen for the same server: hash is remembered.
	l2, err := Open(path, "https://a.example")
	if err != nil {
		t.Fatal(err)
	}
	if !l2.Has("deadbeef") {
		t.Fatal("reopened ledger lost the hash")
	}

	// A different server starts empty (per-server isolation).
	l3, err := Open(path, "https://b.example")
	if err != nil {
		t.Fatal(err)
	}
	if l3.Has("deadbeef") {
		t.Fatal("hash must not leak across servers")
	}
}

func TestMaybeCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "led.json")
	l, _ := Open(path, "s")

	l.Add("a")
	if err := l.MaybeCheckpoint(3); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("should not checkpoint before threshold")
	}
	l.Add("b")
	l.Add("c")
	if err := l.MaybeCheckpoint(3); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("should have checkpointed at threshold: %v", err)
	}
}

func TestHashFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// sha256("abc")
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Fatalf("HashFile = %s, want %s", got, want)
	}
}
