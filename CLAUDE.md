# CLAUDE.md — ledgerline-cli

Operating manual and single source of truth for the state of THIS repo, for the
next agent (human or model). Re-read the LIVING sections at every session
bootstrap and reconcile them at close-out. Keep it current in the SAME commit as
any change that affects it. Never put a real secret/key/token here — use obvious
placeholders.

---

## 1. Identity & scope

`ledgerline-cli` is the command-line client of the **Ledgerline** self-hosted
personal-cloud platform. As of 2026-08-11 the CLI is deliberately scoped to
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
- `GET /avatar` — stream the stored avatar image (404 = none stored).
- `GET/PUT/DELETE /account/webdav` — app-specific WebDAV/CardDAV/CalDAV
  password status/set/clear (`WebDavAccess`; distinct from the login password).

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
- **Audit trail:** local-only JSONL metadata (§7); never content or secrets.

Host assumptions: the host may be multi-user; argv/env/shell-history/temp paths
are treated as hostile to the bearer. The binary is fully disclosed — no
compiled-in secrets.

## 4. Architecture & module map

```
cmd/                    command tree: root, status, auth, audit, gallery, files (upload/ls/download/rm/
                        mkdir/sync + rename/mv/copy/folder/trash/versions/labels/search/stats/activity)
internal/api/           typed /api/v1 client: transport (client.go), auth, gallery, multipart helper;
                        files split by feature — files.go (core CRUD) + files_folders/trash/versions/
                        labels/activity/chunked/shares/archive/crypto.go (full Files-tag surface, §2)
internal/gallery/       local helpers: media-file walking, upload naming
internal/files/         local helpers: folder-tree render (tree.go) + two-way sync (sync.go) + watch service (watch.go)
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
- **Audit trail:** LOCAL-only JSONL at `<config>/audit.log` (0600, size-rotated).
  Operation METADATA only (`event,outcome,target,count,duration_ms,detail,pid`).
  Never a token or content. Managed with `audit show|path|purge`.

## 7. Command surface

```
auth login|logout|status|avatar   device pairing → bearer; identity; revoke; avatar image (login/status: --json)
auth webdav show|set|clear        app-specific WebDAV/CardDAV/CalDAV password (--json)
status                            local build info + update check
gallery upload <path...>          multipart upload (files/dirs, --jobs)
gallery list                      photo/video list
gallery download <id...>          originals (--out, --variant)
gallery rm <id...>                trash (bulk when >1)
files upload <file...>            multipart upload (--folder, --jobs)
files ls                          folder/file tree + usage (--json)
files download <id...>            raw bytes (--out)
files rm <id...>                  trash a file
files mkdir <name>                create folder (--parent)
files sync <dir>                  two-way sync (--direction, --conflict, --interval, --service, --json,
                                   --hidden, --remote-folder, --keep-versions, --max-versions)
