# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Settings that a desktop client should have.** A General tab with: start
  Ledgerline when you sign in, pause syncing (it stays paused after a restart),
  don't sync on battery or on a metered connection, transfer limits, how many
  versions of a file the server keeps, and a list of names never to sync —
  scratch files, `Thumbs.db`, partial downloads.
- **Right-click menu in Explorer.** Copy a share link, encrypt or decrypt with
  your keys, upload, add to the gallery, or start syncing a folder — without
  opening the app. On Windows 11 it is under “Show more options”. It can be
  turned off in Settings.
- **Photos tab.** Upload files or a whole folder to your gallery, and name one
  folder to watch: new photos and videos in it are uploaded on their own while
  Ledgerline is running. Removing the local copy afterwards is off by default.
- **Pause syncing in the tray menu**, where you would look for it.

- **The windows look like the web app.** Settings and sign-in are now drawn the
  same way the Ledgerline web app is, with the same colours, spacing and dark
  mode, instead of looking like a dialog from an older Windows. They need the
  Microsoft Edge WebView2 runtime, which Windows 11 already has.
- **Your picture and your storage moved into Settings.** The tray menu was too
  small a place for them. Profile now shows your avatar and a bar for how much
  space you are using, split into files and gallery when the server reports it.
- **The folder list reads as a list.** Each row shows the local folder, where it
  goes on the server and when it runs, with a plain badge for on, paused,
  syncing or failed. Adding and editing a folder happens in the same window.
- **Windows tray application (`ledgerline-gui`).** A tray icon showing the
  version, the signed-in account and the server, with Sign in, Settings, Sign
  out, Open web app, Refresh and Quit in its menu. It shares the CLI's credential, certificate pins and remote kill
  switch, so both see the same session.
- **Sign in with your password, or with a code — your choice.** The tray opens a
  proper window (no browser, no console) with both routes on screen: e-mail and
  password plus your two-factor code, or the one-time code you approve in the
  web app. The same choice is in the terminal: `auth login` for credentials,
  `auth pair` for a code. Your second factor still gates everything — the server
  issues nothing until the code is right, and a recovery code works in its
  place. The password is typed without being echoed, can be piped in with
  `--password-stdin`, and is never written anywhere.
- **Settings window.** The tray's **Settings…** opens one window with three
  tabs: **Profile** (who you are, which server, how much storage, and Sign out),
  **Synced folders**, and **About** (version, where the program keeps its
  settings, and a button to its log folder).
- **The tray says what it is doing.** Signed out it shows only "Sign in…".
  Signed in it is two rows — your account and the sync state — each opening a
  submenu with the detail, so the menu stays readable. The icon is muted when
  signed out or offline, plain when everything is current, and carries a green
  dot while a sync is running.
- **A log you can find.** The installer creates a `logs` folder next to the
  programs and the tray writes there: refreshes, sign-outs, every sync and every
  failure. **Settings → About → Open log folder** opens it. It never contains
  your token, password or two-factor code.
- **One tray at a time.** Starting the tray while it is already running no
  longer adds a second icon (and a second sync loop).
- **Synced folders.** Set up as many folder pairs as you like, each with its own
  remote folder, direction, conflict policy and schedule: `sync add`, `sync ls`,
  `sync set`, `sync rm`, `sync run`, and `sync service` to keep them running.
  A pair syncs **as soon as a local file changes** as well as on its interval —
  either can be switched off (`--no-watch`, or `--interval 0`). In Settings →
  Synced folders, adding or editing a pair asks for **both** ends: the local
  folder from the usual picker and the remote folder from your server's own
  folder tree, plus the interval in minutes. Removing a pair deletes nothing —
  it stops the arrangement, and your files stay where they are on both sides.
  Deletions are still never propagated between the two.
- **Windows installer.** One setup .exe per architecture installs both the CLI
  and the tray application, creates Start-menu shortcuts, optionally adds the
  install directory to PATH and optionally starts the tray at sign-in, and
  registers a proper uninstaller. Uninstalling removes the programs but leaves
  your credential and configuration alone.

