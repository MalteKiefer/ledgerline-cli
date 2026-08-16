# Design: Plaintext-relational rewrite — gallery + files only

**Date:** 2026-08-11
**Branch:** feat/gallery-immich-import (rewrite lands here)
**Author:** Malte Kiefer (+ assistant)

## 1. Motivation

The Ledgerline server pivoted from the zero-knowledge sealed-store contract to a
**plaintext-relational** API (openapi v1.521.0, 2026-07-31). The whole reason the
CLI existed — being the ZK "capability floor" that writes sealed v3 records — is
gone. The new API:

- **Auth:** `email+password` (or existing device-pairing) → Sanctum bearer. No
  passphrase, no vault key (VK), no Argon2, no KEM.
- **Gallery:** `POST /gallery` multipart; bytes stored **plaintext** under
  `gallery/{uuid}`; server computes sha256, thumbnails, EXIF, CLIP, faces. Photos
  are integer-id rows (`GalleryPhoto`), listed via `GET /gallery/data`.
- **Files:** `GET /files/data` (folders + files + labels + usage), `POST
  /files/entries` multipart upload, `GET /files/entries/{id}/raw` download, plain
  folder tree (`FileFolder`, integer ids, `parent_id`).

The CLI must be reduced to **only gallery and files**, rewritten as a thin REST
client over the new plaintext endpoints. No local ML, no Immich import, no other
modules.

## 2. Scope decisions (settled)

| Decision | Choice |
|---|---|
| Approach | Full rewrite now against the plaintext API |
| ZK code | Delete entirely |
| Modules kept | gallery, files, auth (plumbing) only |
| GUI (`cmd/ledgerline-gui`) | **Delete entirely** (+ `Dockerfile.gui`, `packaging/`) |
| files sync service | **Keep**, ported to the REST API |
| Command surface | **Minimal** (see §5) |
| Local ML / Immich | Removed |

## 3. What survives (transport + plumbing)

Reused essentially unchanged — the transport layer already speaks bearer-token
Sanctum and matches the new auth surface:

- `internal/api/client.go` — TLS 1.3, cert pinning, retry/backoff, error shape.
- `internal/api/auth.go` — device pairing (`/auth/pair`, `/auth/pair/collect`),
  `Me`, `Devices`, `Heartbeat`, `Logout`, wipe. Already aligned to the new spec.
- `internal/session` — token storage (OS keyring + 0600 fallback), **trimmed**:
  drop the `VaultKey`/`VaultExpires` cache and the Immich key cache.
- `internal/config`, `internal/certpin`, `internal/audit`, `internal/version`,
  `internal/ui`, `internal/settings` (trimmed of sync-mapping ZK bits as needed).
- `cmd/root.go` (trim command registrations), `cmd/auth.go`, `cmd/status.go`,
  `cmd/audit.go`, `cmd/device_test.go`.
- `cmd/client.go` — **simplified**: drop `blobcache`/shard-cache helpers and the
  `degradable`/`warnIfDegraded` sharded-store plumbing (no sharded store exists
  anymore). Keep `auditLog`, `purgeAudit`, `newAPIClient`.

## 4. What is deleted

**ZK crypto/store stack** (no consumer after the rewrite):
`internal/crypto`, `internal/canonicaljson`, `internal/shard`, `internal/vault`,
`internal/blobcache`, `internal/manifeststore`, `internal/conformance`,
`internal/ml`.

**Other modules:**
`internal/todo`, `internal/notes`, `internal/passwords`, `internal/health`,
`internal/bookmarks`; `cmd/todo.go`; `cmd/lock.go` (vault unlock).

**Old sealed engines & API wrappers** (replaced in §6):
`internal/gallery/*` (sharded store, pipeline, immich, exiftool, import ledger),
`internal/files/*` (sharded store, records, tree — sync/watch kept but rewritten),
`internal/api/store.go`, `internal/api/blob.go`, `internal/api/gallery.go`,
`internal/api/files.go`, `internal/api/vault.go`.

**GUI & packaging:** `cmd/ledgerline-gui/`, `Dockerfile.gui`, `packaging/`,
`apk-out/`, `out/`, stray `ledgerline-cli` binary.

## 5. New command surface (minimal)

```
gallery upload <path...>        POST /gallery (multipart, whole)         — walks dirs, per-file
gallery list                    GET  /gallery/data                       — id, name, size, taken_at, media_type
gallery download <id> [--out]   GET  /gallery/{id}/download?variant=original
gallery rm <id...>              DELETE /gallery/{id}  (or /gallery/bulk-destroy for many)

files upload <path...> [--folder <id>]   POST /files/entries (multipart)
files ls [folder-id]                     GET  /files/data     — tree print
files download <id> [--out]              GET  /files/entries/{id}/raw?download=1
files rm <id...>                         DELETE /files/entries/{id}
files mkdir <name> [--parent <id>]       POST /files/folders
files sync <local-dir> [flags]           two-way sync (see §7)
```