files rename <id> <name>          rename a file
files mv <id...>                  move files (--to <folder-id> | --root)
files copy <id>                   duplicate a file (--to <folder-id>)
files folder rename|mv|rm|restore folder rename/move/trash(+--force)/restore
files trash ls|restore|rm|empty   trashed files+folders (rm/empty are permanent; ls: --json)
files versions ls|download|restore <file-id> [version]   version history
files labels ls|create|rm|set     coloured labels + per-file label set
files search <query>              full-text/OCR search
files stats                       usage by type + suspected duplicates
files activity [--file <id>]      Files activity feed
audit show|path|purge             local audit trail
```

Deliberately CLI-less (available only as `internal/api` Go methods — sharing,
encryption, archives, chunked upload and the rest of the Files-tag surface;
see §2 for the full list). A future GTK desktop sync client is the intended
consumer for those; add a CLI command for one only if a concrete CLI need
shows up.

## 8. Open items  [LIVING]

- The wider gallery API (albums, versions, favorites, trash management, shares,
  chunked upload, ML/faces/search) is intentionally not wrapped — gallery stays
  a minimal capability floor. Files was widened in full (§2); do the same for
  gallery, feature-file by feature-file, only when a concrete need arises.
- `files sync` propagates no deletions and keeps no last-seen state; that is a
  deliberate safety choice, not a bug. A stateful three-way sync (with delete
  propagation) would be a separate, carefully-reviewed feature.
- The full `internal/api` Files surface (§2) has no CLI command for sharing,
  encryption, archives or chunked upload by design (§7) — it exists for a
  future GTK desktop sync client to import as a library. `internal/files`
  (tree render + sync engine) is still local-CLI-only today.
- **The GTK client now exists**, as a separate sibling repo/Go module
  (`../ledgerline-gtk`), not a subdirectory of this one — it does **not**
  import `internal/*` (Go's own-module `internal/` visibility rule would
  forbid that across module boundaries anyway). Instead it drives this
  repo's built binary as a subprocess via `auth login/status --json` and
  `files sync --json` (§7). Keep those three flags' output shapes
  (`loginJSONResult`/`statusJSONResult`/`syncJSONEvent` in `cmd/auth.go` /
  `cmd/files_sync.go`) additive/backward-compatible — ledgerline-gtk's
  `internal/cliexec.CheckJSONSupport` feature-sniffs for `--json` in `files
  sync --help` at startup and refuses to run against an older binary, but it
  does not pin an exact version.
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

- 2026-08-16 feat: **`auth webdav show|set|clear`**, for ledgerline-gtk's
  GNOME/CardDAV integration. Wraps the existing server-side app-specific
  WebDAV password (`GET/PUT/DELETE /api/v1/account/webdav` — distinct from
  the login password; the one credential shared by every DAV client:
  Evolution Data Server, DAVx5, Thunderbird, ...). `internal/api.WebDavAccess`
  + three `Client` methods; all three subcommands support `--json`. This is
  the CLI-side half of native CardDAV/CalDAV sync — the server already speaks
  real DAV (Sabre, unified `/dav`, `.well-known/carddav`+`.well-known/caldav`
  redirects) and GNOME's Evolution Data Server (already on this Fedora, no
  new install) does the actual two-way contact/calendar sync; ledgerline-gtk
  only needs to manage this one password + register a local EDS source.
- 2026-08-16 feat: **local file versioning safety net + trash --json**, for
  ledgerline-gtk. `files sync` gains `--keep-versions`/`--max-versions`: right
  before a pull would overwrite an existing local file, it's snapshotted into
  `<localDir>/.ledgerline-versions/<reldir>/<stem>~<timestamp><ext>` (pruned
  to `--max-versions`, default 5) — a local, Syncthing-`.stversions`-shaped
  safety net independent of the server's own file version history (`files
  versions`), which only protects the *remote* copy (`internal/files/
  sync.go`: `snapshotLocalVersion`/`pruneVersions`, wired into `pullTo`).
  `.ledgerline-versions/` is always excluded from the sync scan itself
  (`scanLocal`), even with `--hidden`. `files trash ls --json` rounds it out
  (raw trashed files/folders, for a GUI trash view). Both opt-in/additive;
  default `files sync` behaviour is unchanged. Full suite green.
- 2026-08-16 feat: **multi-folder sync + hidden files/ignore list + avatar**,
  for ledgerline-gtk's multiple-sync-pair UI. `files sync` gains
  `--remote-folder <id>` (scope a pass to one remote folder's subtree instead
  of the whole root — `internal/files/sync.go`'s `remoteModel` now indexes
  paths relative to that scope; `ensureFolder("")` resolves to the scope
  folder itself rather than true root) and `--hidden` (include dotfiles,
  default still skips them). New `internal/files/ignore.go`: a
  `.ledgerline-ignore` file at a synced directory's root (gitignore-lite glob
  patterns, one per line) excludes matching paths from both push and pull;
  the ignore file itself is never synced. `files ls --json` (raw
  folders/files/usage, for a GUI's remote-folder picker) and
  `internal/api.Avatar` + `auth avatar` (streams `GET /api/v1/avatar`; 404 =
  no avatar stored, the common case) round it out. All additive; existing
  `files sync` behaviour with no new flags is unchanged (full suite green).
- 2026-08-16 feat: **`--json` output for the GTK client's subprocess use** —
  `auth login --json`, `auth status --json` (single JSON line each,
  `cmd/auth.go`), and `files sync --json`/`--service --json` (NDJSON: one
  `{"type":"log",...}` line per progress line the existing sync engine
  writes, plus a `{"type":"summary",...}` line per completed pass;
  `cmd/files_sync.go`'s `ndjsonLineWriter` wraps the `io.Writer` `internal/
  files.Sync`/`RunService` already take — neither of those, nor any other
  `internal/*` package, changed). Purely additive cmd/-layer flags; existing
  human-text output is unchanged when `--json` is absent. Written for
  `../ledgerline-gtk` (see §8), a separate sibling Go module that drives this
  binary as a subprocess rather than importing this repo's code.
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
