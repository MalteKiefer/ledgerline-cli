//go:build windows

package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/authflow"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
	"github.com/MalteKiefer/ledgerline-cli/internal/version"
	"github.com/MalteKiefer/ledgerline-cli/internal/win32ui"
)

// Messages the worker goroutine posts back to the dialog. Everything that
// touches a control has to happen on the UI thread, so results travel as
// window messages rather than being written from the goroutine.
const (
	msgDone = win32ui.UserMessage + iota
	msgFailed
	msgTwoFactor
	msgWaitingForApproval
)

// loginTimeout bounds one attempt. Pairing waits for a human to approve the
// device in the web app, so it is generous; a password attempt returns long
// before this.
const loginTimeout = 10 * time.Minute

// loginDialog is the sign-in window: credentials with the account's second
// factor, or the one-time code from the web profile. Which one is on screen is
// the user's choice, because both are legitimate and neither is always
// preferable: a password is fewer steps, a code never types the password into a
// desktop program at all.
type loginDialog struct {
	win *win32ui.Window

	server *win32ui.Control
	device *win32ui.Control

	emailLabel *win32ui.Control
	email      *win32ui.Control
	pwLabel    *win32ui.Control
	password   *win32ui.Control
	otpLabel   *win32ui.Control
	otp        *win32ui.Control

	codeLabel *win32ui.Control
	code      *win32ui.Control

	hint     *win32ui.Control
	status   *win32ui.Control
	submit   *win32ui.Control
	switcher *win32ui.Control

	pairing bool
	busy    atomic.Bool

	// result is the stored session on success; read after Run returns.
	result  session.Session
	success bool

	// message carries the last error text for the caller to surface.
	message string
}

// runLoginDialog shows the window and blocks until it closes. It returns the
// stored session and true when the user signed in.
func runLoginDialog(defaultServer string) (session.Session, bool, string) {
	win32ui.EnableDPIAwareness()

	win, err := win32ui.NewWindow("Sign in to Ledgerline", 420, 330)
	if err != nil {
		return session.Session{}, false, err.Error()
	}
	d := &loginDialog{win: win}
	d.build(defaultServer)
	win.Run()
	return d.result, d.success, d.message
}

// build lays the controls out. Coordinates are 96-dpi pixels; the toolkit
// scales them, so the dialog keeps its proportions on a scaled display.
func (d *loginDialog) build(defaultServer string) {
	const (
		labelX = 16
		fieldX = 130
		fieldW = 274
		rowH   = 24
		gap    = 8
	)
	y := 16

	d.win.Label("Server", labelX, y+4, 110, 20)
	d.server = d.win.Edit(defaultServer, fieldX, y, fieldW, rowH, false)
	y += rowH + gap

	// Password fields and the pairing field occupy the same three rows; only
	// one set is visible at a time.
	credY := y
	d.emailLabel = d.win.Label("E-mail", labelX, credY+4, 110, 20)
	d.email = d.win.Edit("", fieldX, credY, fieldW, rowH, false)
	d.pwLabel = d.win.Label("Password", labelX, credY+rowH+gap+4, 110, 20)
	d.password = d.win.Edit("", fieldX, credY+rowH+gap, fieldW, rowH, true)
	d.otpLabel = d.win.Label("Two-factor code", labelX, credY+2*(rowH+gap)+4, 110, 20)
	d.otp = d.win.Edit("", fieldX, credY+2*(rowH+gap), fieldW, rowH, false)

	d.codeLabel = d.win.Label("One-time code", labelX, credY+4, 110, 20)
	d.code = d.win.Edit("", fieldX, credY, fieldW, rowH, false)

	y = credY + 3*(rowH+gap)

	d.win.Label("This device", labelX, y+4, 110, 20)
	d.device = d.win.Edit(deviceName(), fieldX, y, fieldW, rowH, false)
	y += rowH + gap + 4

	d.hint = d.win.Label("", labelX, y, 388, 34)
	y += 40

	d.status = d.win.Label("", labelX, y, 388, 34)
	y += 42

	d.submit = d.win.Button("Sign in", 232, y, 84, 26, true, d.onSubmit)
	d.win.Button("Cancel", 320, y, 84, 26, false, func() { d.win.Close() })
	d.switcher = d.win.Button("Use a code", labelX, y, 120, 26, false, d.toggleMode)

	d.win.OnMessage(msgDone, func(uintptr) {
		d.success = true
		d.setStatus("Signed in.")
		// Leave the confirmation on screen briefly rather than yanking the
		// window away the instant the request returns.
		go func() {
			time.Sleep(700 * time.Millisecond)
			d.win.Close()
		}()
	})
	d.win.OnMessage(msgFailed, func(uintptr) {
		d.setBusy(false)
		d.setStatus(d.message)
	})
	d.win.OnMessage(msgTwoFactor, func(uintptr) {
		d.setBusy(false)
		d.setStatus("This account needs its two-factor code. Enter it and sign in again.")
		d.otp.Focus()
	})
	d.win.OnMessage(msgWaitingForApproval, func(uintptr) {
		d.setStatus("Code accepted. Approve this device in the web app to finish.")
	})

	d.applyMode()
	d.server.Focus()
}