- **`files webdav` — mount the remote files as a network drive.** Serves the
  Files module over WebDAV on a local address (default `127.0.0.1:9800`) so the
  operating system can mount it: `net use` on Windows, `gio mount`/`davfs2` on
  Linux, Finder/`mount_webdav` on macOS. Every operation is a REST call against
  the server; the only local state is the temp file of a body in flight, removed
  when the handle closes. The endpoint is bound to loopback and gated by
  per-run generated Basic-auth credentials (printed once, never stored);
  `--no-auth` is an explicit opt-out and is refused off loopback, and binding a
  non-loopback address needs `--allow-remote`. `--read-only` refuses every
  write, and a delete through the mount trashes server-side rather than
  force-deleting.
- **CLI commands for the whole Files API surface**, which until now existed only
  as Go methods: `files share` (public token links, plus internal viewer/editor
  shares to other accounts), `files upload-link` (inbound links for external
  uploaders), `files shared` (the receiving side: browse, download, upload,
  rename, delete), `files zip` (stream a ZIP to disk), `files archive
  create|extract` (server-side zip/tar.gz/tar.xz/7z), and `files keys`,
  `files encrypt`, `files decrypt` (server-side PGP/S-MIME public-key
  encryption of a file or a folder subtree).
- **Chunked upload for large files.** `files upload` switches to the
  chunked-upload session above 64 MiB, so a failed transfer only costs the
  current part; `--no-chunked` forces a single multipart body.
- **Windows support in the release.** `windows/amd64` and `windows/arm64` ship as
  plain static `.exe` files (no CGO, no installer); the bearer token uses the
  Windows Credential Manager.
- **Linux packages.** Every release now also ships `.deb` and `.rpm` for amd64
  and arm64, built from the same checksummed binaries via a pinned nfpm recipe,
  including bash/zsh/fish completions and the licence and changelog under
  `/usr/share/doc/ledgerline-cli/`.
- `--password-stdin` on every password flag (share links, upload links,
  archives) so a secret never reaches argv or shell history. A private-key
  passphrase has only the stdin path (`--passphrase-stdin`), no flag at all.

### Changed

- **CI is split into four gates** — tests (build, unit tests and the race
  detector on Linux, macOS and Windows), lint, security (govulncheck plus a
  gitleaks scan of the full history) and supply chain (go.mod tidiness, module
  checksum verification, SBOM drift, reproducible build, dependency review on
  pull requests). A release tag re-runs all of it, on all three platforms,
  before anything is built or published.
- golangci-lint now also runs gosec, bodyclose, errorlint, noctx, unconvert and
  misspell; the findings were fixed rather than suppressed (download directories
  are created `0750`, the WebDAV listener binds through a context-aware
  `net.ListenConfig`, sentinel-error comparisons use `errors.Is`).
- The SBOM generator is pinned to `GOOS=linux` so regenerating it from a Windows
  or macOS workstation produces the file CI expects instead of one whose purls
  carry that host's platform.
- README rewritten: it still documented the pre-pivot zero-knowledge client
  (vault passphrase, a `todo` module, `files open`, `--map` sync) which no longer
  exists.

### Fixed

- **The Windows release binaries had no `.exe` extension.** `make release`
  built every target with the same name pattern, so `ledgerline-cli-<version>-
  windows-amd64` shipped without the suffix Windows needs to run it.
- `TestConfigFileIsOwnerOnly` failed on Windows, where Go reports `0666` for
  every file because there are no POSIX mode bits; the credential file's
  confidentiality there comes from the per-user directory ACL and the primary
  store is the Credential Manager. The test is now OS-aware, so the suite is
  green on Windows.
- A `.gitattributes` forces LF for Go, YAML, JSON, Markdown, shell and the
  Makefile, so a commit from a Windows checkout cannot introduce CRLF that
  breaks the Linux CI scripts or `gofmt -l`.
