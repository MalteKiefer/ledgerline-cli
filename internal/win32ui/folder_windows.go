// Package win32ui is the last piece of native UI the desktop client keeps: the
// shell's own folder chooser.
//
// The rest of the windows moved to WebView2 (see internal/deskui), because a
// hand-drawn Win32 dialog can only ever look like one. A file dialog is the
// exception — reimplementing the shell's tree, its network places and its "New
// folder" button in a page would be worse in every way, and users know this one.
package win32ui

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32 = windows.NewLazySystemDLL("shell32.dll")
	ole32   = windows.NewLazySystemDLL("ole32.dll")

	procSHBrowseForFolder   = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")
	procCoTaskMemFree       = ole32.NewProc("CoTaskMemFree")
)

// browseInfo is BROWSEINFOW.
type browseInfo struct {
	Owner       windows.Handle
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	Callback    uintptr
	LParam      uintptr
	Image       int32
}

const (
	bifReturnOnlyFileSystem = 0x0001
	bifNewDialogStyle       = 0x0040
	bifEditBox              = 0x0010
)

// PickFolder shows the shell folder chooser and returns the selected path, or
// "" when the user cancelled.
//
// SHBrowseForFolder rather than the newer IFileDialog: the modern dialog is a
// COM interface whose vtable would have to be walked by hand from Go, and this
// one is a single call that produces the same tree with the same "New folder"
// button.
func PickFolder(owner windows.Handle, title string) string {
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return ""
	}
	display := make([]uint16, windows.MAX_PATH)

	bi := browseInfo{
		Owner:       owner,
		DisplayName: &display[0],
		Title:       titlePtr,
		Flags:       bifReturnOnlyFileSystem | bifNewDialogStyle | bifEditBox,
	}
	idList, _, _ := procSHBrowseForFolder.Call(uintptr(unsafe.Pointer(&bi)))
	if idList == 0 {
		return "" // cancelled
	}
	// The shell allocated the id list; it is ours to free either way.
	defer func() { _, _, _ = procCoTaskMemFree.Call(idList) }()

	buf := make([]uint16, windows.MAX_PATH)
	ok, _, _ := procSHGetPathFromIDList.Call(idList, uintptr(unsafe.Pointer(&buf[0])))
	if ok == 0 {
		return "" // a virtual folder with no file-system path
	}
	return windows.UTF16ToString(buf)
}

// The file chooser.
//
// GetOpenFileName rather than IFileOpenDialog for the same reason as the folder
// picker: the modern dialog is a COM interface whose vtable would have to be
// walked by hand from Go, and this one is a single call that produces the
// dialog users already know, multi-select included.
var procGetOpenFileName = comdlg32.NewProc("GetOpenFileNameW")

var comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")

const (
	ofnExplorer      = 0x00080000
	ofnAllowMultiple = 0x00000200
	ofnFileMustExist = 0x00001000
	ofnPathMustExist = 0x00000800
	ofnHideReadOnly  = 0x00000004

	// filenameBuffer holds the returned selection: with multi-select the dialog
	// packs the directory and every file name into one buffer, so it has to be
	// large enough for a plausible batch. 32 KiB is about 400 photo names.
	filenameBuffer = 32 * 1024
)

// MediaFilter is the filter for photos and videos, in the packed
// "label\0patterns\0...\0\0" form the dialog expects.
var MediaFilter = []string{
	"Photos and videos", "*.jpg;*.jpeg;*.png;*.gif;*.webp;*.heic;*.heif;*.avif;*.tif;*.tiff;*.bmp;" +
		"*.mp4;*.mov;*.m4v;*.avi;*.mkv;*.webm;*.3gp;*.mts;*.m2ts",
	"All files", "*.*",
}

// openFileName is OPENFILENAMEW.
type openFileName struct {
	StructSize    uint32
	Owner         windows.Handle
	Instance      windows.Handle
	Filter        *uint16
	CustomFilter  *uint16
	MaxCustFilter uint32
	FilterIndex   uint32
	File          *uint16
	MaxFile       uint32
	FileTitle     *uint16
	MaxFileTitle  uint32
	InitialDir    *uint16
	Title         *uint16
	Flags         uint32
	FileOffset    uint16
	FileExtension uint16
	DefExt        *uint16
	CustData      uintptr
	FnHook        uintptr
	TemplateName  *uint16
	PvReserved    uintptr
	DwReserved    uint32
	FlagsEx       uint32
}

// PickFiles shows the file chooser and returns the selected paths, or nil when
// the user cancelled. filter is label/pattern pairs; nil offers all files.
func PickFiles(owner windows.Handle, title string, filter []string) []string {
	buf := make([]uint16, filenameBuffer)

	ofn := openFileName{
		StructSize: uint32(unsafe.Sizeof(openFileName{})),
		Owner:      owner,
		File:       &buf[0],
		MaxFile:    uint32(len(buf)),
		Flags: ofnExplorer | ofnAllowMultiple | ofnFileMustExist |
			ofnPathMustExist | ofnHideReadOnly,
	}
	if titlePtr, err := windows.UTF16PtrFromString(title); err == nil {
		ofn.Title = titlePtr
	}
	if packed := packFilter(filter); packed != nil {
		ofn.Filter = &packed[0]
	}

	ok, _, _ := procGetOpenFileName.Call(uintptr(unsafe.Pointer(&ofn)))
	if ok == 0 {
		return nil // cancelled, or the dialog failed; both mean "no selection"
	}
	return unpackSelection(buf)
}

// packFilter builds the double-NUL-terminated filter string.
func packFilter(pairs []string) []uint16 {
	if len(pairs) == 0 {
		return nil
	}
	var out []uint16
	for _, part := range pairs {
		encoded, err := windows.UTF16FromString(part)
		if err != nil {
			return nil
		}
		out = append(out, encoded...) // UTF16FromString already terminates each one
	}
	return append(out, 0)
}

// unpackSelection reads the dialog's result buffer.
//
// One file comes back as a plain path. Several come back as the directory,
// a NUL, then each bare name — which is why this cannot just be a string split
// on NUL and be done.
func unpackSelection(buf []uint16) []string {
	parts := splitNulTerminated(buf)
	switch len(parts) {
	case 0:
		return nil
	case 1:
		return parts
	}
	dir := parts[0]
	out := make([]string, 0, len(parts)-1)
	for _, name := range parts[1:] {
		out = append(out, dir+`\`+name)
	}
	return out
}

// splitNulTerminated reads consecutive NUL-terminated strings until an empty one.
func splitNulTerminated(buf []uint16) []string {
	var out []string
	for i := 0; i < len(buf); {
		end := i
		for end < len(buf) && buf[end] != 0 {
			end++
		}
		if end == i {
			break // the empty string that terminates the list
		}
		out = append(out, windows.UTF16ToString(buf[i:end]))
		i = end + 1
	}
	return out
}
