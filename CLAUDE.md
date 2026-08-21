# CLAUDE.md — ledgerline-cli

Operating manual and single source of truth for the state of THIS repo, for the
next agent (human or model). Re-read the LIVING sections at every session
bootstrap and reconcile them at close-out. Keep it current in the SAME commit as
any change that affects it. Never put a real secret/key/token here — use obvious
placeholders.

---

## 1. Identity & scope

`ledgerline-cli` is the desktop client of the **Ledgerline** self-hosted
personal-cloud platform: a command-line tool plus, since 2026-08-20, a Windows
tray application (`cmd/ledgerline-gui`) that shares its session, pins and API
client. The Windows installer ships both. As of 2026-08-11 the CLI is deliberately scoped to
**two modules only: gallery and files** (plus the auth/status/audit plumbing they
need). Everything else was removed.

The server API is **plaintext-relational** (openapi `v1.521.0`, the ZK→plaintext
pivot of 2026-07-31). Bytes are uploaded and stored plaintext on the server
(under `gallery/{uuid}` and `files/{uuid}`); the server computes sha256,
thumbnails, EXIF, video renditions, and ML (CLIP/faces) itself. There is **no
client-side encryption** in this client and no zero-knowledge sealed store — the
prior ZK crypto/canonical-JSON/sharding stack was deleted in the same pivot.

**Cross-client authority:** the API contract in the web (Laravel) repo's
`openapi.yaml`. Where this repo and the spec disagree, the spec wins and the
disagreement is logged here (§9), not silently resolved.

## 2. API surface consumed  [LIVING]

Base path `/api/v1`. All calls carry a Sanctum bearer (`Authorization: Bearer …`).

**Auth / identity** (`internal/api/auth.go`):
- `POST /auth/pair`, `POST /auth/pair/collect` — device pairing (copy/paste code).
- `GET /me` — identity + storage usage + remote-wipe flag (kill switch).
- `GET /devices`, `DELETE /devices/{id}`, `POST /devices/{id}/wipe`,
  `POST /device/heartbeat`, `DELETE /auth/session` (logout).

**Files — fully wrapped (2026-08-16).** Unlike gallery, every `Files`-tagged
openapi operation (v1.671.0) has a typed Go method, split by feature area under
`internal/api/files*.go`, so the package works standalone as the API library for
a future GTK desktop sync client (Nextcloud-desktop-shaped: browse, two-way
sync, trash/restore, version conflicts, sharing) — not just the CLI's own
subset. `internal/api/files.go` (core CRUD: data/upload/replace/download/
delete/update/toggle-favorite/copy) plus:
- `files_folders.go` — folder CRUD, move (cycle-guarded), trash/restore/force-delete.
- `files_trash.go` — trash listing, file restore/force-delete, empty-trash.
- `files_versions.go` — version history list/download/restore.
- `files_labels.go` — coloured label CRUD + per-file label set.
- `files_activity.go` — activity feed (global/per-file), show, rich info panel, search, stats.
- `files_chunked.go` — chunked-upload session (init/part/complete/abort) +
  `UploadFileChunked` convenience wrapper for large files.
- `files_shares.go` — public share links, internal (viewer/editor) folder
  shares, inbound upload links, shared-with-me browse/upload/rename/delete.
- `files_archive.go` — ZIP export, create-archive (zip/tar.gz/tar.xz/7z), extract-archive.
- `files_crypto.go` — PGP/S-MIME encrypt/decrypt a file, encrypt a folder, keyring read.

`GET /files/data` → `{folders,files,labels,usage}` remains the one-shot listing
the CLI's `ls`/`sync`/upload-dedup paths use. `PUT /files/entries/{id}` and
`PUT /files/rel-shares/{id}` are optimistic-concurrency (body carries `version`;
a 409 body is `{error:"version_conflict",version:N}`, decoded onto
`APIError.Version` — re-fetch, merge, retry with the new version).

Not wrapped (genuinely out of scope, not "Files" tag): `/mounts/*` (S3/SFTP
external storage — a separate feature), `/crypto/keys*` and
`/crypto/recipients` (own-key generation/import/recipient management — the
`files_crypto.go` wrapper only reads the keyring to resolve a `key_id`), the
public no-auth upload-link consumption endpoint (`/upload-link/{token}`, for
external anonymous uploaders, not this account), `/invoices/ocr` (Finance
module, unrelated).

As of 2026-08-20 the whole wrapped Files surface also has a CLI command (§7) —
the "library-only by design" split is gone — and `internal/webdavfs` turns that
same surface into a `webdav.FileSystem`, so `files webdav` serves it as a local
WebDAV endpoint the OS can mount.

**Gallery** (`internal/api/gallery.go`) stays minimal — unchanged from the
plaintext pivot:
- `GET /gallery/data` → `{photos:[GalleryPhoto]}` (list, no bytes).
- `POST /gallery` (multipart `file`) → `{photo}`; HTTP 200 + `duplicate:true` on a
  sha256 match, 201 on a new row.
- `GET /gallery/{id}/download?variant=original|edited` → raw bytes.
- `DELETE /gallery/{id}` (soft delete) / `POST /gallery/bulk-destroy {ids}`.

Endpoints the gallery API exposes but the CLI deliberately does NOT consume:
albums, favorite/toggle, trash/restore/empty, force-delete, people/faces,
semantic search, shares. (Files' equivalents are now wrapped; gallery's
capability floor is unchanged — widen it the same way, feature-file by
feature-file, if a concrete need arises.)

