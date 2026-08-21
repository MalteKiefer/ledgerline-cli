//go:build windows

package main

// The context-menu window's page.
//
// One window serves every verb. Most of them need no input and could have been
// a notification, but a notification cannot say "this folder is not synced, and
// here is what to do instead" — and that is the answer people will hit most
// often. Two of them do need input: an upload needs a destination, and an
// encryption needs the keys.

const contextBody = `
<div class="app">
  <div class="body">
    <div class="card">
      <div class="identity" style="gap:14px">
        <div class="avatar" id="glyph" style="width:44px;height:44px;border-radius:12px"></div>
        <div class="who">
          <div class="name" id="title">…</div>
          <div class="mail pick" id="subtitle"></div>
        </div>
      </div>
    </div>

    <!-- Upload and sync: pick where it goes on the server. -->
    <div class="card" id="ask-folder" hidden>
      <h2>Destination</h2>
      <div class="inline">
        <label class="field"><span>Folder on the server</span>
          <input id="folder" type="text" spellcheck="false" placeholder="(top level)"></label>
        <button onclick="browseRemote()">Browse…</button>
      </div>
      <div class="list" id="remote-list" hidden></div>
    </div>

    <!-- Encryption: one of your keys signs, any number of recipients can open. -->
    <div class="card" id="ask-keys" hidden>
      <h2>Encrypt for</h2>
      <p class="sub" style="margin-bottom:12px">Your own key is always included, so you can still
        open the file afterwards.</p>
      <label class="field"><span>Your key</span><select id="own"></select></label>
      <div id="recipients"></div>
    </div>

    <p class="status" id="status"></p>

    <div class="card" id="done" hidden>
      <div class="row">
        <button id="copy" hidden onclick="copyLink()"></button>
        <button id="reveal" hidden onclick="reveal()"></button>
      </div>
    </div>
  </div>

  <div class="footer">
    <button onclick="closeWindow()" id="cancel">Cancel</button>
    <button class="primary" id="go" onclick="go()">Continue</button>
  </div>
</div>`

