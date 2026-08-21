package trayui

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"math"
)

// icoHeaderLen is ICONDIR (6 bytes) plus one ICONDIRENTRY (16 bytes): the
// offset at which the single image payload starts.
const icoHeaderLen = 6 + 16

// IconSize is the edge length used for menu/tray icons. Windows asks for 16x16
// in menus; a single square keeps the conversion trivial and looks right after
// the shell's own scaling.
const IconSize = 16

// ICOFromImage encodes img as a Windows .ico holding one PNG-compressed entry.
// Windows Vista and later read PNG inside ICO, which avoids hand-rolling a
// bottom-up BMP with an AND mask — fewer places to get transparency wrong.
func ICOFromImage(img image.Image, size int) ([]byte, error) {
	if img == nil {
		return nil, errors.New("trayui: no image")
	}
	if size <= 0 || size > 256 {
		size = IconSize
	}
	scaled := scaleSquare(img, size)

	var payload bytes.Buffer
	if err := png.Encode(&payload, scaled); err != nil {
		return nil, err
	}
	// The ICO directory stores the payload length in 32 bits. A 16-pixel icon
	// cannot come close, but the encoder's output is not this function's to
	// trust silently.
	if payload.Len() > math.MaxUint32-icoHeaderLen {
		return nil, errors.New("trayui: icon payload too large for the ICO format")
	}

	// ICONDIR (6 bytes) + one ICONDIRENTRY (16 bytes) + the PNG payload.
	var out bytes.Buffer
	write := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	write(uint16(0)) // reserved
	write(uint16(1)) // type: icon
	write(uint16(1)) // image count
	dim := byte(size)
	if size >= 256 {
		dim = 0 // 0 means 256 in the ICO format
	}
	out.WriteByte(dim)           // width
	out.WriteByte(dim)           // height
	out.WriteByte(0)             // palette size (0 = truecolour)
	out.WriteByte(0)             // reserved
	write(uint16(1))             // colour planes
	write(uint16(32))            // bits per pixel
	write(uint32(payload.Len())) //nolint:gosec // bounded above
	write(uint32(icoHeaderLen))  // offset of the payload
	out.Write(payload.Bytes())
	return out.Bytes(), nil
}

// scaleSquare resamples to size x size with a box filter. The standard library
// has no scaler and pulling golang.org/x/image in for a 16-pixel icon is not
// worth a dependency; a box filter is the right quality for this size anyway.
func scaleSquare(img image.Image, size int) *image.NRGBA {
	src := image.NewNRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(src, src.Bounds(), img, img.Bounds().Min, draw.Src)
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	if sw == 0 || sh == 0 {
		return dst
	}
	for y := range size {
		y0 := y * sh / size
		y1 := max((y+1)*sh/size, y0+1)
		for x := range size {
			x0 := x * sw / size
			x1 := max((x+1)*sw/size, x0+1)
			var r, g, b, a, n float64
			for sy := y0; sy < y1 && sy < sh; sy++ {
				for sx := x0; sx < x1 && sx < sw; sx++ {
					i := src.PixOffset(sx, sy)
					r += float64(src.Pix[i])
					g += float64(src.Pix[i+1])
					b += float64(src.Pix[i+2])
					a += float64(src.Pix[i+3])
					n++
				}
			}
			if n == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i] = clamp8(r / n)
			dst.Pix[i+1] = clamp8(g / n)
			dst.Pix[i+2] = clamp8(b / n)
			dst.Pix[i+3] = clamp8(a / n)
		}
	}
	return dst
}

func clamp8(v float64) uint8 {
	return uint8(math.Max(0, math.Min(255, math.Round(v))))
}
