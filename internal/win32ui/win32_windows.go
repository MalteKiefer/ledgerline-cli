// Package win32ui is a very small window toolkit for the desktop client: a
// window, labels, text fields, buttons, a list and a message loop.
//
// Why hand-rolled rather than a UI library: the client must keep building as a
// single static binary with CGO_ENABLED=0 for six targets, and every Go GUI
// toolkit either needs cgo or drags in a rendering stack for what amounts to
// two dialogs. Everything here is a plain syscall into user32/gdi32, which the
// tray dependency already does.
//
// What it deliberately does NOT do: visual styles. Themed controls need a
// side-by-side manifest compiled into the executable, which needs a resource
// object the Go toolchain cannot produce on its own. Controls therefore use the
// classic look, but with the correct system font and DPI scaling, so text is
// sized and spaced like the rest of the desktop.
package win32ui

import (
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procRegisterClassEx    = user32.NewProc("RegisterClassExW")
	procCreateWindowEx     = user32.NewProc("CreateWindowExW")
	procDefWindowProc      = user32.NewProc("DefWindowProcW")
	procDestroyWindow      = user32.NewProc("DestroyWindow")
	procShowWindow         = user32.NewProc("ShowWindow")
	procUpdateWindow       = user32.NewProc("UpdateWindow")
	procGetMessage         = user32.NewProc("GetMessageW")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procDispatchMessage    = user32.NewProc("DispatchMessageW")
	procIsDialogMessage    = user32.NewProc("IsDialogMessageW")
	procPostQuitMessage    = user32.NewProc("PostQuitMessage")
	procSendMessage        = user32.NewProc("SendMessageW")
	procPostMessage        = user32.NewProc("PostMessageW")
	procSetWindowText      = user32.NewProc("SetWindowTextW")
	procGetWindowTextLen   = user32.NewProc("GetWindowTextLengthW")
	procGetWindowText      = user32.NewProc("GetWindowTextW")
	procEnableWindow       = user32.NewProc("EnableWindow")
	procSetFocus           = user32.NewProc("SetFocus")
	procLoadCursor         = user32.NewProc("LoadCursorW")
	procAdjustWindowRect   = user32.NewProc("AdjustWindowRectEx")
	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procSystemParamsInfo   = user32.NewProc("SystemParametersInfoW")
	procSetForegroundWin   = user32.NewProc("SetForegroundWindow")
	procGetDpiForWindow    = user32.NewProc("GetDpiForWindow")
	procSetProcessDpiCtx   = user32.NewProc("SetProcessDpiAwarenessContext")
	procMoveWindow         = user32.NewProc("MoveWindow")
	procGetWindowRect      = user32.NewProc("GetWindowRect")
	procCreateFontIndirect = gdi32.NewProc("CreateFontIndirectW")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
)

// Window styles, control styles and messages used here.
const (
	wsOverlapped   = 0x00000000
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsTabStop      = 0x00010000
	wsBorder       = 0x00800000
	wsVScroll      = 0x00200000
	wsClipChildren = 0x02000000

	esPassword    = 0x0020
	esAutoHScroll = 0x0080
	bsPushButton  = 0x0000
	bsDefPushBtn  = 0x0001
	lbsNotify     = 0x0001
	lbsNoIntegral = 0x0100
	ssLeft        = 0x0000

	exClientEdge = 0x00000200

	cwUseDefault = 0x80000000
	swHide       = 0
	swShow       = 5

	wmDestroy = 0x0002
	wmClose   = 0x0010
	wmCommand = 0x0111
	wmSetFont = 0x0030
	wmApp     = 0x8000

	bnClicked      = 0
	lbAddString    = 0x0180
	lbSetCurSel    = 0x0186
	lbGetCurSel    = 0x0188
	lbResetContent = 0x0184

	smCxScreen   = 0
	smCyScreen   = 1
	spiGetNCM    = 0x0029
	idcArrow     = 32512
	colorBtnFace = 15
)

// UserMessage is the first message id callers may use for their own posts.
const UserMessage = wmApp + 1

type point struct{ X, Y int32 }

