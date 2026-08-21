//go:build windows

package main

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// contextTimeout bounds one context-menu action. It is generous compared with a
// refresh because these do real work — a folder upload walks a tree — but it is
// bounded, because a window that never finishes is worse than one that says it
// gave up.
const contextTimeout = 10 * time.Minute

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	procRtlMoveMemory    = kernel32.NewProc("RtlMoveMemory")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// copyToClipboard puts text on the clipboard as UTF-16.
//
// The page cannot do this itself: navigator.clipboard needs a secure context
// and a user gesture the WebView will not grant an about:blank document, and
// document.execCommand('copy') is gone. So the one thing a share link is for —
// being pasted somewhere — has to come from here.
//
// The clipboard takes ownership of the memory on success, which is why the free
// only happens on the failure paths.
func copyToClipboard(text string) error {
	utf16, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}

	if ok, _, err := procOpenClipboard.Call(0); ok == 0 {
		return errors.New("the clipboard is in use by another program: " + err.Error())
	}
	defer procCloseClipboard.Call() //nolint:errcheck // closing a clipboard we opened cannot fail usefully

	if ok, _, err := procEmptyClipboard.Call(); ok == 0 {
		return errors.New("could not clear the clipboard: " + err.Error())
	}

	size := uintptr(len(utf16) * 2) // UTF-16 code units, including the terminator
	handle, _, err := procGlobalAlloc.Call(gmemMoveable, size)
	if handle == 0 {
		return errors.New("out of memory for the clipboard: " + err.Error())
	}

	locked, _, err := procGlobalLock.Call(handle)
	if locked == 0 {
		_, _, _ = procGlobalFree.Call(handle)
		return errors.New("could not lock the clipboard buffer: " + err.Error())
	}
	// Copied with RtlMoveMemory rather than through a uintptr-to-pointer cast:
	// the cast is what go vet flags as a possible misuse of unsafe.Pointer, and
	// it is right to — the value came back from a syscall, not from Go's heap.
	_, _, _ = procRtlMoveMemory.Call(locked, uintptr(unsafe.Pointer(&utf16[0])), size)
	_, _, _ = procGlobalUnlock.Call(handle)

	if ok, _, err := procSetClipboardData.Call(cfUnicodeText, handle); ok == 0 {
		_, _, _ = procGlobalFree.Call(handle)
		return errors.New("could not write to the clipboard: " + err.Error())
	}
	return nil
}
