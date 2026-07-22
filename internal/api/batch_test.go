package api

import (
	"encoding/binary"
	"testing"
)

// frame builds one raw-batch entry: [u32le idLen][id][u32le dataLen][data].
func frame(id string, data []byte) []byte {
	var b []byte
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(id)))
	b = append(b, n[:]...)
	b = append(b, id...)
	binary.LittleEndian.PutUint32(n[:], uint32(len(data)))
	b = append(b, n[:]...)
	b = append(b, data...)
	return b
}

func TestParseBatchStreamRoundTrip(t *testing.T) {
	stream := append(frame("aaaa", []byte("first")), frame("bbbb", []byte("second"))...)
	out := map[string][]byte{}
	if err := parseBatchStream(stream, out); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out) != 2 || string(out["aaaa"]) != "first" || string(out["bbbb"]) != "second" {
		t.Fatalf("bad parse: %v", out)
	}

	// Empty stream → empty map, no error.
	empty := map[string][]byte{}
	if err := parseBatchStream(nil, empty); err != nil || len(empty) != 0 {
		t.Fatalf("empty stream: err=%v len=%d", err, len(empty))
	}
}

func TestParseBatchStreamRejectsHostileLengths(t *testing.T) {
	huge := make([]byte, 8)
	binary.LittleEndian.PutUint32(huge[0:], 0xffffffff) // idLen way past the buffer
	cases := map[string][]byte{
		"truncated id length": {0x01, 0x00},
		"idLen past buffer":   huge,
		"dataLen past buffer": frame("id", []byte("x"))[:len("id")+8], // claims 1 data byte, none present
		"zero id length":      {0x00, 0x00, 0x00, 0x00, 0x05, 0x00, 0x00, 0x00},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			out := map[string][]byte{}
			if err := parseBatchStream(data, out); err == nil {
				t.Fatalf("expected error, got out=%v", out)
			}
		})
	}
}

// FuzzParseBatchStream: arbitrary bytes must never panic or over-allocate.
func FuzzParseBatchStream(f *testing.F) {
	f.Add(append(frame("aaaa", []byte("data")), frame("bb", []byte{})...))
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = parseBatchStream(data, map[string][]byte{})
	})
}