const contextScript = `
const $ = id => document.getElementById(id);
const esc = s => String(s == null ? '' : s).replace(/[&<>"']/g, c =>
  ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

let target = {}, link = '', folderOfResult = '', busy = false;

// What each verb calls itself, and whether it needs anything before it runs.
const VERBS = {
  share:   { title: 'Copy share link',     icon: 'link',      go: 'Create link' },
  encrypt: { title: 'Encrypt',             icon: 'lock',      go: 'Encrypt', keys: true },
  decrypt: { title: 'Decrypt',             icon: 'lock_open', go: 'Decrypt' },
  upload:  { title: 'Upload to Ledgerline',icon: 'upload',    go: 'Upload', folder: true },
  gallery: { title: 'Add to gallery',      icon: 'gallery',   go: 'Add' },
  openweb: { title: 'Show in the web app', icon: 'external',  go: 'Open' },
  sync:    { title: 'Keep this folder in sync', icon: 'sync', go: 'Start syncing', folder: true },
};

function icon(name) { return '<svg class="ic lg"><use href="#i-' + name + '"/></svg>'; }
function say(text, kind) { const s = $('status'); s.textContent = text; s.className = 'status ' + (kind || ''); }

function bytes(n) {
  if (!n) return '';
  if (n < 1024) return n + ' B';
  const u = ['KiB','MiB','GiB','TiB'];
  let i = -1, v = n;
  do { v /= 1024; i++; } while (v >= 1024 && i < u.length - 1);
  return v.toFixed(1) + ' ' + u[i];
}

describe().then(t => {
  target = t;
  const spec = VERBS[t.verb] || { title: t.verb, icon: 'file', go: 'Continue' };
  $('title').textContent = spec.title;
  $('glyph').innerHTML = icon(spec.icon);
  $('go').textContent = spec.go;

  const size = t.isDir ? 'Folder' : bytes(t.size);
  $('subtitle').textContent = t.name + (size ? '  ·  ' + size : '');

  if (t.error) { fatal(t.error); return; }
  if (!t.signedIn) { fatal('Sign in from the tray first.'); return; }

  // Server-side verbs need the file to exist there. Saying so up front beats
  // letting somebody press Encrypt and then explaining.
  if (['share', 'encrypt', 'decrypt', 'openweb'].includes(t.verb) && !t.synced) {
    fatal('This is not inside a synced folder, so the server has no copy of it.\n' +
          'Upload it first, or add its folder under Settings › Synced folders.');
    return;
  }
  if (t.synced) $('subtitle').textContent += '  ·  ' + t.remote;

  if (spec.folder) {
    $('ask-folder').hidden = false;
    // A sync pair defaults to a remote folder named after the local one, which
    // is a guess — but it is the guess the user can see and change here.
    if (t.verb === 'sync') $('folder').value = t.name;
  }
  if (spec.keys) { $('ask-keys').hidden = false; loadKeys(); }
});

function fatal(text) {
  say(text, 'bad');
  $('go').hidden = true;
  $('cancel').textContent = 'Close';
}

/* ------------------------------------------------------------- keys ------ */

function loadKeys() {
  loadKeyring().then(list => {
    const own = (list || []).filter(k => k.kind === 'own');
    const others = (list || []).filter(k => k.kind === 'recipient');

    if (!own.length) {
      fatal('You have no encryption key yet. Add one in the web app under Profile › Keys.');
      return;
    }
    $('own').innerHTML = own.map(k =>
      '<option value="' + k.id + '">' + esc(k.label) + ' (' + esc(k.type.toUpperCase()) + ')</option>').join('');

    $('recipients').innerHTML = others.length
      ? '<label class="field"><span>Other recipients</span></label>' + others.map(k =>
          '<label class="check"><input type="checkbox" class="rcpt" value="' + k.id + '">' +
          '<span>' + esc(k.label) + ' <span style="opacity:.6">' + esc(k.type.toUpperCase()) + '</span></span></label>').join('')
      : '<p class="sub" style="margin:0">No saved recipients. Add them in the web app to share an ' +
        'encrypted file with someone else.</p>';
  }).catch(e => fatal(String(e)));
}

/* --------------------------------------------------- remote folder ------- */

function browseRemote() {
  const box = $('remote-list');
  box.hidden = false;
  box.innerHTML = '<div class="empty"><span class="spin"></span>Loading…</div>';
  remoteFolderList().then(list => {
    const rows = ['(top level)'].concat(list || []);
    box.innerHTML = rows.map((r, i) =>
      '<div class="item" onclick="takeFolder(' + i + ')"><div class="grow"><div class="path">' +
      esc(r) + '</div></div></div>').join('');
    window.__rows = rows;
  }).catch(e => { box.innerHTML = '<div class="empty">' + esc(e) + '</div>'; });
}

function takeFolder(i) {
  const rows = window.__rows || [];
  $('folder').value = i === 0 ? '' : (rows[i] || '');
  $('remote-list').hidden = true;
}

/* -------------------------------------------------------------- run ------ */

function go() {
  if (busy) return;
  busy = true;
  $('go').disabled = true;
  say('Working…');

  const options = { folder: $('folder').value || '' };
  if (!$('ask-keys').hidden) {
    options.key = $('own').value;
    options.recipients = Array.from(document.querySelectorAll('.rcpt:checked')).map(c => c.value).join(',');
  }

  runVerb(options).then(r => {
    busy = false;
    say(r.message, r.ok ? 'ok' : 'bad');
    if (!r.ok) { $('go').disabled = false; return; }

    $('go').hidden = true;
    $('cancel').textContent = 'Close';
    $('ask-folder').hidden = true;
    $('ask-keys').hidden = true;

    if (r.link) {
      link = r.link;
      $('done').hidden = false;
      $('copy').hidden = false;
      $('copy').innerHTML = icon('copy') + 'Copy again';
    }
    if (r.folder) {
      folderOfResult = r.folder;
      $('done').hidden = false;
      $('reveal').hidden = false;
      $('reveal').innerHTML = icon('folder') + 'Show the folder';
    }
  }).catch(e => { busy = false; $('go').disabled = false; say(String(e), 'bad'); });
}

function copyLink() { copyText(link).then(() => say('Link copied to the clipboard.', 'ok')); }
function reveal() { openPath('folder', folderOfResult); }

addEventListener('keydown', e => {
  if (e.key === 'Escape') closeWindow();
  if (e.key === 'Enter' && !busy && !$('go').hidden) go();
});`
