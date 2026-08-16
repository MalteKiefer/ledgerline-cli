# Plaintext Rewrite (gallery + files only) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce ledgerline-cli to a thin plaintext REST client exposing only gallery and files against the new (v1.521.0) plaintext-relational API, deleting the entire zero-knowledge stack and all other modules.

**Architecture:** Keep the token-bearer transport + auth-pairing + CLI plumbing untouched. Delete the ZK crypto/sealed-store stack and non-gallery/files modules and the GUI. Rewrite the gallery/files API wrappers as plain JSON + streamed-multipart calls, and slim domain helpers for dir-walking/tree/sync.

**Tech Stack:** Go 1.25+, cobra, zalando/go-keyring, fsnotify (sync), stdlib net/http + mime/multipart.

## Global Constraints

- Module path: `github.com/MalteKiefer/ledgerline-cli`.
- No client-side encryption anywhere; bytes transit plaintext over TLS 1.3 (transport already enforces `MinVersion: VersionTLS13`).
- `go build ./...`, `go vet ./...`, `go test ./...` must all pass at the end of each task that touches compilable code (deletion tasks may leave transient breakage only within their own task, resolved before the task's final commit).
- No secrets in the audit trail or logs (existing invariant).
- Multipart uploads stream the body (bounded memory), never buffer the whole file.
- Commit messages: no AI attribution (repo hook-enforced).
- Reference spec: `docs/superpowers/specs/2026-08-11-plaintext-rewrite-gallery-files-design.md`.

---

### Task 1: Delete GUI, packaging, and build artifacts

**Files:**
- Delete: `cmd/ledgerline-gui/`, `Dockerfile.gui`, `packaging/`, `apk-out/`, `out/`, root `ledgerline-cli` binary, `.superpowers/` if stray.

- [ ] **Step 1:** Remove the directories/files:
```bash
git rm -r --cached --ignore-unmatch cmd/ledgerline-gui Dockerfile.gui packaging 2>/dev/null; \
rm -rf cmd/ledgerline-gui Dockerfile.gui packaging apk-out out ledgerline-cli
```
- [ ] **Step 2:** Confirm no remaining in-module reference to the gui path:
```bash
grep -rn "ledgerline-gui" --include='*.go' . ; echo "exit=$?"
```
Expected: no matches.
- [ ] **Step 3:** Commit.
```bash
git add -A && git commit -m "chore: remove GTK GUI, Docker/packaging (CLI-only refocus)"
```

---

### Task 2: Delete other modules + ZK stack; strip command tree to core

Get to a compiling core with only `auth`, `status`, `audit` commands (gallery/files temporarily removed, re-added in later tasks).

**Files:**
- Delete: `internal/todo/`, `internal/notes/`, `internal/passwords/`, `internal/health/`, `internal/bookmarks/`, `internal/ml/`, `internal/crypto/`, `internal/canonicaljson/`, `internal/shard/`, `internal/vault/`, `internal/blobcache/`, `internal/manifeststore/`, `internal/conformance/`.
- Delete: `internal/gallery/`, `internal/files/`, `internal/api/gallery.go`, `internal/api/files.go`, `internal/api/store.go`, `internal/api/blob.go`, `internal/api/vault.go` (all replaced later).
- Delete: `cmd/todo.go`, `cmd/lock.go`, `cmd/lock_test.go`, `cmd/gallery*.go`, `cmd/files*.go`.
- Modify: `cmd/root.go` (drop `newGalleryCommand`, `newFilesCommand`, `newTodoCommand` registrations for now — re-added in Tasks 4/6), `cmd/client.go` (drop `blobcache`, shard-cache helpers, `degradable`/`warnIfDegraded`), `cmd/auth.go` (drop shard-cache purge on logout if referenced).

- [ ] **Step 1:** Delete packages/files:
```bash
rm -rf internal/todo internal/notes internal/passwords internal/health internal/bookmarks \
  internal/ml internal/crypto internal/canonicaljson internal/shard internal/vault \
  internal/blobcache internal/manifeststore internal/conformance internal/gallery internal/files
rm -f internal/api/gallery.go internal/api/files.go internal/api/store.go internal/api/blob.go internal/api/vault.go \
  internal/api/batch_test.go cmd/todo.go cmd/lock.go cmd/lock_test.go \
  cmd/gallery.go cmd/gallery_download.go cmd/gallery_import.go cmd/gallery_test.go \
  cmd/files.go cmd/files_ls.go cmd/files_open.go cmd/files_rm.go cmd/files_sync.go cmd/files_sync_test.go
```
- [ ] **Step 2:** Edit `cmd/root.go`: remove `newGalleryCommand()`, `newFilesCommand()`, `newTodoCommand()` from `AddCommand(...)`. Leave `newStatusCommand()`, `newAuthCommand()`, `newAuditCommand()`.
- [ ] **Step 3:** Edit `cmd/client.go`: delete the `blobcache` import, `shardCacheDir`, `shardCache`, `purgeShardCaches`, `degradable`, `warnIfDegraded`. Keep `auditLog`, `purgeAudit`, `newAPIClient`.
- [ ] **Step 4:** Fix fallout in `cmd/auth.go` / `cmd/status.go`: remove any calls to `purgeShardCaches`, VK/vault, `api.Vault*`, `session.SaveVaultKey/LoadVaultKey`, Immich key. Grep to find them:
```bash
grep -rn "purgeShardCaches\|VaultKey\|api\.Vault\|Immich\|warnIfDegraded\|blobcache" --include='*.go' cmd internal/api
```
- [ ] **Step 5:** Build the reduced tree:
```bash
go build ./... 2>&1 | head -40
```
Expected: compiles (only `cmd`, `internal/api`, `internal/session`, `internal/config`, `internal/certpin`, `internal/audit`, `internal/version`, `internal/ui`, `internal/settings` remain).
- [ ] **Step 6:** `go test ./... 2>&1 | tail -20` — green (some suites gone). Fix/trim any remaining test referencing deleted symbols.
- [ ] **Step 7:** Commit.
```bash
git add -A && git commit -m "chore: remove ZK stack + non-gallery/files modules"
```

---

### Task 3: Trim session + settings of ZK/sync-ZK remnants

**Files:**
- Modify: `internal/session/session.go` — remove `VaultKey`/`VaultExpires` fields, `SaveVaultKey`/`LoadVaultKey`/`ErrNoVaultKey`/`ErrNoKeychainForVaultKey`, Immich key funcs (`SaveImmichKey`/`LoadImmichKey`/`ClearImmichKey`/`immichKeyringUser`), and vault clearing in `Clear`/`Logout`.
- Modify: `internal/session/session_test.go` — drop `TestVaultKeyCache*`, `TestLogoutClearsVaultKey`, Immich tests.
- Modify: `internal/settings/*` — drop ZK/sealed sync-mapping fields if any reference deleted types (keep plain sync-mapping config used by Task 7).

- [ ] **Step 1:** Edit `internal/session/session.go` to remove the vault/immich symbols listed above.
- [ ] **Step 2:** Edit `internal/session/session_test.go` to remove the corresponding tests.
- [ ] **Step 3:** `grep -rn "SaveVaultKey\|LoadVaultKey\|ImmichKey\|VaultKey" --include='*.go' .` → no matches.
- [ ] **Step 4:** `go build ./... && go test ./internal/session/... -v 2>&1 | tail -20` — green.
- [ ] **Step 5:** Commit.
```bash
git add -A && git commit -m "chore(session): drop vault-key + immich-key caches (no client crypto)"
```

---

### Task 4: New gallery API wrapper + shared multipart helper

**Files:**
- Create: `internal/api/multipart.go` — streamed multipart request builder.
- Create: `internal/api/gallery.go` — types + gallery calls.
- Test: `internal/api/gallery_test.go`.

**Interfaces:**
- Produces:
  - `func (c *Client) uploadMultipart(ctx context.Context, path, fieldName, fileName string, extra map[string]string, r io.Reader, out any) error`
  - `type GalleryPhoto struct { ID int64; Name string; Mime string; Width, Height int; Size int64; Favorite, Thumb, Preview, Motion bool; MediaType, Status string; Duration *int; Rotation int; FlipH bool; TakenAt, Camera, Place *string; Lat, Lng *float64; Version int; CreatedAt *string }` (json tags per openapi)
  - `func (c *Client) ListPhotos(ctx context.Context) ([]GalleryPhoto, error)`
  - `func (c *Client) UploadPhoto(ctx context.Context, fileName string, r io.Reader) (GalleryPhoto, bool, error)`
  - `func (c *Client) DownloadPhoto(ctx context.Context, id int64, variant string, w io.Writer) error`
  - `func (c *Client) DeletePhoto(ctx context.Context, id int64) error`
  - `func (c *Client) BulkDeletePhotos(ctx context.Context, ids []int64) error`

- [ ] **Step 1:** Write `internal/api/gallery_test.go` with an `httptest.Server` asserting: `ListPhotos` GETs `/api/v1/gallery/data` and decodes `{photos:[...]}`; `UploadPhoto` POSTs multipart to `/api/v1/gallery` with a `file` part and returns `duplicate` from a 200 body; `DownloadPhoto` GETs `/api/v1/gallery/{id}/download?variant=original` and copies the body; `DeletePhoto` DELETEs `/api/v1/gallery/{id}`. Use `api.New(srv.URL, api.WithHTTPClient(srv.Client()))`.
- [ ] **Step 2:** `go test ./internal/api/ -run Gallery -v` → FAIL (undefined).
- [ ] **Step 3:** Implement `internal/api/multipart.go`: build request with `io.Pipe` + `multipart.Writer` writing `extra` fields then the file part streamed from `r`; set the writer's `Content-Type`; run through `c.retriableDo` **without** body-replay (multipart from a one-shot reader is not replayable → single attempt; document this) and decode JSON via `readJSON`.
- [ ] **Step 4:** Implement `internal/api/gallery.go` per the interfaces. Endpoints all under `/api/v1`. `UploadPhoto` treats HTTP 200 as `duplicate=true`, 201 as new. `DownloadPhoto` streams the response body to `w` (custom path, bypassing JSON decode).
- [ ] **Step 5:** `go test ./internal/api/ -run Gallery -v` → PASS.
- [ ] **Step 6:** `go build ./... && go vet ./...` clean.
- [ ] **Step 7:** Commit.
```bash
git add internal/api/multipart.go internal/api/gallery.go internal/api/gallery_test.go && \
git commit -m "feat(api): plaintext gallery wrapper (list/upload/download/delete) + multipart helper"
```

---

### Task 5: gallery command (upload/list/download/rm)

**Files:**
- Create: `internal/gallery/gallery.go` — `WalkMedia(paths []string) ([]string, error)` (recurse dirs, keep image/video extensions), `GuessName(path string) string`.
- Create: `internal/gallery/gallery_test.go`.
- Create: `cmd/gallery.go` — `newGalleryCommand()` with `upload`, `list`, `download`, `rm`.
- Modify: `cmd/root.go` — re-add `newGalleryCommand()`.

**Interfaces:**
- Consumes: `api.Client` gallery methods (Task 4), `session.Load` for token/server, `newAPIClient`.
- Produces: `newGalleryCommand() *cobra.Command`.

- [ ] **Step 1:** Write `internal/gallery/gallery_test.go`: `WalkMedia` on a temp dir with `a.jpg`, `b.txt`, `sub/c.png` returns `a.jpg` + `sub/c.png` (sorted), skips `b.txt`.
- [ ] **Step 2:** `go test ./internal/gallery/ -v` → FAIL.
- [ ] **Step 3:** Implement `internal/gallery/gallery.go` (`WalkMedia`, `GuessName`, an exported `IsMedia(ext string) bool` with the openapi extension set: jpg jpeg png webp gif heic heif + common video mp4 mov m4v webm mkv avi).
- [ ] **Step 4:** `go test ./internal/gallery/ -v` → PASS.
- [ ] **Step 5:** Implement `cmd/gallery.go`:
  - `upload <path...> [--jobs N]`: walk media, bounded worker pool, `UploadPhoto` each (open file → stream), print `uploaded <name>` / `duplicate <name>`; count summary.
  - `list`: `ListPhotos`, print a table: id, name, size, media_type, taken_at/created_at.
  - `download <id...> [--out DIR]`: `DownloadPhoto` variant=original into `<out>/<name>` (fetch name via list or Content-Disposition; simplest: require it from `list`, name file `<id>` fallback).
  - `rm <id...>`: single → `DeletePhoto`; ≥2 → `BulkDeletePhotos`.
  - Each command builds the client via `mustClient(cmd)` helper (loads session, errors if unauthenticated).
- [ ] **Step 6:** Re-add `newGalleryCommand()` in `cmd/root.go`.
- [ ] **Step 7:** `go build ./... && go vet ./...` clean; `go test ./...` green.
- [ ] **Step 8:** Commit.
```bash
git add -A && git commit -m "feat(gallery): upload/list/download/rm against plaintext API"
```

---

### Task 6: New files API wrapper + files command (upload/ls/download/rm/mkdir)

**Files:**
- Create: `internal/api/files.go` — types + calls.
- Test: `internal/api/files_test.go`.
- Create: `internal/files/tree.go` — folder-tree model + pretty printer.
- Create: `internal/files/tree_test.go`.
- Create: `cmd/files.go` — `newFilesCommand()` with `upload`, `ls`, `download`, `rm`, `mkdir`.
- Modify: `cmd/root.go` — re-add `newFilesCommand()`.

**Interfaces:**
- Produces (`internal/api/files.go`):
  - `type FileFolder struct { ID, ParentID *int64 ... }` — note `ID int64`, `ParentID *int64`, `Name string`, `Version int`, timestamps `*string`.
  - `type FileEntry struct { ID int64; FileFolderID *int64; Name string; Mime *string; Size int64; Sha256 *string; Tags []string; Note *string; Favorite bool; Version int; CreatedAt, UpdatedAt *string }`
  - `type FileLabel struct { ID int64; Name, Color string }`
  - `type FilesUsage struct { Used int64; Quota *int64 }`
  - `func (c *Client) FilesData(ctx) (folders []FileFolder, files []FileEntry, usage FilesUsage, err error)`
  - `func (c *Client) UploadFile(ctx, name string, folderID *int64, r io.Reader) (FileEntry, error)`
  - `func (c *Client) ReplaceFileContent(ctx, id int64, r io.Reader) (FileEntry, error)` (POST `/files/entries/{id}/content`)
  - `func (c *Client) DownloadFile(ctx, id int64, w io.Writer) error`
  - `func (c *Client) DeleteFile(ctx, id int64) error`
  - `func (c *Client) CreateFolder(ctx, name string, parentID *int64) (FileFolder, error)`
- Produces (`internal/files/tree.go`): `func BuildTree(folders []api.FileFolder, files []api.FileEntry) *Node`, `func (n *Node) Print(w io.Writer)`.

- [ ] **Step 1:** Write `internal/api/files_test.go` (httptest): `FilesData` GET `/api/v1/files/data` decodes folders+files+usage; `UploadFile` multipart POST `/api/v1/files/entries` sends `file` + optional `file_folder_id`; `DownloadFile` GET `/api/v1/files/entries/{id}/raw?download=1`; `CreateFolder` POST `/api/v1/files/folders` with `{name,parent_id}`; `DeleteFile` DELETE.
- [ ] **Step 2:** `go test ./internal/api/ -run Files -v` → FAIL.
- [ ] **Step 3:** Implement `internal/api/files.go` (reuse `uploadMultipart`; `DownloadFile` streams body).
- [ ] **Step 4:** `go test ./internal/api/ -run Files -v` → PASS.
- [ ] **Step 5:** Write `internal/files/tree_test.go`: build tree from 2 folders (one nested) + 2 files, assert `Print` output indents children under parents.
- [ ] **Step 6:** Implement `internal/files/tree.go` (`BuildTree`, `Print`), `go test ./internal/files/ -v` → PASS.
- [ ] **Step 7:** Implement `cmd/files.go`:
  - `upload <path...> [--folder ID] [--jobs N]`: for each file, `UploadFile` (stream). (Directory recursion mirrors folders? Minimal: flat upload into `--folder`; recursion handled by `sync`.)
  - `ls`: `FilesData` → `BuildTree` → `Print`; also print usage line.
  - `download <id...> [--out DIR]`: `DownloadFile` → `<out>/<name>` (name from `FilesData` lookup, fallback `<id>`).
  - `rm <id...>`: `DeleteFile` each.
  - `mkdir <name> [--parent ID]`: `CreateFolder`.
- [ ] **Step 8:** Re-add `newFilesCommand()` in `cmd/root.go`.
- [ ] **Step 9:** `go build ./... && go vet ./... && go test ./...` clean.
- [ ] **Step 10:** Commit.
```bash
git add -A && git commit -m "feat(files): upload/ls/download/rm/mkdir against plaintext API"
```

---

### Task 7: Port files sync (two-way + --service watch)

**Files:**
- Create: `internal/files/sync.go` — reconcile engine over the REST API.
- Create: `internal/files/sync_test.go`.
- Create: `internal/files/watch.go` — fsnotify watch + interval loop + single-instance lock + signal shutdown (adapt the deleted originals, swapping sealed-store ops for REST).
- Create: `cmd/files_sync.go` — `newFilesSyncCommand()`.
- Modify: `cmd/files.go` — add `newFilesSyncCommand()` to the files group.

**Interfaces:**
- Consumes: `api.Client` files methods (Task 6), `internal/files.BuildTree`.
- Produces:
  - `type SyncOptions struct { Dir string; Direction string; Conflict string; Interval time.Duration; Service bool }`
  - `func Sync(ctx context.Context, c *api.Client, opts SyncOptions, log io.Writer) error`

- [ ] **Step 1:** Write `internal/files/sync_test.go` against an httptest server modelling `/files/data`, `/files/entries` (upload), `/files/folders`, and `/files/entries/{id}/raw`: a local temp dir with one new file pushes it (POST seen); a remote-only file pulls it (local file appears); an unchanged file (sha256 match) is a no-op. Cover `--conflict skip` on a both-changed file.
- [ ] **Step 2:** `go test ./internal/files/ -run Sync -v` → FAIL.
- [ ] **Step 3:** Implement `internal/files/sync.go`:
  - Fetch remote via `FilesData`; build path→FileEntry and path→FileFolder maps (path = folder-name chain + file name).
  - Walk local dir (respect an ignore list — reuse a small `.llignore`/hidden-file skip; keep simple).
  - For each local file not on remote (or sha256 differs): ensure folder chain exists (`CreateFolder` as needed, memoized), then `UploadFile` (new) or `ReplaceFileContent` (existing id) per direction.
  - For each remote file not local (or differs): `DownloadFile` to the local path (pull direction).
  - Conflict (both changed since last seen): honour `--conflict newest|keep-both|skip` (newest by local mtime vs remote `UpdatedAt`).
  - `--push`/`--pull` gate the two halves; default two-way.
- [ ] **Step 4:** `go test ./internal/files/ -run Sync -v` → PASS.
- [ ] **Step 5:** Implement `internal/files/watch.go` (fsnotify recursive add, debounce, interval re-scan, single-instance lock file under config dir, SIGINT/SIGTERM graceful stop, stop on `api.Status(err)==401`). Adapt from git history of the deleted `service.go`/`watch.go` (`git show HEAD~:internal/files/watch.go`), replacing store calls with `Sync`.
- [ ] **Step 6:** Implement `cmd/files_sync.go`: `files sync <dir> [--push|--pull] [--conflict ...] [--interval D] [--service]`. Non-service runs one `Sync`; `--service` runs the watch loop.
- [ ] **Step 7:** `go build ./... && go vet ./... && go test ./...` clean.
- [ ] **Step 8:** Commit.
```bash
git add -A && git commit -m "feat(files): port two-way sync + --service watch to plaintext REST API"
```

---

### Task 8: Docs, deps, memory

**Files:**
- Modify: `CLAUDE.md` — rewrite to the plaintext model (see below).
- Modify: `go.mod`/`go.sum` — `go mod tidy` (drops `golang.org/x/crypto`; keeps cobra, go-keyring, fsnotify, x/term, x/sys).
- Modify: memory `store-v3-wire-contract.md` + `MEMORY.md` index; delete `gallery-edited-contentid-followup.md` (Immich-era, obsolete).

- [ ] **Step 1:** Rewrite `CLAUDE.md`: §1 identity = plaintext REST CLI for gallery+files; delete/replace the ZK crypto inventory (§6), threat-model ZK specifics (§3/§7/§8), shared byte-contract (§2), sharding/conformance (§4/§12/§13/§17). Keep: dependency policy, transport (TLS 1.3 + cert pinning), audit trail, command map (gallery/files/auth/status/audit). Add a changelog entry dated 2026-08-11 describing the pivot rewrite.
- [ ] **Step 2:** `go mod tidy` then `go build ./... && go test ./...` clean.
- [ ] **Step 3:** Update memory: rewrite `store-v3-wire-contract.md` to "API is plaintext-relational (v1.521.0); CLI is a thin plaintext REST client, ZK removed 2026-08-11"; fix its `MEMORY.md` line; delete the obsolete Immich follow-up memory + its index line.
- [ ] **Step 4:** Commit.
```bash
git add -A && git commit -m "docs: rewrite CLAUDE.md for plaintext model; go mod tidy (drop x/crypto)"
```

---

### Task 9: End-to-end smoke verification

- [ ] **Step 1:** `go build -o /tmp/llcli . && go vet ./... && go test ./...` all green.
- [ ] **Step 2:** If a dev server + token are available: `auth login` (pair), `gallery upload <jpg>`, `gallery list` (shows it), `gallery download <id>` (bytes round-trip), `files upload`, `files ls`, `files download`, `files sync <dir>`. Record results. If no server, note smoke as manual-pending and rely on the unit suites.
- [ ] **Step 3:** Final commit if any fixups.

---

## Self-Review

- **Spec coverage:** §3 survives → Tasks 2/3; §4 deletes → Tasks 1/2; §5 surface → Tasks 5/6/7; §6 wrappers → Tasks 4/6; §7 sync → Task 7; §9 docs → Task 8; §10 verify → Task 9. All covered.
- **Type consistency:** `GalleryPhoto`/`FileEntry`/`FileFolder`/`FilesUsage` defined once in `internal/api` (Tasks 4/6) and consumed by cmd + tree + sync. IDs are `int64`; nullable fields are pointers.
- **Placeholders:** none — each task has concrete files, endpoints, and commit lines.
