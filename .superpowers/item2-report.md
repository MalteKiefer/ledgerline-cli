# Item 2: send `counts` on gallery + files sealed-store PUT

## Status: DONE

Branch: `feat/store-counts-and-docs` (unchanged — no branch switch).
Commit: `9c532c8` feat(store): send per-slice counts on gallery+files PUT for anomaly-scan (complete-or-omit)

## What changed

### `internal/api` (contract-owned wire shape)
- `internal/api/gallery.go`: `SaveGalleryStore` gained a fifth param
  `counts map[string]int`. The PUT body gets a `"counts"` key added **only**
  when `counts != nil` — nil means the key is omitted entirely, never sent as
  `null` or a partial map.
- `internal/api/store.go`: `SaveFilesStore` — identical shape/rule for the
  `{"files","fileFolders"}` map.
- Everything else about the two calls (ciphertext, version, shards, 409 →
  `ErrVersionConflict`, missing_shard → `ErrMissingShard`) is unchanged.

### `internal/files/store.go` (always-complete)
- `saveOnce` now builds `counts := map[string]int{"files": sumDescriptorCounts(descriptors), "fileFolders": len(folders)}`
  and passes it to `SaveFilesStore`. Added helper `sumDescriptorCounts`. This
  map is **always** non-nil — Files has no opaque collections, both slices are
  fully known to this client on every save.

### `internal/gallery/store.go` (complete-or-nil)
- Added `Store.collCountCache map[string]int` (ref → count memo; refs are
  content-addressed and this client never edits albums/people, so a ref's
  count never changes once known).
- Added `collectionCount(ctx, ref, key) (int, error)`: ref=="" → `(0, nil)`
  with no fetch; otherwise `GetGalleryBlob` → `DecryptContent` → array length,
  reusing the exact same decode used for record shards (no retry — a single
  failure here means "abort counting this round", not "retry").
- Added `buildCounts(ctx, photos) map[string]int`: computes albums/people via
  `collectionCount` against `rootString(s.root, "albumsRef"/"albumsKey"/...)`.
  **Any** error counting a present collection → returns `nil` for the whole
  map (never a partial `{photos:N, albums:0, people:0}`). The save itself is
  unaffected — `saveOnce` proceeds and just calls `SaveGalleryStore(..., nil)`.
- `saveOnce` now sums `descriptor.Count` across all shard buckets for
  `photos`, calls `buildCounts`, and passes the result (possibly nil) to
  `SaveGalleryStore`.
- No change to ciphertext, shard descriptors, canonical bytes, or shard
  hashing — counts ride along as pure metadata.

### Test/mock changes
- `internal/api/api_test.go`: updated the 3 existing `SaveGalleryStore` call
  sites for the new signature (`nil` counts); added 4 new tests —
  `TestSaveGalleryStoreSendsCountsWhenNonNil`,
  `TestSaveGalleryStoreOmitsCountsWhenNil`,
  `TestSaveFilesStoreSendsCountsWhenNonNil`,
  `TestSaveFilesStoreOmitsCountsWhenNil` — using a small `captureBody` helper
  server that decodes the raw PUT body into `map[string]json.RawMessage` so
  presence/absence of the `"counts"` key itself is asserted, not just its
  value.
- `internal/files/files_test.go`: mock gained `lastCounts map[string]int`,
  decoded from the PUT body's `counts` field (nil if the server received no
  such key). Added `TestSavePUTCarriesCompleteCounts` (seeds 2 files across a
  shard + 1 folder, adds a file, asserts `lastCounts == {files:3, fileFolders:1}`).
- `internal/gallery/gallery_test.go`: `mockServer` gained `lastCounts`, decoded
  the same way. Added a new test helper `seedGalleryRoot(photos, albums,
  people []any)` that seals a v3 root directly with a photo shard plus
  optional albums/people collection blobs (nil slice → ref absent/unset; `[]any{}`
  → present-but-empty). Two new tests:
  - `TestGallerySavePUTCarriesCompleteCounts` — seeds 2 photos + 1 album + 1
    person, uploads one more photo, asserts `lastCounts == {photos:3, albums:1, people:1}`.
  - `TestGallerySaveOmitsCountsOnCollectionFetchFailure` — seeds an albums
    collection, then deletes that blob from the mock's store before saving
    (simulating an unfetchable/undecryptable collection); asserts the save
    still succeeds AND `lastCounts` is `nil` (no `counts` key sent at all).