- Removed a lint exclusion for `internal/crypto/secretstream.go`, a file deleted
  with the zero-knowledge stack.

## [0.7.4] - 2026-07-24

### Added

- **Local audit log.** Every operation is recorded to an append-only JSON-lines
  file (`audit.log` in the config directory, `0600`, size-rotated): one line per
  operation with its command, outcome, duration and timestamp. It holds operation
  metadata only — never keys, tokens, passphrases, or content — and is local-only
  (never sent anywhere). New `audit` command: `audit show [-n N] [--raw]`,
  `audit path`, `audit purge --yes`. Successful logins/logouts and a degraded
  (missing-shard) store are recorded as explicit events.

### Fixed

- **Sharded-store data-loss safety** (aligned to the web client): the CLI no
  longer eagerly deletes a superseded record-shard or collection blob on save — a
  concurrent writer could reuse that ref, and deleting it would dangle the other
  writer's root and corrupt the index. Orphans are reclaimed by the server's
  grace-gated reconcile instead.
- Every sharded-store save now sends the live blob refs as a referential-
  integrity guard; the server refuses (422 `missing_shard`) a root that would
  dangle at a shard whose upload never durably landed.
- A permanently-missing record shard (HTTP 404) is now tolerated: the gallery or
  files store loads the surviving records in a read-only *degraded* state and
  refuses to save, rather than failing the whole load or re-sealing a partial set
  (which would lose the missing shard's records for good).

## [0.7.3] - 2026-07-22

### Added

- `files upload --jobs N` uploads up to N files in parallel (default 4), matching
  `gallery upload` — a large bulk import is no longer one-file-at-a-time. The slow
  per-file network step runs concurrently; the manifest is still saved safely at
  each `--batch` boundary.
- A content-addressed **shard cache** under the config directory: repeated or
  resumed gallery/files runs against a large library skip re-downloading unchanged
  record shards. The cache holds only **ciphertext** (never plaintext), is bounded
  to the current library's shards, and is purged on `auth logout`.

## [0.7.2] - 2026-07-22

### Changed

- Cold shard loads (gallery and files) fetch all record shards in one
  `raw-batch` round-trip instead of one request per shard, falling back to
  individual fetches for anything the batch omits.
- Gallery and files re-seals now reclaim the shard (and replaced folder-
  collection) blobs the new manifest no longer references, so repeated edits no
  longer accumulate orphaned blobs server-side. Deletion happens only after the
  new manifest is safely stored. **(Reverted in Unreleased — this eager deletion
  is a data-loss race with concurrent writers; see the Fixed entry above.)**

### Security

- TLS floor raised to **1.3** (was 1.2) for all remote servers; loopback http
  is still allowed for local development.
- Wrong-passphrase and wrong-recovery-code failures now take a uniform minimum
  time (monotonic floor), removing a timing signal and slowing brute-force
  attempts.

## [0.7.1] - 2026-07-22

### Added

- `files upload --batch N`: save progress every N uploaded/updated files (default
  50; `0` saves once at the end), so an interrupted upload keeps what it already
  stored and a re-run resumes (same-size files are skipped) — matching
  `gallery upload --batch`.

### Changed

- Gallery records now tag `embModel` from the CLIP model name the server returns
  in the `/gallery/process` response (falling back to the configured name on an
  older server), so semantic search only ever compares embeddings from the same
  model across clients. Local-ML (`--ml-local`) still tags the local model.

### Security

- The vault unlock now checks the host/cgroup memory ceiling before the Argon2id
  key derivation and fails closed with a clear error when it cannot hold the
  derivation, instead of risking an OOM kill mid-derivation on a memory-
  constrained host or container.
- Decryption failures now surface a single uniform error — a truncated blob is
  indistinguishable from a wrong key or corrupt ciphertext (no failure-cause
  oracle). Added fuzz tests for the canonical-JSON, blob-frame and sealed-manifest
  parsers, and tests asserting no key material appears in errors or stored bytes.

## [0.7.0] - 2026-07-22

Store v3: a clean-slate, post-quantum sealed-store upgrade shared with the web,
iOS and Android clients. **Requires a Store v3 server. There is no migration and
no v1/v2 compatibility** — the CLI reads and writes only the v3 format.

### Added

- **Post-quantum hybrid key exchange** for cross-user sharing/identity: X25519 +
  ML-KEM-768 (FIPS 203, Go `crypto/mlkem`) combined via HKDF-SHA256, byte-aligned
  with the web client and validated against the shared NIST ML-KEM-768 KAT.
- **Canonical JSON** (`internal/canonicaljson`) — sorted keys, compact,
  integer-only hot records (lat/lng as fixed 6-dp decimal strings) — so every
  client seals byte-identical bytes. Gated by the shared §17 conformance fixtures
  (canonical JSON, shard hashing, blob framing, `sig`, ML-KEM KAT).
- **Crypto-suite envelope** (`suite:1`) on every sealed manifest; an unknown
  suite fails closed.
- `gallery upload --process`: opt in to server-side thumbnail/EXIF derivation.

### Changed

- **Gallery is now a Store v3 content-addressed, id-bucketed sharded store**: any
  edit touches exactly one shard bucket (no array-position cascade), buckets are
  stable across clients, and albums/people live in their own collection blobs.
- **`gallery upload` no longer sends plaintext to the server by default.** It
  writes a partial record (original + basics, `thumbPending`) with no plaintext
  egress; a GUI client — or `--process` / `--ml` / `--ml-local` — derives
  thumbnails, EXIF and ML later. Previously every upload called the server's
  transient-plaintext `process` step.
- **Files graduated to its own sharded store** (`/files/store`, root + id-bucketed
  shards + a folders collection blob), matching the gallery engine.
- **Store modules moved to per-module sealed rows** (`/store/{module}`): the CLI's
  todos now live in their own `todos` row instead of one shared manifest.

### Added (earlier, unreleased)

- `gallery download --edited`: bake edited date/GPS into exported files and
  export Live Photo motion (still + matching `.mov`) via exiftool.
- `files sync --override`: on any difference, overwrite the remote copy with the
  local one (skips the newest/keep-both resolution).
- `files sync` shows a live progress bar on a terminal — position (done/total)
  plus running tallies (unchanged/up/down/removed/conflicts) and the current
  file — so a slow pass (content comparisons download blobs) no longer looks
  frozen.

### Changed (earlier, unreleased)

- `files sync` no longer flags every pre-existing file as a conflict on the
  first run: a local and remote copy holding the same content are left untouched
  (reported as `unchanged`). Matching size + mtime is trusted directly; when the
  remote timestamp is unreliable (e.g. a web upload stamps the upload time) the
  bytes are compared instead, so identical files are not needlessly downloaded.
- `files sync` default conflict policy is now `newest` (was `keep-both`), so a
  file that differs on both sides keeps whichever side changed last instead of
  duplicating it.

## [0.6.1] - 2026-07-13

### Fixed

- Uploads now ride out a struggling gateway: the client retries the `502`, `503`
  and `504` gateway statuses (in addition to `429`) and transient transport
  failures (request timeouts, connection resets/EOF) with the same backoff, so a
  parallel bulk upload that momentarily overloads the server or its reverse proxy
  no longer aborts the run — including on the final manifest save.

### Changed

- Bump GitHub Actions to Node 24 majors (`actions/checkout@v6`,
  `actions/setup-go@v6`, `actions/attest-build-provenance@v4`), clearing the
  Node 20 deprecation warnings.

### Documentation

- README documents verifying a release: the keyless cosign signature over
  `checksums.txt` and the per-binary build-provenance attestation.

## [0.6.0] - 2026-07-13

### Added

- Trust-on-first-use (TOFU) certificate pinning for the API server: the first
  connection to an https server records its certificate's public-key hash (in
  `pins.json` in the config dir, `0600`), and a later connection whose key
  differs is refused with a message pointing at the file to remove if the change
  is expected. This runs on top of normal CA validation (defence-in-depth against
  a mis-issued or swapped certificate) and is skipped for loopback/http.