Deferred (not in minimal surface): albums, favorite/toggle, trash/restore/empty,
force-delete, file versions, folder move/rename/copy, chunked upload, people/faces,
semantic search, shares, upload-links. The API exposes them; the CLI does not wrap
them yet.

## 6. New API wrappers

Rewrite `internal/api/gallery.go` + `internal/api/files.go` as plain typed JSON /
multipart calls (no crypto, no sharding):

**Types** mirror the openapi schemas: `GalleryPhoto`, `FileEntry`, `FileFolder`,
`FileLabel`, `FilesUsage`.

**Gallery:**
- `ListPhotos(ctx, albumID *int) ([]GalleryPhoto, error)` — `GET /gallery/data`
- `UploadPhoto(ctx, name string, r io.Reader) (GalleryPhoto, dup bool, error)` —
  multipart `POST /gallery`; 200+`duplicate=true` on sha256 match, 201 otherwise
- `DownloadPhoto(ctx, id int, variant string, w io.Writer) error` — streams
  `GET /gallery/{id}/download`
- `DeletePhoto(ctx, id int) error`; `BulkDeletePhotos(ctx, ids []int) error`

**Files:**
- `FilesData(ctx) (folders []FileFolder, files []FileEntry, usage FilesUsage, error)`
  — `GET /files/data`
- `UploadFile(ctx, name string, folderID *int, r io.Reader) (FileEntry, error)` —
  multipart `POST /files/entries`
- `DownloadFile(ctx, id int, w io.Writer) error` — `GET /files/entries/{id}/raw?download=1`
- `DeleteFile(ctx, id int) error`
- `CreateFolder(ctx, name string, parentID *int) (FileFolder, error)`

Multipart upload streams the file body (bounded memory), sets its own
`Content-Type: multipart/form-data`, and reuses the client's retry transport. A
shared `internal/api/multipart.go` helper builds the streamed multipart request so
gallery + files share one code path.

**Thin domain helpers** (replace the deleted engines):
- `internal/gallery` — upload walking (recurse a path, mime by extension, skip
  non-media), download-name resolution.
- `internal/files` — upload walking, folder-tree pretty-print for `ls`, plus the
  ported sync engine (§7).

## 7. files sync (ported)

Keep `files sync` and the continuous `--service` watch (fsnotify), rewritten
against the REST API:

- **Remote state:** `GET /files/data` → the folder tree + files (each with
  `sha256`, `size`, `updated_at`, `version`).
- **Local state:** walk the local dir; hash files (sha256) to compare against the
  server-computed `sha256`.
- **Reconcile:** map local relative paths ↔ remote folder tree by name. Push new/
  changed local files (`POST /files/entries`, creating folders via `POST
  /files/folders` as needed); pull new/changed remote files (`GET
  .../raw`). Changed = sha256 differs.
- **Conflict:** keep the existing `--conflict newest|keep-both|skip` flag; "newest"
  compares local mtime vs remote `updated_at`.
- **Direction flags:** preserve existing `--push`/`--pull`/two-way semantics.
- **Service mode:** fsnotify watch + interval re-scan, single-instance lock,
  graceful SIGINT/SIGTERM, stop on auth-expiry — all reused; only the store calls
  change from sealed-shard ops to REST calls.
- Content replace of an existing file uses `POST /files/entries/{id}/content`
  (server archives the prior bytes as a version) rather than a fresh upload, so the
  remote file id/history is preserved.

## 8. Non-goals / removed guarantees

- **No client-side encryption.** Bytes leave the host in plaintext (over TLS 1.3).
  This is the server's new model; the CLAUDE.md ZK threat-model sections (§3/§6/§7/
  §8) no longer describe the product and will be rewritten to match.
- No canonical-JSON / shard / manifest byte contract, no conformance fixtures.
- No local thumbnails/EXIF/ML — the server derives all of that.

## 9. Docs / metadata to update

- `CLAUDE.md` — rewrite §1–§8, §12–§15 to the plaintext model; drop the ZK
  contract, crypto inventory, sharding, conformance. Record the pivot in the
  changelog.
- `go.mod` — drop now-unused deps: `golang.org/x/crypto`, `crypto/mlkem` usage,
  `fsnotify` **stays** (sync kept). Re-run `go mod tidy`.
- Memory: update `store-v3-wire-contract` note — ZK is dead, API is
  plaintext-relational as of the pivot.

## 10. Verification

- `go build ./...` and `go vet ./...` clean (module builds without the GUI).
- `go test ./...` green (delete ZK/conformance tests; keep/adjust api + session +
  gallery + files + sync tests).
- Manual smoke against a dev server: `auth` (pair) → `gallery upload` a JPEG →
  `gallery list` shows it → `gallery download` round-trips bytes; `files upload` →
  `files ls` → `files download`; `files sync` a small dir both directions.
