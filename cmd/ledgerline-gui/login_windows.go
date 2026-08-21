//go:build windows

package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/authflow"
	"github.com/MalteKiefer/ledgerline-cli/internal/deskui"
	"github.com/MalteKiefer/ledgerline-cli/internal/session"
	"github.com/MalteKiefer/ledgerline-cli/internal/version"
)

// loginTimeout bounds the one-time-code exchange, which waits for the owner to
// approve the device in the web app.
const loginTimeout = 3 * time.Minute

// runLoginDialog shows the sign-in window and blocks until it closes. It returns
// the stored session and true when the user signed in.
//
// Both ways in are offered because they suit different situations: e-mail,
// password and a two-factor code needs nothing but the credentials, while the
// one-time code from the web profile avoids typing a password into a window
// that is not the browser. Which is safer depends on the user, so it is theirs
// to choose.
func runLoginDialog(defaultServer string) (session.Session, bool, string) {
	d := &loginDialog{}
	err := deskui.Run(deskui.Options{
		Title:  "Sign in to Ledgerline",
		Width:  520,
		Height: 560,
		Theme:  windowTheme(),
		Body:   loginBody,
		Script: loginScript,
		Bindings: []deskui.Binding{
			{Name: "defaults", Func: func() (map[string]string, error) {
				return map[string]string{"server": defaultServer, "device": deviceName()}, nil
			}},
			{Name: "signIn", Func: d.signIn},
			{Name: "pairCode", Func: d.pair},
		},
	})
	if err != nil {
		return session.Session{}, false, err.Error()
	}
	return d.result, d.success, d.message
}

// loginDialog holds the outcome. The page drives the flow; this only records
// what came back, so the tray can act on it once the window closes.
type loginDialog struct {
	mu      sync.Mutex
	result  session.Session
	success bool
	message string
}

// signInResult is what the page gets back. A failed attempt is a result, not an
// error: distinguishing "wrong password" from "needs a second factor" is the
// whole point, and an error string cannot carry that.
type signInResult struct {
	OK        bool   `json:"ok"`
	NeedsCode bool   `json:"needsCode"`
	Message   string `json:"message"`
}

func (d *loginDialog) signIn(server, email, password, code, device string) (signInResult, error) {
	server, email = strings.TrimSpace(server), strings.TrimSpace(email)
	code, device = strings.TrimSpace(code), strings.TrimSpace(device)
	switch {
	case server == "":
		return signInResult{Message: "Enter the server address."}, nil
	case email == "":
		return signInResult{Message: "Enter your e-mail address."}, nil
	case password == "":
		return signInResult{Message: "Enter your password."}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	sess, err := authflow.Password(ctx, authflow.PasswordOptions{
		Server:     server,
		Email:      email,
		Password:   password,
		Code:       code,
		DeviceName: device,
		AppVersion: version.Version,
	})
	switch {
	case errors.Is(err, authflow.ErrTwoFactorRequired):
		return signInResult{NeedsCode: true, Message: "Enter the code from your authenticator app."}, nil
	case err != nil:
		return signInResult{Message: err.Error()}, nil
	}
	d.finish(sess)
	return signInResult{OK: true}, nil
}

func (d *loginDialog) pair(server, code, device string) (signInResult, error) {
	server, code, device = strings.TrimSpace(server), strings.TrimSpace(code), strings.TrimSpace(device)
	switch {
	case server == "":
		return signInResult{Message: "Enter the server address."}, nil
	case code == "":
		return signInResult{Message: "Enter the one-time code from your web profile."}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	defer cancel()

	sess, err := authflow.Run(ctx, authflow.Options{
		Server:     server,
		Code:       code,
		DeviceName: device,
	})
	if err != nil {
		return signInResult{Message: err.Error()}, nil
	}
	d.finish(sess)
	return signInResult{OK: true}, nil
}

// finish records the session. The window closes itself from the page once the
// promise resolves, which is also what stops the message loop.
func (d *loginDialog) finish(s session.Session) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.result, d.success, d.message = s, true, ""
}

const loginBody = `
<div class="app">
  <div class="body">
    <div class="card">
      <h1>Sign in</h1>
      <p class="sub">Connect this computer to your Ledgerline server.</p>

      <div class="row" style="margin-bottom:18px">
        <button id="tab-pw" class="primary" onclick="mode('password')">E-mail and password</button>
        <button id="tab-code" onclick="mode('code')">One-time code</button>
      </div>

      <label class="field"><span>Server</span>
        <input id="server" type="text" placeholder="https://ledgerline.example" spellcheck="false"></label>

      <div id="pane-pw">
        <label class="field"><span>E-mail</span>
          <input id="email" type="email" spellcheck="false"></label>
        <label class="field"><span>Password</span>
          <input id="password" type="password"></label>
        <label class="field" id="otp-field" hidden><span>Two-factor code</span>
          <input id="otp" type="text" inputmode="numeric" autocomplete="one-time-code" spellcheck="false"></label>
      </div>

      <div id="pane-code" hidden>
        <label class="field"><span>One-time code</span>
          <input id="code" type="text" spellcheck="false"></label>
        <p class="sub">Create the code in the web app under Profile &rsaquo; Devices, then approve
          this computer there.</p>
      </div>

      <label class="field"><span>Device name</span>
        <input id="device" type="text" spellcheck="false"></label>

      <p class="status" id="status"></p>
    </div>
  </div>
  <div class="footer">
    <button onclick="closeWindow()">Cancel</button>
    <button id="submit" class="primary" onclick="submit()">Sign in</button>
  </div>
</div>`

const loginScript = `
const $ = id => document.getElementById(id);
let pairing = false, busy = false;

defaults().then(d => { $('server').value = d.server || ''; $('device').value = d.device || '';
  ($('server').value ? $('email') : $('server')).focus(); });

function mode(which) {
  pairing = which === 'code';
  $('pane-pw').hidden = pairing;
  $('pane-code').hidden = !pairing;
  $('tab-pw').className = pairing ? '' : 'primary';
  $('tab-code').className = pairing ? 'primary' : '';
  $('submit').textContent = pairing ? 'Submit code' : 'Sign in';
  say('');
}

function say(text, kind) { const s = $('status'); s.textContent = text; s.className = 'status ' + (kind || ''); }

function setBusy(on) {
  busy = on;
  document.querySelectorAll('input, button').forEach(el => { el.disabled = on; });
}

function submit() {
  if (busy) return;
  setBusy(true);
  const server = $('server').value, device = $('device').value;
  // The code exchange waits for the owner to approve the device, so it can sit
  // for a while; saying so beats an unexplained pause.
  say(pairing ? 'Waiting for approval in the web app…' : 'Signing in…');
  const call = pairing
    ? pairCode(server, $('code').value, device)
    : signIn(server, $('email').value, $('password').value, $('otp').value, device);
  call.then(r => {
    if (r.ok) { say('Signed in.', 'ok'); closeWindow(); return; }
    setBusy(false);
    if (r.needsCode) { $('otp-field').hidden = false; $('otp').focus(); say(r.message); return; }
    say(r.message, 'bad');
  }).catch(e => { setBusy(false); say(String(e), 'bad'); });
}

addEventListener('keydown', e => {
  if (e.key === 'Enter' && !busy) submit();
  if (e.key === 'Escape') closeWindow();
});`
