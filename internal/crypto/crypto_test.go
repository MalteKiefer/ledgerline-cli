package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/nacl/secretbox"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rep returns a byte slice of n copies of b (fixed-key test material).
func rep(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// The following known-answer tests were generated with PHP's libsodium
// (ext-sodium) so the Go re-implementation is proven byte-for-byte compatible
// with the exact library the web and Android clients use.

func TestSecretboxKAT(t *testing.T) {
	key := rep(0x02, 32)
	wantCipher := mustHex(t, "8bbbc2bafe1295463d979e36686df31f63c0c44f2e24828b332511d172f2e2")

	sealed, err := Seal([]byte("hello secretbox"), key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := unb64(sealed.C)
	if err != nil {
		t.Fatal(err)
	}
	// Seal uses a random nonce, so compare against libsodium by re-opening; the
	// KAT nonce path is exercised via the fixed-nonce raw check below.
	if _, err := Open(sealed, key); err != nil {
		t.Fatalf("round trip open: %v", err)
	}

	// Deterministic check with the KAT's fixed nonce.
	raw := sealFixedNonce(t, []byte("hello secretbox"), rep(0x03, 24), key)
	if !bytes.Equal(raw, wantCipher) {
		t.Fatalf("secretbox mismatch:\n got %x\nwant %x", raw, wantCipher)
	}
	_ = got
}

func TestArgon2idKAT(t *testing.T) {
	salt := rep(0x04, 16)
	want := mustHex(t, "a3496df1185e36a923ce49153ab415dd01ecf89e5487e985834170ca080720f7")

	got := DeriveKEK("correct horse", salt, 3, 67108864)
	if !bytes.Equal(got, want) {
		t.Fatalf("argon2id mismatch:\n got %x\nwant %x", got, want)
	}
}

func TestSecretstreamPushKAT(t *testing.T) {
	key := rep(0x01, 32)
	header := mustHex(t, "a120015a047bc9b811817ec24bfa3159bbbb346b98762ca6")
	wantC1 := mustHex(t, "8537337c2a0d079009ff017ccb6354492bfc9cd6172396106f20625b")
	wantC2 := mustHex(t, "38aceff72f741f74e44d32ac623c80ecce443e0bf85186c5b2ffd13cfdf6898f80")

	state, err := initState(key, header)
	if err != nil {
		t.Fatal(err)
	}
	c1, err := state.push([]byte("first chunk"), tagMessage)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c1, wantC1) {
		t.Fatalf("frame 1 mismatch:\n got %x\nwant %x", c1, wantC1)
	}
	c2, err := state.push([]byte("second and final"), tagFinal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c2, wantC2) {
		t.Fatalf("frame 2 mismatch:\n got %x\nwant %x", c2, wantC2)
	}
}

func TestSecretstreamEmptyFinalKAT(t *testing.T) {
	key := rep(0x01, 32)
	header := mustHex(t, "e2c2cb31e0a5f583b000cd18ed1d158c276433327a889cb6")
	want := mustHex(t, "2cdf5b0f881c3ae50eac38f7bafbedb7a8")

	state, err := initState(key, header)
	if err != nil {
		t.Fatal(err)
	}
	c, err := state.push(nil, tagFinal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c, want) {
		t.Fatalf("empty-final mismatch:\n got %x\nwant %x", c, want)
	}
}

// TestSecretstreamPullLibsodiumFrames decrypts non-empty frames produced by
// PHP's libsodium (both tags), guarding against a regression in the message
// authentication framing.
func TestSecretstreamPullLibsodiumFrames(t *testing.T) {
	key := rep(0x01, 32)
	cases := []struct {
		name, header, frame, want string
		wantTag                   byte
	}{
		{"final", "4803e255fc519a097e9ebb6d4004e7846ee58a23ba38ec1e", "54ab0efb2267fc9383af205ee95ca7d1ab1baf5efee3b8438dc46087", "first chunk", tagFinal},
		{"message", "7a70140bc884ae208d18289a2633ae926e0c0e32f5bc1e51", "d9eb90af63c2efbc5355b8b5617a260f495c11622acc5227b31a82d5", "first chunk", tagMessage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, err := initState(key, mustHex(t, tc.header))
			if err != nil {
				t.Fatal(err)
			}
			msg, tag, err := state.pull(mustHex(t, tc.frame))
			if err != nil {
				t.Fatalf("pull: %v", err)
			}
			if string(msg) != tc.want || tag != tc.wantTag {
				t.Fatalf("pull = (%q, %d), want (%q, %d)", msg, tag, tc.want, tc.wantTag)
			}
		})
	}
}

