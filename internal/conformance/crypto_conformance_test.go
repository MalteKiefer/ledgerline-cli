package conformance

import (
	"crypto/mlkem"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
	"github.com/MalteKiefer/ledgerline-cli/internal/gallery"
)

// lcgBytes reproduces the fixture's pure LCG byte generator (see the web
// store-v3-conformance test): s = (s*1664525 + 1013904223) mod 2^32, byte = s&0xff.
func lcgBytes(n int, seed uint32) []byte {
	out := make([]byte, n)
	s := seed
	for i := 0; i < n; i++ {
		s = s*1664525 + 1013904223 // uint32 wraparound == JS >>> 0
		out[i] = byte(s & 0xff)
	}
	return out
}

// TestSigFixture gates gallery.FileSig against the shared sig.json fixture (§6.5).
func TestSigFixture(t *testing.T) {
	var fx struct {
		Cases []struct {
			Name     string `json:"name"`
			Size     int    `json:"size"`
			Seed     uint32 `json:"seed"`
			Expected string `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(fixture(t, "sig.json"), &fx); err != nil {
		t.Fatalf("parse sig.json: %v", err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("no sig fixture cases")
	}
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := gallery.FileSig(lcgBytes(tc.Size, tc.Seed))
			if got != tc.Expected {
				t.Fatalf("got %q want %q", got, tc.Expected)
			}
		})
	}
}

// TestMLKEMKATFixture gates ML-KEM-768 against the NIST/FIPS-203 KAT fixture
// (§17). Go's crypto/mlkem exposes deterministic key generation from the 64-byte
// seed, so the encapsulation-key digest is a strong cross-implementation anchor
// (it must equal @noble/post-quantum's for the same seed). Go's Encapsulate is not
// seedable and DecapsulationKey768.Bytes() returns the 64-byte seed (not the
// 2400-byte expanded key), so the ct/sharedSecret/dk-digest KAT values (which
// require a seeded encaps / expanded dk) are validated on the JS side; here we
// pin ekSha256 + lengths and an encaps→decaps round-trip.
func TestMLKEMKATFixture(t *testing.T) {
	var kat struct {
		Seed     string `json:"seed"`
		EkSha256 string `json:"ekSha256"`
		EkLen    int    `json:"ekLen"`
		CtLen    int    `json:"ctLen"`
	}
	if err := json.Unmarshal(fixture(t, "mlkem768-kat.json"), &kat); err != nil {
		t.Fatalf("parse mlkem768-kat.json: %v", err)
	}
	seed, err := hex.DecodeString(kat.Seed)
	if err != nil {
		t.Fatalf("decode seed: %v", err)
	}

	dk, err := mlkem.NewDecapsulationKey768(seed)
	if err != nil {
		t.Fatalf("deterministic keygen: %v", err)
	}
	ek := dk.EncapsulationKey().Bytes()
	if len(ek) != kat.EkLen {
		t.Fatalf("ek len = %d want %d", len(ek), kat.EkLen)
	}
	sum := sha256.Sum256(ek)
	if got := hex.EncodeToString(sum[:]); got != kat.EkSha256 {
		t.Fatalf("ekSha256 = %s want %s (FIPS-203 keygen divergence)", got, kat.EkSha256)
	}

	// Encaps → decaps round-trip closes the interop loop.
	ss1, ct := dk.EncapsulationKey().Encapsulate()
	if len(ct) != kat.CtLen {
		t.Fatalf("ct len = %d want %d", len(ct), kat.CtLen)
	}
	if len(ss1) != mlkem.SharedKeySize {
		t.Fatalf("shared secret len = %d want %d", len(ss1), mlkem.SharedKeySize)
	}
	ss2, err := dk.Decapsulate(ct)
	if err != nil {
		t.Fatalf("decapsulate: %v", err)
	}
	if hex.EncodeToString(ss1) != hex.EncodeToString(ss2) {
		t.Fatal("encaps/decaps shared secrets differ")
	}
}

// TestHybridKEMWrapUnwrap exercises the full §6.3 hybrid wrap→unwrap of a known
// vault key across two freshly generated identities (encaps/decaps interop).
func TestHybridKEMWrapUnwrap(t *testing.T) {
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	vk := make([]byte, 32)
	for i := range vk {
		vk[i] = byte(i)
	}
	env, err := crypto.HybridWrap(vk, id.X25519Pub, id.MLKEMEncapKey, "vault:conformance")
	if err != nil {
		t.Fatal(err)
	}
	out, err := crypto.HybridUnwrap(env, id, "vault:conformance")
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(out) != hex.EncodeToString(vk) {
		t.Fatal("hybrid wrap/unwrap did not recover the vault key")
	}
}
