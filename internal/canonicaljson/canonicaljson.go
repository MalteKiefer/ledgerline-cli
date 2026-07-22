// Package canonicaljson produces the byte-identical JSON serialization used by
// the Ledgerline Store v3 web client (resources/js/shared/canonical-json.js),
// the source of truth gated by the §17 fixture: object keys sorted ascending by
// Unicode scalar value, compact separators with no insignificant whitespace,
// UTF-8 strings emitted verbatim (NO Unicode normalization — combining
// sequences survive) with minimal escaping (matching JS JSON.stringify), and
// integer-only numbers (decimals such as lat/lng are carried as fixed-decimal
// strings, never floats).
//
// Every byte that is sealed or hashed for dirty-detection must pass through this
// package so that JS, Swift, Go and Kotlin clients agree on the exact bytes.
// Cold per-photo meta blobs (which carry embedding floats) are immutable and
// never hashed, so they are not canonicalized.
package canonicaljson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ErrFloat is returned when a non-integer number is encountered. Hot records are
// integer-only by contract; decimals must be dec-strings.
var ErrFloat = errors.New("canonicaljson: floating-point numbers are not allowed")

// Marshal serializes v to canonical JSON. It is the sealing/hashing entry point
// for typed values; it round-trips through encoding/json and then canonicalizes.
func Marshal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Canonicalize(raw)
}

// Canonicalize re-serializes arbitrary valid JSON into the canonical form. It
// preserves array order, sorts object keys, strips whitespace, NFC-normalizes
// strings, and rejects floating-point numbers.
func Canonicalize(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := encode(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encode(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		return encodeNumber(buf, t)
	case string:
		encodeString(buf, t)
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encode(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		// Sort ascending by Unicode scalar value. Go string comparison is by
		// byte, and UTF-8 byte order matches code-point order, so this is the
		// required ordering.
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			encodeString(buf, k)
			buf.WriteByte(':')
			if err := encode(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonicaljson: unsupported type %T", v)
	}
	return nil
}

// encodeNumber emits an integer verbatim (its canonical decimal form is identical
// across languages) and rejects anything with a fractional or exponent part.
func encodeNumber(buf *bytes.Buffer, n json.Number) error {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		return ErrFloat
	}
	// Confirm it is a valid integer (guards odd inputs like "+1" or leading zeros
	// that JSON would already have rejected, but be strict regardless).
	if _, err := strconv.ParseInt(s, 10, 64); err != nil {
		// Fall back to big integers beyond int64 range.
		for i, r := range s {
			if r == '-' && i == 0 {
				continue
			}
			if r < '0' || r > '9' {
				return ErrFloat
			}
		}
	}
	buf.WriteString(s)
	return nil
}

// encodeString writes a JSON string with minimal escaping, matching JS
// JSON.stringify: escape only '"', '\\' and control characters (<0x20); emit all
// other runes (including non-ASCII) literally as UTF-8, with no Unicode
// normalization so combining sequences survive verbatim (§5.2 byte-stability).
func encodeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				buf.WriteString(`\u`)
				const hex = "0123456789abcdef"
				buf.WriteByte('0')
				buf.WriteByte('0')
				buf.WriteByte(hex[(r>>4)&0xf])
				buf.WriteByte(hex[r&0xf])
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}

// FormatDecimal renders f as a fixed 6-decimal-place string (the canonical
// dec-string form for lat/lng), matching JavaScript's Number.prototype.toFixed(6).
func FormatDecimal(f float64) string {
	return strconv.FormatFloat(f, 'f', 6, 64)
}
