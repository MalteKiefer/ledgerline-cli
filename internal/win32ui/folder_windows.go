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
