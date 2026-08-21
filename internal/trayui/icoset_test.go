package trayui

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

// TestBrandICOCarriesEverySize: the shell picks the entry closest to what it
// needs, so a missing size means a scaled, muddy icon somewhere.
func TestBrandICOCarriesEverySize(t *testing.T) {
	raw, err := BrandICO()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 6 {
		t.Fatal("truncated")
	}
	count := int(binary.LittleEndian.Uint16(raw[4:6]))
	if count != len(AppIconSizes) {
		t.Fatalf("entries = %d, want %d", count, len(AppIconSizes))
	}

	seen := map[int]bool{}
	for i := range count {
		e := raw[6+16*i:]
		size := int(e[0])
		if size == 0 {
			size = 256
		}
		seen[size] = true

		length := int(binary.LittleEndian.Uint32(e[8:12]))
		offset := int(binary.LittleEndian.Uint32(e[12:16]))
		if offset+length > len(raw) {
			t.Fatalf("entry %d points past the end", i)
		}
		img, err := png.Decode(bytes.NewReader(raw[offset : offset+length]))
		if err != nil {
			t.Fatalf("entry %d is not a PNG: %v", i, err)
		}
		if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
			t.Fatalf("entry %d is %dx%d, declared %d", i, b.Dx(), b.Dy(), size)
		}
	}
	for _, want := range AppIconSizes {
		if !seen[want] {
			t.Fatalf("no %dpx entry", want)
		}
	}
}

func TestBrandICORejectsAnImpossibleSize(t *testing.T) {
	if _, err := BrandICO(512); err == nil {
		t.Fatal("512 accepted; the ICO format tops out at 256")
	}
}
