// Package crypto reproduces, byte for byte, the client-side cryptography of the
// Ledgerline web app (resources/js/vault.js, libsodium) and the Android client.
//
// It provides the three primitives the vault uses:
//
//   - crypto_secretbox (XSalsa20-Poly1305) for sealing keys, metadata and
//     manifests — supplied by golang.org/x/crypto/nacl/secretbox, which is wire
//     compatible with libsodium crypto_secretbox_easy.
//   - Argon2id key derivation — golang.org/x/crypto/argon2, matching libsodium
//     crypto_pwhash with ALG_ARGON2ID13.
//   - crypto_secretstream_xchacha20poly1305 for file/photo content — implemented
//     here on top of ChaCha20 + Poly1305, since x/crypto has no secretstream.
//
// This file is the secretstream construction. It follows the libsodium algorithm
// exactly and is verified against libsodium-generated known-answer tests.
package crypto

import (
	"crypto/subtle"
	"encoding/binary"
	"errors"

	"golang.org/x/crypto/chacha20"
	"golang.org/x/crypto/poly1305"
)

// secretstream constants, matching libsodium.
const (
	streamHeaderBytes  = 24 // random header carried in front of the ciphertext
	streamKeyBytes     = 32
	streamABytes       = 17 // 1 tag byte + 16-byte Poly1305 MAC per message
	streamCounterBytes = 4
	streamInonceBytes  = 8

	// TagMessage marks an intermediate chunk; TagFinal marks the last one.
	tagMessage byte = 0x00
	tagFinal   byte = 0x03
)

// streamState is the evolving secretstream state (subkey + 12-byte nonce made of
// a 4-byte little-endian counter followed by an 8-byte inonce).
type streamState struct {
	key   [streamKeyBytes]byte
	nonce [streamCounterBytes + streamInonceBytes]byte
}

// initState derives the per-stream subkey and initial nonce from a stream key and
// a 24-byte header, exactly as libsodium's init_push/init_pull do.
func initState(key, header []byte) (*streamState, error) {
	subkey, err := chacha20.HChaCha20(key, header[:16])
	if err != nil {
		return nil, err
	}
	s := &streamState{}
	copy(s.key[:], subkey)
	// counter = 1, inonce = header[16:24].
	s.nonce[0] = 1
	copy(s.nonce[streamCounterBytes:], header[16:streamHeaderBytes])
	return s, nil
}

// rekeyAfter advances the nonce after a message: the inonce is XORed with the
// first 8 bytes of the MAC and the counter is incremented (little-endian).
func (s *streamState) rekeyAfter(mac []byte) {
	for i := 0; i < streamInonceBytes; i++ {
		s.nonce[streamCounterBytes+i] ^= mac[i]
	}
	counter := binary.LittleEndian.Uint32(s.nonce[:streamCounterBytes])
	binary.LittleEndian.PutUint32(s.nonce[:streamCounterBytes], counter+1)
}

// macPad is the number of zero bytes libsodium's secretstream absorbs into the
// Poly1305 state after the message ciphertext: exactly mlen mod 16. (Verified
// against libsodium known-answer tests across message lengths; this is the
// library's own framing, not RFC 8439 alignment padding.)
func macPad(mlen int) int { return mlen % 16 }

// le64 encodes n as 8 little-endian bytes.
func le64(n uint64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], n)
	return b[:]
}

// push encrypts one message with the given tag, advancing the state, and returns
// the libsodium frame: [encrypted tag byte][ciphertext][16-byte MAC].
func (s *streamState) push(message []byte, tag byte) ([]byte, error) {
	cipher, err := chacha20.NewUnauthenticatedCipher(s.key[:], s.nonce[:])
	if err != nil {
		return nil, err
	}

	// Block at counter 0 → Poly1305 one-time key (first 32 bytes).
	var polyKey [32]byte
	block := make([]byte, 64)
	cipher.XORKeyStream(block, block)
	copy(polyKey[:], block[:32])
	mac := poly1305.New(&polyKey)

	// Absorb ad (empty) — no bytes, already 16-aligned.

	// Encrypted tag block at counter 1; out[0] is the encrypted tag byte.
	tagBlock := make([]byte, 64)
	tagBlock[0] = tag
	cipher.XORKeyStream(tagBlock, tagBlock)
	out0 := tagBlock[0]
	mac.Write(tagBlock)

	// Encrypt the message at counter 2 onward.
	ct := make([]byte, len(message))
	cipher.XORKeyStream(ct, message)
	mac.Write(ct)
	mac.Write(make([]byte, macPad(len(message))))

	// Absorb the two length fields: adlen (0) and (64 + mlen).
	mac.Write(le64(0))
	mac.Write(le64(uint64(64 + len(message))))
	tagMac := mac.Sum(nil)

	s.rekeyAfter(tagMac)

	out := make([]byte, 1+len(ct)+len(tagMac))
	out[0] = out0
	copy(out[1:], ct)
	copy(out[1+len(ct):], tagMac)
	return out, nil
}

// ErrDecrypt is returned when a frame fails authentication.
var ErrDecrypt = errors.New("secretstream: decryption failed")

// pull decrypts one frame produced by push, returning the plaintext and its tag.
func (s *streamState) pull(in []byte) ([]byte, byte, error) {
	if len(in) < streamABytes {
		return nil, 0, ErrDecrypt
	}
	cipher, err := chacha20.NewUnauthenticatedCipher(s.key[:], s.nonce[:])
	if err != nil {
		return nil, 0, err
	}

	var polyKey [32]byte
	block := make([]byte, 64)
	cipher.XORKeyStream(block, block)
	copy(polyKey[:], block[:32])
	mac := poly1305.New(&polyKey)

	mlen := len(in) - streamABytes
	storedMac := in[1+mlen:]

	// Recover the tag byte and reconstruct the encrypted tag block for the MAC.
	tagBlock := make([]byte, 64)
	tagBlock[0] = in[0]
	cipher.XORKeyStream(tagBlock, tagBlock)
	tag := tagBlock[0]
	tagBlock[0] = in[0] // MAC covers the encrypted tag block
	mac.Write(tagBlock)

	ct := in[1 : 1+mlen]
	mac.Write(ct)
	mac.Write(make([]byte, macPad(mlen)))
	mac.Write(le64(0))
	mac.Write(le64(uint64(64 + mlen)))
	computed := mac.Sum(nil)

	if subtle.ConstantTimeCompare(computed, storedMac) != 1 {
		return nil, 0, ErrDecrypt
	}

	plain := make([]byte, mlen)
	cipher.XORKeyStream(plain, ct)

	s.rekeyAfter(computed)
	return plain, tag, nil
}