## 3. Threat model & trust boundaries

The server is trusted with plaintext content (this is the platform's model since
the pivot). What the CLI still protects:

- **Transport:** TLS 1.3 floor (`MinVersion: VersionTLS13`), full certificate
  verification always on, TOFU public-key pinning (`internal/certpin`). No
  `InsecureSkipVerify`. Loopback `http` allowed for local dev only. Redirects that
  downgrade scheme or cross host are refused (bearer never follows off-origin).
- **Credential:** the Sanctum bearer is a secret. Stored in the OS keychain where
  available, else a 0600 file in a 0700 dir (headless/SSH hosts). Never in argv,
  env, or logs. Logout revokes server-side and clears locally.
- **Remote kill switch:** every gallery/files command starts via `authedClient`,
  which calls `/me`; a 401 clears the local credential and a pending wipe erases
  all local state (`session.WipeLocal`).
- **Secrets on argv:** a share/archive password may be passed as a flag for
  convenience, but every such flag has a `--password-stdin` twin, and a
  private-key passphrase has ONLY the stdin path — argv, the environment and
  shell history are all readable by other users on a shared host.
- **Local WebDAV endpoint (`files webdav`):** a local socket is reachable by
  every user on the host, so the endpoint is bound to loopback and gated by
  per-run, randomly generated Basic-auth credentials (printed once, never
  stored). `--no-auth` is an explicit opt-out and is refused off loopback;
  binding a non-loopback address requires `--allow-remote`. A body in flight
  lives in a temp file removed when the handle closes, and a delete through the
  mount trashes server-side rather than force-deleting, so a file manager's
  stray delete stays recoverable.
- **Sign-in:** the password is held for the duration of one request and never
  written to disk, to the audit log or to argv (terminal read or stdin; the
  `--password` flag exists but is documented as the visible option). The second
  factor is enforced by the server, which issues nothing until the code is
  right, so adding the password route did not weaken it. A failed attempt stores
  nothing, and the issued token is verified against /me before it is stored.
- **Install id:** 16 random bytes beside the configuration, not derived from the
  machine and not a secret. It exists so the server can replace this
  installation's device entry on a re-login instead of accumulating dead ones —
  which previously pushed live devices out of the cap.
- **Sync configuration:** local paths and remote folder names in sync.json,
  0600. Not secret, but not other users' business either. It holds no
  credential; the runner uses the same stored session as every command.
- **Application log:** the installer creates `logs` beside the programs and
  grants the machine's users write access, because a log nobody can find is a
  log nobody reads. That makes one directory inside Program Files writable by
  any local user: the trade-off is deliberate and bounded — nothing is executed
  or trusted from it, and the log records events and error messages, never a
  token, a password or a two-factor code. A copy that cannot write there falls
  back to the per-user configuration directory rather than logging nowhere.
- **Tray GUI:** shares the CLI's credential, pins and kill switch — it calls
  `/me` on every refresh, clears the credential on a 401 and wipes on a pending
  remote wipe, exactly as the CLI does. It launches two subprocesses, both as
  one argv array with no shell: rundll32 with the configured server URL, which
  is scheme-checked to http(s) first so a tampered config cannot turn a menu
  click into a shell action. The avatar it fetches is capped at 4 MiB. Sign-in
  no longer launches anything: it opens a window in-process.
- **Audit trail:** local-only JSONL metadata (§7); never content or secrets.

Host assumptions: the host may be multi-user; argv/env/shell-history/temp paths
are treated as hostile to the bearer. The binary is fully disclosed — no
compiled-in secrets.

## 4. Architecture & module map

```
cmd/ledgerline-gui/     Windows tray app (systray wiring only; logic lives in internal/trayui)
cmd/                    command tree: root, status, auth, audit, gallery, files (upload/ls/download/rm/
                        mkdir/sync + rename/mv/copy/folder/trash/versions/labels/search/stats/activity
                        + share/upload-link/shared/zip/archive/keys/encrypt/decrypt/webdav);
                        secret.go = stdin-first secret handling for passwords/passphrases
internal/api/           typed /api/v1 client: transport (client.go), auth, gallery, multipart helper;
                        files split by feature — files.go (core CRUD) + files_folders/trash/versions/
                        labels/activity/chunked/shares/archive/crypto.go (full Files-tag surface, §2)
internal/gallery/       local helpers: media-file walking, upload naming
internal/files/         local helpers: folder-tree render (tree.go) + two-way sync (sync.go) + watch service (watch.go)
internal/webdavfs/      webdav.FileSystem over internal/api (the `files webdav` mount backend)
internal/trayui/        tray menu model + avatar/brand icon rendering (platform-free, unit tested)
internal/authflow/      the two sign-in routes (password+2FA, one-time code), shared by every front end
internal/deskui/        the desktop windows: a WebView2 host plus the web app's own design tokens
internal/deskprefs/     this computer's preferences (autostart, pause policy, exclusions, camera folder)
internal/deskintegrate/ shell integration: the per-user Run key and the Explorer context menu
internal/deskpower/     mains power and metered-connection questions, both fail-open
internal/win32ui/       the one native dialog left: the shell folder chooser
internal/syncrunner/    the sync loop both front ends run: intervals + fsnotify change detection
internal/applog/        the desktop client's rotating log file
internal/syncconfig/    the configured folder pairs; one file, read and written by CLI and tray alike
internal/installid/     stable per-installation id so a re-login replaces this machine's device entry
internal/clientset/     one place that turns the stored session into a pinned, authenticated client
packaging/nfpm.yaml     .deb / .rpm recipe
packaging/windows/      NSIS installer (CLI + tray GUI, Start menu, PATH, autostart)
internal/uploadledger/  per-server sha256 dedup ledger (skip re-uploading known bytes)
internal/session/       durable credential (OS keychain + 0600 file fallback)
internal/certpin/       TOFU certificate pinning
internal/audit/         local JSONL operation audit trail (0600, rotated, no secrets)
internal/config/ ui/ version/
```