### Security

- Harden the local-ML client (`internal/ml`): the `/predict` response body is now
  size-bounded (64 MiB) before decoding, renditions whose header declares an
  implausible pixel count are rejected before `image.Decode` (decompression-bomb
  guard), non-finite bounding-box coordinates are dropped, and the HTTP client
  refuses redirects so a decrypted rendition is never replayed to a host the user
  did not name.
- `MergeLivePhotos` now takes the store mutex, closing a latent data race on the
  shared photo records, and `isLoopback` accepts the whole `127.0.0.0/8` / `::1`
  loopback range (via `net.IP.IsLoopback`) rather than three literals.
- Release workflow now runs with least-privilege `contents: read` by default,
  elevating only the release job, and publishes a signed build-provenance
  attestation plus a keyless (cosign) signature over the checksums. CI pins
  `govulncheck` to a fixed version rather than `@latest`. `.gitignore` gains
  secret-file patterns as defense-in-depth.

### Changed

- Added a `golangci-lint` gate to CI (and a `make lint` target) with a
  configuration that keeps the tree clean; removed an unreachable image-crop
  fallback branch in the ML client.

### Fixed

- A paired Live Photo motion clip that fails to read or upload no longer fails
  silently: the still is still stored, and the per-item warning is printed so the
  dropped motion half is visible.

