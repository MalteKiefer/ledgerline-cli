package crypto

import (
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Post-quantum hybrid KEM for asymmetric key-wraps (Store v3 §6.3). Used only for
// cross-user sharing + identity — wrapping a shared vault key to a recipient. The
// personal gallery/files stores stay symmetric (secretbox under VK).
//
// Hybrid = X25519 (classical) + ML-KEM-768 (FIPS 203, post-quantum), combined via
// HKDF-SHA256 over ss_ec ‖ ss_pq. Confidentiality holds unless BOTH primitives
// fall (PQXDH-style). Interop is by standard, not byte-identical code, and is
// validated by the §17 ML-KEM-768 NIST KAT + wrap/unwrap round-trip fixtures.
//
// Byte-for-byte aligned with the web client resources/js/shared/pq-kem.js:
//   info = "ledgerline/kem/v1" + context, empty HKDF salt, ikm = ss_ec ‖ ss_pq.

const (
	kemInfoPrefix = "ledgerline/kem/v1"
	wrapKeyLen    = 32
)

// Identity is a user's hybrid identity keypair (§6.3). The public halves
// (X25519Pub, MLKEMEncapKey) are published; the secret halves are sealed under VK.
type Identity struct {
	X25519Pub     string // base64 X25519 public key
	MLKEMEncapKey string // base64 ML-KEM-768 encapsulation key (public, non-secret)

	x25519Secret []byte                     // raw 32-byte X25519 secret key
	mlkemDecap   *mlkem.DecapsulationKey768 // ML-KEM-768 secret key
}

// X25519Secret returns the raw X25519 secret key bytes (to be sealed under VK).
func (id *Identity) X25519Secret() []byte { return id.x25519Secret }

// MLKEMSeed returns the 64-byte ML-KEM-768 seed (d‖z) that regenerates the secret
// key (to be sealed under VK). crypto/mlkem stores the compact seed form.
func (id *Identity) MLKEMSeed() []byte { return id.mlkemDecap.Bytes() }

// GenerateIdentity creates a fresh hybrid identity: an X25519 keypair plus an
// ML-KEM-768 keypair (§6.3), matching VaultShareCrypto.newIdentity in the web.
func GenerateIdentity() (*Identity, error) {
	sk := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(sk); err != nil {
		return nil, err
	}
	pub, err := curve25519.X25519(sk, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	dk, err := mlkem.GenerateKey768()
	if err != nil {
		return nil, err
	}
	return &Identity{
		X25519Pub:     b64(pub),
		MLKEMEncapKey: b64(dk.EncapsulationKey().Bytes()),
		x25519Secret:  sk,
		mlkemDecap:    dk,
	}, nil
}

// NewIdentityFromSecrets rebuilds an Identity from sealed secret material: a raw
// 32-byte X25519 secret key and the 64-byte ML-KEM-768 seed.
func NewIdentityFromSecrets(x25519Secret, mlkemSeed []byte) (*Identity, error) {
	if len(x25519Secret) != curve25519.ScalarSize {
		return nil, errors.New("crypto: x25519 secret must be 32 bytes")
	}
	pub, err := curve25519.X25519(x25519Secret, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	dk, err := mlkem.NewDecapsulationKey768(mlkemSeed)
	if err != nil {
		return nil, err
	}
	return &Identity{
		X25519Pub:     b64(pub),
		MLKEMEncapKey: b64(dk.EncapsulationKey().Bytes()),
		x25519Secret:  x25519Secret,
		mlkemDecap:    dk,
	}, nil
}

// KEMEnvelope is the hybrid-wrap envelope (§6.3): {suite, epk, kem_ct, c, n}, all
// base64. It is an opaque string the server stores in wrapped_vault_key.
type KEMEnvelope struct {
	Suite int    `json:"suite"`
	Epk   string `json:"epk"`
	KemCt string `json:"kem_ct"`
	C     string `json:"c"`
	N     string `json:"n"`
}

// deriveWrapKey computes HKDF-SHA256(ss_ec ‖ ss_pq, info="ledgerline/kem/v1"+ctx)
// → a 32-byte wrap key, with an empty salt (standard HKDF default).
func deriveWrapKey(ssEc, ssPq []byte, context string) ([]byte, error) {
	ikm := make([]byte, 0, len(ssEc)+len(ssPq))
	ikm = append(ikm, ssEc...)
	ikm = append(ikm, ssPq...)
	info := []byte(kemInfoPrefix + context)
	r := hkdf.New(sha256.New, ikm, nil, info)
	out := make([]byte, wrapKeyLen)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

// HybridWrap wraps payload (e.g. a raw vault key) to a recipient's public identity
// using the post-quantum hybrid KEM (§6.3). It returns the JSON-encoded envelope.
func HybridWrap(payload []byte, recipientX25519PubB64, recipientMLKEMEkB64, context string) (string, error) {
	recipPub, err := unb64(recipientX25519PubB64)
	if err != nil {
		return "", fmt.Errorf("crypto: recipient x25519 pub: %w", err)
	}
	ekBytes, err := unb64(recipientMLKEMEkB64)
	if err != nil {
		return "", fmt.Errorf("crypto: recipient mlkem ek: %w", err)
	}
	ek, err := mlkem.NewEncapsulationKey768(ekBytes)
	if err != nil {
		return "", fmt.Errorf("crypto: parse mlkem ek: %w", err)
	}

	// PQ leg: ML-KEM-768 encapsulation to the recipient's ek.
	ssPq, kemCt := ek.Encapsulate()

	// Classical leg: ephemeral X25519 DH against the recipient's x25519 pub.
	ephSk := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(ephSk); err != nil {
		return "", err
	}
	ephPk, err := curve25519.X25519(ephSk, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	ssEc, err := curve25519.X25519(ephSk, recipPub)
	if err != nil {
		return "", fmt.Errorf("crypto: x25519 wrap: %w", err)
	}

	wrapKey, err := deriveWrapKey(ssEc, ssPq, context)
	if err != nil {
		return "", err
	}
	sealed, err := Seal(payload, wrapKey)
	if err != nil {
		return "", err
	}
	env := KEMEnvelope{Suite: SuiteV3, Epk: b64(ephPk), KemCt: b64(kemCt), C: sealed.C, N: sealed.N}
	data, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// HybridUnwrap recovers a payload from a hybrid-KEM envelope with the recipient's
// own secret identity keys. Fail-closed on an unknown suite or authentication
// failure (§6.1/§6.3).
func HybridUnwrap(envelopeJSON string, id *Identity, context string) ([]byte, error) {
	var env KEMEnvelope
	if err := json.Unmarshal([]byte(envelopeJSON), &env); err != nil {
		return nil, err
	}
	if env.Suite != SuiteV3 {
		return nil, fmt.Errorf("crypto: unknown KEM envelope suite: %d", env.Suite)
	}
	kemCt, err := unb64(env.KemCt)
	if err != nil {
		return nil, err
	}
	ssPq, err := id.mlkemDecap.Decapsulate(kemCt)
	if err != nil {
		return nil, fmt.Errorf("crypto: mlkem decapsulate: %w", err)
	}
	epk, err := unb64(env.Epk)
	if err != nil {
		return nil, err
	}
	ssEc, err := curve25519.X25519(id.x25519Secret, epk)
	if err != nil {
		return nil, fmt.Errorf("crypto: x25519 unwrap: %w", err)
	}
	wrapKey, err := deriveWrapKey(ssEc, ssPq, context)
	if err != nil {
		return nil, err
	}
	return Open(Sealed{C: env.C, N: env.N}, wrapKey)
}
