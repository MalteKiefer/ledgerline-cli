package conformance

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
)

// TestBlobFrameFixture gates the sealed-content blob framing + Padmé bucketing
// against blob-frame.json (§17). Per-file keys/nonces are random, so raw frame
// bytes are not pinned; we pin the deterministic frame SIZE and Padmé bucket, plus
// a live encrypt→decrypt round-trip.
func TestBlobFrameFixture(t *testing.T) {
	var fx struct {
		HeaderBytes int `json:"HEADERBYTES"`
		ABytes      int `json:"ABYTES"`
		Chunk       int `json:"CHUNK"`
		Frames      []struct {
			PlaintextLen int `json:"plaintextLen"`
			FrameSize    int `json:"frameSize"`
			PadmeSize    int `json:"padmeSize"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(fixture(t, "blob-frame.json"), &fx); err != nil {
		t.Fatalf("parse blob-frame.json: %v", err)
	}
	if fx.Chunk != crypto.ChunkSize {
		t.Fatalf("CHUNK = %d want %d", crypto.ChunkSize, fx.Chunk)
	}

	vk := make([]byte, 32)
	for i := range vk {
		vk[i] = byte(0x33)
	}

	for _, fr := range fx.Frames {
		// Padmé bucket of the plaintext length.
		if got := crypto.PadmeSize(fr.PlaintextLen); got != fr.PadmeSize {
			t.Fatalf("PadmeSize(%d) = %d want %d", fr.PlaintextLen, got, fr.PadmeSize)
		}
		// frameSize = HEADERBYTES + plaintextLen + chunks*(ABYTES+4).
		chunks := (fr.PlaintextLen + fx.Chunk - 1) / fx.Chunk
		if chunks == 0 {
			chunks = 1
		}
		wantFrame := fx.HeaderBytes + fr.PlaintextLen + chunks*(fx.ABytes+4)
		if wantFrame != fr.FrameSize {
			t.Fatalf("fixture frameSize inconsistent: computed %d fixture %d", wantFrame, fr.FrameSize)
		}

		plain := make([]byte, fr.PlaintextLen)
		blob, encKey, err := crypto.EncryptContent(plain, vk)
		if err != nil {
			t.Fatalf("EncryptContent(%d): %v", fr.PlaintextLen, err)
		}
		if len(blob) != fr.FrameSize {
			t.Fatalf("blob len for %d = %d want %d", fr.PlaintextLen, len(blob), fr.FrameSize)
		}
		// Round-trip decrypt.
		out, err := crypto.DecryptContent(blob, encKey, vk)
		if err != nil {
			t.Fatalf("DecryptContent(%d): %v", fr.PlaintextLen, err)
		}
		if !bytes.Equal(out, plain) {
			t.Fatalf("round-trip mismatch for len %d", fr.PlaintextLen)
		}
	}
}
