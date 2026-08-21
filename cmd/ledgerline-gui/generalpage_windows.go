//go:build windows

package main

// The General tab: this computer's preferences.
//
// Laid out the way Proton Drive and Google Drive lay theirs out — icon, label,
// one line of explanation, control on the right — because a settings page is
// read to find one thing, and a column of bare check boxes makes you read all
// of them. Grouped by what the setting is about rather than by which file it
// lives in: the user does not care that autostart is a registry value and the
// version cap is an account setting.

const generalBody = `
<section class="page" id="page-general">
  <div class="card">
    <h2>Startup and appearance</h2>

    <div class="pref">
      <svg class="ic"><use href="#i-power"/></svg>
      <div class="text"><b>Start Ledgerline when I sign in</b>
        <span>Keeps folders in sync without opening anything.</span></div>
      <div class="control"><label class="switch">
        <input type="checkbox" id="p-launch" onchange="pref('launchAtLogin', this.checked)"><i></i></label></div>
    </div>

    <div class="pref">
      <svg class="ic"><use href="#i-language"/></svg>
      <div class="text"><b>Language</b>
        <span>Also relabels the Explorer menu.</span></div>
      <div class="control"><select id="p-language" onchange="pref('language', this.value)">
        <option value="">Follow Windows</option>
        <option value="en">English</option>
        <option value="de">Deutsch</option>
        <option value="ru">Русский</option>
      </select></div>
    </div>

    <div class="pref">
      <svg class="ic"><use href="#i-palette"/></svg>
      <div class="text"><b>Appearance</b>
        <span>Windows and dialogs follow this.</span></div>
      <div class="control"><select id="p-theme" onchange="pref('theme', this.value)">
        <option value="system">Follow Windows</option>
        <option value="light">Light</option>
        <option value="dark">Dark</option>
      </select></div>
    </div>

    <div class="pref">
      <svg class="ic"><use href="#i-bell"/></svg>
      <div class="text"><b>Notifications</b>
        <span>A sync that fails is worth an interruption; one that worked is not.</span></div>
      <div class="control"><select id="p-notify" onchange="pref('notify', this.value)">
        <option value="problems">Only problems</option>
        <option value="all">Every sync</option>
        <option value="none">None</option>
      </select></div>
    </div>
  </div>

  <div class="card">
    <h2>Syncing</h2>

    <div class="pref">
      <svg class="ic"><use href="#i-pause"/></svg>
      <div class="text"><b>Pause syncing</b>
        <span>Stays paused until you turn it back on, including after a restart.
          “Sync now” still works.</span></div>
      <div class="control"><label class="switch">
        <input type="checkbox" id="p-paused" onchange="pref('paused', this.checked)"><i></i></label></div>
    </div>

    <div class="pref">
      <svg class="ic"><use href="#i-battery"/></svg>
      <div class="text"><b>Not on battery</b>
        <span>Skip scheduled syncs while this computer is unplugged.</span></div>
      <div class="control"><label class="switch">
        <input type="checkbox" id="p-battery" onchange="pref('pauseOnBattery', this.checked)"><i></i></label></div>
    </div>

    <div class="pref">
      <svg class="ic"><use href="#i-wifi"/></svg>
      <div class="text"><b>Not on metered connections</b>
        <span>Skip scheduled syncs on a connection Windows reports as metered.</span></div>
      <div class="control"><label class="switch">
        <input type="checkbox" id="p-metered" onchange="pref('pauseOnMetered', this.checked)"><i></i></label></div>
    </div>

    <div class="pref">
      <svg class="ic"><use href="#i-speed"/></svg>
      <div class="text"><b>Transfer limits</b>
        <span>Kilobytes per second. Empty means no limit.</span></div>
      <div class="control">
        <input type="number" min="0" id="p-up" placeholder="Upload"
               onchange="pref('uploadKbps', this.value)">
        <input type="number" min="0" id="p-down" placeholder="Download"
               onchange="pref('downloadKbps', this.value)">
      </div>
    </div>

    <div class="pref">
      <svg class="ic"><use href="#i-versions"/></svg>
      <div class="text"><b>Versions kept per file</b>
        <span>Stored on the server, so it applies wherever you sign in.</span></div>
      <div class="control"><input type="number" min="1" max="200" id="p-versions"
        onchange="pref('maxVersions', this.value)"></div>
    </div>
  </div>

  <div class="card">
    <h2>Explorer</h2>

    <div class="pref">
      <svg class="ic"><use href="#i-share"/></svg>
      <div class="text"><b>Right-click menu</b>
        <span>Copy a share link, encrypt, upload or add to the gallery straight from
          Explorer. On Windows 11 it sits under “Show more options”.</span></div>
      <div class="control"><label class="switch">
        <input type="checkbox" id="p-explorer" onchange="pref('explorerMenu', this.checked)"><i></i></label></div>
    </div>
  </div>

  <div class="card">
    <h2>Photos</h2>

    <div class="pref">
      <svg class="ic"><use href="#i-camera"/></svg>
      <div class="text"><b>Upload photos from this folder</b>
        <span id="p-camera-path">Nothing is watched.</span></div>
      <div class="control">
        <button onclick="chooseCamera()">Choose…</button>
        <button class="quiet" id="p-camera-clear" onclick="pref('cameraFolder','')" hidden>Clear</button>
      </div>
    </div>

    <div class="pref">
      <svg class="ic"><use href="#i-delete"/></svg>
      <div class="text"><b>Remove the local copy once uploaded</b>
        <span>Off by default: a client that deletes originals by surprise is one
          people stop trusting.</span></div>
      <div class="control"><label class="switch">
        <input type="checkbox" id="p-camdel" onchange="pref('cameraDelete', this.checked)"><i></i></label></div>
    </div>
  </div>

  <div class="card">
    <h2>Never sync these</h2>
    <p class="sub" style="margin-bottom:0">Matched against a file or folder name, so
      <code>*.tmp</code> applies at any depth.</p>
    <div class="chips" id="p-exclude"></div>
    <div class="inline" style="margin-top:14px">
      <label class="field" style="margin:0"><span>Add a pattern</span>
        <input type="text" id="p-exclude-new" spellcheck="false" placeholder="*.bak"
               onkeydown="if(event.key==='Enter')addExcludePattern()"></label>
      <button style="margin-top:26px" onclick="addExcludePattern()">Add</button>
    </div>
  </div>

  <p class="status" id="g-status"></p>
</section>`

