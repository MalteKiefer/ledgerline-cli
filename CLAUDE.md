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

**Gallery** (`internal/api/gallery.go`):
- `GET /gallery/data` → `{photos:[GalleryPhoto]}` (list, no bytes).
- `POST /gallery` (multipart `file`) → `{photo}`; HTTP 200 + `duplicate:true` on a
  sha256 match, 201 on a new row.
- `GET /gallery/{id}/download?variant=original|edited` → raw bytes.
- `DELETE /gallery/{id}` (soft delete) / `POST /gallery/bulk-destroy {ids}`.

**Files** (`internal/api/files.go`):
- `GET /files/data` → `{folders,files,labels,usage}`.
- `POST /files/entries` (multipart `file` + optional `file_folder_id`,`name`) → `{file}`.
- `POST /files/entries/{id}/content` (multipart) — replace bytes, archives a version.
- `GET /files/entries/{id}/raw?download=1` → raw bytes.
- `DELETE /files/entries/{id}` (soft delete).
- `POST /files/folders {name,parent_id}` → `{folder}`.

Endpoints the API exposes but the CLI deliberately does NOT consume (out of the
minimal capability surface): albums, favorite/toggle, trash/restore/empty,
force-delete, file versions list/restore, folder move/rename/copy, chunked
upload, people/faces, semantic search, shares, upload-links, labels.

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
cmd/                    command tree: root, status, auth, audit, gallery, files (+ files sync)
internal/api/           typed /api/v1 client: transport (client.go), auth, gallery, files, multipart helper
internal/gallery/       local helpers: media-file walking, upload naming
internal/files/         local helpers: folder-tree render (tree.go) + two-way sync (sync.go) + watch service (watch.go)
internal/session/       durable credential (OS keychain + 0600 file fallback)
internal/certpin/       TOFU certificate pinning
internal/audit/         local JSONL operation audit trail (0600, rotated, no secrets)
internal/config/ settings/ ui/ version/
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
files rm <id...>                  trash
files mkdir <name>                create folder (--parent)
files sync <dir>                  two-way sync (--direction, --conflict, --interval, --service)
audit show|path|purge             local audit trail
```

## 8. Open items  [LIVING]

- The wider gallery/files API (albums, versions, favorites, trash management,
  shares, chunked upload, ML/faces/search) is intentionally not wrapped — the CLI
  is a minimal capability floor. Add wrappers only when a concrete need arises.
- `files sync` propagates no deletions and keeps no last-seen state; that is a
  deliberate safety choice, not a bug. A stateful three-way sync (with delete
  propagation) would be a separate, carefully-reviewed feature.
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
