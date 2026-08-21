//go:build windows

// Package deskui hosts the desktop client's windows in WebView2.
//
// The windows used to be hand-drawn Win32 dialogs. They worked, but they could
// only ever look like Win32 dialogs, and this client's other face is a web app
// with its own visual language. Rendering the same markup and the same design
// tokens is the only way the two read as one product; approximating a stylesheet
// with owner-drawn buttons is not.
//
// The runtime is the Edge WebView2 control, present on Windows 11 and on any
// Windows 10 with a current Edge. Nothing is fetched: every page is embedded in
// the binary and handed to the control as a string, so a window opens with no
// network at all and there is no origin for a remote page to be loaded from.
package deskui

import (
	_ "embed"
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

//go:embed assets/base.css
var baseCSS string

// Binding is a Go function exposed to the page. The name becomes a global
// JavaScript function returning a promise. The function must return either
// (value, error) or error alone.
type Binding struct {
	Name string
	Func any
}

// Options describe one window.
type Options struct {
	Title  string
	Width  int
	Height int

	// Body is the page's markup: everything that goes inside <body>.
	Body string
	// Script is the page's JavaScript, run after the markup is parsed.
	Script string

	Bindings []Binding
}

// Run opens the window and blocks until it is closed. It must be called from a
// goroutine that owns its OS thread, because the WebView2 control and its
// message loop are thread-affine.
func Run(opts Options) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug: false,
		WindowOptions: webview2.WindowOptions{
			Title:  opts.Title,
			Width:  uint(opts.Width),
			Height: uint(opts.Height),
			Center: true,
		},
	})
	if w == nil {
		return fmt.Errorf("the Microsoft Edge WebView2 runtime is not installed")
	}
	defer w.Destroy()

	// Every page gets a way to dismiss itself. window.close() is not reliable
	// for a control that was not opened by script, and a page cannot reach the
	// message loop any other way.
	if err := w.Bind("closeWindow", func() { w.Terminate() }); err != nil {
		return fmt.Errorf("bind closeWindow: %w", err)
	}
	for _, b := range opts.Bindings {
		if err := w.Bind(b.Name, b.Func); err != nil {
			return fmt.Errorf("bind %s: %w", b.Name, err)
		}
	}

	decorate(w.Window())
	w.SetHtml(page(opts))
	w.Run()
	return nil
}

// page assembles the document. The stylesheet is inlined rather than linked:
// there is no server to link to, and a data: URI for the sheet would be one more
// thing to escape.
func page(opts Options) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">`)
	b.WriteString(`<meta http-equiv="Content-Security-Policy" content="`)
	// Nothing loads from anywhere: the only script is the one embedded below,
	// and the only images are the data: URIs the Go side passes in. A page that
	// cannot reach the network cannot be turned into one by a stray string.
	b.WriteString(`default-src 'none'; style-src 'unsafe-inline'; img-src data:; script-src 'unsafe-inline'`)
	b.WriteString(`"><style>`)
	b.WriteString(baseCSS)
	b.WriteString("</style></head><body>")
	b.WriteString(opts.Body)
	b.WriteString("<script>")
	b.WriteString(opts.Script)
	b.WriteString("</script></body></html>")
	return b.String()
}

var (
	dwmapi  = windows.NewLazySystemDLL("dwmapi.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")
	user32  = windows.NewLazySystemDLL("user32.dll")

	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	procExtractIconEx         = shell32.NewProc("ExtractIconExW")
	procSendMessage           = user32.NewProc("SendMessageW")
)

const (
	dwmDarkMode      = 20
	dwmDarkModePre   = 19
	dwmCornerPolicy  = 33
	dwmCornerRound   = 2
	wmSetIcon        = 0x0080
	iconSmall        = 0
	iconBig          = 1
	appsUseLightPath = `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`
)

// decorate gives the WebView2 host window the frame a current Windows app has:
// the title bar follows the app-mode preference, the corners are rounded, and
// the program's own mark sits in the title bar and the Alt-Tab list.
//
// The page inside styles itself; the frame around it is the window manager's,
// and it has to be asked.
func decorate(handle unsafe.Pointer) {
	if handle == nil {
		return
	}
	hwnd := windows.Handle(uintptr(handle))

	var dark int32
	if darkMode() {
		dark = 1
	}
	for _, attr := range []uintptr{dwmDarkMode, dwmDarkModePre} {
		_, _, _ = procDwmSetWindowAttribute.Call(
			uintptr(hwnd), attr, uintptr(unsafe.Pointer(&dark)), unsafe.Sizeof(dark))
	}
	corner := int32(dwmCornerRound)
	_, _, _ = procDwmSetWindowAttribute.Call(
		uintptr(hwnd), dwmCornerPolicy, uintptr(unsafe.Pointer(&corner)), unsafe.Sizeof(corner))

	if big, small := appIcon(); big != 0 || small != 0 {
		_, _, _ = procSendMessage.Call(uintptr(hwnd), wmSetIcon, iconBig, uintptr(big))
		_, _, _ = procSendMessage.Call(uintptr(hwnd), wmSetIcon, iconSmall, uintptr(small))
	}
}