type msg struct {
	HWnd    windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type rect struct{ Left, Top, Right, Bottom int32 }

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type logFont struct {
	Height         int32
	Width          int32
	Escapement     int32
	Orientation    int32
	Weight         int32
	Italic         byte
	Underline      byte
	StrikeOut      byte
	CharSet        byte
	OutPrecision   byte
	ClipPrecision  byte
	Quality        byte
	PitchAndFamily byte
	FaceName       [32]uint16
}

type nonClientMetrics struct {
	Size            uint32
	BorderWidth     int32
	ScrollWidth     int32
	ScrollHeight    int32
	CaptionWidth    int32
	CaptionHeight   int32
	CaptionFont     logFont
	SmCaptionWidth  int32
	SmCaptionHeight int32
	SmCaptionFont   logFont
	MenuWidth       int32
	MenuHeight      int32
	MenuFont        logFont
	StatusFont      logFont
	MessageFont     logFont
}

// EnableDPIAwareness opts the process into per-monitor DPI scaling so text is
// not blurred by the compatibility scaler. Safe on older Windows, where the
// entry point is simply absent.
func EnableDPIAwareness() {
	if procSetProcessDpiCtx.Find() != nil {
		return
	}
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is the pseudo-handle -4.
	perMonitorAwareV2 := ^uintptr(3)
	_, _, _ = procSetProcessDpiCtx.Call(perMonitorAwareV2)
}

// Window is a top-level window with child controls.
type Window struct {
	hwnd windows.Handle
	font windows.Handle
	dpi  int

	mu       sync.Mutex
	commands map[uintptr]func()
	messages map[uint32]func(uintptr)
	nextID   uintptr
	onClose  func()
}

// registry lets the shared window procedure find the receiver from a handle.
var (
	registryMu sync.Mutex
	registry   = map[windows.Handle]*Window{}
	classOnce  sync.Once
	classErr   error
	className  *uint16
)

// NewWindow creates a fixed-size dialog-style window, centred on screen. Width
// and height are the CLIENT area in 96-dpi pixels; the frame is added on top.
// Controls are added by the caller, then Run pumps messages.
func NewWindow(title string, width, height int) (*Window, error) {
	runtime.LockOSThread() // every window API used here is thread-affine

	if err := ensureClass(); err != nil {
		return nil, err
	}

	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return nil, err
	}

	w := &Window{
		commands: map[uintptr]func(){},
		messages: map[uint32]func(uintptr){},
		nextID:   100,
		dpi:      96,
	}

	style := uintptr(wsOverlapped | wsCaption | wsSysMenu | wsClipChildren)
	hwnd, _, callErr := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(titlePtr)),
		style,
		cwUseDefault, cwUseDefault, 100, 100,
		0, 0, uintptr(instance()), 0,
	)
	if hwnd == 0 {
		return nil, callErr
	}
	w.hwnd = windows.Handle(hwnd)

	registryMu.Lock()
	registry[w.hwnd] = w
	registryMu.Unlock()

	w.dpi = dpiFor(w.hwnd)
	w.font = messageFont(w.dpi)
	w.resizeClient(width, height)
	w.centre()
	return w, nil
}

// Scale converts a 96-dpi design pixel to this window's DPI.
func (w *Window) Scale(px int) int { return px * w.dpi / 96 }

// Handle exposes the raw HWND, for parenting a message box.
func (w *Window) Handle() windows.Handle { return w.hwnd }

// Label adds static text.
func (w *Window) Label(text string, x, y, cx, cy int) *Control {
	return w.control("STATIC", text, wsChild|wsVisible|ssLeft, 0, x, y, cx, cy)
}

// Edit adds a single-line text field; a password field masks its input.
func (w *Window) Edit(text string, x, y, cx, cy int, password bool) *Control {
	style := uintptr(wsChild | wsVisible | wsTabStop | wsBorder | esAutoHScroll)
	if password {
		style |= esPassword
	}
	return w.control("EDIT", text, style, exClientEdge, x, y, cx, cy)
}

// Button adds a push button and registers its click handler.
func (w *Window) Button(text string, x, y, cx, cy int, def bool, onClick func()) *Control {
	style := uintptr(wsChild | wsVisible | wsTabStop | bsPushButton)
	if def {
		style |= bsDefPushBtn
	}
	c := w.control("BUTTON", text, style, 0, x, y, cx, cy)
	w.mu.Lock()
	w.commands[c.id] = onClick
	w.mu.Unlock()
	return c
}

// List adds a single-selection list box.
func (w *Window) List(x, y, cx, cy int) *Control {
	style := uintptr(wsChild | wsVisible | wsTabStop | wsBorder | wsVScroll | lbsNotify | lbsNoIntegral)
	return w.control("LISTBOX", "", style, exClientEdge, x, y, cx, cy)
}

