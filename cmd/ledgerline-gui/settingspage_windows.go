//go:build windows

package main

// The settings window's markup and behaviour. They live beside the Go bindings
// they call rather than in the toolkit package: deskui hosts a page, it does not
// know what this one is for.

const settingsBody = `
<div class="app">
  <div class="tabs" role="tablist">
    <button class="tab" role="tab" aria-selected="true"  onclick="show(0)">
      <svg class="ic sm"><use href="#i-account"/></svg>Profile</button>
    <button class="tab" role="tab" aria-selected="false" onclick="show(1)">
      <svg class="ic sm"><use href="#i-settings"/></svg>General</button>
    <button class="tab" role="tab" aria-selected="false" onclick="show(2)">
      <svg class="ic sm"><use href="#i-sync"/></svg>Synced folders</button>
    <button class="tab" role="tab" aria-selected="false" onclick="show(3)">
      <svg class="ic sm"><use href="#i-gallery"/></svg>Photos</button>
    <button class="tab" role="tab" aria-selected="false" onclick="show(4)">
      <svg class="ic sm"><use href="#i-info"/></svg>About</button>
  </div>

  <div class="body">
    <!-- ------------------------------------------------------- profile -- -->
    <section class="page" id="page-0">
      <div class="card">
        <div class="identity">
          <div class="avatar" id="avatar">?</div>
          <div class="who">
            <div class="name" id="p-name">…</div>
            <div class="mail pick" id="p-mail"></div>
          </div>
        </div>
      </div>

      <div class="card">
        <h2>Connection</h2>
        <dl class="rows">
          <dt>Server</dt><dd class="pick" id="p-server">—</dd>
        </dl>
      </div>

      <div class="card">
        <h2>Storage</h2>
        <div id="storage"></div>
      </div>

      <p class="status" id="p-status"></p>

      <div class="row">
        <button class="danger" id="p-signout" onclick="doSignOut()"><svg class="ic"><use href="#i-logout"/></svg>Sign out</button>
        <button onclick="openWebApp('')"><svg class="ic"><use href="#i-external"/></svg>Open web app</button>
        <div class="spacer"></div>
        <button class="quiet" onclick="refreshProfile()"><svg class="ic"><use href="#i-refresh"/></svg>Refresh</button>
      </div>
    </section>

    ` + generalBody + `

    <!-- ------------------------------------------------------- folders -- -->
    <section class="page" id="page-2" hidden>
      <div class="row" style="margin-bottom:14px">
        <button class="primary" onclick="openPair(null)"><svg class="ic"><use href="#i-add"/></svg>Add folder…</button>
        <button id="f-run-all" onclick="runAll()"><svg class="ic"><use href="#i-sync"/></svg>Sync all</button>
      </div>

      <div class="list" id="pairs"></div>
      <p class="status" id="f-status">Removing a pair removes the arrangement only. No files are deleted,
here or on the server, and deletions are never propagated either way.</p>
    </section>

    ` + galleryBody + `

    <!-- --------------------------------------------------------- about -- -->
    <section class="page" id="page-4" hidden>
      <div class="card">
        <h2>Build</h2>
        <dl class="rows">
          <dt>Version</dt><dd class="pick" id="a-version"></dd>
          <dt>Commit</dt><dd class="pick" id="a-commit"></dd>
          <dt>Built</dt><dd class="pick" id="a-built"></dd>
          <dt>Go</dt><dd class="pick" id="a-go"></dd>
        </dl>
      </div>
      <div class="card">
        <h2>On this computer</h2>
        <dl class="rows">
          <dt>Program</dt><dd class="pick" id="a-exe"></dd>
          <dt>Settings</dt><dd class="pick" id="a-config"></dd>
          <dt>Logs</dt><dd class="pick" id="a-logs"></dd>
        </dl>
        <div class="row" style="margin-top:14px">
          <button onclick="openPath('folder', about.logDir)"><svg class="ic"><use href="#i-folder"/></svg>Open log folder</button>
          <button onclick="openPath('folder', about.configDir)"><svg class="ic"><use href="#i-settings"/></svg>Open settings folder</button>
        </div>
      </div>
      <div class="card">
        <h2>Command line</h2>
        <p class="sub" style="margin:0">The ledgerline-cli command shares this sign-in. Run
          <code>ledgerline-cli --help</code> for everything the tray does not show.</p>
      </div>
    </section>
  </div>

  <div class="footer">
    <button onclick="closeWindow()">Close</button>
  </div>
</div>

<!-- ------------------------------------------------------- pair editor -- -->
<div class="scrim" id="pair-scrim" hidden>
  <div class="modal">
    <header><h1 id="pair-title">Add a folder</h1>
      <p class="sub">Both ends are chosen here: the remote folder is not guessed from the local one.</p>
    </header>
    <div class="content">
      <div class="inline">
        <label class="field"><span>Folder on this computer</span>
          <input id="e-local" type="text" spellcheck="false"></label>
        <button onclick="chooseLocal()">Choose…</button>
      </div>
      <div class="inline">
        <label class="field"><span>Folder on the server</span>
          <input id="e-remote" type="text" spellcheck="false" placeholder="(top level)"></label>
        <button onclick="chooseRemote()">Browse…</button>
      </div>
      <label class="field"><span>Direction</span>
        <select id="e-direction">
          <option value="both">Both ways</option>
          <option value="push">Upload only</option>
          <option value="pull">Download only</option>
        </select></label>
      <label class="field"><span>If both sides changed</span>
        <select id="e-conflict">
          <option value="newest">Keep the newer file</option>
          <option value="keep-both">Keep both, renaming one</option>
          <option value="skip">Leave both alone</option>
        </select></label>
      <label class="field"><span>Check every … minutes (0 for never)</span>
        <input id="e-interval" type="number" min="0" step="1"></label>
      <label class="check"><input id="e-watch" type="checkbox">
        <span>Sync as soon as a local file changes</span></label>
      <p class="status" id="e-status"></p>
    </div>
    <footer>
      <button onclick="closePair()">Cancel</button>
      <button class="primary" onclick="savePairForm()">Save</button>
    </footer>
  </div>
</div>

<!-- ------------------------------------------------------ remote picker -- -->
<div class="scrim" id="remote-scrim" hidden>
  <div class="modal">
    <header><h1>Choose a folder on the server</h1></header>
    <div class="content">
      <div class="list" id="remote-list"><div class="empty">Loading…</div></div>
    </div>
    <footer><button onclick="closeRemote()">Cancel</button></footer>
  </div>
</div>`