The gallery mock **could** express a collection blob for the count test (via
`addBlob` + hand-built root, same technique the files mock already uses in
`seedFiles`) — no `NEEDS_CONTEXT` blocker here.

## TDD evidence (RED → GREEN)

**api package** (RED via compile failure, since the signature literally
didn't have a 5th param yet):
```
internal/api/api_test.go:179:77: too many arguments in call to c.SaveGalleryStore
	have (context.Context, string, number, nil, nil)
	want (context.Context, string, int64, []string)
... (7 call sites total)
FAIL	github.com/MalteKiefer/ledgerline-cli/internal/api [build failed]
```
After implementing the `counts` param + body-key logic: all `internal/api`
tests pass (see full run below).

**gallery package** — verified RED with a temporary stub (kept the plumbing —
`SaveGalleryStore(ctx, sealed, s.version, live, nil)` — but removed the
`buildCounts`/photo-sum logic) to isolate a genuine assertion failure rather
than a build error:
```
=== RUN   TestGallerySavePUTCarriesCompleteCounts
    gallery_test.go:516: expected a complete counts map, got none
--- FAIL: TestGallerySavePUTCarriesCompleteCounts (0.02s)
```
Restored the real implementation → GREEN (see below). (The
`OmitsCountsOnCollectionFetchFailure` test trivially "passed" under the stub
too, since the stub always sends nil — expected, and it still passed under the
real implementation for the right reason: the collection fetch genuinely
fails and `buildCounts` returns nil.)

**files package** — same technique (stubbed the counts-building block back to
`SaveFilesStore(ctx, sealed, s.version, live, nil)`):
```
=== RUN   TestSavePUTCarriesCompleteCounts
    files_test.go:857: expected a complete counts map, got none
--- FAIL: TestSavePUTCarriesCompleteCounts (0.02s)
```
Restored the real implementation → GREEN.

## Verification (GREEN, final state)

```
go test ./internal/api/ ./internal/gallery/ ./internal/files/ -v
```
→ all tests pass, including the 4 new api-level counts tests, the 2 new
gallery counts tests, and the 1 new files counts test, plus every
pre-existing test in all three packages (shard integrity guards, degraded
read-only, sync round-trips, etc.).

```
go build ./...        → clean
go vet ./...           → clean
go test ./...          → all packages ok (cmd, api, audit, blobcache,
                          canonicaljson, certpin, config, conformance, crypto,
                          files, gallery, ml, session, settings, shard, todo,
                          vault, version)
go test -race ./internal/api/ ./internal/gallery/ ./internal/files/
                        → clean (no data races)
gofmt -l <touched files> → no output (already formatted)
```

## CLAUDE.md updates (same commit, per the manual's own rule)

- §2 Consumed API surface: noted the gallery/files store PUT bodies now also
  carry the optional `counts` map, complete-or-omitted only.
- §13 Conflicts & decisions: added a dated entry explaining the
  complete-or-nil safety rule and why gallery's albums/people need the
  fetch+decrypt-then-abort-to-nil path while files' map is always complete.
- §15 Changelog: added a `2026-08-01 feat(store): ...` entry summarizing the
  change and confirming no ciphertext/shard/hash impact.

## Concerns / notes for the next agent

- None functional. The one thing worth flagging: `collectionCount`'s memo
  cache (`Store.collCountCache`) is process-lifetime only (not persisted),
  which is fine — it only exists to avoid re-fetching the same immutable
  collection blob across multiple `Save()` calls within one CLI invocation
  (e.g. retry-after-conflict, or a long-running batch upload with several
  save barriers).
- No `--jobs`/parallelism interaction: `buildCounts`/`collectionCount` run at
  the same save barrier as the rest of `saveOnce` (single-threaded, no
  upload workers in flight), consistent with the existing concurrency model
  documented in `gallery/store.go`'s `Store.mu` comment.