// control creates a child control, applies the system font and returns it.
func (w *Window) control(class, text string, style, exStyle uintptr, x, y, cx, cy int) *Control {
	classPtr, err := windows.UTF16PtrFromString(class)
	if err != nil {
		return &Control{}
	}
	textPtr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return &Control{}
	}

	w.mu.Lock()
	id := w.nextID
	w.nextID++
	w.mu.Unlock()

	hwnd, _, _ := procCreateWindowEx.Call(
		exStyle,
		uintptr(unsafe.Pointer(classPtr)),
		uintptr(unsafe.Pointer(textPtr)),
		style,
		uintptr(w.Scale(x)), uintptr(w.Scale(y)), uintptr(w.Scale(cx)), uintptr(w.Scale(cy)),
		uintptr(w.hwnd), id, uintptr(instance()), 0,
	)
	c := &Control{hwnd: windows.Handle(hwnd), id: id}
	if w.font != 0 {
		_, _, _ = procSendMessage.Call(hwnd, wmSetFont, uintptr(w.font), 1)
	}
	return c
}

// OnMessage registers a handler for a message the caller posts with Post. That
// is how a background goroutine reports back: the handler runs on the UI thread,
// so it may touch controls.
func (w *Window) OnMessage(id uint32, fn func(param uintptr)) {
	w.mu.Lock()
	w.messages[id] = fn
	w.mu.Unlock()
}

// Post sends a registered message to the window from any goroutine.
func (w *Window) Post(id uint32, param uintptr) {
	_, _, _ = procPostMessage.Call(uintptr(w.hwnd), uintptr(id), param, 0)
}

// OnClose registers a callback invoked when the user closes the window.
func (w *Window) OnClose(fn func()) {
	w.mu.Lock()
	w.onClose = fn
	w.mu.Unlock()
}

// Run shows the window and pumps messages until it closes. It must run on the
// thread that created the window.
func (w *Window) Run() {
	_, _, _ = procShowWindow.Call(uintptr(w.hwnd), swShow)
	_, _, _ = procUpdateWindow.Call(uintptr(w.hwnd))
	_, _, _ = procSetForegroundWin.Call(uintptr(w.hwnd))

	var m msg
	for {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 { // 0 is WM_QUIT, -1 an error
			break
		}
		// IsDialogMessage gives Tab, Shift-Tab, Enter and Escape the behaviour a
		// dialog is expected to have, instead of hand-written key handling.
		if handled, _, _ := procIsDialogMessage.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&m))); handled != 0 {
			continue
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// Close asks the window to shut, ending Run. Safe from any goroutine.
func (w *Window) Close() {
	_, _, _ = procPostMessage.Call(uintptr(w.hwnd), wmClose, 0, 0)
}

// dispatch routes a window message and reports whether it was consumed.
func (w *Window) dispatch(message uint32, wParam, lParam uintptr) bool {
	switch message {
	case wmCommand:
		if lParam != 0 && (wParam>>16)&0xFFFF == bnClicked {
			w.mu.Lock()
			fn := w.commands[wParam&0xFFFF]
			w.mu.Unlock()
			if fn != nil {
				fn()
				return true
			}
		}
	case wmClose:
		w.mu.Lock()
		fn := w.onClose
		w.mu.Unlock()
		if fn != nil {
			fn()
		}
		_, _, _ = procDestroyWindow.Call(uintptr(w.hwnd))
		return true
	case wmDestroy:
		w.cleanup()
		_, _, _ = procPostQuitMessage.Call(0)
		return true
	default:
		if message >= wmApp {
			w.mu.Lock()
			fn := w.messages[message]
			w.mu.Unlock()
			if fn != nil {
				fn(wParam)
				return true
			}
		}
	}
	return false
}

func (w *Window) cleanup() {
	if w.font != 0 {
		_, _, _ = procDeleteObject.Call(uintptr(w.font))
		w.font = 0
	}
	registryMu.Lock()
	delete(registry, w.hwnd)
	registryMu.Unlock()
}

// resizeClient grows the window so its CLIENT area is the requested size: the
// caller lays out controls in client coordinates and should not have to guess
// the border and caption height.
func (w *Window) resizeClient(cx, cy int) {
	r := rect{0, 0, int32(w.Scale(cx)), int32(w.Scale(cy))}
	_, _, _ = procAdjustWindowRect.Call(
		uintptr(unsafe.Pointer(&r)),
		uintptr(wsOverlapped|wsCaption|wsSysMenu),
		0, 0,
	)
	_, _, _ = procMoveWindow.Call(uintptr(w.hwnd), 0, 0,
		uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 1)
}

// centre puts the window a third of the way down the primary screen, which
// reads better than dead centre for a dialog.
func (w *Window) centre() {
	var r rect
	if ok, _, _ := procGetWindowRect.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&r))); ok == 0 {
		return
	}
	cx, _, _ := procGetSystemMetrics.Call(smCxScreen)
	cy, _, _ := procGetSystemMetrics.Call(smCyScreen)
	width, height := r.Right-r.Left, r.Bottom-r.Top
	x := (int32(cx) - width) / 2
	y := (int32(cy) - height) / 3
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	_, _, _ = procMoveWindow.Call(uintptr(w.hwnd), uintptr(x), uintptr(y), uintptr(width), uintptr(height), 1)
}

