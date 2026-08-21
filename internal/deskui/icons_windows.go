//go:build windows

package deskui

import (
	"sort"
	"strings"
)

// The icon set.
//
// Monochrome line icons on a 24-unit grid with a 1.75 stroke, drawn to match
// the web app's Material Symbols Outlined: same weight, same optical size, same
// "one colour, no fill" rule. They are hand-built rather than pulled from a
// font because a page that cannot reach the network also cannot fetch a font,
// and embedding a 320 kB woff2 as a data URI to draw thirty glyphs is a poor
// trade. As stroke paths they inherit `currentColor` and stay crisp at any DPI.
//
// Every icon is rendered once into a <symbol> sprite and referenced by <use>,
// so an icon repeated in twenty rows costs one definition.
var icons = map[string]string{
	// --- identity and account ---
	"account": `<circle cx="12" cy="8" r="3.5"/><path d="M5 20a7 7 0 0 1 14 0"/>`,
	"key":     `<circle cx="8" cy="12" r="3"/><path d="M11 12h9m-3 0v3m-2-3v2"/>`,
	"logout":  `<path d="M14 5H6a1 1 0 0 0-1 1v12a1 1 0 0 0 1 1h8"/><path d="M17 8l3 4-3 4M10 12h10"/>`,
	"shield":  `<path d="M12 3l7 3v6c0 4-3 7.5-7 9-4-1.5-7-5-7-9V6z"/>`,

	// --- storage and files ---
	"cloud":    `<path d="M7 18h9a3.5 3.5 0 0 0 .3-7A5 5 0 0 0 7 10.5 3.75 3.75 0 0 0 7 18z"/>`,
	"database": `<ellipse cx="12" cy="6" rx="7" ry="3"/><path d="M5 6v12c0 1.7 3.1 3 7 3s7-1.3 7-3V6"/><path d="M5 12c0 1.7 3.1 3 7 3s7-1.3 7-3"/>`,
	"folder":   `<path d="M4 7a1 1 0 0 1 1-1h4l2 2h8a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1z"/>`,
	"file":     `<path d="M14 4H7a1 1 0 0 0-1 1v14a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V8z"/><path d="M14 4v4h4"/>`,
	"sync":     `<path d="M4 12a8 8 0 0 1 13.7-5.7L20 8"/><path d="M20 4v4h-4"/><path d="M20 12a8 8 0 0 1-13.7 5.7L4 16"/><path d="M4 20v-4h4"/>`,
	"upload":   `<path d="M12 17V5m-4 4 4-4 4 4"/><path d="M5 19h14"/>`,
	"download": `<path d="M12 5v12m-4-4 4 4 4-4"/><path d="M5 21h14"/>`,
	"versions": `<path d="M4 12a8 8 0 1 0 3-6.2"/><path d="M4 4v4h4"/><path d="M12 8v4l3 2"/>`,

	// --- gallery ---
	"gallery": `<rect x="4" y="5" width="16" height="14" rx="1.5"/><circle cx="9" cy="10" r="1.5"/><path d="M5.5 17l4-4 3.5 3.5L16 13l3 3.2"/>`,
	"camera":  `<path d="M4 8h3l1.5-2h7L17 8h3a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1z"/><circle cx="12" cy="13" r="3.25"/>`,

	// --- sharing and encryption ---
	"share":     `<circle cx="6" cy="12" r="2.5"/><circle cx="18" cy="6" r="2.5"/><circle cx="18" cy="18" r="2.5"/><path d="M8.2 10.8l7.6-3.6M8.2 13.2l7.6 3.6"/>`,
	"link":      `<path d="M10 14a4 4 0 0 1 0-5.7l2-2a4 4 0 0 1 5.7 5.7l-1 1"/><path d="M14 10a4 4 0 0 1 0 5.7l-2 2A4 4 0 0 1 6.3 12l1-1"/>`,
	"lock":      `<rect x="5" y="11" width="14" height="9" rx="1.5"/><path d="M8.5 11V8a3.5 3.5 0 0 1 7 0v3"/>`,
	"lock_open": `<rect x="5" y="11" width="14" height="9" rx="1.5"/><path d="M8.5 11V8a3.5 3.5 0 0 1 6.6-1.7"/>`,
	"people":    `<circle cx="9" cy="8" r="3"/><path d="M3 19a6 6 0 0 1 12 0"/><path d="M16 6.3a3 3 0 0 1 0 5.4M17 19a5.5 5.5 0 0 0-1.7-4"/>`,

	// --- state ---
	"check": `<path d="M5 12.5l4.5 4.5L19 7.5"/>`,
	"close": `<path d="M6 6l12 12M18 6L6 18"/>`,
	"alert": `<path d="M12 4.5 21 20H3z"/><path d="M12 10v4m0 3v.5"/>`,
	"pause": `<path d="M9 5v14M15 5v14"/>`,
	"play":  `<path d="M8 5l11 7-11 7z"/>`,
	"clock": `<circle cx="12" cy="12" r="8"/><path d="M12 7.5V12l3 2"/>`,
	"bell":  `<path d="M6 16V11a6 6 0 0 1 12 0v5l1.5 2.5H4.5z"/><path d="M10 19a2 2 0 0 0 4 0"/>`,

	// --- actions ---
	"add":      `<path d="M12 5v14M5 12h14"/>`,
	"edit":     `<path d="M4 20h4L20 8l-4-4L4 16z"/><path d="M14.5 5.5 18.5 9.5"/>`,
	"delete":   `<path d="M5 7h14M9 7V5h6v2M6.5 7l1 13h9l1-13"/>`,
	"refresh":  `<path d="M20 12a8 8 0 1 1-2.3-5.7L20 8"/><path d="M20 4v4h-4"/>`,
	"external": `<path d="M13 5h6v6"/><path d="M19 5l-8 8"/><path d="M18 14v4a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h4"/>`,
	"search":   `<circle cx="11" cy="11" r="6"/><path d="M15.5 15.5 20 20"/>`,
	"copy":     `<rect x="9" y="9" width="11" height="11" rx="1.5"/><path d="M15 6V5a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v9a1 1 0 0 0 1 1h1"/>`,
	"chevron":  `<path d="M9.5 6l6 6-6 6"/>`,
	"terminal": `<rect x="3" y="5" width="18" height="14" rx="1.5"/><path d="M7 10l2.5 2L7 14M12 15h5"/>`,

	// --- preferences ---
	"settings": `<circle cx="12" cy="12" r="3"/><path d="M12 3v2.5M12 18.5V21M3 12h2.5M18.5 12H21M5.6 5.6l1.8 1.8M16.6 16.6l1.8 1.8M18.4 5.6l-1.8 1.8M7.4 16.6l-1.8 1.8"/>`,
	"power":    `<path d="M12 4v7"/><path d="M7.5 7.5a6.5 6.5 0 1 0 9 0"/>`,
	"battery":  `<rect x="3" y="8" width="15" height="8" rx="1.5"/><path d="M20 11v2"/><path d="M6 11v2"/>`,
	"wifi":     `<path d="M4 9a12 12 0 0 1 16 0"/><path d="M7 12.5a8 8 0 0 1 10 0"/><path d="M10 16a4 4 0 0 1 4 0"/><path d="M12 19.5v.5"/>`,
	"speed":    `<path d="M12 20a8 8 0 1 1 8-8"/><path d="M12 12l4-3"/>`,
	"palette":  `<path d="M12 20a8 8 0 1 1 8-8c0 2.5-2 3-3.5 3H14a2 2 0 0 0-1 3.7c.5.4.4 1.3-1 1.3z"/><circle cx="9" cy="9.5" r="1"/><circle cx="13" cy="8" r="1"/>`,
	"language": `<circle cx="12" cy="12" r="8"/><path d="M4 12h16"/><path d="M12 4c2.5 2.2 2.5 13.8 0 16-2.5-2.2-2.5-13.8 0-16z"/>`,
	"info":     `<circle cx="12" cy="12" r="8"/><path d="M12 11v5.5"/><path d="M12 8v.5"/>`,
	"filter":   `<path d="M4 6h16l-6 7v5l-4 2v-7z"/>`,
}

// Sprite is the <svg> definitions block for the page, containing one <symbol>
// per icon. It is emitted once at the top of the body.
func Sprite() string {
	names := make([]string, 0, len(icons))
	for name := range icons {
		names = append(names, name)
	}
	sort.Strings(names) // stable output, so the page is byte-identical run to run

	var b strings.Builder
	b.WriteString(`<svg class="sprite" aria-hidden="true"><defs>`)
	for _, name := range names {
		b.WriteString(`<symbol id="i-` + name + `" viewBox="0 0 24 24">`)
		b.WriteString(icons[name])
		b.WriteString(`</symbol>`)
	}
	b.WriteString(`</defs></svg>`)
	return b.String()
}

// Icon references a sprite symbol. An unknown name renders nothing rather than
// a broken box: a missing glyph should be invisible, not a defect on screen.
func Icon(name string, class ...string) string {
	if _, ok := icons[name]; !ok {
		return ""
	}
	cls := "ic"
	if len(class) > 0 && class[0] != "" {
		cls += " " + class[0]
	}
	return `<svg class="` + cls + `" aria-hidden="true"><use href="#i-` + name + `"/></svg>`
}
