package crypto

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MalteKiefer/ledgerline-cli/internal/canonicaljson"
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

// SuiteV3 is the Store v3 crypto-suite id (§6.1). Every sealed manifest carries
// it; an unknown suite fails closed (never guessed).
const SuiteV3 = 1

// sealedManifest is the v3 sealed-manifest envelope: {suite, c, n}.
type sealedManifest struct {
	Suite int    `json:"suite"`
	C     string `json:"c"`
	N     string `json:"n"`
}

// SealManifest seals a manifest into the Store v3 {suite,c,n} envelope (§6.1).
// The input JSON is canonicalized (§5.2), padded with trailing spaces to a Padmé
// bucket with a 4 KiB floor (size blurring; JSON parsers ignore the padding),
// secretbox-sealed under the vault key, and tagged with the v3 suite.
//
// The web client computes the pad target from the JavaScript string length; here
// it is the UTF-8 byte length. The difference only affects how much size blur is
// applied, never decryptability — the sealed bytes still parse back to the same
// object (each client re-seals with a fresh random nonce, so manifest ciphertext
// is never byte-pinned across clients).
func SealManifest(manifestJSON []byte, vaultKey []byte) (string, error) {
	canon, err := canonicaljson.Canonicalize(manifestJSON)
	if err != nil {
		return "", fmt.Errorf("crypto: canonicalize manifest: %w", err)
	}
	target := PadmeSize(len(canon) + 1)
	if target < 4096 {
		target = 4096
	}
	padded := make([]byte, target)
	copy(padded, canon)
	for i := len(canon); i < target; i++ {
		padded[i] = ' '
	}

	sealed, err := Seal(padded, vaultKey)
	if err != nil {
		return "", err
	}
	env := sealedManifest{Suite: SuiteV3, C: sealed.C, N: sealed.N}
	data, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// OpenManifest reverses SealManifest, returning the raw (space-padded, canonical)
// manifest JSON. It fails closed on an unknown suite tag — never guessing the
// crypto stack (§6.1). A missing suite is tolerated (treated as the current
// suite) to match the web client's openManifest.
func OpenManifest(sealedString string, vaultKey []byte) ([]byte, error) {
	var env struct {
		Suite *int   `json:"suite"`
		C     string `json:"c"`
		N     string `json:"n"`
	}
	if err := json.Unmarshal([]byte(sealedString), &env); err != nil {
		return nil, err
	}
	if env.Suite != nil && *env.Suite != SuiteV3 {
		return nil, fmt.Errorf("crypto: unknown sealed-manifest suite: %d", *env.Suite)
	}
	return Open(Sealed{C: env.C, N: env.N}, vaultKey)
}
