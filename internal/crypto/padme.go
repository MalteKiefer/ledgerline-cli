package crypto

import (
	"crypto/rand"
	"math/bits"
)

// PadmeSize rounds n up to its Padmé bucket, the same length-hiding scheme the
// web client uses before storing a blob. It leaks only O(log log n) bits about
// the true size (≤ ~12% overhead). Ported from vault.js padmeSize.
func PadmeSize(n int) int {
	if n < 2 {
		return n
	}
	e := bits.Len(uint(n)) - 1 // floor(log2 n)
	s := bits.Len(uint(e)) - 1 + 1
	padBits := e - s
	if padBits <= 0 {
		return n
	}
	mask := (1 << padBits) - 1
	return (n + mask) & ^mask
}

// PadBlob appends cryptographically-random bytes to blob so its stored length is
// rounded up to the next Padmé bucket. The secretstream decryptor stops at its
// FINAL frame, so the trailing bytes are never parsed — decryption is
// unaffected while the on-disk size only reveals a Padmé bucket.
func PadBlob(blob []byte) ([]byte, error) {
	pad := PadmeSize(len(blob)) - len(blob)
	if pad <= 0 {
		return blob, nil
	}
	out := make([]byte, len(blob)+pad)
	copy(out, blob)
	if _, err := rand.Read(out[len(blob):]); err != nil {
		return nil, err
	}
	return out, nil
}