## [0.5.0] - 2026-07-13

### Changed

- Device pairing now polls `POST /api/v1/auth/pair/collect` with the one-time
  code in the JSON request body, replacing `GET /api/v1/auth/pair?code=…`. The
  code no longer travels in a URL/query string, so it can never land in server
  access logs or intermediary proxies. **This requires Ledgerline web
  `v1.452.0` or newer** — existing tokens and all data operations keep working
  with an older server; only new `auth login` pairings need the updated server.

### Fixed

- Rate-limited (`429 Too Many Attempts`) responses no longer abort an upload. A
  bursty parallel run — many blob uploads plus the `process` and manifest-save
  calls — can trip the server's rate limit; the API client now retries such
  responses (and `503`s) with exponential, jittered backoff that honours the
  server's `Retry-After`, across blob upload/download, `process` and the gallery
  save. This makes `gallery upload --jobs N` robust at higher concurrency.
- Progress lines during a parallel upload are numbered by a monotonic completion
  counter instead of the item's input position, so they read `[1/N] [2/N] …` in
  the order items finish rather than appearing shuffled.
- `--ml-local` now parses the real immich-machine-learning `/predict` response:
  embeddings are returned as a string holding a JSON float array (not base64
  float32) and bounding-box coordinates as floats. The previous decoding dropped
  every face and the CLIP embedding; verified against a live immich-ml instance.

## [0.4.0] - 2026-07-13

### Added

- Parallel gallery uploads: `gallery upload --jobs N` (`-j`, default 4) uploads N
  items concurrently. Since most of an upload is spent waiting on the network and
  the server's transform step, this is a large speed-up for big libraries. The
  manifest is still saved once per `--batch` at a barrier, so a save never races
  an in-flight upload; duplicate detection, `--delete` verification and Live Photo
  pairing are unchanged.
