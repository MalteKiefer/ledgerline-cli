# Design: `gallery download --edited`

## Summary

Add an `--edited` flag to `ledgerline-cli gallery download`. When set, exported
files carry the record's current (possibly in-app-edited) metadata baked into
their EXIF/QuickTime tags, and Live Photos are exported as still + motion pairs
that re-pair correctly on import (Apple Photos, etc.).

Without `--edited`, behavior is unchanged: the original decrypted bytes are
written and `taken_at` is applied only as the filesystem mtime.

## Background

Current state (`cmd/gallery_download.go`, `internal/gallery/download.go`):

- `download` fetches `OriginalRef` only and writes the decrypted bytes.
- `taken_at` is applied via `os.Chtimes` (mtime); nothing is written into EXIF.
- `MotionRef`/`MotionKey` exist on the record but are never fetched — Live Photo
  motion is lost on export.

Edit model (confirmed with maintainer):

- **Date** edits live in the top-level record field `TakenAt`.
- **Location** edits live in top-level `Lat`/`Lng`.
- **Rotation** is stored as the EXIF Orientation tag inside the original blob
  (no separate field, no re-encoded variant). Preserving the original bytes'
  orientation tag is sufficient; no pixel rotation is performed.
- **Live Photo pairing** relies on the Apple ContentIdentifier. It may exist in
  the still's Apple MakerNote and the motion MOV's QuickTime metadata, and is
  reliably available in the decrypted `metaBlob.ContentID`.

## Goals

- `--edited` bakes `TakenAt` and `Lat`/`Lng` into image EXIF (and standalone
  video QuickTime tags), preserving Orientation and Apple MakerNotes.
- Live Photos export as `<stem>.<ext>` + `<stem>.mov` with a matching
  ContentIdentifier injected into both, so motion survives re-import.
- Missing `exiftool` degrades gracefully: warn once, fall back to plain export.

## Non-Goals

- No pixel rotation / re-encoding of image data.
- No editing UI or write-back to the server; export only.
- `--edited` does not change the set of items selected (existing
  `--from/--to/--images/--videos` filters are unchanged).

## Approach

`exiftool` is invoked as an external process. It is the only option that
reliably writes HEIC and MOV metadata while preserving Apple MakerNotes and the
ContentIdentifier that makes Live Photo motion re-pair. Pure-Go EXIF libraries
do not support HEIC writing and risk destroying MakerNotes.

### Flag

`cmd/gallery_download.go`:

- Add `--edited` (bool) to `downloadOptions`.
- When set, `runDownload` performs an upfront `exiftool` availability check.
  If absent, print one warning (edits/motion will not be embedded; falling back
  to plain export) and proceed as if `--edited` were off.

### Per-item export (`--edited` on, exiftool present)

For each planned target:

1. **Image (non-video):**
   - Fetch + decrypt original (as today) into a temp file in the target dir.
   - Run exiftool to set:
     - `-DateTimeOriginal`, `-CreateDate` from `TakenAt`
       (format `YYYY:MM:DD HH:MM:SS`), when `TakenAt` is known.
     - `-GPSLatitude`/`-GPSLatitudeRef`/`-GPSLongitude`/`-GPSLongitudeRef` from
       `Lat`/`Lng`, only when both are present. When absent, existing EXIF GPS
       is left untouched.
     - Orientation and MakerNotes are never written (exiftool preserves them
       when editing individual tags).
   - `-overwrite_original` (no `_original` sidecar).
   - Atomic rename temp → final path.
   - Apply `TakenAt` as mtime (as today).

2. **Live Photo** (image record with `MotionRef != ""`):
   - Do image handling above for the still.
   - Fetch + decrypt `metaBlob` via `MetaRef`/`MetaKey`; read `ContentID`.
     (metaBlob is fetched only for Live Photos — date/GPS are top-level.)
   - Fetch + decrypt the motion blob (`MotionRef`/`MotionKey`); write as
     `<stem>.mov` (extension from motion mime, default `.mov`) beside the still.
   - When `ContentID` is present and valid (UUID-shaped), inject
     `ContentIdentifier` into **both** the still and the MOV via exiftool.
   - A failure to fetch/write the motion half is non-fatal: warn, keep the still.

3. **Standalone video** (`media_type == "video"`):
   - Write decrypted bytes to a temp file.
   - exiftool sets `QuickTime:CreateDate` from `TakenAt` and GPS from
     `Lat`/`Lng` when present.
   - Atomic rename; apply mtime.

### exiftool wrapper (`internal/gallery/exiftool.go`)

New file isolating all external-process concerns:

- `exiftoolAvailable() bool` — resolves `exiftool` on PATH once.
- A function that takes a file path plus the tags to set (date, optional GPS,
  optional ContentIdentifier, media kind) and runs exiftool.
- All arguments passed as an `exec.CommandContext` arg slice; values passed as
  `-Tag=VALUE` (single arg, no shell). No user value ever reaches a shell.

### Fetch helpers (`internal/gallery/download.go`)

- `FetchMotion(ctx, client, vk, rec)` — decrypt the motion blob.
- `FetchMeta(ctx, client, vk, rec)` — decrypt metaBlob, return `ContentID`.
- Extend the per-target export path used by `cmd` to branch on `--edited`.

## Data Flow

```
record ─┬─ OriginalRef/Key ─→ decrypt ─→ tmp file ─┐
        │                                           ├─ exiftool(date,gps[,cid]) ─→ rename ─→ <stem>.<ext>
        ├─ (live) MetaRef/Key ─→ decrypt ─→ ContentID┘
        └─ (live) MotionRef/Key ─→ decrypt ─→ tmp ─── exiftool(cid) ─→ rename ─→ <stem>.mov
```

## Error Handling

- Missing exiftool: single upfront warning, plain fallback for the whole run.
- Per-item exiftool failure: item counted as failed (like a failed download),
  processing continues. The plain original is not silently substituted — the
  user sees the failure line.
- Motion-half failure on a Live Photo: non-fatal warning; still is kept and
  counted as downloaded.
- Existing symlink guard (`writeAtomic`) and `withinDir` traversal guard are
  reused for both the still and the `.mov` sidecar path.

## Security

- exiftool is executed via `exec.CommandContext` with an explicit argument
  slice; no shell interpolation.
- Tag values use the `-Tag=VALUE` form so a value can never be parsed as an
  exiftool option.
- `ContentID` from the (attacker-influenceable) metaBlob is validated as a
  UUID before being injected; non-conforming values are skipped, not passed.
- Sidecar `.mov` path is validated with the same `withinDir` check as the still.

## Testing

- exiftool is not assumed present in CI. Tests that exercise real EXIF writing
  are guarded by an availability check and skipped (`t.Skip`) when exiftool is
  absent.
- Unit tests without exiftool:
  - Flag wiring: `--edited` set, exiftool missing → plain fallback path taken,
    warning emitted, files still written.
  - exiftool arg construction: date/GPS/ContentIdentifier produce the expected
    argument slice (pure function, no process spawn).
  - ContentID UUID validation: valid injected, malformed skipped.
  - Live Photo target produces both `<stem>.<ext>` and `<stem>.mov` planned
    paths with matching stems.
- Integration test (skipped without exiftool): export a fixture image, read
  back DateTimeOriginal/GPS and assert values; assert Orientation preserved.

## Open Implementation Detail

The exact exiftool tag name for the still-image ContentIdentifier (Apple
MakerNote group) vs. the video (QuickTime Keys group) must be verified during
implementation. `-ContentIdentifier=` is expected to route correctly per file
type, but this is confirmed against a real HEIC+MOV pair before finalizing.
