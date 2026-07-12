package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/nacl/secretbox"
)

// Byte sizes shared with libsodium.
const (
	// KeyBytes is the vault-key / secretbox key length.
	KeyBytes = 32
	// nonceBytes is the crypto_secretbox nonce length.
	nonceBytes = 24
	// SaltBytes is the Argon2id salt length (crypto_pwhash_SALTBYTES).
	SaltBytes = 16
)

// b64 encodes with libsodium's ORIGINAL variant: standard base64 with padding.
func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// unb64 decodes a libsodium ORIGINAL base64 string.
func unb64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// Sealed is the JSON envelope the vault uses everywhere for a secretbox result:
// {"c": base64(ciphertext), "n": base64(nonce)}.
type Sealed struct {
	C string `json:"c"`
	N string `json:"n"`
}

// MarshalString renders the envelope as the compact JSON string stored in the
// manifest (encFileKey / encMeta fields).
func (s Sealed) MarshalString() (string, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ParseSealed parses a {"c","n"} JSON string.
func ParseSealed(s string) (Sealed, error) {
	var out Sealed
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return Sealed{}, err
	}
	return out, nil
}

// Seal encrypts data with key using crypto_secretbox (XSalsa20-Poly1305) and a
// fresh random nonce, returning the base64 {c,n} envelope. Wire-compatible with
// vault.js seal().
func Seal(data, key []byte) (Sealed, error) {
	if len(key) != KeyBytes {
		return Sealed{}, errors.New("crypto: key must be 32 bytes")
	}
	var nonce [nonceBytes]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Sealed{}, err
	}
	var keyArr [KeyBytes]byte
	copy(keyArr[:], key)

	cipher := secretbox.Seal(nil, data, &nonce, &keyArr)
	return Sealed{C: b64(cipher), N: b64(nonce[:])}, nil
}

// Open reverses Seal, returning the plaintext. It fails on a wrong key or
// tampered ciphertext.
func Open(sealed Sealed, key []byte) ([]byte, error) {
	if len(key) != KeyBytes {
		return nil, errors.New("crypto: key must be 32 bytes")
	}
	cipher, err := unb64(sealed.C)
	if err != nil {
		return nil, err
	}
	nonceBytesRaw, err := unb64(sealed.N)
	if err != nil {
		return nil, err
	}
	if len(nonceBytesRaw) != nonceBytes {
		return nil, errors.New("crypto: bad nonce length")
	}
	var nonce [nonceBytes]byte
	copy(nonce[:], nonceBytesRaw)
	var keyArr [KeyBytes]byte
	copy(keyArr[:], key)

	out, ok := secretbox.Open(nil, cipher, &nonce, &keyArr)
	if !ok {
		return nil, ErrDecrypt
	}
	return out, nil
}

// GenericHashKey reproduces libsodium crypto_generichash(32, input): a keyless
// BLAKE2b digest truncated to a 32-byte key, used to turn the recovery bytes
// into a key-encryption key.
func GenericHashKey(input []byte) []byte {
	sum := blake2b.Sum256(input)
	return sum[:]
}

// DeriveKEK reproduces vault.js deriveKek: Argon2id (ALG_ARGON2ID13) over the
// passphrase and salt, using the server's opslimit/memlimit, to a 32-byte
// key-encryption key.
//
// libsodium's crypto_pwhash maps opslimit to Argon2 time cost and memlimit
// (bytes) to memory cost in KiB, with a single lane; this mirrors that mapping.
func DeriveKEK(passphrase string, salt []byte, opslimit uint64, memlimitBytes uint64) []byte {
	memKiB := uint32(memlimitBytes / 1024)
	return argon2.IDKey([]byte(passphrase), salt, uint32(opslimit), memKiB, 1, KeyBytes)
}