- `gallery upload --ml-local <url>` runs the CLIP-embedding and face-detection
  pass on a local [immich-machine-learning](https://immich.app/) instance instead
  of the server, offloading the most expensive part of an upload to a box you can
  put on a GPU and tune. The client asks the server only for the cheap
  derivations, sends the medium rendition to the local instance's `/predict`
  endpoint, crops faces locally and folds the results into the photo's metadata
  exactly as a server-ML upload would; blobs are still encrypted on the client
  first. Tunable via `--ml-clip-model`, `--ml-face-model` and `--ml-min-score`
  (which must match the server's configured models for cross-client consistency).
  `--ml` and `--ml-local` are mutually exclusive.
- `internal/ml` package: a client for the immich-machine-learning `/predict` API.

### Changed

- The gallery `Store` is now safe for concurrent uploads (its added-record list
  and signature index are mutex-guarded); a new race test covers the parallel
  path.

## [0.3.1] - 2026-07-13

### Security

- Sync heartbeat no longer discloses metadata: it previously sent remote folder
  names and progress counts to the server; it now reports only a generic module
  tag, as a zero-knowledge client must.
- Path-traversal containment on `files download` and `files sync`: local write
  destinations built from manifest-controlled names are now confined to the
  target directory, so a hostile record name cannot escape it.
- Decrypted output is written `0600` and its directories `0700`; both
  `files`/`gallery` writers refuse to follow a pre-existing symlink at the target.
- The cached vault key is never written as plaintext: caching is refused when no
  OS keychain is available, and `session` logout clears both stored secrets.
- Server-supplied Argon2id parameters are clamped to a safe range, and blob
  downloads are size-bounded, preventing a hostile server from weakening the key
  derivation or exhausting memory.
- Transport hardening: TLS 1.2 minimum, redirects that refuse scheme downgrades
  and cross-host hops, and a version-less User-Agent to the server.
- The build is pinned to Go 1.26.5, clearing GO-2026-5856 (crypto/tls Encrypted
  Client Hello privacy leak); `govulncheck` reports no reachable vulnerabilities.

### Changed

- Introduced the shared `internal/manifeststore` engine and reduced the Files and
  Todos stores to thin wrappers over it, removing the duplicated conflict-safe
  save logic (no behavioural change).
- The sync state key uses SHA-256, and `github.com/spf13/pflag` is updated to
  v1.0.10.
- Added a CI workflow (build, vet, gofmt, tests, vulnerability scan) and a
  release workflow that cross-compiles the Linux and macOS binaries, publishes
  SHA-256 checksums, and creates the GitHub release from the changelog.

## [0.3.0] - 2026-07-12

### Added

- `files` command group — a full sync client for the zero-knowledge Files module
  (which lives in the shared workspace manifest; other modules like notes and
  bookmarks are always preserved verbatim).
  - `files ls [path]` — list a folder's subfolders and files (colour-coded with a
    monochrome per-type Nerd Font icon), `-R` to recurse; `--color` and `--icons`
    control styling.
  - `files upload` — upload a local folder tree, recreating subfolders; a changed
    file adds a version, an unchanged one is skipped. `--hidden` includes dotfiles.
  - `files download` — decrypt files to a local folder, preserving the tree;
    `--remote` limits to a subtree, `--force` overwrites.
  - `files sync` — **bidirectional** sync between local folders and the encrypted
    files, with a local sync-state database to detect which side changed:
    - Map one or more remote folders to local directories (`--map remote:local`,
      repeatable) or map the whole store into one folder; mappings can also live
      in the settings file.
    - Deletion propagation via `--delete` (`both` | `additive` | `to-remote`).
    - Conflict handling via `--conflict` (`keep-both` | `newest` | `skip`).
    - `--hidden` for dotfiles, ignore patterns from the settings file plus
      `--ignore`, and `--dry-run` to preview.
- `todo` command group — manage encrypted todos and lists (also in the shared
  manifest; other modules preserved): `ls` (filter by list/tag, open/done/marked/
  trash), `add` (title, `--desc/--url/--due/--priority/--list/--tags/--mark`),
  `done`/`undone`, `mark`/`unmark`, `edit`, `rm` (`--force` to delete),
  `restore`, and `lists` (`add`/`rm`/`rename`). Todos are referenced by a short
  id prefix.
- `files rm <path>` — delete a file or folder: trash by default (restorable in
  the web app) or `--force` to erase permanently and reclaim blobs; a folder
  needs `--recursive`.
- `files open <path>` — decrypt a file to a private temp copy and open it with
  the OS default app (`--wait` deletes the copy after the app closes).
- `auth unlock` / `auth lock` — cache the unlocked vault key (default 24h, e.g.
  `--remember 7d`) so gallery/files/todo run without re-entering the passphrase;
  the key lives in the OS keychain (or a 0600 file with a warning). A logout or a
  server-side device revoke clears it, and any 401 wipes the local credential and
  cached key cleanly. `auth status` shows the vault lock state.
- Remote kill switch + sync heartbeat: the CLI reports sync activity to the
  server (so the web shows whether a client is syncing), and when the owner
  requests a remote wipe from the web the client erases all local state
  (credential, cached key, sync state, settings) on its next contact.
- User-editable settings file (`settings.json` in the config dir): `ignore`
  patterns, sync `sync` mappings, and a `hidden` default.
- `gallery upload --batch N` — flush (save, and with `--delete` remove verified
  local files) after every N uploads instead of a fixed 50.
- `internal/files` and `internal/settings` packages; shared blob helpers.

## [0.2.0] - 2026-07-12

### Added

- `gallery upload` — end-to-end-encrypted photo/video upload (folder and Google
  Photos Takeout modes). Files are encrypted on the client before upload; the
  server only ever stores ciphertext.
  - Reproduces the web vault's cryptography in Go (crypto_secretbox, Argon2id,
    and a from-scratch XChaCha20-Poly1305 secretstream), verified byte-for-byte
    against libsodium known-answer tests, so uploads interoperate with the web
    and Android clients.
  - Derives and seals thumbnails, medium renditions, EXIF, location, perceptual
    hash and — with `--ml` — face crops and search embeddings, exactly as the
    web client stores them, into the sharded v2 gallery manifest.
  - Handles Live / Motion photos from any vendor: a same-named video is paired as
    the motion clip, Apple Live Photos split across two files are paired by
    content id, and Google/Samsung embedded Motion Photos are extracted.
  - Accepts all common image and video formats (including RAW and HEIC/HEIF/AVIF)
    and skips byte-identical duplicates already in the gallery.
  - `-d`/`--delete` removes each local file only after its upload is saved and the
    stored copy has been re-downloaded, decrypted and verified byte-for-byte.
