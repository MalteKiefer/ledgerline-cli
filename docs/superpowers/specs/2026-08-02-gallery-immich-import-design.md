# Design — `gallery import --immich`: direct Immich → Ledgerline gallery import

Status: approved design (2026-08-02). Next: implementation plan (writing-plans).

## 1. Goal

Import media from a self-hosted **Immich** server directly into the Ledgerline
gallery, end-to-end, without staging the whole library on disk first and without
plaintext leaving the host for *metadata*. Reuse the existing gallery upload
pipeline (sealing, sharded store, partial records, `--jobs`/`--batch`, dedup) and
the existing `internal/ml` immich-machine-learning client. The new surface is a
new **source** (Immich REST) plus a small metadata-injection seam and a resumable
import ledger.

Non-goals (v1): albums/date/favorites filters (whole-library sweep only), reusing
Immich's precomputed CLIP embeddings (technically impossible — see §3),
bidirectional/continuous sync, importing archived/hidden/trashed assets.

## 2. Facts that shape the design (verified 2026-08-02)

Immich app is on the **v3.x** line (OpenAPI 3.1.0); "ml v3" = Immich v3's ML
service, whose inference API is a single **unversioned** `POST /predict`. Verified
against the live OpenAPI spec + immich-app/immich `main`:

- **Auth:** header `x-api-key: <secret>`; base URL `http://<host>:2283/api`. Key
  minted in the Immich web UI (Account → API Keys).
- **No "list all" endpoint.** Enumerate via `POST /api/search/metadata`
  (`MetadataSearchDto`): pagination `page`+`size` (server caps ~1000, default 250),
  loop until `nextPage` is null. **Must** set `withExif=true` (and `withPeople=true`
  if faces wanted) or `exifInfo`/`people` are omitted. Filters available:
  `type`, `albumIds`, `personIds`, `takenAfter/Before`, `isFavorite`,
  `visibility`, `isMotion`, `withDeleted`.
- **Download original:** `GET /api/assets/{id}/original` → `application/octet-stream`.
- **Live Photos:** still asset's `livePhotoVideoId` (nullable) = the paired motion
  video's asset id (itself a hidden VIDEO asset). Fetch its `/original`.
- **Embeddings are NOT retrievable** from the server API (no embedding field on
  assets/faces; CLIP vectors live only in pgvector, reachable only indirectly via
  `search/smart`). So embedding **reuse is impossible** — ML must be recomputed.
- **Per-asset `checksum` = base64 SHA-1** of the original → natural dedup key.
- User's Immich models: CLIP **`ViT-B-32__openai`**, face **`buffalo_l`**, ML at
  `http://immich-machine-learning:3003`. These already equal the CLI defaults
  (`cmd/gallery.go` `defaultClipModel`/`defaultFaceModel`).

## 3. Reuse map (what already exists)

- `internal/ml/immich.go` already speaks immich-ml `/predict` with the current
  `entries` pipeline (`clip.visual.modelName`, `facial-recognition`) — matches the
  user's ML service. The importer reuses it verbatim via the existing `--ml-local`
  path (`buildAnalyzer` in `cmd/gallery.go`).
- `internal/gallery` pipeline (`Uploader`, `pipeline.go`, `store.go`) already:
  uploads the sealed original, optionally derives via server `/process` or the
  local analyzer, writes **partial records** (`thumbPending`/`mlPending`), pairs
  Live Photo halves, dedups by an exact-file **head+tail signature** (`indexSigs`),
  and batches with a checkpoint save.
- `Item{StillPath, MotionPath, SidecarTaken}` is the existing per-item shape.
  Google Photos already **injects** taken-time via `SidecarTaken` when files lack
  EXIF — the exact seam Immich needs, generalized.
- Content dedup needs the original **bytes** (it hashes head+tail), so it can only
  skip *after* download. The **import ledger** skips *before* download.

## 4. Architecture

