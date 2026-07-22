package crypto

import "testing"

// FuzzDecryptContent feeds arbitrary bytes as a "blob" to the secretstream frame
// decoder. A hostile length prefix or truncated/garbage frame must fail cleanly
// (never panic, never drive an unbounded allocation from an attacker-controlled
// u32 length — the decoder slices the existing blob, it does not make() its
// size). §6/§31.
func FuzzDecryptContent(f *testing.F) {
	vk := rep(0x42, 32)
	// A real blob as a seed so the corpus includes a well-formed frame.
	blob, key, err := EncryptContent([]byte("fuzz seed plaintext"), vk)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(blob)
	f.Add([]byte{})
	f.Add(make([]byte, streamHeaderBytes))
	f.Add(append(append([]byte{}, blob[:streamHeaderBytes]...), 0xff, 0xff, 0xff, 0xff)) // header + huge len prefix

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must return cleanly regardless of input; a decode success on random
		// data is astronomically unlikely but also fine (it authenticated).
		_, _ = DecryptContent(data, key, vk)
	})
}

// FuzzOpenManifest feeds arbitrary strings to the sealed-manifest envelope parser
// and opener: malformed JSON, an unknown suite, or garbage ciphertext must fail
// closed without panicking.
func FuzzOpenManifest(f *testing.F) {
	vk := rep(0x7e, 32)
	sealed, err := SealManifest([]byte(`{"v":3,"a":1}`), vk)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(sealed)
	f.Add(`{"suite":2,"c":"","n":""}`)
	f.Add(`{"c":"!!not-base64","n":"x"}`)
	f.Add(`not json`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, s string) {
		_, _ = OpenManifest(s, vk)
	})
}