- `gallery download` — decrypt and download the gallery to a local folder (a
  plaintext export). Restrict with `--from`/`--to` (date range), `--images` or
  `--videos`; existing files are skipped unless `--force`. Each file keeps its
  original name (id-suffixed on collisions) and capture time; trashed photos are
  excluded.
- `internal/crypto`, `internal/vault` and `internal/gallery` packages.

## [0.1.0] - 2026-07-12

### Added

- Initial release: a console client for a self-hosted Ledgerline server.
- `auth login` — authenticate with a one-time, 60-second code copied from the
  web profile's "Command-line client" card. The client claims the code, waits
  for the owner to approve the device in the web app, and collects a durable
  first-party token. Authentication is zero-knowledge: the token proves identity
  only and never unlocks a vault.
- `auth status` — show the current identity, target server, token storage
  backend, and storage usage; detects a revoked or expired credential.
- `auth logout` — revoke the token server-side and remove it locally.
- `status` — print the repository, installed version, commit hash, build date,
  platform, and whether a newer release is available on GitHub.
- Secure credential storage: the token is kept in the operating system keychain
  (macOS Keychain / Linux Secret Service) and falls back to a `0600` file when
  no keychain is available.
- `gallery upload` command surface (folder and Google Photos modes) with input
  validation; the upload pipeline itself lands in a later release.
- Cross-platform build tooling producing Linux and macOS binaries with embedded
  version metadata.

[Unreleased]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.6.1...HEAD
[0.6.1]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/MalteKiefer/ledgerline-cli/releases/tag/v0.1.0