Transport (`internal/api/client.go`) is the reuse seam: TLS 1.3, cert pinning,
retry/backoff (429 + gateway + transient transport), and the Laravel error-shape
decoder. `uploadMultipart`/`getStream` (`internal/api/multipart.go`) are the
shared streamed-upload / raw-download paths for both modules.

## 5. Dependency inventory  [LIVING]

Policy: **standard library first.** Every third-party module is justified, pinned
in `go.mod`+`go.sum`, checksum-verified (`GOFLAGS=-mod=readonly` in CI).

Direct:
- `github.com/spf13/cobra` — CLI command tree.
- `github.com/zalando/go-keyring` — OS keyring for the bearer token.
- `github.com/fsnotify/fsnotify` — cross-platform recursive filesystem watch for
  `files sync --service` (no stdlib OS file-event API).
- `fyne.io/systray` — the tray icon and menu. Pure Go on Windows (Win32 through
  `golang.org/x/sys`), so the GUI keeps the CGO_ENABLED=0 static build the CLI
  has. Writing the shell notification-area plumbing by hand would be several
  hundred lines of Win32 for no gain.
- `github.com/jchv/go-webview2` — hosts the settings and sign-in windows in the
  Edge WebView2 control. Pure Go (the COM plumbing goes through `x/sys`), so the
  CGO_ENABLED=0 static build survives. The alternative was the hand-rolled Win32
  toolkit this replaced: it worked, but it could only ever look like a Win32
  dialog, and this client's other face is a web app.
- `golang.org/x/net` — only for `golang.org/x/net/webdav`: the WebDAV
  handler/FileSystem contract behind `files webdav`. There is no stdlib WebDAV,
  and hand-rolling PROPFIND/LOCK XML would be a worse trade than one
  Go-team-maintained module.

The zero-knowledge crypto dependencies (`golang.org/x/crypto`, `golang.org/x/term`)
were removed with the ZK stack; `go mod tidy` keeps the require blocks minimal.
Stdlib only for hashing (`crypto/sha256` for the sync content compare), transport
(`crypto/tls`), and multipart (`mime/multipart`).

## 6. Data classification & flows

- **Bearer token:** secret. OS keyring or 0600 file fallback; never argv/env/logs.
- **Content (photos/files):** plaintext. Upload: open file → stream multipart →
  server stores plaintext + derives metadata. Download: stream raw bytes to disk.
- **Upload dedup:** each upload command hashes files (sha256) and skips ones
  already sent — gallery via a local per-server ledger (`internal/uploadledger`,
  `<config>/uploads-gallery.json`); files via the server's `sha256` (authoritative,
  cross-host) plus the same ledger. `--force` bypasses it; `--batch N` checkpoints
  the ledger every N uploads. Uploads and a single `sync` pass show an in-place
  progress bar on a TTY (plain per-line output when piped).
- **Sync state:** the sync compares by sha256 (local, computed on the fly) vs the
  server-reported `sha256`; no persisted last-seen state, so deletions are never
  propagated (a missing file is never treated as a delete — safe by default).
- **WebDAV mount:** `files webdav` holds no local copy of the tree beyond a
  5-second path->id snapshot of `GET /files/data` (invalidated on every mutation
  it performs). A read downloads the body once into a temp file; a write buffers
  into a temp file and uploads on handle close (the API has no partial-write
  endpoint — a WebDAV PUT is one whole-body upload). Both temp files are removed
  on close.
- **Audit trail:** LOCAL-only JSONL at `<config>/audit.log` (0600, size-rotated).
  Operation METADATA only (`event,outcome,target,count,duration_ms,detail,pid`).
  Never a token or content. Managed with `audit show|path|purge`.

## 7. Command surface

