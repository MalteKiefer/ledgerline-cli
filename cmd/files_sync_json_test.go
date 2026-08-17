package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

// TestNDJSONLineWriter checks that ndjsonLineWriter re-emits each line written
// to it (in one or several Write calls, with or without a trailing newline) as
// exactly one {"type":"log","text":...} NDJSON record, and never drops or
// duplicates a partial line split across writes.
func TestNDJSONLineWriter(t *testing.T) {
	var buf bytes.Buffer
	w := newNDJSONLineWriter(&buf)

	// One full line in a single Write (the common case: fmt.Fprintln).
	if _, err := w.Write([]byte("pushed   a/b.txt\n")); err != nil {
		t.Fatal(err)
	}
	// A line split across two Write calls, no trailing newline on the second.
	if _, err := w.Write([]byte("pulled   c/")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("d.txt\n")); err != nil {
		t.Fatal(err)
	}
	// Two lines in one Write.
	if _, err := w.Write([]byte("conflict e.txt (skipped)\nfailed   push f.txt: boom\n")); err != nil {
		t.Fatal(err)
	}

	dec := json.NewDecoder(&buf)
	want := []string{
		"pushed   a/b.txt",
		"pulled   c/d.txt",
		"conflict e.txt (skipped)",
		"failed   push f.txt: boom",
	}
	for i, wantText := range want {
		var ev syncJSONEvent
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("line %d: decode: %v", i, err)
		}
		if ev.Type != "log" || ev.Text != wantText {
			t.Fatalf("line %d: got %+v, want log %q", i, ev, wantText)
		}
	}
	if dec.More() {
		t.Fatal("unexpected extra NDJSON records")
	}
}

// TestNDJSONLineWriterEmptyWrites checks that empty and newline-only writes
// (as e.g. a bare fmt.Fprintln("") would produce) don't panic or emit a bogus
// empty log record.
func TestNDJSONLineWriterEmptyWrites(t *testing.T) {
	var buf bytes.Buffer
	w := newNDJSONLineWriter(&buf)
	for _, p := range [][]byte{nil, []byte(""), []byte("\n"), []byte("\n\n")} {
		if _, err := w.Write(p); err != nil {
			t.Fatalf("write %q: %v", p, err)
		}
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output for blank writes, got %q", buf.String())
	}
}

// TestNDJSONLineWriterManyLines is a light stress check that repeated
// Write calls (as internal/files.Sync makes, one bar.Println per file) keep
// the internal buffer bounded and every line intact.
func TestNDJSONLineWriterManyLines(t *testing.T) {
	var buf bytes.Buffer
	w := newNDJSONLineWriter(&buf)
	const n = 500
	for i := 0; i < n; i++ {
		if _, err := fmt.Fprintf(w, "pushed   file-%d.txt\n", i); err != nil {
			t.Fatal(err)
		}
	}
	dec := json.NewDecoder(&buf)
	for i := 0; i < n; i++ {
		var ev syncJSONEvent
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		want := fmt.Sprintf("pushed   file-%d.txt", i)
		if ev.Text != want {
			t.Fatalf("line %d: got %q want %q", i, ev.Text, want)
		}
	}
}