const generalScript = `
function refreshGeneral() {
  loadPrefs().then(p => {
    $('p-launch').checked = p.launchAtLogin;
    $('p-language').value = p.language || '';
    $('p-theme').value = p.theme;
    $('p-notify').value = p.notify;
    $('p-paused').checked = p.paused;
    $('p-battery').checked = p.pauseOnBattery;
    $('p-metered').checked = p.pauseOnMetered;
    $('p-explorer').checked = p.explorerMenu;
    $('p-up').value = p.uploadKbps || '';
    $('p-down').value = p.downloadKbps || '';
    $('p-camdel').checked = p.cameraDelete;

    // The version cap comes from the server. Nothing read means nothing to
    // edit, and a zero in the box would read as "keep none".
    const v = $('p-versions');
    v.value = p.maxVersions || '';
    v.disabled = !p.maxVersions;
    v.placeholder = p.maxVersions ? '' : 'unavailable';

    $('p-camera-path').textContent = p.cameraFolder || 'Nothing is watched.';
    $('p-camera-clear').hidden = !p.cameraFolder;

    $('p-exclude').innerHTML = (p.exclude || []).map(e =>
      '<span class="chip">' + esc(e) +
      '<button title="Remove" onclick="dropExclude(' + JSON.stringify(e).replace(/"/g, '&quot;') + ')">' +
      '<svg class="ic"><use href="#i-close"/></svg></button></span>').join('');

    if (p.error) { $('g-status').textContent = p.error; $('g-status').className = 'status bad'; }
    else { $('g-status').textContent = ''; $('g-status').className = 'status'; }
  }).catch(e => fail('g-status', e));
}

// pref sends one change and re-reads, so the page always shows what was stored
// rather than what was clicked. A switch that stays on after the write failed is
// a lie the user acts on.
function pref(key, value) {
  setPref(key, String(value)).then(msg => {
    if (msg) { $('g-status').textContent = msg; $('g-status').className = 'status bad'; }
    else { $('g-status').textContent = ''; $('g-status').className = 'status'; }
    refreshGeneral();
  }).catch(e => fail('g-status', e));
}

function chooseCamera() {
  pickCameraFolder().then(dir => { if (dir) pref('cameraFolder', dir); });
}

function addExcludePattern() {
  const input = $('p-exclude-new');
  const value = input.value.trim();
  if (!value) return;
  addExclude(value).then(msg => {
    if (msg) { $('g-status').textContent = msg; $('g-status').className = 'status bad'; return; }
    input.value = '';
    refreshGeneral();
  });
}

function dropExclude(pattern) { removeExclude(pattern).then(refreshGeneral); }`