// Control is a child window: a label, field, button or list.
type Control struct {
	hwnd windows.Handle
	id   uintptr
}

// Text returns the control's current text.
func (c *Control) Text() string {
	if c.hwnd == 0 {
		return ""
	}
	n, _, _ := procGetWindowTextLen.Call(uintptr(c.hwnd))
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	_, _, _ = procGetWindowText.Call(uintptr(c.hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf)
}

// SetText replaces the control's text.
func (c *Control) SetText(s string) {
	if c.hwnd == 0 {
		return
	}
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return
	}
	_, _, _ = procSetWindowText.Call(uintptr(c.hwnd), uintptr(unsafe.Pointer(p)))
}

// SetEnabled greys the control out or brings it back.
func (c *Control) SetEnabled(on bool) {
	var flag uintptr
	if on {
		flag = 1
	}
	_, _, _ = procEnableWindow.Call(uintptr(c.hwnd), flag)
}

// SetVisible shows or hides the control.
func (c *Control) SetVisible(on bool) {
	state := uintptr(swHide)
	if on {
		state = swShow
	}
	_, _, _ = procShowWindow.Call(uintptr(c.hwnd), state)
}

// Focus moves the keyboard focus here.
func (c *Control) Focus() { _, _, _ = procSetFocus.Call(uintptr(c.hwnd)) }

// ListClear empties a list box.
func (c *Control) ListClear() {
	_, _, _ = procSendMessage.Call(uintptr(c.hwnd), lbResetContent, 0, 0)
}

// ListAdd appends a row to a list box.
func (c *Control) ListAdd(s string) {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return
	}
	_, _, _ = procSendMessage.Call(uintptr(c.hwnd), lbAddString, 0, uintptr(unsafe.Pointer(p)))
}

// ListSelected returns the selected row index, or -1 when nothing is selected.
func (c *Control) ListSelected() int {
	r, _, _ := procSendMessage.Call(uintptr(c.hwnd), lbGetCurSel, 0, 0)
	return int(int32(r))
}

// ListSelect selects a row by index.
func (c *Control) ListSelect(i int) {
	_, _, _ = procSendMessage.Call(uintptr(c.hwnd), lbSetCurSel, uintptr(i), 0)
}

// --- process-wide plumbing ---

func ensureClass() error {
	classOnce.Do(func() {
		className, classErr = windows.UTF16PtrFromString("LedgerlineWindow")
		if classErr != nil {
			return
		}
		cursor, _, _ := procLoadCursor.Call(0, idcArrow)
		wc := wndClassEx{
			Size:       uint32(unsafe.Sizeof(wndClassEx{})),
			WndProc:    windows.NewCallback(wndProc),
			Instance:   instance(),
			Cursor:     windows.Handle(cursor),
			Background: windows.Handle(colorBtnFace + 1),
			ClassName:  className,
		}
		if atom, _, callErr := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
			classErr = callErr
		}
	})
	return classErr
}

// wndProc is shared by every window; it looks the receiver up by handle.
func wndProc(hwnd windows.Handle, message uint32, wParam, lParam uintptr) uintptr {
	registryMu.Lock()
	w := registry[hwnd]
	registryMu.Unlock()
	if w != nil && w.dispatch(message, wParam, lParam) {
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return ret
}

func instance() windows.Handle {
	var h windows.Handle
	// A null instance is acceptable for these classes, so a failure here is not
	// worth propagating: the window still gets created.
	_ = windows.GetModuleHandleEx(0, nil, &h)
	return h
}

func dpiFor(hwnd windows.Handle) int {
	if procGetDpiForWindow.Find() != nil {
		return 96
	}
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	if dpi < 72 || dpi > 480 {
		return 96
	}
	return int(dpi)
}

// messageFont returns the shell's UI font (Segoe UI on current Windows) scaled
// to the window's DPI, so the dialog matches the rest of the desktop instead of
// falling back to the 1990s bitmap system font.
func messageFont(dpi int) windows.Handle {
	var ncm nonClientMetrics
	ncm.Size = uint32(unsafe.Sizeof(ncm))
	ok, _, _ := procSystemParamsInfo.Call(spiGetNCM, uintptr(ncm.Size), uintptr(unsafe.Pointer(&ncm)), 0)
	if ok == 0 {
		return 0
	}
	lf := ncm.MessageFont
	lf.Height = lf.Height * int32(dpi) / 96
	h, _, _ := procCreateFontIndirect.Call(uintptr(unsafe.Pointer(&lf)))
	return windows.Handle(h)
}
