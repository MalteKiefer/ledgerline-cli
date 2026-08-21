//go:build windows

package main

// The Photos tab.
//
// A desktop client's job with a gallery is narrow: get what is on this computer
// into it, and say what happened. Browsing belongs in the web app, which already
// does it well; reimplementing a photo grid in a settings window would be a
// worse version of something one click away.
//
// The shape is the one Google Drive's photo upload and Proton Drive's albums
// use: one watched folder, a record of what went up, and an explicit "upload
// these now" for everything else.

const galleryBody = `
<section class="page" id="page-3" hidden>
  <div class="card">
    <h2>Upload photos and videos</h2>
    <p class="sub">Everything goes to your gallery on the server, where the web app
      sorts it by the date the photo was taken.</p>
    <div class="row">
      <button class="primary" onclick="uploadPhotos()">
        <svg class="ic"><use href="#i-upload"/></svg>Choose files…</button>
      <button onclick="uploadPhotoFolder()">
        <svg class="ic"><use href="#i-folder"/></svg>Upload a folder…</button>
      <div class="spacer"></div>
      <button class="quiet" onclick="openGalleryWeb()">
        <svg class="ic"><use href="#i-external"/></svg>Open the gallery</button>
    </div>
  </div>

  <div class="card">
    <h2>Watched folder</h2>
    <div class="pref" style="padding-top:0">
      <svg class="ic"><use href="#i-camera"/></svg>
      <div class="text"><b id="g-camera">Nothing is watched.</b>
        <span>New photos and videos in this folder are uploaded automatically while
          Ledgerline is running.</span></div>
      <div class="control">
        <button onclick="chooseCameraFromGallery()">Choose…</button>
      </div>
    </div>
  </div>

  <div class="card">
    <h2>Recently uploaded</h2>
    <div class="list" id="g-recent"><div class="empty">Nothing yet.</div></div>
  </div>

  <p class="status" id="gal-status"></p>
</section>`

const galleryScript = `
function refreshGallery() {
  loadGallery().then(g => {
    $('g-camera').textContent = g.cameraFolder || 'Nothing is watched.';
    $('g-recent').innerHTML = (g.recent || []).length
      ? g.recent.map(r =>
          '<div class="item" style="cursor:default">' +
          '<svg class="ic muted"><use href="#i-' + (r.video ? 'gallery' : 'camera') + '"/></svg>' +
          '<div class="grow"><div class="path">' + esc(r.name) + '</div>' +
          '<div class="meta">' + esc(r.when) + (r.duplicate ? ' · already in the gallery' : '') +
          '</div></div></div>').join('')
      : '<div class="empty">Nothing yet.</div>';

    if (g.error) { $('gal-status').textContent = g.error; $('gal-status').className = 'status bad'; }
  }).catch(e => fail('gal-status', e));
}

function galleryBusy(note) {
  $('gal-status').className = 'status';
  $('gal-status').innerHTML = '<span class="spin"></span>' + esc(note);
}

function uploadPhotos() {
  pickPhotos().then(paths => {
    if (!paths || !paths.length) return;
    galleryBusy('Uploading ' + paths.length + '…');
    sendPhotos(paths).then(msg => {
      $('gal-status').textContent = msg;
      $('gal-status').className = 'status ' + (msg.indexOf('Could not') === 0 ? 'bad' : 'ok');
      refreshGallery();
    }).catch(e => fail('gal-status', e));
  });
}

function uploadPhotoFolder() {
  pickPhotoFolder().then(dir => {
    if (!dir) return;
    galleryBusy('Looking through ' + dir + '…');
    sendPhotoFolder(dir).then(msg => {
      $('gal-status').textContent = msg;
      $('gal-status').className = 'status ' + (msg.indexOf('Could not') === 0 ? 'bad' : 'ok');
      refreshGallery();
    }).catch(e => fail('gal-status', e));
  });
}

function chooseCameraFromGallery() {
  pickCameraFolder().then(dir => {
    if (!dir) return;
    setPref('cameraFolder', dir).then(() => { refreshGallery(); refreshGeneral(); });
  });
}

function openGalleryWeb() { openWebApp('gallery'); }`
