package crypto

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
)

// ChunkSize is the plaintext slice size per secretstream message (4 MiB),
// matching vault.js CHUNK so blobs are framed identically to the web client.
const ChunkSize = 4 * 1024 * 1024

// newPush generates a random header and initialises a push state for streamKey.
func newPush(streamKey []byte) (*streamState, []byte, error) {
	header := make([]byte, streamHeaderBytes)
	if _, err := rand.Read(header); err != nil {
		return nil, nil, err
	}
	state, err := initState(streamKey, header)
	if err != nil {
		return nil, nil, err
	}
	return state, header, nil
}

// u32le encodes n as 4 little-endian bytes (the per-frame length prefix).
func u32le(n uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], n)
	return b[:]
}

// EncryptContent seals plain with a fresh per-file secretstream key and wraps
// that key with the vault key. It returns the blob bytes exactly as vault.js
// encryptContent produces them — a 24-byte header followed by length-prefixed
// [u32le][frame] messages, TAG_FINAL on the last — and the {c,n} JSON string of
// the wrapped file key (the manifest's *Key field).
//
// A zero-length input still emits a single final (empty) message, matching the
// web client, so an empty file round-trips.
func EncryptContent(plain, vaultKey []byte) (blob []byte, encFileKey string, err error) {
	streamKey := make([]byte, streamKeyBytes)
	if _, err = rand.Read(streamKey); err != nil {
		return nil, "", err
	}

	state, header, err := newPush(streamKey)
	if err != nil {
		return nil, "", err
	}

	blob = append(blob, header...)
	total := len(plain)
	for off := 0; off < total || off == 0; {
		end := off + ChunkSize
		if end > total {
			end = total
		}
		last := end >= total
		tag := tagMessage
		if last {
			tag = tagFinal
		}
		frame, ferr := state.push(plain[off:end], tag)
		if ferr != nil {
			return nil, "", ferr
		}
		blob = append(blob, u32le(uint32(len(frame)))...)
		blob = append(blob, frame...)
		off = end
		if last {
			break
		}
	}

	sealed, err := Seal(streamKey, vaultKey)
	if err != nil {
		return nil, "", err
	}
	encFileKey, err = sealed.MarshalString()
	if err != nil {
		return nil, "", err
	}
	return blob, encFileKey, nil
}

// DecryptContent reverses EncryptContent: it unwraps the per-file key with the
// vault key and decrypts the framed blob back to plaintext.
func DecryptContent(blob []byte, encFileKey string, vaultKey []byte) ([]byte, error) {
	sealed, err := ParseSealed(encFileKey)
	if err != nil {
		return nil, err
	}
	streamKey, err := Open(sealed, vaultKey)
	if err != nil {
		return nil, err
	}
	if len(blob) < streamHeaderBytes {
		return nil, errors.New("crypto: blob shorter than stream header")
	}

	state, err := initState(streamKey, blob[:streamHeaderBytes])
	if err != nil {
		return nil, err
	}

	var out []byte
	off := streamHeaderBytes
	for {
		if off+4 > len(blob) {
			return nil, ErrDecrypt
		}
		frameLen := int(binary.LittleEndian.Uint32(blob[off : off+4]))
		off += 4
		if frameLen < streamABytes || off+frameLen > len(blob) {
			return nil, ErrDecrypt
		}
		msg, tag, perr := state.pull(blob[off : off+frameLen])
		if perr != nil {
			return nil, perr
		}
		off += frameLen
		out = append(out, msg...)
		if tag == tagFinal {
			break
		}
	}
	return out, nil
}

// SealManifest seals an already-serialised manifest JSON, padding it with
// trailing spaces to the next 4 KiB boundary (size blurring; JSON parsers ignore
// the padding) and returning the {"c","n"} JSON string for the store API.
//
// The web client computes the pad target from the JavaScript string length; here
// it is the UTF-8 byte length. The difference only affects how much size blur is
// applied, never decryptability — the sealed bytes still parse back to the same
// object.
func SealManifest(manifestJSON []byte, vaultKey []byte) (string, error) {
	const bucket = 4096
	// ceil((len+1)/bucket) * bucket
	target := ((len(manifestJSON) + 1 + bucket - 1) / bucket) * bucket
	padded := make([]byte, target)
	copy(padded, manifestJSON)
	for i := len(manifestJSON); i < target; i++ {
		padded[i] = ' '
	}

	sealed, err := Seal(padded, vaultKey)
	if err != nil {
		return "", err
	}
	return sealed.MarshalString()
}

// OpenManifest reverses SealManifest, returning the raw (padded) manifest JSON.
func OpenManifest(sealedString string, vaultKey []byte) ([]byte, error) {
	sealed, err := ParseSealed(sealedString)
	if err != nil {
		return nil, err
	}
	return Open(sealed, vaultKey)
}