New source `internal/gallery/immich.go` + a new command `gallery import`. The
importer is a **batch-streaming producer**, not a pre-download:

```
enumerate (search/metadata, page loop)                      [Immich]
  → per page: filter out already-imported (ledger)          [local ledger]
  → for a batch of N (= --batch): download original (+motion) to a temp dir,
      build Item{StillPath, MotionPath, SidecarTaken, Meta}  [Immich /original]
  → run the EXISTING processBatch over the batch (--jobs workers):
      seal+upload original, optional ML (--ml/--ml-local), build partial record,
      inject Immich metadata into the meta blob                [existing pipeline]
  → save the sealed gallery manifest (checkpoint)             [existing store.Save]
  → mark the batch's asset ids in the ledger; delete the temp batch
repeat until nextPage == null
```

Disk is bounded to ~one batch (`--batch` × avg asset size). Bandwidth is bounded
by the ledger (already-imported assets are never re-downloaded). Every failure
mode inherits the api.Client retry/backoff; a per-asset failure is counted and the
run continues; an Immich 401/403 or a Ledgerline auth-fatal aborts.

### 4.1 Metadata injection seam (small pipeline change)

Today EXIF/GPS reach the record only through a `/process` (`api.ProcessResult`) or
the local analyzer. Without ML, `Uploader.upload` early-returns a bare partial
record (no meta blob). New: `Item` gains an optional `Imported *ImportedMeta`
holding `{TakenAt time.Time, Lat, Lon *float64, CameraMake, CameraModel string,
Width, Height int, DurationSec *float64, Favorite bool}` mapped from Immich
`exifInfo`. When set, the pipeline writes the cold meta blob (GPS/camera/dims/taken)
from it **even when `/process` and ML are off** — so a no-egress import still
produces rich records. When ML *is* on, injected metadata fills the fields the
analyzer/`/process` doesn't (GPS/camera), and derived thumbs/embeddings/faces layer
on top. `taken_at` precedence: injected `TakenAt` > `/process` EXIF > `Created`.
This is the only change to the contract-adjacent record builder; it adds fields,
never alters the canonical/sealed byte layout of an existing field.

### 4.2 Live Photos

If a still's `livePhotoVideoId` is set, download that asset's `/original` to the
temp dir and set `Item.MotionPath`. The existing pipeline uploads the motion blob
and sets `MotionRef`/`MotionKey` — no `contentID` round-trip needed (the pairing
is explicit from Immich). Motion-part assets (hidden, `isMotion`) are **not**
enumerated as standalone items (default `visibility: timeline` excludes them; we
also skip any asset id that appears as another's `livePhotoVideoId`).

### 4.3 Import ledger (resume/dedup)

A local JSONL/DB at `<config>/immich-import/<server-hash>.state` (mirrors
`sync-state`), keyed by Immich `assetId` → `{checksum, ledgerlineRecordId,
importedAt}`. Before downloading a page's assets, drop ids already in the ledger.
After a batch's checkpoint save succeeds, append its ids. A re-run thus resumes
where it stopped and never re-downloads. The ledger is per Immich server (hashed
base URL) so multiple servers don't collide. Non-secret (asset ids + SHA-1 +
Ledgerline record ids); stored 0600 like the rest of the config dir.

## 5. CLI surface

New subcommand under `gallery` (per decision — own command, shared pipeline):

```
ledgerline-cli gallery import --immich \
  --immich-url http://host:2283 \
  [--immich-key <KEY> | env IMMICH_API_KEY | prompt] \
  [--ml-local http://immich-machine-learning:3003 | --ml | (none=partial)] \
  [--ml-clip-model ViT-B-32__openai] [--ml-face-model buffalo_l] \
  [--jobs N] [--batch N] [--dry-run]
```

- The API key is a secret: **never** a required argv value. Resolution order:
  `IMMICH_API_KEY` env → interactive no-echo prompt → `--immich-key` (documented as
  leaking into the process list; discouraged). Consistent with the passphrase
  posture (§7 of CLAUDE.md).