```
auth login|logout|status          device pairing -> bearer; identity; revoke
status                            local build info + update check
gallery upload <path...>          multipart upload (files/dirs, --jobs)
gallery list                      photo/video list
gallery download <id...>          originals (--out, --variant)
gallery rm <id...>                trash (bulk when >1)
files upload <file...>            multipart upload; >64 MiB auto-switches to the
                                  chunked session (--no-chunked forces one body)
files ls                          folder/file tree + usage
files download <id...>            raw bytes (--out)
files rm <id...>                  trash a file
files mkdir <name>                create folder (--parent)
files sync <dir>                  two-way sync (--direction, --conflict, --interval, --service)
files webdav                      serve the module as a local WebDAV drive to mount
                                  (--addr, --read-only, --user, --no-auth, --allow-remote)
files rename <id> <name>          rename a file
files mv <id...>                  move files (--to <folder-id> | --root)
files copy <id>                   duplicate a file (--to <folder-id>)
files folder rename|mv|rm|restore folder rename/move/trash(+--force)/restore
files trash ls|restore|rm|empty   trashed files+folders (rm/empty are permanent)
files versions ls|download|restore <file-id> [version]   version history
files labels ls|create|rm|set     coloured labels + per-file label set
files search <query>              full-text/OCR search
files stats                       usage by type + suspected duplicates
files activity [--file <id>]      Files activity feed
files share ls|create|update|rm   public token links (password/expiry/download gate)
files share folder ls|add|role|remove|rm   internal viewer/editor shares to other accounts
files upload-link ls|create|rm    inbound links for external uploaders (folder + expiry required)
files shared ls|browse|download|upload|rename|rm   the receiving side of an internal share
files zip [<id>...]               stream a ZIP of a selection/--folder to a local file
files archive create|extract      server-side archive build (zip/tar.gz/tar.xz/7z) and extraction
files keys                        keyring: own PGP/S-MIME keys + recipients
files encrypt|decrypt             server-side public-key encrypt (file or --folder) / decrypt
audit show|path|purge             local audit trail

auth login                        e-mail + password (+ TOTP or recovery code)
auth pair                         one-time code from the web profile
sync add|ls|set|rm                the standing folder pairs
sync run [id] | run --all         sync now
sync service                      run each pair on its own schedule
```

The desktop GUI (`ledgerline-gui`, Windows) is a tray icon plus two windows, not
a second command surface. The menu shows the version, the signed-in account
(with its avatar), the server host and the storage usage broken out per module
(files, gallery, total against the shared quota), and offers Sign in / Synced
folders / Sign out / Open web app / Refresh / Quit.

Sign-in is a window of its own (`cmd/ledgerline-gui/login_windows.go` over
`internal/deskui`) — no browser, no console — offering both routes and letting
the user choose: e-mail and password with the account's second factor, or the
one-time code approved in the web app. Neither is universally better, which is
why both are on screen: the password route is fewer steps, the code route never
types the password into a desktop program.

The second factor is not weakened by offering the password route. The server
answers 422 {two_factor:true} until a valid TOTP or recovery code arrives, so
the API is exactly as gated as the web app; this client only relays the code and
never stores it. What the client now does handle, which it previously did not,
is the password itself — held in memory for one request and never written
anywhere.

Settings is one window with three tabs — Profile (identity, server, storage,
sign-out), Synced folders (the `internal/syncconfig` list: add, edit, pause,
sync now, remove) and About (build, paths, log folder). One window rather than
one per topic, because a tray that scatters windows is a tray that loses them.
Adding or editing a pair opens a form where **both** ends are chosen: the local
directory from the shell picker, the remote folder from the server's own tree.
Deriving the remote from the local folder's name was right by accident and
silently wrong otherwise.

Each window runs on its own OS thread with its own message loop, so neither can
freeze the tray, and only one tray runs per user (a named mutex): two would mean
two icons and two sync loops racing on one configuration file.

The tray icon has three states — muted, idle, syncing — and the syncing one
carries a dot rather than only a different hue, because 16 px of peripheral
vision is a bad place to rely on colour.

Secret handling: every share/archive/upload-link password flag has a
`--password-stdin` twin so the secret never reaches argv; a private-key
passphrase has ONLY the stdin path (`--passphrase-stdin`), no flag at all,
because it unlocks key material rather than gating a link (§3). The account
password follows the same rule and defaults to a non-echoing terminal read.
Fixing that exposed a real defect in the stdin reader: it buffered all of stdin,
so a piped password swallowed whatever came after it (a prompted 2FA code, for
instance). It now reads one line without reading ahead.

The whole wrapped Files surface now has a CLI command. `internal/api` still
carries more than the CLI strictly needs (it is also the intended Go library for
a future desktop sync client), but nothing is CLI-less by design any more.

## 8. Open items  [LIVING]

- The wider gallery API (albums, versions, favorites, trash management, shares,
  chunked upload, ML/faces/search) is intentionally not wrapped — gallery stays
  a minimal capability floor. Files was widened in full (§2), CLI included (§7);
  do the same for gallery, feature-file by feature-file, only when a concrete
  need arises.
- `files sync` propagates no deletions and keeps no last-seen state; that is a
  deliberate safety choice, not a bug. A stateful three-way sync (with delete
  propagation) would be a separate, carefully-reviewed feature. The configured
  pairs (§7) inherit exactly that behaviour: removing a pair removes the
  arrangement, never a file.
- The desktop UI is still English only. The strings are in the page constants
  rather than a catalogue, which is the next piece of work and the reason the
  language preference exists but has nothing to switch yet: shipping a
  half-translated window would be worse than shipping an English one.
- Transfer caps are stored and shown but not yet enforced: throttling means a
  rate-limited reader around every upload and download, which belongs in
  `internal/api` rather than bolted onto the desktop. Stored now so the setting
  and its plumbing land together rather than in two releases.
- The windows need the Edge WebView2 runtime. It ships with Windows 11 and with
  any current Edge on Windows 10, so in practice it is there — but "in practice"
  is not "always", and a machine without it gets an explanatory error rather
  than a window. Bundling the evergreen installer would add ~150 MB to a 8 MB
  program; the fixed-version distribution is the answer if that ever bites.
