package loginui

import (
	"html/template"
	"net/http"
)

// renderForm serves the dialog. The page is self-contained — no external font,
// script or stylesheet — so it renders with the strict CSP below and works with
// no network at all.
func (s *Server) renderForm(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// 'unsafe-inline' covers the one inline script and style block; there is no
	// other source, and nothing on this page comes from the network.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; form-action 'self'; base-uri 'none'")

	_ = pageTemplate.Execute(w, map[string]string{"Device": s.deviceName})
}

// pageTemplate is escaped by html/template: the only interpolated value is the
// device name, which comes from the local hostname.
var pageTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in to Ledgerline</title>
<style>
  :root {
    --brand: #6750a4; --bg: #fdfcff; --fg: #1c1b1f; --muted: #5f5c68;
    --field: #ffffff; --line: #cac4d0; --err: #b3261e; --ok: #1b6b3a;
  }
  @media (prefers-color-scheme: dark) {
    :root { --bg: #141318; --fg: #e6e1e9; --muted: #a7a2ae; --field: #1f1d24; --line: #48454e; }
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; min-height: 100vh; display: grid; place-items: center;
    background: var(--bg); color: var(--fg);
    font: 15px/1.5 "Segoe UI Variable", "Segoe UI", system-ui, sans-serif;
  }
  .card { width: min(30rem, 92vw); padding: 2rem; }
  h1 { font-size: 1.35rem; margin: 0 0 .25rem; }
  p.lead { color: var(--muted); margin: 0 0 1.5rem; }
  ol { color: var(--muted); padding-left: 1.2rem; margin: 0 0 1.5rem; }
  li { margin: .35rem 0; }
  label { display: block; font-weight: 600; margin: 1rem 0 .35rem; }
  input {
    width: 100%; padding: .6rem .7rem; font: inherit; color: var(--fg);
    background: var(--field); border: 1px solid var(--line); border-radius: .5rem;
  }
  input:focus { outline: 2px solid var(--brand); outline-offset: 1px; }
  button {
    margin-top: 1.5rem; width: 100%; padding: .7rem 1rem; font: inherit; font-weight: 600;
    color: #fff; background: var(--brand); border: 0; border-radius: 999px; cursor: pointer;
  }
  button[disabled] { opacity: .6; cursor: default; }
  .msg { margin-top: 1.25rem; padding: .75rem .9rem; border-radius: .5rem; border: 1px solid var(--line); }
  .msg.err { color: var(--err); border-color: var(--err); }
  .msg.ok { color: var(--ok); border-color: var(--ok); }
  .hide { display: none; }
  code { background: var(--field); border: 1px solid var(--line); border-radius: .3rem; padding: 0 .25rem; }
</style>
</head>
<body>
<main class="card">
  <h1>Sign in to Ledgerline</h1>
  <p class="lead">This computer pairs with your server using a one-time code.</p>
  <ol>
    <li>Open your Ledgerline web app and sign in there — including your
        two-factor step, if you use one.</li>
    <li>In your profile, generate a device code and copy it.</li>
    <li>Paste it below, then approve <em>this</em> device in the web app.</li>
  </ol>

  <form id="f">
    <label for="server">Server URL</label>
    <input id="server" name="server" type="url" inputmode="url" placeholder="https://ledger.example.com"
           autocomplete="url" required>

    <label for="code">One-time code</label>
    <input id="code" name="code" type="text" inputmode="text" autocomplete="off"
           spellcheck="false" required>

    <label for="device">This device's name</label>
    <input id="device" name="device" type="text" value="{{.Device}}" autocomplete="off">

    <button id="go" type="submit">Sign in</button>
  </form>

  <div id="msg" class="msg hide" role="status" aria-live="polite"></div>
  <p class="lead" style="margin-top:1.5rem">Your password never reaches this
     program: only the code, and the token your server issues afterwards.</p>
</main>
<script>
  const form = document.getElementById('f');
  const button = document.getElementById('go');
  const msg = document.getElementById('msg');
  let polling = null;

  function show(text, kind) {
    msg.textContent = text;
    msg.className = 'msg' + (kind ? ' ' + kind : '');
  }

  function render(state) {
    if (state.phase === 'waiting') {
      button.disabled = true;
      show(state.message || 'Waiting for approval…');
      return true;
    }
    if (state.phase === 'done') {
      form.classList.add('hide');
      show('Signed in as ' + (state.account || 'your account') +
           '. You can close this tab — the tray icon has updated.', 'ok');
      return false;
    }
    if (state.phase === 'error') {
      button.disabled = false;
      show(state.message || 'Sign-in failed.', 'err');
      return false;
    }
    return false;
  }

  async function poll() {
    try {
      const r = await fetch('status', { cache: 'no-store' });
      if (!render(await r.json()) && polling) {
        clearInterval(polling);
        polling = null;
      }
    } catch (e) { /* the dialog closed; nothing useful to say */ }
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    button.disabled = true;
    show('Submitting the code…');
    try {
      const r = await fetch('start', { method: 'POST', body: new FormData(form) });
      if (render(await r.json()) && !polling) polling = setInterval(poll, 1500);
    } catch (err) {
      button.disabled = false;
      show(String(err), 'err');
    }
  });
</script>
</body>
</html>
`))
