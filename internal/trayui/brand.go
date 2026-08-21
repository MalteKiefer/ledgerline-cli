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
	// The busy dot: green enough to read as activity, dark enough to hold its
	// shape against a white ring at 16 px.
	busyDot = color.NRGBA{R: 0x1b, G: 0x87, B: 0x54, A: 0xff}
)

// trayIconSize is drawn larger than a menu icon: the shell scales the tray icon
// per DPI, and downscaling a 32px source beats upscaling a 16px one.
const trayIconSize = 32

// IconState is what the tray icon says at a glance.
type IconState int

const (
	// IconMuted: signed out, or the server could not be reached.
	IconMuted IconState = iota
	// IconIdle: signed in, nothing running.
	IconIdle
	// IconBusy: a folder sync is running right now.
	IconBusy
)

var (
	brandOnce  sync.Once
	brandIcons [3][]byte // indexed by IconState
)

// BrandIcon returns the tray icon as .ico bytes. It is drawn in code rather
// than shipped as a binary asset so there is one source of truth for the shape
// and nothing to keep in sync in the repo.
func BrandIcon(active bool) []byte {
	if active {
		return StateIcon(IconIdle)
	}
	return StateIcon(IconMuted)
}

// StateIcon returns the tray icon for a state. The three are told apart by more
// than colour — the busy mark carries a visible dot — because a tray icon is
// 16 pixels of someone's peripheral vision and colour alone is a poor signal
// there, worse still for a colour-blind user.
func StateIcon(state IconState) []byte {
	brandOnce.Do(func() {
		specs := []struct {
			fill color.NRGBA
			busy bool
		}{
			{brandMuted, false},
			{brandActive, false},
			{brandActive, true},
		}
		for i, spec := range specs {
			ico, err := ICOFromImage(drawBrandState(trayIconSize, spec.fill, spec.busy), trayIconSize)
			if err != nil {
				// Drawing and PNG-encoding an in-memory image cannot realistically
				// fail; an empty icon degrades to the shell's default rather than
				// taking the tray down.
				ico = nil
			}
			brandIcons[i] = ico
		}
	})
	if state < 0 || int(state) >= len(brandIcons) {
		state = IconMuted
	}
	return brandIcons[state]
}

// drawBrand paints a filled circle with a white "L" — legible at 16 px, which
// a more detailed mark would not be.
func drawBrand(size int, fill color.NRGBA) image.Image {
	return drawBrandState(size, fill, false)
}

// drawBrandState paints the mark, optionally with the "busy" dot in the corner.
func drawBrandState(size int, fill color.NRGBA, busy bool) image.Image {
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

	if busy {
		drawBusyDot(img, size)
	}
	return img
}

// drawBusyDot marks "syncing" with a filled dot in the lower-right corner,
// ringed in white so it reads against both the mark and whatever the task bar
// is coloured.
func drawBusyDot(img *image.NRGBA, size int) {
	cx := float64(size) * 0.74
	cy := float64(size) * 0.74
	outer := float64(size) * 0.26
	inner := outer - math.Max(1, float64(size)*0.06)

	for y := range size {
		for x := range size {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			switch {
			case d <= inner:
				img.SetNRGBA(x, y, busyDot)
			case d <= outer:
				img.SetNRGBA(x, y, brandGlyph)
			}
		}
	}
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