- Windows only, still: the Linux tray (StatusNotifierItem) and the macOS one
  (Cocoa) are separate work. `internal/syncrunner`, `internal/syncconfig` and
  `internal/applog` are deliberately platform-free so that work is a front end,
  not a rewrite.
- The sync engine still keeps no last-seen state, so change detection means
  "something changed here, reconcile the pair", not "this file changed, send
  it". For a large tree that is a full comparison each time; it is correct and
  bounded, but a stateful index is what would make it cheap.
- `internal/files` (tree render + sync engine) and `internal/webdavfs` are both
  CLI-facing today: the sync engine writes progress to stdout, and the WebDAV
  filesystem has no progress/conflict callbacks. A GUI-facing layer would need
  those hooks; the GTK client itself still does not exist in this repo.
- `files webdav` serves plain HTTP on loopback because that is what the OS mount
  clients speak (`net use`, `gio mount`, `mount_webdav`). It is bound to loopback
  and Basic-auth-gated by default; a TLS-on-loopback variant (self-signed +
  per-run trust) has not been built and would mostly fight the mount clients.
- The server also exposes its own `/dav` WebDAV endpoint (sabre/dav) with a
  separate app password. `files webdav` deliberately does NOT proxy that: it
  serves the REST surface this client already speaks, so it works with the same
  bearer, pinning and kill switch as every other command. If the server's `/dav`
  ever gains capabilities the REST surface lacks, revisit.
- The server's `/files/changes-stream` (SSE) is documented upstream as existing
  for this client; nothing here consumes it. `files sync` polls (`--interval`) or
  watches the local filesystem instead, so a mount/sync notices a remote change
  on the next poll rather than instantly. Wiring SSE would cut that latency —
  and would pin a server worker for the life of the connection, which is why the
  server caps concurrent streams.
- The tray GUI is Windows-only. Linux (StatusNotifierItem, .desktop autostart,
  a Start-menu entry in the .deb/.rpm) and macOS (Cocoa, which needs CGO and
  therefore breaks the static build the release assumes) are separate slices;
  `cmd/ledgerline-gui/main_other.go` is a stub that says so rather than
  pretending.
- The tray shows state, signs in and out, and opens the settings window. It does
  not expose the WebDAV mount or notifications — those are the obvious next
  slices, and each needs a decision about what a tray should do when a long
  operation fails.
- The Windows installer is NSIS, chosen because it cross-builds on the Linux
  release runner. Group Policy deployment would want an MSI (WiX on a Windows
  runner); nothing depends on that today.
- Neither the binaries nor the installer are Authenticode-signed, so Windows
  SmartScreen warns on first run. Signing needs a certificate the project does
  not have; the release is attested and checksum-signed with cosign instead.
- CI-infra items inherited from before the pivot (SBOM/reproducible-build/signed
  commits) are org-policy, not in the repo tree.

## 9. Conflicts & decisions  [LIVING]

- **ZK → plaintext pivot (2026-08-11).** The server API moved to plaintext-
  relational (openapi v1.521.0). The CLI was rewritten from a zero-knowledge
  sealed-store client to a thin plaintext REST client, and its scope cut to
  gallery + files only. The GUI (`cmd/ledgerline-gui`), all other modules
  (todos/notes/passwords/health/bookmarks), local ML, and the Immich importer
  were deleted. Design/plan: `docs/superpowers/specs/2026-08-11-plaintext-rewrite-gallery-files-design.md`,
  `docs/superpowers/plans/2026-08-11-plaintext-rewrite-gallery-files.md`.

## 10. Changelog