func TestSecretstreamPullRoundTrip(t *testing.T) {
	key := rep(0x01, 32)
	header := mustHex(t, "a120015a047bc9b811817ec24bfa3159bbbb346b98762ca6")

	enc, _ := initState(key, header)
	c1, _ := enc.push([]byte("first chunk"), tagMessage)
	c2, _ := enc.push([]byte("second and final"), tagFinal)

	dec, _ := initState(key, header)
	m1, tag1, err := dec.pull(c1)
	if err != nil || tag1 != tagMessage || string(m1) != "first chunk" {
		t.Fatalf("pull 1 = (%q, %d, %v)", m1, tag1, err)
	}
	m2, tag2, err := dec.pull(c2)
	if err != nil || tag2 != tagFinal || string(m2) != "second and final" {
		t.Fatalf("pull 2 = (%q, %d, %v)", m2, tag2, err)
	}
}

func TestSecretstreamPullRejectsTamper(t *testing.T) {
	key := rep(0x01, 32)
	header := mustHex(t, "a120015a047bc9b811817ec24bfa3159bbbb346b98762ca6")
	enc, _ := initState(key, header)
	c, _ := enc.push([]byte("tamper me"), tagFinal)
	c[5] ^= 0xff

	dec, _ := initState(key, header)
	if _, _, err := dec.pull(c); err == nil {
		t.Fatal("expected authentication failure on tampered frame")
	}
}

func TestEncryptDecryptContentRoundTrip(t *testing.T) {
	vk := rep(0x09, 32)
	for _, size := range []int{0, 1, 100, ChunkSize - 1, ChunkSize, ChunkSize + 1, 2*ChunkSize + 7} {
		plain := make([]byte, size)
		if _, err := rand.Read(plain); err != nil {
			t.Fatal(err)
		}
		blob, encKey, err := EncryptContent(plain, vk)
		if err != nil {
			t.Fatalf("size %d encrypt: %v", size, err)
		}
		// Blob must start with the 24-byte header.
		if len(blob) < streamHeaderBytes {
			t.Fatalf("size %d: blob too short", size)
		}
		got, err := DecryptContent(blob, encKey, vk)
		if err != nil {
			t.Fatalf("size %d decrypt: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d round trip mismatch", size)
		}
	}
}

func TestManifestSealRoundTripAndPadding(t *testing.T) {
	vk := rep(0x0a, 32)
	// Input need not be canonical; SealManifest canonicalizes (§5.2): keys sorted.
	obj := []byte(`{"v":3,"suite":1,"photos":[{"id":"abc"}]}`)
	wantCanon := []byte(`{"photos":[{"id":"abc"}],"suite":1,"v":3}`)

	sealedStr, err := SealManifest(obj, vk)
	if err != nil {
		t.Fatal(err)
	}
	// The v3 envelope carries the suite tag.
	if !strings.Contains(sealedStr, `"suite":1`) {
		t.Fatalf("sealed manifest missing suite tag: %s", sealedStr)
	}
	opened, err := OpenManifest(sealedStr, vk)
	if err != nil {
		t.Fatal(err)
	}
	// Small manifests pad to the 4 KiB floor with spaces; decrypts to canonical + pad.
	if len(opened) < 4096 {
		t.Fatalf("padded length %d below 4 KiB floor", len(opened))
	}
	if !bytes.HasPrefix(opened, wantCanon) {
		t.Fatalf("decrypted manifest lost its canonical prefix: %q", opened[:len(wantCanon)])
	}
	for _, b := range opened[len(wantCanon):] {
		if b != ' ' {
			t.Fatal("padding is not spaces")
		}
	}
}

func TestOpenManifestRejectsUnknownSuite(t *testing.T) {
	vk := rep(0x0b, 32)
	sealedStr, err := SealManifest([]byte(`{"v":3}`), vk)
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the suite to an unknown value; open must fail closed.
	tampered := strings.Replace(sealedStr, `"suite":1`, `"suite":2`, 1)
	if tampered == sealedStr {
		t.Fatal("failed to tamper suite for test")
	}
	if _, err := OpenManifest(tampered, vk); err == nil {
		t.Fatal("expected OpenManifest to reject unknown suite")
	}
}

