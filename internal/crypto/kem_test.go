package crypto

import (
	"bytes"
	"strings"
	"testing"
)

func TestHybridWrapUnwrapRoundTrip(t *testing.T) {
	recipient, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	vk := rep(0x5a, 32)

	env, err := HybridWrap(vk, recipient.X25519Pub, recipient.MLKEMEncapKey, "vault:conformance")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env, `"suite":1`) || !strings.Contains(env, `"epk"`) || !strings.Contains(env, `"kem_ct"`) {
		t.Fatalf("envelope missing expected fields: %s", env)
	}

	out, err := HybridUnwrap(env, recipient, "vault:conformance")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, vk) {
		t.Fatalf("unwrapped payload mismatch")
	}
}

func TestHybridUnwrapFailsClosed(t *testing.T) {
	recipient, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	env, err := HybridWrap(rep(0x11, 32), recipient.X25519Pub, recipient.MLKEMEncapKey, "ctx-a")
	if err != nil {
		t.Fatal(err)
	}

	// Wrong context → different HKDF wrap key → authentication failure.
	if _, err := HybridUnwrap(env, recipient, "ctx-b"); err == nil {
		t.Fatal("expected failure on wrong context")
	}

	// Unknown suite → fail closed.
	tampered := strings.Replace(env, `"suite":1`, `"suite":2`, 1)
	if _, err := HybridUnwrap(tampered, recipient, "ctx-a"); err == nil {
		t.Fatal("expected failure on unknown suite")
	}
}

func TestIdentityRoundTripFromSecrets(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	// Rebuild from sealed secret material; the wrap→unwrap must still succeed.
	rebuilt, err := NewIdentityFromSecrets(id.X25519Secret(), id.MLKEMSeed())
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.X25519Pub != id.X25519Pub || rebuilt.MLKEMEncapKey != id.MLKEMEncapKey {
		t.Fatal("rebuilt identity public keys differ")
	}
	vk := rep(0x7c, 32)
	env, err := HybridWrap(vk, id.X25519Pub, id.MLKEMEncapKey, "ctx")
	if err != nil {
		t.Fatal(err)
	}
	out, err := HybridUnwrap(env, rebuilt, "ctx")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, vk) {
		t.Fatal("round-trip via rebuilt identity failed")
	}
}