- 2026-08-21 feat: **the desktop client grew the parts a desktop client has.**
  Preferences, a shell context menu, and photo upload — measured against what
  Proton Drive and Google Drive put in front of a user, and cut to what this
  server can actually back.

  **Preferences** (`internal/deskprefs`, a JSON file beside the session) are the
  choices that belong to one computer: launch at login, pause syncing, do not
  sync on battery or on a metered connection, transfer caps, the never-sync
  list, the camera folder. Deliberately not on the server — one laptop's power
  policy following the user to their desktop would be a bug, not a feature. The
  one account setting on the same page is the file version cap, because that is
  where a user looks for it; it needed a client for `/settings`, which the server
  had and the Go client did not.

  Every one of them is wired to something. `internal/syncrunner` gained a `Hold`
  hook the tray fills from the preferences (`internal/deskpower` answers the
  power and cost questions; both fail open, because a sync that stopped for an
  unreadable battery flag is a bug you debug for an afternoon), and
  `files.SyncOptions` gained `Skip`, so the exclusion list applies to the walk
  rather than being a list nobody reads. A setting that does nothing is worse
  than a setting that is missing.

  **The Explorer menu** (`internal/deskintegrate`) is registry verbs, not a COM
  handler: no DLL loaded into Explorer, and a crash can only ever take down the
  process it launched. Each verb runs
  `ledgerline-gui --context <verb> "<path>"` — a separate short-lived process,
  chosen over routing to the running tray because a local IPC channel that can
  make the tray upload, share or decrypt a file is a channel worth attacking.
  Copy a share link, encrypt or decrypt with the account's keyring, upload,
  add to the gallery, keep a folder in sync.

  They live under **HKLM**\SOFTWARE\Classes, which costs an elevation prompt and
  was not the first choice. Per-user verbs under HKCU were written correctly —
  `ExtendedSubCommandsKey` cascade, label in the key's default value, a `Position`
  and an `Icon` — and Explorer did not draw them on the test machine, while a
  byte-identical registration under HKLM appeared immediately. That was measured
  with a pair of otherwise identical probe keys, not inferred, after three
  rounds of guessing at the shape (the label in `MUIVerb` alone is not enough,
  `CommandFlags` must be a DWORD, and Windows' own cascades do not set
  `SubCommands`). The installer registers it as administrator; the switch in
  Settings re-runs `ledgerline-cli shell-menu install` elevated, so a consent
  prompt appears exactly when somebody flips the switch and never otherwise.

  Two bugs on the way there are worth remembering, because both were invisible
  in English. `isNotFound` compared `err.Error()` against `"cannot find"`, and on
  a German Windows the message is "Das System kann die angegebene Datei nicht
  finden" — so clearing a store that did not exist yet read as a failure and
  registration stopped after writing the parent key. It matches error *codes*
  now, and the test asserts on codes so it fails for the old implementation even
  on an English runner. The same comparison was wrong in the autostart removal.
  And in PowerShell, `*` in a registry path is a wildcard: reading
  `HKEY_CLASSES_ROOT\*\shell\...` without `-LiteralPath` silently returns
  nothing, which cost one wrong conclusion about a key that was there all along.

  Encryption goes through the server's own keyring, the same one the web app's
  Files module uses, so a file encrypted from Explorer opens in the browser. The
  honest limit is stated rather than hidden: share, encrypt and decrypt need a
  counterpart on the server, so outside a synced folder the window says so and
  offers the upload instead of quietly uploading a plaintext copy first.

  **Photos**: one watched folder, polled rather than fsnotify-watched, because
  a card reader produces a create event long before the last byte and a poll
  that only takes files whose size stopped changing cannot upload half a video.

  Presentation: a monochrome line-icon set (`internal/deskui/icons_windows.go`)
  drawn to match the web app's Material Symbols and emitted once as an SVG
  sprite — hand-built rather than a font, because a page with `default-src
  'none'` cannot fetch one and a 320 kB woff2 as a data URI to draw thirty
  glyphs is a poor trade.

- 2026-08-21 feat: **the desktop windows are WebView2 pages, not Win32 dialogs.**
  The hand-rolled toolkit (`internal/win32ui`: window, fields, buttons, list,
  tabs, owner-drawn accent button, DWM frame) is gone; what is left of it is the
  shell folder chooser, which is the one dialog worth keeping native. Settings
  and sign-in are now HTML rendered by the Edge WebView2 control through
  `internal/deskui`, using the web app's own tokens — the same violet, the same
  surfaces, the same radii, the same dark-mode flip.

  This was not a paint job. A themed Win32 dialog still looks like a Win32
  dialog: the sunken field wells, the flat grey, the 12 px caption font and the
  outlined default button are what the platform draws, and the two rounds spent
  approximating a modern stylesheet with `WM_CTLCOLOR*` and `WM_DRAWITEM` made
  that plain. Rendering the markup directly costs one runtime dependency and
  removes ~1 400 lines of syscall plumbing.

  The page is sealed shut: no origin, no network, everything embedded, and a
  content policy of `default-src 'none'` with `img-src data:`. The avatar
  reaches it as a data URI the Go side fetched, sniffed and refused unless it
  really was an image; every outward jump (Explorer, the browser) goes through a
  binding that decides what a safe target is. What the page can do is exactly
  the set of functions bound to it.

  **Picking a folder on the server is a browser, not a list.** The first version
  listed every folder path at once, which is a thing you read rather than a place
  you move through. `remotebrowser_windows.go` is one implementation shared by
  both windows — the pair editor and the Explorer verb — so the two cannot drift:
  breadcrumb, double-click to enter, up, new folder, and the files shown greyed
  out and unselectable, because a dialog that returns a folder should not offer
  rows that look clickable and are not. The whole tree arrives in one call
  (`remotetree_windows.go`) and navigation happens in the page: the files listing
  is the only endpoint there is — there is no "children of this folder" call — so
  a request per click would fetch everything on every click anyway.

  **Dark mode** is a stored preference, not only a system follow. The stylesheet
  carries three blocks: the light palette on `:root`, the dark one under
  `prefers-color-scheme: dark` guarded by `:root:not(.light)`, and the dark one
  again on `:root.dark`, so an explicit choice beats the system in both
  directions. Every window reads the preference as it opens rather than
  capturing it once, which means changing the setting shows in the next window
  and not in the one already on screen — the honest limit of not re-rendering a
  live page.

  The tray menu gave up its identity half to the window entirely: first the
  **avatar** and the **storage figures** (now a two-tone bar with the files and
  gallery split beside it), then the account row and its submenu — name, e-mail,
  server. A tray menu is read at a glance, four numbers behind a hover were four
  numbers nobody read, and the menu is also on screen for anyone walking past
  the machine. `trayui.State` lost `UserName`, `UserEmail` and `Usage` with it:
  the `/me` refresh still runs — it is how a revoked device and a remote wipe
  are noticed — but its identity half now stops at the caller instead of
  travelling to a renderer that would drop it. Two orphans went with the row,
  `ICOFromAvatar` and `StorageLine`, which only their tests still called. The folder list lost its em
  dashes with the rest of the widget — a dash between a path and its state reads
  as a correction, not a label.