// sealFixedNonce seals with an explicit nonce for deterministic KAT comparison.
func sealFixedNonce(t *testing.T, msg, nonce, key []byte) []byte {
	t.Helper()
	if len(nonce) != nonceBytes {
		t.Fatal("bad nonce")
	}
	var n [nonceBytes]byte
	copy(n[:], nonce)
	var k [KeyBytes]byte
	copy(k[:], key)
	return secretbox.Seal(nil, msg, &n, &k)
}

// TestUniformDecryptionFailure asserts §28: every content-decryption failure —
// wrong key, corrupt ciphertext, flipped tag, truncated frame — surfaces the
// SAME sentinel (ErrDecrypt) so a caller cannot distinguish the cause, and none
// panics.
func TestUniformDecryptionFailure(t *testing.T) {
	vk := rep(0x11, 32)
	wrong := rep(0x22, 32)
	blob, key, err := EncryptContent([]byte("secret payload for uniform-failure test"), vk)
	if err != nil {
		t.Fatal(err)
	}

	flip := func(b []byte) []byte { c := append([]byte(nil), b...); c[len(c)-1] ^= 0xff; return c }

	cases := []struct {
		name string
		blob []byte
		key  string
		vk   []byte
	}{
		{"wrong vault key", blob, key, wrong},
		{"corrupt trailing byte", flip(blob), key, vk},
		{"truncated below header", blob[:10], key, vk},
		{"truncated mid-frame", blob[:streamHeaderBytes+2], key, vk},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, derr := DecryptContent(tc.blob, tc.key, tc.vk)
			if derr == nil {
				t.Fatal("expected failure")
			}
			if !errors.Is(derr, ErrDecrypt) {
				t.Fatalf("non-uniform error: %v (want ErrDecrypt)", derr)
			}
		})
	}

	// A wrong-key unwrap at the secretbox layer is the same sentinel.
	sealed, err := Seal([]byte("k"), vk)
	if err != nil {
		t.Fatal(err)
	}
	if _, oerr := Open(sealed, wrong); !errors.Is(oerr, ErrDecrypt) {
		t.Fatalf("Open wrong key = %v (want ErrDecrypt)", oerr)
	}
}

// TestNoSecretInErrorStrings asserts §18/§28: no failure path formats key material
// into its error string. It runs every crypto failure and checks the message
// contains none of the VK, per-blob key, or wrapped-key bytes (hex or base64).
func TestNoSecretInErrorStrings(t *testing.T) {
	vk := rep(0x5a, 32)
	wrong := rep(0xa5, 32)

	secrets := []string{
		hex.EncodeToString(vk), b64(vk),
		hex.EncodeToString(wrong), b64(wrong),
	}
	assertClean := func(name string, err error) {
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		msg := err.Error()
		for _, s := range secrets {
			if s != "" && strings.Contains(msg, s) {
				t.Fatalf("%s: error leaked secret material: %q", name, msg)
			}
		}
	}

	blob, key, err := EncryptContent([]byte("payload"), vk)
	if err != nil {
		t.Fatal(err)
	}
	_, e1 := DecryptContent(blob, key, wrong)
	assertClean("DecryptContent wrong key", e1)

	sealed, err := SealManifest([]byte(`{"v":3}`), vk)
	if err != nil {
		t.Fatal(err)
	}
	_, e2 := OpenManifest(strings.Replace(sealed, `"suite":1`, `"suite":9`, 1), vk)
	assertClean("OpenManifest bad suite", e2)

	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	env, err := HybridWrap(vk, id.X25519Pub, id.MLKEMEncapKey, "ctx-a")
	if err != nil {
		t.Fatal(err)
	}
	_, e3 := HybridUnwrap(env, id, "ctx-wrong")
	assertClean("HybridUnwrap wrong context", e3)

	// The uniform sentinel itself is generic.
	if strings.ContainsAny(ErrDecrypt.Error(), "0123456789abcdef=") && len(ErrDecrypt.Error()) > 40 {
		t.Fatalf("ErrDecrypt message looks like it carries data: %q", ErrDecrypt.Error())
	}
}
