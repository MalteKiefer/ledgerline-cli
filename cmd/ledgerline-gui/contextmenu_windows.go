//go:build windows

package main

import "github.com/MalteKiefer/ledgerline-cli/internal/deskintegrate"

// The Explorer menu's shape. It is defined here rather than in
// internal/deskintegrate because the labels are UI text and the verbs are this
// program's commands; that package only knows how to write registry keys.
//
// The entries are chosen the way Proton Drive and Google Drive choose theirs:
// only actions that make sense on something outside the browser, and nothing
// that needs a second window to explain itself. "Copy share link" is the one
// people reach for; encryption is next, because a file on disk is exactly where
// you want to encrypt it before it goes anywhere.
func explorerMenus() (files, dirs deskintegrate.Menu) {
	files = deskintegrate.Menu{
		Title: "Ledgerline",
		Verbs: []deskintegrate.Verb{
			{Order: 10, Name: "share", Label: "Copy share link"},
			{Order: 20, Name: "encrypt", Label: "Encrypt…"},
			{Order: 30, Name: "decrypt", Label: "Decrypt"},
			{Order: 40, Name: "upload", Label: "Upload to Ledgerline…", Separator: true},
			{Order: 50, Name: "gallery", Label: "Add to gallery"},
			{Order: 60, Name: "openweb", Label: "Show in the web app", Separator: true},
		},
	}
	dirs = deskintegrate.Menu{
		Title: "Ledgerline",
		Verbs: []deskintegrate.Verb{
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