- `--immich` is a required marker on `import` (leaves room for other import
  backends later). `--jobs`/`--batch`/`--ml*`/`--dry-run` reuse the gallery upload
  definitions and semantics.
- `--dry-run` enumerates + reports counts (new/already-imported) without
  downloading or writing.

## 6. Immich client (`internal/gallery/immich.go`)

Thin typed client, same posture as `internal/api` (TLS floor, bounded response
reads, no secret in logs):

- `NewImmich(baseURL, apiKey string) (*Immich, error)` — validate URL, set
  `x-api-key`.
- `Ping(ctx)` — `GET /api/server/ping` (fail fast on bad URL/key).
- `Iterate(ctx, opts) (<-chan AssetPage, <-chan error)` or a paging iterator over
  `POST /api/search/metadata` with `withExif=true`, `withPeople=(ml on)`,
  `visibility=timeline`, `withDeleted=false`, ordered stably; loops on `nextPage`.
  To dodge deep-offset slowdown at 100k, enumerate in **descending taken-time
  windows** internally (transparent; not a user filter).
- `DownloadOriginal(ctx, assetID, dest) error` — stream `GET /api/assets/{id}/original`
  to a temp file, size-bounded.
- Typed `Asset` + `ExifInfo` subset (id, originalFileName, checksum, type,
  fileCreatedAt, localDateTime, duration, livePhotoVideoId, exifInfo{...}).

## 7. Error handling & limits

- Immich `401/403` → abort with a clear "check --immich-url/key/permissions".
- Per-asset download/decode failure → count `failed`, continue; logged without the
  key.
- Ledgerline auth-fatal (`401`) mid-run → abort (token dead), ledger already has
  the completed batches so a re-run resumes.
- Respect ctx cancellation between batches (Ctrl-C leaves a consistent ledger +
  saved manifest).
- Bounded reads on every Immich response (search JSON + original bytes) per the
  hostile-server rule (§3/§31 of the manual).

## 8. Testing

- `internal/gallery` Immich mock (`httptest`): `search/metadata` paging, `/original`
  download, a Live-Photo pair (`livePhotoVideoId`), `exifInfo` mapping.
- Metadata-mapping unit tests (Immich `exifInfo` → `ImportedMeta` → meta blob:
  taken precedence, GPS floats, duration parse `HH:MM:SS.ffffff`→seconds).
- Ledger resume test: two runs, second is a no-op (no downloads, all skipped).
- Batch-streaming test: N assets, `--batch B`, assert ceil(N/B) checkpoint saves +
  temp dir emptied per batch; race-clean under `go test -race`.
- Partial-record path (no ML): assert record carries injected GPS/camera/taken and
  `thumbPending`/`mlPending`, no plaintext egress (mock `/process` asserts it's
  never called).
- Conformance untouched (no canonical/sealed byte change).

## 9. Contract impact

**None to the byte contract.** The record gains no new *sealed* field shape; the
meta blob is cold/per-photo/never-hashed (floats already allowed there). The
metadata-injection seam only changes *where* the meta values come from (Immich vs
`/process`), not their serialization. No change to canonical JSON, sharding, blob
frame, or KEM. CLAUDE.md §2 "contract impact: no". Changelog + §4 module map get
the new `internal/gallery/immich.go` + `gallery import` command in the same commit.

## 10. Open questions / follow-ups (not v1)

- Filters (`--immich-album`, date window as a user flag, `--immich-favorites`,
  archived/hidden sweep) — deferred; enumeration already supports them internally.
- Reusing Immich face **boxes + person names** (retrievable, unlike embeddings) to
  pre-tag faces — possible later; embeddings still need recompute.
- Verifying the Ledgerline **server** CLIP model is `ViT-B-32__openai` so recomputed
  embeddings are search-coherent (user to confirm; mismatched model → embeddings
  are stored but useless cross-client, §8.5).
