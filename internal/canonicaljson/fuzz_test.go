package canonicaljson

import (
	"bytes"
	"testing"
)

// FuzzCanonicalize feeds arbitrary bytes to the canonical-JSON encoder: it must
// never panic on hostile input, and any input it accepts must re-encode to itself
// (idempotence — the canonical form is a fixed point).
func FuzzCanonicalize(f *testing.F) {
	for _, s := range []string{
		`{}`, `[]`, `{"b":1,"a":2}`, `{"z":[3,1,2],"a":{"y":1}}`,
		`{"lat":"52.520008"}`, `"str"`, `123`, `true`, `null`,
		`{"nested":{"deep":{"x":[1,2,{"k":"v"}]}}}`, `{"unicode":"café日本"}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := Canonicalize(data)
		if err != nil {
			return // invalid JSON or a float — rejected cleanly, no panic
		}
		again, err := Canonicalize(out)
		if err != nil {
			t.Fatalf("re-canonicalize of accepted output failed: %v (out=%q)", err, out)
		}
		if !bytes.Equal(out, again) {
			t.Fatalf("not idempotent:\n first=%q\n second=%q", out, again)
		}
	})
}
