package canonicaljson

import "testing"

func TestCanonicalizeSortsKeys(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"flat keys", `{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{"nested + array order", `{"z":[3,1,2],"a":{"y":1,"x":2}}`, `{"a":{"x":2,"y":1},"z":[3,1,2]}`},
		{"whitespace stripped", "{\n  \"a\" : 1 ,\n  \"b\" : 2\n}", `{"a":1,"b":2}`},
		{"bools and null", `{"c":null,"b":false,"a":true}`, `{"a":true,"b":false,"c":null}`},
		{"unicode scalar sort", `{"b":1,"B":2,"a":3,"A":4}`, `{"A":4,"B":2,"a":3,"b":1}`},
		{"nested objects keep array order, sort within", `[{"b":1,"a":2},{"d":3,"c":4}]`, `[{"a":2,"b":1},{"c":4,"d":3}]`},
		{"empty object/array", `{"o":{},"a":[]}`, `{"a":[],"o":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Canonicalize([]byte(tc.in))
			if err != nil {
				t.Fatalf("Canonicalize: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestCanonicalizeStringEscaping(t *testing.T) {
	// Minimal JSON escaping (ASCII cases), matching JS JSON.stringify: escape only
	// '"', '\\' and control characters. Non-ASCII / no-normalization behaviour is
	// asserted by the fixture-driven conformance test (internal/conformance).
	cases := []struct{ in, want string }{
		{`{"s":"a\nb"}`, `{"s":"a\nb"}`},
		{`{"s":"tab\tend"}`, `{"s":"tab\tend"}`},
		{`{"s":"q\"q"}`, `{"s":"q\"q"}`},
		{`{"s":"back\\slash"}`, `{"s":"back\\slash"}`},
		{`{"s":""}`, `{"s":""}`},
		{`{"s":"\r\b\f"}`, `{"s":"\r\b\f"}`},
	}
	for _, tc := range cases {
		got, err := Canonicalize([]byte(tc.in))
		if err != nil {
			t.Fatalf("Canonicalize(%q): %v", tc.in, err)
		}
		if string(got) != tc.want {
			t.Fatalf("in %q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalizeIntegers(t *testing.T) {
	in := `{"big":123456789012345,"neg":-7,"zero":0}`
	want := `{"big":123456789012345,"neg":-7,"zero":0}`
	got, err := Canonicalize([]byte(in))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestCanonicalizeRejectsFloats(t *testing.T) {
	for _, in := range []string{`{"lat":52.520008}`, `{"x":1.0}`, `{"x":1e3}`} {
		if _, err := Canonicalize([]byte(in)); err == nil {
			t.Fatalf("expected float rejection for %q", in)
		}
	}
}

func TestCanonicalizeDecStringDecimals(t *testing.T) {
	in := `{"lng":"13.404954","lat":"52.520008"}`
	want := `{"lat":"52.520008","lng":"13.404954"}`
	got, err := Canonicalize([]byte(in))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMarshal(t *testing.T) {
	v := map[string]any{"b": 2, "a": 1}
	got, err := Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != `{"a":1,"b":2}` {
		t.Fatalf("got %q", got)
	}
}

func TestFormatDecimal(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{52.520008, "52.520008"},
		{13.404954, "13.404954"},
		{-1.5, "-1.500000"},
		{0, "0.000000"},
	}
	for _, tc := range cases {
		if got := FormatDecimal(tc.in); got != tc.want {
			t.Fatalf("FormatDecimal(%v) = %q want %q", tc.in, got, tc.want)
		}
	}
}
