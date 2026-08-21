//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// runElevated launches the CLI with the given arguments and waits for it.
//
// The Explorer menu is a machine-wide registration, so writing it needs an
// administrator. The tray runs as the signed-in user and must not ask for
// elevation on its own — a UAC prompt at every sign-in would be worse than no
// menu — so the switch in Settings asks for it once, here, at the moment
// somebody flips it.
//
// ShellExecuteEx with the "runas" verb is the documented way to request it: it
// shows the consent prompt, and a user who declines produces a cancelled error
// rather than a silent failure.
func runElevated(args ...string) error {
	exe, err := cliExecutable()
	if err != nil {
		return err
	}

	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString(quoteArgs(args))
	if err != nil {
		return err
	}

	info := shellExecuteInfo{
		Size:  uint32(unsafe.Sizeof(shellExecuteInfo{})),
		Mask:  seeMaskNoCloseProcess | seeMaskNoAsync,
		Verb:  verb,
		File:  file,
		Param: params,
		Show:  swHide, // a console program with nothing to show
	}
	if ok, _, callErr := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		return fmt.Errorf("elevation was declined or failed: %w", callErr)
	}
	if info.Process == 0 {
		return nil // it ran, but told us nothing; treat as done
	}
	defer windows.CloseHandle(info.Process) //nolint:errcheck // nothing actionable

	// Wait, so the caller can re-read the registration state and report the
	// real outcome rather than an optimistic one.
	if _, err := windows.WaitForSingleObject(info.Process, 60_000); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.Process, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("the command failed (exit %d)", code)
	}
	return nil
}

// cliExecutable is the console client next to this program.
func cliExecutable() (string, error) {
	exe, err := executablePath()
	if err != nil {
		return "", err
	}
	cli := filepath.Join(filepath.Dir(exe), "ledgerline-cli.exe")
	if _, err := statFile(cli); err != nil {
		return "", fmt.Errorf("ledgerline-cli.exe is not next to the tray: %w", err)
	}
	return cli, nil
}

// quoteArgs joins arguments into a command line, quoting the ones that need it.
func quoteArgs(args []string) string {
	out := make([]byte, 0, 64)
	for i, a := range args {
		if i > 0 {
			out = append(out, ' ')
		}
		if needsQuote(a) {
			out = append(out, '"')
			out = append(out, a...)
			out = append(out, '"')
			continue
		}
		out = append(out, a...)
	}
	return string(out)
}

func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '"' {
			return true
		}
	}
	return false
}

var (
	shell32            = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteEx = shell32.NewProc("ShellExecuteExW")
)

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	swHide                = 0
)

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	Size      uint32
	Mask      uint32
	Hwnd      windows.Handle
	Verb      *uint16
	File      *uint16
	Param     *uint16
	Directory *uint16
	Show      int32
	InstApp   windows.Handle
	IDList    uintptr
	Class     *uint16
	KeyClass  windows.Handle
	HotKey    uint32
	Icon      windows.Handle
	Process   windows.Handle
}
