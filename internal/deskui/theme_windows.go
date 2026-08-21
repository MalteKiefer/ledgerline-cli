//go:build windows

package deskui

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// darkMode reports the user's app-mode preference. Unreadable is treated as
// light, which is what Windows itself defaults to.
func darkMode() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, appsUseLightPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false
	}
	return v == 0
}

// appIcon loads this executable's own icon.
//
// It reads it out of the file rather than by resource id: the id depends on how
// the resource object was generated, and guessing wrong leaves the title bar
// blank — which is exactly what the obvious LoadImage(1) did.
func appIcon() (large, small windows.Handle) {
	exe, err := os.Executable()
	if err != nil {
		return 0, 0
	}
	path, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, 0
	}
	var big, tiny windows.Handle
	_, _, _ = procExtractIconEx.Call(
		uintptr(unsafe.Pointer(path)),
		0, // the first icon in the file
		uintptr(unsafe.Pointer(&big)),
		uintptr(unsafe.Pointer(&tiny)),
		1,
	)
	return big, tiny
}
