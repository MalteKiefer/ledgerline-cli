package trayui

import (
	"image"
	"image/color"
	"math"
	"sync"
)

// Brand colours, matching the web app's Material seed (#6750a4) so the tray
// reads as the same product. The muted variant marks "signed out or server
// unreachable" without needing a second visual language.
var (
	brandActive = color.NRGBA{R: 0x67, G: 0x50, B: 0xa4, A: 0xff}
	brandMuted  = color.NRGBA{R: 0x8a, G: 0x8a, B: 0x8e, A: 0xff}
	brandGlyph  = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
)

// trayIconSize is drawn larger than a menu icon: the shell scales the tray icon
// per DPI, and downscaling a 32px source beats upscaling a 16px one.
const trayIconSize = 32

var (
	brandOnce  sync.Once
	brandIcons [2][]byte // [0] = muted, [1] = active
)

// BrandIcon returns the tray icon as .ico bytes. It is drawn in code rather
// than shipped as a binary asset so there is one source of truth for the shape
// and nothing to keep in sync in the repo.
func BrandIcon(active bool) []byte {
	brandOnce.Do(func() {
		for i, fill := range []color.NRGBA{brandMuted, brandActive} {
			ico, err := ICOFromImage(drawBrand(trayIconSize, fill), trayIconSize)
			if err != nil {
				// Drawing and PNG-encoding an in-memory image cannot realistically
				// fail; an empty icon degrades to the shell's default rather than
				// taking the tray down.
				ico = nil
			}
			brandIcons[i] = ico
		}
	})
	if active {
		return brandIcons[1]
	}
	return brandIcons[0]
}

// drawBrand paints a filled circle with a white "L" — legible at 16 px, which
// a more detailed mark would not be.
func drawBrand(size int, fill color.NRGBA) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	c := float64(size-1) / 2
	radius := float64(size)/2 - float64(size)*0.03

	for y := range size {
		for x := range size {
			// Distance from the centre, with a one-pixel feather so the circle
			// does not look jagged at tray size.
			d := math.Hypot(float64(x)-c, float64(y)-c)
			switch {
			case d <= radius-1:
				img.SetNRGBA(x, y, fill)
			case d <= radius:
				a := uint8(math.Round(float64(fill.A) * (radius - d)))
				img.SetNRGBA(x, y, color.NRGBA{R: fill.R, G: fill.G, B: fill.B, A: a})
			}
		}
	}

	// The "L": a vertical stem plus a foot, sized in fractions of the icon so it
	// scales with any size passed in.
	stemX0 := int(math.Round(float64(size) * 0.34))
	stemX1 := int(math.Round(float64(size) * 0.45))
	top := int(math.Round(float64(size) * 0.26))
	bottom := int(math.Round(float64(size) * 0.72))
	footX1 := int(math.Round(float64(size) * 0.68))
	footY0 := int(math.Round(float64(size) * 0.63))

	fillRect(img, stemX0, top, stemX1, bottom, brandGlyph)
	fillRect(img, stemX0, footY0, footX1, bottom, brandGlyph)
	return img
}

func fillRect(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if image.Pt(x, y).In(img.Bounds()) {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}