- 2026-08-21 fix+feat: **the tray told the truth about storage, and grew a
  settings window.** `/me` returns `{used, quota}`; the client read `files` and
  `gallery`, which are not in that payload, so a 57 GiB account displayed
  0 B — twice, once per module. `api.Usage` now reads `used` and treats the
  per-module fields as optional (pointers, absent ≠ zero), and the tray shows
  one `Storage:` line when the server does not break it down instead of
  inventing two empty ones. The server side was fixed in the same pass:
  `StorageUsage::snapshotForUser` now carries `files` and `gallery` alongside
  `used`, from queries it was already running.

  Around that: the menu is two rows with submenus instead of five stacked
  figures, and says nothing at all before sign-in; the icon has a third state
  with a dot for "syncing" and the sync row says what is running or what failed;
  one tray per user (a named mutex — two were running, each with its own sync
  loop); a rotating log in `logs` beside the programs, with **Open log folder**
  in the menu; the mark from `internal/trayui` is now generated as a multi-size
  .ico and stamped into both executables, the installer, the shortcuts and the
  window title bars (which also brought the side-by-side manifest, so the
  controls are themed and DPI-aware — the open item from the previous entry).

  Sync grew the two things that make it usable: **fsnotify change detection**
  (`internal/syncrunner`, shared by `sync service` and the tray: debounced,
  per-pair, re-armed when the list changes) and a **form that asks for the
  remote folder** instead of guessing it from the local folder's name, with the
  interval in minutes and a watch toggle. Tests cover the runner's behaviour
  without a server or a tray: a change fires a sync, a burst collapses into one,
  a paused pair does not run, a signed-out client does nothing, and a change
  outside every pair is ignored.

- 2026-08-20 feat: **password sign-in, a native window, and configured folder
  pairs.** Sign-in no longer goes through a browser page: `internal/win32ui` is
  a small hand-rolled Win32 toolkit (window, fields, buttons, list, folder
  picker, message loop, DPI-aware, system font) and the dialog on top of it
  offers both routes — credentials with the account's second factor, or the
  one-time code — because both are legitimate and the choice is the user's.
  `internal/authflow` holds both and is shared with the CLI, which gains
  `auth login` next to `auth pair`. The factor stays server-enforced; what is
  new is that this client now handles a password at all, so it is read without
  echo, kept for one request, and never written anywhere. That work exposed a
  real bug: the stdin secret reader buffered all of stdin and swallowed the next
  line, so a piped password ate the prompted 2FA code.

  The second half is the sync area the tray was missing. `internal/syncconfig`
  stores folder pairs (local directory, remote folder, direction, conflict
  policy, schedule, last outcome) in one file that the CLI (`sync add|ls|set|
  rm|run|service`) and the tray window both read and write, and the sync engine
  grew a RemoteRoot so a pair can be scoped to one remote folder instead of
  mirroring the whole tree — without which "several folders" cannot mean
  anything. The tray runs due pairs in the background. Removing a pair removes
  the arrangement and no files, and deletions are still never propagated.

  Tests: the factor is enforced (nothing stored until the code is right),
  recovery codes work, bad credentials keep the server's deliberate ambiguity
  with a blocked account, unverified e-mail is its own failure, the token is
  verified before storage, the install id is sent, scoping really scopes (files
  outside the pair's folder are neither pulled nor uploaded, an escaping root is
  refused, a missing root is created), and the CLI round-trips add/list/pause/
  run/remove. The toolkit has an opt-in manual test that puts a real window on
  screen, since "does it look right" cannot be asserted.

- 2026-08-20 feat: **graphical sign-in + per-module storage in the tray.**
  Sign-in no longer opens a console: `internal/loginui` serves a small dialog on
  loopback under a per-run path token and opens it in the browser — which is
  where the pairing code comes from anyway, and where the clipboard and IME just
  work. The pairing sequence moved into `internal/pairflow` (claim → wait for
  approval → verify the token → store), so terminal and dialog share one
  implementation and one set of error messages. Nine tests drive the dialog over
  real HTTP: the 404 for a wrong path token, the cross-origin refusal, retrying
  after a rejected code, and that neither the page nor the status endpoint ever
  echoes the code or the bearer. The tray now breaks storage out per module
  (Files / Gallery / Total against the shared quota) instead of one summed line.
  Worth stating because it is a recurring question: **no password and no second
  factor ever reach this client** — the user authenticates in the web app, and
  only the short-lived code and the issued token cross the boundary.
- 2026-08-20 feat: **Windows tray GUI + one installer for both programs.** New
  `cmd/ledgerline-gui` (build-tagged windows; a stub elsewhere) draws a tray
  icon whose menu shows the version, the signed-in account with its avatar, the
  server host and the storage usage, plus Sign in / Sign out / Open web app /
  Refresh / Quit. All the decidable behaviour lives in `internal/trayui` — the
  menu model, the storage/host formatting, avatar→ICO conversion and the brand
  icon, drawn in code rather than shipped as an asset — so it is unit tested
  instead of eyeballed; the systray file is wiring only. `internal/clientset`
  is the one place that turns a stored session into a pinned client, used by
  both the CLI and the GUI. New `internal/api` `Avatar()`; `humanBytes` moved
  to `internal/ui` so tray and terminal print the same string. One dependency,
  `fyne.io/systray` (pure Go on Windows, keeps CGO_ENABLED=0). Packaging: an
  NSIS installer (`packaging/windows/installer.nsi`) puts both executables in
  Program Files with Start-menu shortcuts, an optional PATH entry, optional
  autostart and an uninstaller that leaves the user's config alone; built by
  `make installer-windows` on the Linux release runner. Two gosec findings in
  the new code were fixed rather than suppressed (the ICO length conversion is
  now bounded, and the server URL is scheme-checked before it reaches
  rundll32). **Also fixed a real packaging bug found on the way:** `make
  release` never appended `.exe` to the Windows binaries — an earlier patch had
  silently not applied — so the released Windows artefacts were extensionless.