const settingsScript = generalScript + galleryScript + `
const $ = id => document.getElementById(id);
const esc = s => String(s == null ? '' : s).replace(/[&<>"']/g, c =>
  ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

let about = {}, pairs = [], selected = null, editing = null, busy = false;

/* ------------------------------------------------------------- tabs ------ */

function ic(name) { return '<svg class="ic"><use href="#i-' + name + '"/></svg>'; }

function show(i) {
  document.querySelectorAll('.tab').forEach((t, n) => t.setAttribute('aria-selected', n === i));
  document.querySelectorAll('.page').forEach((p, n) => { p.hidden = n !== i; });
  // Each tab reads its own data when it becomes visible rather than all of it
  // up front: the General tab asks the server for the version cap, and paying
  // for that on a window that opens on Profile is paying for nothing.
  if (i === 1) refreshGeneral();
  if (i === 2) refreshPairs();
  if (i === 3) refreshGallery();
  if (i === 4) refreshAbout();
}

/* ---------------------------------------------------------- profile ------ */

function bytes(n) {
  if (n < 1024) return n + ' B';
  const u = ['KiB','MiB','GiB','TiB','PiB'];
  let i = -1, v = n;
  do { v /= 1024; i++; } while (v >= 1024 && i < u.length - 1);
  return v.toFixed(1) + ' ' + u[i];
}

function refreshProfile() {
  $('p-status').textContent = 'Refreshing…';
  $('p-status').className = 'status';
  loadProfile().then(render).catch(e => fail('p-status', e));
}

function render(p) {
  $('p-name').textContent = p.name || (p.signedIn ? '' : 'Not signed in');
  $('p-mail').textContent = p.email || '';
  $('p-server').textContent = p.server || '—';
  $('p-signout').disabled = !p.signedIn;

  const av = $('avatar');
  if (p.avatar) { av.innerHTML = '<img alt="">'; av.firstChild.src = p.avatar; }
  else { av.textContent = p.initials || '?'; }

  $('storage').innerHTML = storageHTML(p);
  if (p.error) { $('p-status').textContent = p.error; $('p-status').className = 'status bad'; }
  else { $('p-status').textContent = ''; $('p-status').className = 'status'; }
}

function storageHTML(p) {
  if (!p.signedIn || (!p.used && !p.split)) return '<span class="legend">Not available.</span>';
  const total = p.quota > 0 ? p.quota : p.used;
  const pct = v => total > 0 ? (v / total * 100).toFixed(2) + '%' : '0%';

  // Only draw the two-tone bar when the server actually reported the split;
  // inventing a second segment out of the total would be a picture of a fact
  // nobody stated.
  const bar = p.split
    ? '<span class="files" style="width:' + pct(p.files) + '"></span>' +
      '<span class="gallery" style="width:' + pct(p.gallery) + '"></span>'
    : '<span class="files" style="width:' + pct(p.used) + '"></span>';

  let legend = '<span><b>' + bytes(p.used) + '</b> used' +
    (p.quota > 0 ? ' of ' + bytes(p.quota) : '') + '</span>';
  if (p.split) {
    legend += '<span><i class="dot files"></i>Files <b>' + bytes(p.files) + '</b></span>' +
              '<span><i class="dot gallery"></i>Gallery <b>' + bytes(p.gallery) + '</b></span>';
  }
  return '<div class="bar">' + bar + '</div><div class="legend">' + legend + '</div>';
}

function doSignOut() {
  signOut().then(msg => {
    if (msg) { $('p-status').textContent = msg; $('p-status').className = 'status bad'; return; }
    refreshProfile();
  });
}

function fail(id, e) { const s = $(id); s.textContent = String(e); s.className = 'status bad'; }

/* ------------------------------------------------------------ pairs ------ */

function refreshPairs() {
  loadPairs().then(list => { pairs = list || []; renderPairs(); }).catch(e => fail('f-status', e));
}

function renderPairs() {
  const box = $('pairs');
  if (!pairs.length) {
    box.innerHTML = '<div class="empty">No folders yet. Add one to keep it in sync.</div>';
    return;
  }
  box.innerHTML = pairs.map(p => {
    const state = p.running ? '<span class="pill busy">syncing</span>'
      : p.failed ? '<span class="pill bad">failed</span>'
      : p.enabled ? '<span class="pill on">on</span>'
      : '<span class="pill">paused</span>';
    const remote = p.remote || '(top level)';
    const last = p.last ? '<div class="meta">' + esc(p.last) + '</div>' : '';
    return '<div class="item" data-id="' + esc(p.id) + '" aria-selected="' + (p.id === selected) + '"' +
      ' onclick="pick(this.dataset.id)" ondblclick="openPair(this.dataset.id)">' +
      '<div class="grow"><div class="path">' + esc(p.local) + '</div>' +
      '<div class="meta">' + esc(remote) + ' &middot; ' + esc(p.schedule) + '</div>' + last + '</div>' +
      state + '</div>';
  }).join('') + rowActions();
}

// The per-row actions sit under the list rather than inside every row: six
// buttons repeated on every line is a toolbar pretending to be a list.
function rowActions() {
  if (!selected) return '';
  const p = pairs.find(x => x.id === selected);
  if (!p) return '';
  return '<div class="item" style="cursor:default;background:transparent">' +
    '<div class="row grow">' +
    '<button onclick="openPair(\'' + esc(p.id) + '\')">' + ic('edit') + 'Edit…</button>' +
    '<button onclick="syncOne()">' + ic('sync') + 'Sync now</button>' +
    '<button onclick="toggleOne()">' + ic(p.enabled ? 'pause' : 'play') +
      (p.enabled ? 'Pause' : 'Resume') + '</button>' +
    '<button class="danger" onclick="removeOne()">' + ic('delete') + 'Remove</button>' +
    '</div></div>';
}

function pick(id) { selected = id; renderPairs(); }

function withBusy(promise, note) {
  if (busy) return;
  busy = true;
  $('f-status').className = 'status';
  $('f-status').innerHTML = '<span class="spin"></span>' + esc(note);
  promise.then(msg => {
    busy = false;
    $('f-status').textContent = msg || '';
    refreshPairs();
  }).catch(e => { busy = false; fail('f-status', e); });
}

function syncOne() { if (selected) withBusy(runPairs([selected]), 'Syncing…'); }
function runAll() { withBusy(runPairs([]), 'Syncing every enabled folder…'); }
function toggleOne() { if (selected) withBusy(togglePair(selected), 'Updating…'); }

function removeOne() {
  const p = pairs.find(x => x.id === selected);
  if (!p) return;
  if (!confirm('Stop syncing ' + p.local + '?\n\nNothing is deleted: the files stay where they are, ' +
    'here and on the server.')) return;
  withBusy(removePair(p.id), 'Removing…');
}

/* ------------------------------------------------------- pair editor ----- */

function openPair(id) {
  const p = id ? pairs.find(x => x.id === id) : null;
  editing = p ? p.id : '';
  $('pair-title').textContent = p ? 'Edit folder' : 'Add a folder';
  $('e-local').value = p ? p.local : '';
  $('e-local').disabled = !!p;           // the local end identifies the pair
  $('e-remote').value = p ? p.remote : '';
  $('e-direction').value = p ? p.direction : 'both';
  $('e-conflict').value = p ? p.conflict : 'newest';
  $('e-interval').value = p ? p.interval : 15;
  $('e-watch').checked = p ? p.watch : true;
  $('e-status').textContent = '';
  $('pair-scrim').hidden = false;
  $(p ? 'e-remote' : 'e-local').focus();
}

function closePair() { $('pair-scrim').hidden = true; editing = null; }

function chooseLocal() {
  pickLocalFolder($('e-local').value).then(dir => { if (dir) $('e-local').value = dir; });
}

function savePairForm() {
  const s = $('e-status');
  s.className = 'status';
  s.textContent = 'Saving…';
  savePair(editing || '', $('e-local').value, $('e-remote').value,
    $('e-direction').value, $('e-conflict').value,
    parseInt($('e-interval').value, 10) || 0, $('e-watch').checked)
    .then(msg => {
      if (msg) { s.textContent = msg; s.className = 'status bad'; return; }
      closePair();
      refreshPairs();
    }).catch(e => fail('e-status', e));
}

/* ------------------------------------------------------ remote picker ---- */

function chooseRemote() {
  $('remote-scrim').hidden = false;
  $('remote-list').innerHTML = '<div class="empty"><span class="spin"></span>Loading…</div>';
  remoteFolderList().then(list => {
    const rows = ['(top level)'].concat(list || []);
    $('remote-list').innerHTML = rows.map((r, i) =>
      '<div class="item" onclick="takeRemote(' + i + ')"><div class="grow"><div class="path">' +
      esc(r) + '</div></div></div>').join('');
    window.__remote = rows;
  }).catch(e => { $('remote-list').innerHTML = '<div class="empty">' + esc(e) + '</div>'; });
}

function takeRemote(i) {
  const rows = window.__remote || [];
  $('e-remote').value = i === 0 ? '' : (rows[i] || '');
  closeRemote();
}

function closeRemote() { $('remote-scrim').hidden = true; }

/* ------------------------------------------------------------ about ------ */

function refreshAbout() {
  loadAbout().then(a => {
    about = a;
    $('a-version').textContent = a.version || '';
    $('a-commit').textContent = a.commit || '';
    $('a-built').textContent = a.built || '';
    $('a-go').textContent = a.go || '';
    $('a-exe').textContent = a.executable || '';
    $('a-config').textContent = a.configDir || '';
    $('a-logs').textContent = a.logDir || '';
  });
}

/* ------------------------------------------------------------- boot ------ */

addEventListener('keydown', e => {
  if (e.key !== 'Escape') return;
  if (!$('remote-scrim').hidden) { closeRemote(); return; }
  if (!$('pair-scrim').hidden) { closePair(); return; }
  closeWindow();
});

refreshProfile();`
