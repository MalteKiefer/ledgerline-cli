//go:build windows

package deskintegrate

// DefaultMenus is the Explorer menu this program registers.
//
// It lives beside the registry code rather than in the tray, because the CLI
// registers it too: the installer runs `shell-menu install`, and a menu whose
// labels were defined in the GUI binary would be a menu the installer could not
// write.
//
// The entries are chosen the way Proton Drive and Google Drive choose theirs:
// only actions that make sense on something outside the browser, and nothing
// that needs a second window to explain itself. "Copy share link" is the one
// people reach for; encryption is next, because a file on disk is exactly where
// you want to encrypt it before it goes anywhere.
func DefaultMenus() (files, dirs Menu) {
	files = Menu{
		Title: "Ledgerline",
		Verbs: []Verb{
			{Order: 10, Name: "share", Label: "Copy share link"},
			{Order: 20, Name: "encrypt", Label: "Encrypt…"},
			{Order: 30, Name: "decrypt", Label: "Decrypt"},
			{Order: 40, Name: "upload", Label: "Upload to Ledgerline…", Separator: true},
			{Order: 50, Name: "gallery", Label: "Add to gallery"},
			{Order: 60, Name: "openweb", Label: "Show in the web app", Separator: true},
		},
	}
	dirs = Menu{
		Title: "Ledgerline",
		Verbs: []Verb{
			{Order: 10, Name: "sync", Label: "Keep this folder in sync…"},
			{Order: 20, Name: "share", Label: "Copy share link"},
			{Order: 30, Name: "encrypt", Label: "Encrypt…"},
			{Order: 40, Name: "upload", Label: "Upload to Ledgerline…", Separator: true},
			{Order: 50, Name: "gallery", Label: "Add photos to gallery"},
			{Order: 60, Name: "openweb", Label: "Show in the web app", Separator: true},
		},
	}
	return files, dirs
}