- 2026-08-20 feat: **CLI parity for the whole Files surface + a mountable WebDAV
  endpoint + release packaging**. Every previously library-only Files capability
  now has a command (§7): `files share` (public links + internal viewer/editor
  shares), `files upload-link`, `files shared` (receiving side), `files zip`,
  `files archive create|extract`, `files keys|encrypt|decrypt`, and chunked
  upload wired into `files upload` automatically above 64 MiB. New
  `internal/webdavfs` implements `webdav.FileSystem` over `internal/api`, and
  `files webdav` serves it on loopback with generated Basic-auth credentials so
  the OS can mount the remote files as a network drive (`net use`, `gio mount`,
  `mount_webdav`); one new dependency, `golang.org/x/net` (§5). New
  `cmd/secret.go` gives every password flag a `--password-stdin` twin and makes
  the private-key passphrase stdin-only (§3). **Packaging/CI:** release targets
  gained `windows/amd64` and `windows/arm64` (plain `.exe`) and the Linux
  binaries are now wrapped into `.deb`/`.rpm` via a pinned nfpm recipe
  (`packaging/nfpm.yaml`, shell completions + docs included); CI runs build,
  tests and the race detector across Linux/macOS/Windows and splits lint,
  security (govulncheck + gitleaks history scan) and supply chain (tidiness,
  module checksums, SBOM drift, reproducibility, dependency review) into their
  own gates, which a release tag re-runs before publishing. golangci-lint gained
  gosec/bodyclose/errorlint/noctx/unconvert/misspell (all findings fixed, not
  suppressed, except documented path/permission exclusions), the SBOM generator
  is pinned to `GOOS=linux` so a Windows regeneration matches CI, a
  `.gitattributes` forces LF so a Windows commit cannot break the CI shell
  scripts, and the Windows-only failure in `TestConfigFileIsOwnerOnly` (Go
  reports 0666 there because Windows has no POSIX mode bits) is now handled
  OS-aware instead of failing the suite. Also removed a stale
  `internal/crypto/secretstream.go` lint exclusion left over from the ZK era.
  Full suite green on Windows; every release target cross-compiles.
- 2026-08-16 feat: **full Files-tag API surface + sync-core CLI**, as the Go
  library base for a future GTK desktop sync client. `internal/api` now wraps
  every `Files`-tagged openapi operation (v1.671.0; see §2), split across
  `files_folders/trash/versions/labels/activity/chunked/shares/archive/
  crypto.go`; `UpdateFile`/`UpdateFileShare` are optimistic-concurrency
  (`APIError.Version` decodes a 409's `version` for retry). New CLI commands
  for the Nextcloud-client-relevant core: `files rename|mv|copy`, `files
  folder`, `files trash`, `files versions`, `files labels`, `files search`,
  `files stats`, `files activity` (§7); sharing/encryption/archives/chunked
  upload are library-only by design. Also: `git clean`ed a pile of untracked
  pre-pivot files left on disk by earlier `git rm` commits (old ZK GTK GUI,
  crypto/notes/passwords/health/bookmarks/todo/vault/shard/ml packages, the
  old Immich importer, stale `files_ls.go`/`files_rm.go`/`files_open.go`) that
  had been silently breaking `go build ./...`; the repo now matches what §1
  and this changelog document. Full suite + `-race` green.
- 2026-08-11 feat: upload **content-dedup** (sha256) + progress bars. New
  `internal/uploadledger` (per-server hash ledger, `--batch` checkpoint, `--force`
  bypass); `gallery upload` skips already-sent bytes via the ledger, `files upload`
  via the server `sha256` + ledger; `files sync` already no-ops identical content.
  Uploads and a single `files sync` show an in-place progress bar on a TTY
  (`ui.IsTTY`). Removed the now-orphaned `internal/settings` package and the last
  ZK-vocabulary code comments.
- 2026-08-11 feat: **plaintext rewrite, gallery + files only.** Deleted the entire
  ZK stack (`crypto`, `canonicaljson`, `shard`, `vault`, `blobcache`,
  `manifeststore`, `conformance`, `ml`), all non-gallery/files modules
  (`todo`, `notes`, `passwords`, `health`, `bookmarks`), the GTK GUI + Docker/
  packaging, and the Immich importer. Rewrote `internal/api/gallery.go` +
  `internal/api/files.go` as plain JSON + streamed-multipart calls over the new
  plaintext endpoints (`+ multipart.go` helper); new thin `internal/gallery`
  (media walking) and `internal/files` (tree render + REST-based two-way sync +
  fsnotify watch service). Trimmed `session` of the vault-key + Immich-key caches.
  `go mod tidy` dropped `x/crypto` + `x/term`. Full suite + `-race` green.
