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
auth login|logout|status          device pairing → bearer; identity; revoke
status                            local build info + update check
gallery upload <path...>          multipart upload (files/dirs, --jobs)
gallery list                      photo/video list
gallery download <id...>          originals (--out, --variant)
gallery rm <id...>                trash (bulk when >1)
files upload <file...>            multipart upload (--folder, --jobs)
files ls                          folder/file tree + usage
files download <id...>            raw bytes (--out)
files rm <id...>                  trash a file
files mkdir <name>                create folder (--parent)
files sync <dir>                  two-way sync (--direction, --conflict, --interval, --service)
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
  future GTK desktop sync client to import as a library. That GTK app itself
  does not exist yet in this repo; `internal/files` (tree render + sync engine)
  is local-CLI-only today and would need a GUI-facing layer (progress/conflict
  callbacks instead of stdout/a progress bar) when that client is built.
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
