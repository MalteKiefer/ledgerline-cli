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
	procMessageBox          = user32.NewProc("MessageBoxW")
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

	// MessageBox flags.
	mbOKCancel     = 0x00000001
	mbIconWarning  = 0x00000030
	mbIconError    = 0x00000010
	mbIconQuestion = 0x00000020
	idOK           = 1
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

// Confirm asks a yes/no question and reports whether the user agreed. Used for
// the one destructive-looking action in the sync window (removing a pair),
// even though it deletes no files.
func Confirm(owner windows.Handle, title, text string) bool {
	return messageBox(owner, title, text, mbOKCancel|mbIconQuestion) == idOK
}

// Warn shows a message the user cannot miss, for a failure that happened while
// no window was in the foreground.
func Warn(owner windows.Handle, title, text string) {
	messageBox(owner, title, text, mbIconWarning)
}

// Error shows an error box.
func Error(owner windows.Handle, title, text string) {
	messageBox(owner, title, text, mbIconError)
}

func messageBox(owner windows.Handle, title, text string, flags uint32) int {
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0
	}
	textPtr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return 0
	}
	ret, _, _ := procMessageBox.Call(
		uintptr(owner),
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(flags),
	)
	return int(ret)
}