// toggleMode switches between password and one-time code.
func (d *loginDialog) toggleMode() {
	if d.busy.Load() {
		return
	}
	d.pairing = !d.pairing
	d.applyMode()
}

// applyMode shows the fields the current method needs and explains it.
func (d *loginDialog) applyMode() {
	for _, c := range []*win32ui.Control{d.emailLabel, d.email, d.pwLabel, d.password, d.otpLabel, d.otp} {
		c.SetVisible(!d.pairing)
	}
	d.codeLabel.SetVisible(d.pairing)
	d.code.SetVisible(d.pairing)

	if d.pairing {
		d.switcher.SetText("Use a password")
		d.hint.SetText("Generate a code in your web profile, paste it here, then approve\r\nthis device in the web app. Your password is never typed here.")
		d.status.SetText("")
		d.code.Focus()
		return
	}
	d.switcher.SetText("Use a code")
	d.hint.SetText("Sign in with your account credentials. Leave the two-factor field\r\nempty unless your account has one.")
	d.status.SetText("")
	d.email.Focus()
}

// onSubmit validates the visible fields and starts the attempt.
func (d *loginDialog) onSubmit() {
	if !d.busy.CompareAndSwap(false, true) {
		return
	}
	server := strings.TrimSpace(d.server.Text())
	device := strings.TrimSpace(d.device.Text())
	if server == "" {
		d.fail("Enter the server URL.")
		return
	}

	if d.pairing {
		code := strings.TrimSpace(d.code.Text())
		if code == "" {
			d.fail("Enter the one-time code from your web profile.")
			return
		}
		d.setBusy(true)
		d.setStatus("Submitting the code…")
		go d.runPair(server, code, device)
		return
	}

	email := strings.TrimSpace(d.email.Text())
	password := d.password.Text()
	switch {
	case email == "":
		d.fail("Enter your e-mail address.")
		return
	case password == "":
		d.fail("Enter your password.")
		return
	}
	d.setBusy(true)
	d.setStatus("Signing in…")
	go d.runPassword(server, email, password, strings.TrimSpace(d.otp.Text()), device)
}

// runPassword performs the credential sign-in off the UI thread.
func (d *loginDialog) runPassword(server, email, password, otp, device string) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	sess, err := authflow.Password(ctx, authflow.PasswordOptions{
		Server:     server,
		Email:      email,
		Password:   password,
		Code:       otp,
		DeviceName: device,
		AppVersion: version.Version,
	})
	switch {
	case errors.Is(err, authflow.ErrTwoFactorRequired):
		d.win.Post(msgTwoFactor, 0)
	case err != nil:
		d.message = err.Error()
		d.win.Post(msgFailed, 0)
	default:
		d.result = sess
		d.win.Post(msgDone, 0)
	}
}

// runPair performs the one-time-code exchange off the UI thread.
func (d *loginDialog) runPair(server, code, device string) {
	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	defer cancel()

	sess, err := authflow.Run(ctx, authflow.Options{
		Server:     server,
		Code:       code,
		DeviceName: device,
		OnWaiting:  func() { d.win.Post(msgWaitingForApproval, 0) },
	})
	if err != nil {
		d.message = err.Error()
		d.win.Post(msgFailed, 0)
		return
	}
	d.result = sess
	d.win.Post(msgDone, 0)
}

// fail reports a validation problem without having started anything.
func (d *loginDialog) fail(text string) {
	d.busy.Store(false)
	d.setStatus(text)
}

// setBusy disables input while a request is in flight, so a second Enter does
// not start a competing attempt.
func (d *loginDialog) setBusy(on bool) {
	d.busy.Store(on)
	for _, c := range []*win32ui.Control{d.server, d.device, d.email, d.password, d.otp, d.code, d.submit, d.switcher} {
		c.SetEnabled(!on)
	}
}

func (d *loginDialog) setStatus(text string) {
	d.message = text
	d.status.SetText(text)
}
