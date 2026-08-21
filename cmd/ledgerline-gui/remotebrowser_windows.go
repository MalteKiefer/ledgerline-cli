//go:build windows

package main

// The server folder browser.
//
// It replaces a flat list of every folder path, which was a list you read rather
// than a place you moved through: fine with six folders, useless with two
// hundred. This is a browser — breadcrumb, double-click to enter, up, new folder,
// and the files shown greyed out so you can tell whether you are in the right
// place.
//
// Markup and behaviour live here as constants shared by both windows: the pair
// editor in Settings and the destination picker in the Explorer verb. One
// implementation, so the two cannot drift into behaving differently.
//
// The whole tree arrives in one call and navigation happens in the page. The
// files listing is the only endpoint there is — there is no "children of this
// folder" call — so a request per click would fetch everything every time.

const remoteBrowserBody = `
<div class="scrim" id="rb-scrim" hidden>
  <div class="modal browser">
    <header>
      <h1 id="rb-title">Choose a folder on the server</h1>
      <div class="crumbs" id="rb-crumbs"></div>
    </header>

    <div class="toolbar">
      <button class="quiet" id="rb-up" onclick="rbUp()" title="Up one level">
        <svg class="ic"><use href="#i-chevron" style="transform:rotate(-90deg);transform-origin:center"/></svg>Up</button>
      <div class="spacer"></div>
      <button class="quiet" onclick="rbNewFolder()">
        <svg class="ic"><use href="#i-add"/></svg>New folder</button>
    </div>

    <div class="content">
      <div class="list" id="rb-list"><div class="empty">Loading…</div></div>
      <p class="status" id="rb-status"></p>
    </div>

    <footer>
      <span class="here" id="rb-here"></span>
      <div class="spacer"></div>
      <button onclick="rbClose()">Cancel</button>
      <button class="primary" onclick="rbChoose()">Choose this folder</button>
    </footer>
  </div>
</div>`

const remoteBrowserScript = `
// rbTree is the loaded tree; rbAt is the folder we are looking at ('' = top).
let rbTree = null, rbAt = '', rbOnPick = null;

function rbBytes(n) {
  if (!n) return '';
  if (n < 1024) return n + ' B';
  const u = ['KiB','MiB','GiB','TiB'];
  let i = -1, v = n;
  do { v /= 1024; i++; } while (v >= 1024 && i < u.length - 1);
  return v.toFixed(1) + ' ' + u[i];
}

// rbOpen shows the browser. start is the folder to open at, onPick receives the
// chosen path ('' for the top level).
function rbOpen(start, onPick) {
  rbOnPick = onPick;
  rbAt = start || '';
  $('rb-scrim').hidden = false;
  $('rb-status').textContent = '';
  $('rb-list').innerHTML = '<div class="empty"><span class="spin"></span>Loading…</div>';

  // Re-read every time it opens: a folder created in the web app between two
  // uses should be there, and one call is cheap enough not to cache.
  remoteTree().then(t => {
    if (t.error) { rbFail(t.error); return; }
    rbTree = t;
    // If the folder we were asked to start in is gone, fall back to the top
    // rather than showing an empty listing of a path that does not exist.
    if (rbAt && !t.folders.some(f => f.path === rbAt)) rbAt = '';
    rbRender();
  }).catch(e => rbFail(String(e)));
}

function rbClose() { $('rb-scrim').hidden = true; rbOnPick = null; }

function rbFail(text) {
  $('rb-list').innerHTML = '';
  $('rb-status').textContent = text;
  $('rb-status').className = 'status bad';
}

function rbRender() {
  const folders = rbTree.folders.filter(f => f.parent === rbAt);
  const files = rbTree.files.filter(f => f.parent === rbAt);

  $('rb-up').disabled = rbAt === '';
  $('rb-here').textContent = rbAt ? rbAt : '(top level)';

  // Breadcrumb: every ancestor is one click away, which is the difference
  // between a browser and a list.
  const parts = rbAt ? rbAt.split('/') : [];
  let walked = '';
  let crumbs = '<button class="crumb" onclick="rbGo(&quot;&quot;)">Server</button>';
  for (const part of parts) {
    walked = walked ? walked + '/' + part : part;
    crumbs += '<svg class="ic sm muted sep"><use href="#i-chevron"/></svg>' +
      '<button class="crumb" onclick="rbGo(' + JSON.stringify(walked).replace(/"/g, '&quot;') + ')">' +
      esc(part) + '</button>';
  }
  $('rb-crumbs').innerHTML = crumbs;

  if (!folders.length && !files.length) {
    $('rb-list').innerHTML = '<div class="empty">This folder is empty.</div>';
    return;
  }

  const rows = folders.map(f =>
    '<div class="item" ondblclick="rbGo(' + JSON.stringify(f.path).replace(/"/g, '&quot;') + ')"' +
    ' onclick="rbSelect(this, ' + JSON.stringify(f.path).replace(/"/g, '&quot;') + ')">' +
    '<svg class="ic accent"><use href="#i-folder"/></svg>' +
    '<div class="grow"><div class="path">' + esc(f.name) + '</div></div>' +
    '<svg class="ic sm muted"><use href="#i-chevron"/></svg></div>'
  ).concat(files.map(f =>
    // Files are shown for orientation and are not selectable: this dialog
    // returns a folder, and a row that looks clickable but is not would be
    // worse than one that plainly is not.
    '<div class="item file"><svg class="ic muted"><use href="#i-file"/></svg>' +
    '<div class="grow"><div class="path">' + esc(f.name) + '</div></div>' +
    '<span class="meta">' + rbBytes(f.size) + '</span></div>'
  ));
  $('rb-list').innerHTML = rows.join('');
}

// rbSelect marks a folder without entering it, so a single click can pick the
// destination and a double click walks into it.
function rbSelect(el, path) {
  document.querySelectorAll('#rb-list .item').forEach(i => i.removeAttribute('aria-selected'));
  el.setAttribute('aria-selected', 'true');
  $('rb-here').textContent = path;
  rbPending = path;
}
let rbPending = null;

function rbGo(path) { rbAt = path; rbPending = null; rbRender(); }
function rbUp() { if (rbAt) rbGo(rbAt.includes('/') ? rbAt.slice(0, rbAt.lastIndexOf('/')) : ''); }

function rbChoose() {
  // A marked folder wins over the one we are inside: clicking a row and pressing
  // Choose should mean that row.
  const picked = rbPending !== null ? rbPending : rbAt;
  const cb = rbOnPick;
  rbClose();
  if (cb) cb(picked);
}

function rbNewFolder() {
  const name = prompt('Name for the new folder in ' + (rbAt || 'the top level') + ':', '');
  if (!name) return;
  $('rb-status').textContent = 'Creating…';
  $('rb-status').className = 'status';
  createRemoteFolder(rbAt, name).then(created => {
    $('rb-status').textContent = '';
    // Add it to the loaded tree and walk in, rather than re-fetching: the
    // server told us the path it made.
    const parent = created.includes('/') ? created.slice(0, created.lastIndexOf('/')) : '';
    rbTree.folders.push({ path: created, name: created.slice(created.lastIndexOf('/') + 1), parent: parent });
    rbGo(created);
  }).catch(e => { $('rb-status').textContent = String(e); $('rb-status').className = 'status bad'; });
}`
