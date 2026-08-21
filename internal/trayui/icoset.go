package trayui

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image/color"
	"image/png"
	"math"
	"sort"
)

// AppIconSizes are the sizes a Windows executable icon should carry: 16 for the
// small shell views, 32 for the desktop and Alt-Tab, 48 for large icons, 256
// for the extra-large view and the installer's own dialogs. Shipping fewer
// leaves the shell to scale, which is what makes an icon look muddy.
var AppIconSizes = []int{16, 32, 48, 256}

// BrandICO renders the brand mark as a multi-size Windows .ico.
//
// The same drawing code backs the tray icon, so the executable, the installer,
// the Start-menu shortcut and the notification area cannot drift apart — there
// is no checked-in image to forget to update.
func BrandICO(sizes ...int) ([]byte, error) {
	if len(sizes) == 0 {
		sizes = AppIconSizes
	}
	// Largest first is conventional in an ICO directory and keeps the offsets
	// deterministic for a reproducible build.
	ordered := append([]int(nil), sizes...)
	sort.Sort(sort.Reverse(sort.IntSlice(ordered)))

	type entry struct {
		size    int
		payload []byte
	}
	entries := make([]entry, 0, len(ordered))
	for _, size := range ordered {
		if size <= 0 || size > 256 {
			return nil, errors.New("trayui: icon size out of range")
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, drawBrand(size, brandActive)); err != nil {
			return nil, err
		}
		if buf.Len() > math.MaxUint32 {
			return nil, errors.New("trayui: icon payload too large for the ICO format")
		}
		entries = append(entries, entry{size: size, payload: buf.Bytes()})
	}

	var out bytes.Buffer
	write := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	write(uint16(0))              // reserved
	write(uint16(1))              // type: icon
	write(uint16(len(entries)))   //nolint:gosec // bounded by AppIconSizes
	offset := 6 + 16*len(entries) // directory size: header + one entry each

	for _, e := range entries {
		dim := byte(e.size) //nolint:gosec // sizes are validated to 1..256 above
		if e.size >= 256 {
			dim = 0 // 0 means 256 in the ICO format
		}
		out.WriteByte(dim)            // width
		out.WriteByte(dim)            // height
		out.WriteByte(0)              // palette size (0 = truecolour)
		out.WriteByte(0)              // reserved
		write(uint16(1))              // colour planes
		write(uint16(32))             // bits per pixel
		write(uint32(len(e.payload))) //nolint:gosec // bounded above
		write(uint32(offset))         //nolint:gosec // bounded by the payload sizes
		offset += len(e.payload)
	}
	for _, e := range entries {
		out.Write(e.payload)
	}
	return out.Bytes(), nil
}

// BrandColor is the mark's fill, exported so other renderers (an installer
// bitmap, say) can match it exactly.
func BrandColor() color.NRGBA { return brandActive }
