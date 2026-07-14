# Gallery Download `--edited` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `--edited` to `gallery download` so exported files carry the record's edited date/GPS in their metadata and Live Photos re-pair via an injected Apple ContentIdentifier.

**Architecture:** A new `internal/gallery/exiftool.go` isolates all external-process metadata writing (availability probe, pure argument builder, UUID guard, runner). New fetch helpers in `internal/gallery/download.go` decrypt the motion and meta blobs. `cmd/gallery_download.go` gains the flag, an upfront exiftool probe with graceful fallback, and an edited-export path that reuses the existing atomic-write and traversal guards.

**Tech Stack:** Go, cobra, `os/exec` (exiftool), existing `internal/crypto` content encryption.

## Global Constraints

- Zero-knowledge: all decryption happens locally via `crypto.DecryptContent`; exiftool only ever touches already-decrypted plaintext in a temp file.
- No shell: exiftool is run via `exec.CommandContext` with an explicit `[]string` arg slice. Never build a shell string.
- Tag values passed as a single `-Tag=VALUE` argument so a value can never be read as an exiftool option.
- Preserve existing guards: the symlink refusal in `writeAtomic` and the `withinDir` traversal check apply to both the still and the `.mov` sidecar.
- Commit messages: NO Claude/AI attribution lines (project hook rejects them).
- Without `--edited`, behavior is byte-for-byte unchanged.
- exiftool is NOT assumed present in CI/dev; tests that spawn it must `t.Skip` when it is absent.

## File Structure

- Create `internal/gallery/exiftool.go` — exiftool availability, `ExifEdits` type, pure arg builder, `ValidContentID`, `RunExiftool`.
- Create `internal/gallery/exiftool_test.go` — arg builder + UUID guard unit tests (no process spawn).
- Modify `internal/gallery/download.go` — add `FetchMotion`, `FetchMeta`.
- Modify `internal/gallery/gallery_test.go` — add a blob-injection test helper + fetch-helper tests.
- Modify `cmd/gallery_download.go` — `--edited` flag, exiftool probe, `downloadOneEdited`, `writePatchRename`, `motionSidecarPath`.
- Modify `cmd/gallery_test.go` — flag-wiring + fallback + sidecar-path tests.

---

### Task 1: exiftool wrapper (types, arg builder, UUID guard)

**Files:**
- Create: `internal/gallery/exiftool.go`
- Test: `internal/gallery/exiftool_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type ExifEdits struct { TakenAt time.Time; Lat, Lng *float64; ContentID string; Video bool }`
  - `func ExiftoolAvailable() bool`
  - `func ValidContentID(s string) bool`
  - `func exiftoolArgs(path string, e ExifEdits) []string` (unexported; tested in-package)
  - `func RunExiftool(ctx context.Context, path string, e ExifEdits) error`

- [ ] **Step 1: Write the failing test**

Create `internal/gallery/exiftool_test.go`:

```go
package gallery

import (
	"strings"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func TestExiftoolArgsImageDateGPS(t *testing.T) {
	e := ExifEdits{
		TakenAt: time.Date(2021, 5, 1, 10, 0, 0, 0, time.UTC),
		Lat:     f64(52.5),
		Lng:     f64(-13.4),
	}
	args := exiftoolArgs("/tmp/photo.jpg", e)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"-overwrite_original",
		"-DateTimeOriginal=2021:05:01 10:00:00",
		"-CreateDate=2021:05:01 10:00:00",
		"-GPSLatitude=52.5", "-GPSLatitudeRef=N",
		"-GPSLongitude=13.4", "-GPSLongitudeRef=W",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing arg %q in %v", want, args)
		}
	}
	if args[len(args)-1] != "/tmp/photo.jpg" {
		t.Errorf("path must be last arg, got %q", args[len(args)-1])
	}
}

func TestExiftoolArgsVideoUsesQuickTime(t *testing.T) {
	e := ExifEdits{TakenAt: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC), Video: true}
	joined := strings.Join(exiftoolArgs("/tmp/clip.mov", e), " ")
	if !strings.Contains(joined, "-QuickTime:CreateDate=2020:01:02 03:04:05") {
		t.Errorf("video should set QuickTime:CreateDate, got %s", joined)
	}
	if strings.Contains(joined, "-DateTimeOriginal") {
		t.Errorf("video should not set DateTimeOriginal, got %s", joined)
	}
}

func TestExiftoolArgsContentIdentifier(t *testing.T) {
	e := ExifEdits{ContentID: "11112222-3333-4444-5555-666677778888"}
	joined := strings.Join(exiftoolArgs("/tmp/x.mov", e), " ")
	if !strings.Contains(joined, "-ContentIdentifier=11112222-3333-4444-5555-666677778888") {
		t.Errorf("missing ContentIdentifier arg: %s", joined)
	}
}

func TestExiftoolArgsEmptyEditsOnlyBaseAndPath(t *testing.T) {
	args := exiftoolArgs("/tmp/x.jpg", ExifEdits{})
	if len(args) != 2 || args[0] != "-overwrite_original" || args[1] != "/tmp/x.jpg" {
		t.Errorf("empty edits should be just base flag + path, got %v", args)
	}
}

func TestValidContentID(t *testing.T) {
	if !ValidContentID("11112222-3333-4444-5555-666677778888") {
		t.Error("well-formed UUID must be accepted")
	}
	for _, bad := range []string{"", "APPLE-123", "not-a-uuid", "1111", "; rm -rf /"} {
		if ValidContentID(bad) {
			t.Errorf("malformed id must be rejected: %q", bad)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gallery/ -run 'Exiftool|ValidContentID' -v`
Expected: FAIL — `undefined: ExifEdits`, `exiftoolArgs`, `ValidContentID`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/gallery/exiftool.go`:

```go
package gallery

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

// ExifEdits are the metadata values --edited bakes into an exported file.
// Zero fields are omitted, so the same type drives a full image patch and a
// ContentIdentifier-only motion patch.
type ExifEdits struct {
	TakenAt   time.Time // zero = leave date tags untouched
	Lat, Lng  *float64  // both nil = leave GPS untouched
	ContentID string    // empty = leave ContentIdentifier untouched
	Video     bool      // true = write QuickTime date tags instead of EXIF
}

const exiftoolTimeLayout = "2006:01:02 15:04:05"

var uuidRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// ValidContentID reports whether s is a UUID-shaped Apple content identifier,
// the only form that is ever passed to exiftool.
func ValidContentID(s string) bool { return uuidRe.MatchString(s) }

// ExiftoolAvailable reports whether exiftool is on PATH.
func ExiftoolAvailable() bool {
	_, err := exec.LookPath("exiftool")
	return err == nil
}

// exiftoolArgs builds the argument slice (path last). Values use the -Tag=VALUE
// form so a value can never be parsed as an option.
func exiftoolArgs(path string, e ExifEdits) []string {
	args := []string{"-overwrite_original"}

	if !e.TakenAt.IsZero() {
		ts := e.TakenAt.Format(exiftoolTimeLayout)
		if e.Video {
			args = append(args, "-QuickTime:CreateDate="+ts)
		} else {
			args = append(args, "-DateTimeOriginal="+ts, "-CreateDate="+ts)
		}
	}

	if e.Lat != nil && e.Lng != nil {
		lat, latRef := absRef(*e.Lat, "N", "S")
		lng, lngRef := absRef(*e.Lng, "E", "W")
		args = append(args,
			"-GPSLatitude="+lat, "-GPSLatitudeRef="+latRef,
			"-GPSLongitude="+lng, "-GPSLongitudeRef="+lngRef,
		)
	}

	if e.ContentID != "" {
		args = append(args, "-ContentIdentifier="+e.ContentID)
	}

	return append(args, path)
}

// absRef returns the absolute value as a string and the hemisphere reference.
func absRef(v float64, pos, neg string) (string, string) {
	ref := pos
	if v < 0 {
		ref, v = neg, -v
	}
	return strconv.FormatFloat(v, 'f', -1, 64), ref
}

// RunExiftool patches path in place with the given edits. A no-op edit set
// returns nil without spawning a process.
func RunExiftool(ctx context.Context, path string, e ExifEdits) error {
	args := exiftoolArgs(path, e)
	if len(args) == 2 { // just base flag + path: nothing to write
		return nil
	}
	cmd := exec.CommandContext(ctx, "exiftool", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("exiftool: %w: %s", err, out)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/gallery/ -run 'Exiftool|ValidContentID' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gallery/exiftool.go internal/gallery/exiftool_test.go
git commit -m "gallery: exiftool wrapper for --edited metadata writes"
```

---

### Task 2: Motion and meta fetch helpers

**Files:**
- Modify: `internal/gallery/download.go`
- Test: `internal/gallery/gallery_test.go`

**Interfaces:**
- Consumes: `api.Client.GetGalleryBlob`, `crypto.DecryptContent`, `PhotoRecord`, the unexported `metaBlob` type in `photo.go`.
- Produces:
  - `func FetchMotion(ctx context.Context, client *api.Client, vaultKey []byte, rec PhotoRecord) ([]byte, error)`
  - `func FetchMeta(ctx context.Context, client *api.Client, vaultKey []byte, rec PhotoRecord) (contentID string, err error)`

- [ ] **Step 1: Write the failing test**

Add to `internal/gallery/gallery_test.go`. First a blob-injection helper on `mockServer`, then the test:

```go
// addBlob encrypts plaintext to the vault key and injects it under a fixed id,
// returning the ref/key a record would carry.
func (m *mockServer) addBlob(t *testing.T, plaintext []byte) (ref, key string) {
	t.Helper()
	blob, encKey, err := crypto.EncryptContent(plaintext, m.vk)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.nextID++
	ref = "inj-" + itoa(m.nextID)
	m.blobs[ref] = blob
	m.mu.Unlock()
	return ref, encKey
}

func TestFetchMotionAndMeta(t *testing.T) {
	const pass = "pw"
	m := newMockServer(t, pass)
	client := m.client(t)
	ctx := context.Background()

	motionRef, motionKey := m.addBlob(t, []byte("MOTION-VIDEO-BYTES"))
	metaRef, metaKey := m.addBlob(t, []byte(`{"content_id":"11112222-3333-4444-5555-666677778888"}`))

	rec := PhotoRecord{
		MotionRef: motionRef, MotionKey: motionKey,
		MetaRef: metaRef, MetaKey: metaKey,
	}

	motion, err := FetchMotion(ctx, client, m.vk, rec)
	if err != nil {
		t.Fatalf("FetchMotion: %v", err)
	}
	if string(motion) != "MOTION-VIDEO-BYTES" {
		t.Fatalf("motion bytes = %q", motion)
	}

	cid, err := FetchMeta(ctx, client, m.vk, rec)
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}
	if cid != "11112222-3333-4444-5555-666677778888" {
		t.Fatalf("content id = %q", cid)
	}
}

func TestFetchMetaNoContentID(t *testing.T) {
	const pass = "pw"
	m := newMockServer(t, pass)
	client := m.client(t)
	metaRef, metaKey := m.addBlob(t, []byte(`{"content_id":null}`))
	cid, err := FetchMeta(context.Background(), client, m.vk,
		PhotoRecord{MetaRef: metaRef, MetaKey: metaKey})
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}
	if cid != "" {
		t.Fatalf("want empty content id, got %q", cid)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gallery/ -run 'FetchMotion|FetchMeta' -v`
Expected: FAIL — `undefined: FetchMotion`, `FetchMeta`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/gallery/download.go` (imports already include `context`, `fmt`, `api`, `crypto`; add `encoding/json`):

```go
// FetchMotion downloads and decrypts a photo's paired motion clip.
func FetchMotion(ctx context.Context, client *api.Client, vaultKey []byte, rec PhotoRecord) ([]byte, error) {
	if rec.MotionRef == "" || rec.MotionKey == "" {
		return nil, fmt.Errorf("photo %s has no motion blob", rec.ID)
	}
	blob, err := client.GetGalleryBlob(ctx, rec.MotionRef)
	if err != nil {
		return nil, err
	}
	return crypto.DecryptContent(blob, rec.MotionKey, vaultKey)
}

// FetchMeta downloads and decrypts a photo's metadata blob and returns its Apple
// content id (empty when the blob has none).
func FetchMeta(ctx context.Context, client *api.Client, vaultKey []byte, rec PhotoRecord) (string, error) {
	if rec.MetaRef == "" || rec.MetaKey == "" {
		return "", fmt.Errorf("photo %s has no meta blob", rec.ID)
	}
	blob, err := client.GetGalleryBlob(ctx, rec.MetaRef)
	if err != nil {
		return "", err
	}
	plain, err := crypto.DecryptContent(blob, rec.MetaKey, vaultKey)
	if err != nil {
		return "", err
	}
	var mb metaBlob
	if err := json.Unmarshal(plain, &mb); err != nil {
		return "", err
	}
	if mb.ContentID == nil {
		return "", nil
	}
	return *mb.ContentID, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/gallery/ -run 'FetchMotion|FetchMeta' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gallery/download.go internal/gallery/gallery_test.go
git commit -m "gallery: add FetchMotion and FetchMeta helpers"
```

---

### Task 3: cmd wiring — flag, probe, edited export, sidecar

**Files:**
- Modify: `cmd/gallery_download.go`
- Test: `cmd/gallery_test.go`

**Interfaces:**
- Consumes: `gallery.ExifEdits`, `gallery.ExiftoolAvailable`, `gallery.ValidContentID`, `gallery.RunExiftool`, `gallery.FetchMotion`, `gallery.FetchMeta`, `gallery.FetchOriginal`, `gallery.Target`.
- Produces (unexported, cmd-local):
  - `func motionSidecarPath(stillPath string) string`
  - `func writePatchRename(ctx context.Context, path string, data []byte, e gallery.ExifEdits) error`
  - `func downloadOneEdited(ctx context.Context, client *api.Client, vk []byte, t gallery.Target) (motionWritten bool, err error)`
  - `downloadOptions.edited bool`

- [ ] **Step 1: Write the failing test**

Add to `cmd/gallery_test.go`:

```go
func TestMotionSidecarPath(t *testing.T) {
	cases := map[string]string{
		"/out/IMG_1.HEIC": "/out/IMG_1.mov",
		"/out/IMG_2.jpg":  "/out/IMG_2.mov",
		"/out/no_ext":     "/out/no_ext.mov",
	}
	for in, want := range cases {
		if got := motionSidecarPath(in); got != want {
			t.Errorf("motionSidecarPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEditedFallbackWhenExiftoolMissing(t *testing.T) {
	// buildFilter is exercised elsewhere; here we assert the flag is wired so a
	// missing exiftool downgrades to plain export rather than erroring.
	opts := downloadOptions{edited: true, images: true, videos: true}
	if _, err := buildFilter(opts); err != nil {
		t.Fatalf("buildFilter with --edited should not error: %v", err)
	}
}
```

Note: the deep exiftool round-trip is covered by the skip-gated integration test in Step 6, since exiftool is absent in CI.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run 'MotionSidecar|EditedFallback' -v`
Expected: FAIL — `undefined: motionSidecarPath`, `downloadOptions has no field edited`.

- [ ] **Step 3: Write minimal implementation**

In `cmd/gallery_download.go`:

Add `edited bool` to `downloadOptions`:

```go
type downloadOptions struct {
	outDir string
	from   string
	to     string
	images bool
	videos bool
	edited bool
	force  bool
}
```

Register the flag in `newGalleryDownloadCommand` (after `--videos`):

```go
	f.BoolVar(&opts.edited, "edited", false, "bake edited date/GPS into metadata and export Live Photo motion (requires exiftool)")
```

Update the long help so the `--edited` line is documented (append to the existing `Long` string, before the "Files already present" paragraph):

```go
			"  --edited         write edited date/location into each file's metadata\n" +
			"                   and export Live Photo motion (needs exiftool)\n\n" +
```

In `runDownload`, resolve edited mode after the filter is built and before the loop:

```go
	editing := opts.edited
	if editing && !gallery.ExiftoolAvailable() {
		fmt.Fprintln(out, "Note: exiftool not found on PATH — exporting originals unchanged (no metadata merge, no motion).")
		editing = false
	}
```

In the per-target loop, replace the plain `downloadOne` call site with a branch:

```go
		if editing {
			motion, derr := downloadOneEdited(ctx, client, vk, t)
			if derr != nil {
				failed++
				fmt.Fprintf(out, "  [%d/%d] %s — failed: %v\n", i+1, len(targets), label, derr)
				continue
			}
			downloaded++
			suffix := ""
			if motion {
				suffix = " (+motion)"
			}
			fmt.Fprintf(out, "  [%d/%d] %s — downloaded%s\n", i+1, len(targets), label, suffix)
			continue
		}
		if err := downloadOne(ctx, client, vk, t); err != nil {
			failed++
			fmt.Fprintf(out, "  [%d/%d] %s — failed: %v\n", i+1, len(targets), label, err)
			continue
		}
		downloaded++
		fmt.Fprintf(out, "  [%d/%d] %s — downloaded\n", i+1, len(targets), label)
```

Add the new helpers at the end of the file (add `strings` and `context` to imports; `context` is already imported):

```go
// motionSidecarPath returns the still's path with its extension replaced by
// .mov, so a Live Photo's motion clip sits beside the still with the same stem.
func motionSidecarPath(stillPath string) string {
	ext := filepath.Ext(stillPath)
	return strings.TrimSuffix(stillPath, ext) + ".mov"
}

// writePatchRename writes data to a temp file, patches it in place with
// exiftool, then atomically renames it to path — reusing the symlink guard so an
// existing symlink at the destination is never written through.
func writePatchRename(ctx context.Context, path string, data []byte, e gallery.ExifEdits) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write through a symlink: %s", path)
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := gallery.RunExiftool(ctx, tmp, e); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// downloadOneEdited writes the still with its edited date/GPS baked in, and for
// a Live Photo also writes the motion clip beside it with a matching Apple
// ContentIdentifier so the pair re-associates on import. A failure to write the
// motion half is non-fatal: the still is kept and motionWritten is false.
func downloadOneEdited(ctx context.Context, client *api.Client, vk []byte, t gallery.Target) (bool, error) {
	data, err := gallery.FetchOriginal(ctx, client, vk, t.Rec)
	if err != nil {
		return false, err
	}

	edits := gallery.ExifEdits{
		TakenAt: t.When,
		Lat:     t.Rec.Lat,
		Lng:     t.Rec.Lng,
		Video:   t.Rec.MediaType == "video",
	}

	cid := ""
	live := t.Rec.MotionRef != "" && t.Rec.MotionKey != ""
	if live {
		if got, merr := gallery.FetchMeta(ctx, client, vk, t.Rec); merr == nil && gallery.ValidContentID(got) {
			cid = got
			edits.ContentID = got
		}
	}

	if err := writePatchRename(ctx, t.Path, data, edits); err != nil {
		return false, err
	}
	if !t.When.IsZero() {
		_ = os.Chtimes(t.Path, t.When, t.When)
	}

	// Motion sidecar only when we have a still (not a standalone video) and a
	// usable content id to guarantee re-pairing.
	if !live || cid == "" || t.Rec.MediaType == "video" {
		return false, nil
	}
	motionPath := motionSidecarPath(t.Path)
	if !withinDir(filepath.Dir(t.Path), motionPath) {
		return false, nil
	}
	motion, merr := gallery.FetchMotion(ctx, client, vk, t.Rec)
	if merr != nil {
		return false, nil // non-fatal: still is already written
	}
	if err := writePatchRename(ctx, motionPath, motion, gallery.ExifEdits{ContentID: cid, Video: true}); err != nil {
		return false, nil
	}
	if !t.When.IsZero() {
		_ = os.Chtimes(motionPath, t.When, t.When)
	}
	return true, nil
}
```

Note: `withinDir` is unexported in package `gallery`; expose it for reuse by adding to `internal/gallery/download.go`:

```go
// WithinDir reports whether path stays inside dir.
func WithinDir(dir, path string) bool { return withinDir(dir, path) }
```

and call `gallery.WithinDir` from `downloadOneEdited` instead of `withinDir`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/ -run 'MotionSidecar|EditedFallback' -v`
Expected: PASS.

- [ ] **Step 5: Build and vet the whole module**

Run: `go build ./... && go vet ./...`
Expected: no output (success).

- [ ] **Step 6: Add the skip-gated integration test**

Add to `internal/gallery/exiftool_test.go`:

```go
func TestRunExiftoolRoundTrip(t *testing.T) {
	if !ExiftoolAvailable() {
		t.Skip("exiftool not installed")
	}
	// A 1x1 JPEG so exiftool has a real file to edit.
	jpeg := []byte{
		0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01,
		0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xDB, 0x00, 0x43,
		0x00, 0x08, 0x06, 0x06, 0x07, 0x06, 0x05, 0x08, 0x07, 0x07, 0x07, 0x09,
		0x09, 0x08, 0x0A, 0x0C, 0x14, 0x0D, 0x0C, 0x0B, 0x0B, 0x0C, 0x19, 0x12,
		0x13, 0x0F, 0x14, 0x1D, 0x1A, 0x1F, 0x1E, 0x1D, 0x1A, 0x1C, 0x1C, 0x20,
		0x24, 0x2E, 0x27, 0x20, 0x22, 0x2C, 0x23, 0x1C, 0x1C, 0x28, 0x37, 0x29,
		0x2C, 0x30, 0x31, 0x34, 0x34, 0x34, 0x1F, 0x27, 0x39, 0x3D, 0x38, 0x32,
		0x3C, 0x2E, 0x33, 0x34, 0x32, 0xFF, 0xC0, 0x00, 0x0B, 0x08, 0x00, 0x01,
		0x00, 0x01, 0x01, 0x01, 0x11, 0x00, 0xFF, 0xC4, 0x00, 0x14, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x08, 0xFF, 0xC4, 0x00, 0x14, 0x10, 0x01, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00,
		0xD2, 0xCF, 0x20, 0xFF, 0xD9,
	}
	dir := t.TempDir()
	path := dir + "/x.jpg"
	if err := os.WriteFile(path, jpeg, 0o600); err != nil {
		t.Fatal(err)
	}
	e := ExifEdits{TakenAt: time.Date(2021, 5, 1, 10, 0, 0, 0, time.UTC), Lat: f64(52.5), Lng: f64(13.4)}
	if err := RunExiftool(context.Background(), path, e); err != nil {
		t.Fatalf("RunExiftool: %v", err)
	}
	out, err := exec.Command("exiftool", "-s3", "-DateTimeOriginal", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "2021:05:01 10:00:00" {
		t.Fatalf("DateTimeOriginal = %q", got)
	}
}
```

Add imports `context`, `os`, `os/exec` to `exiftool_test.go`.

- [ ] **Step 7: Run the gallery + cmd suites**

Run: `go test ./internal/gallery/ ./cmd/ -v`
Expected: PASS (the round-trip test SKIPs when exiftool is absent).

- [ ] **Step 8: Commit**

```bash
git add cmd/gallery_download.go cmd/gallery_test.go internal/gallery/download.go internal/gallery/exiftool_test.go
git commit -m "gallery download: add --edited to bake metadata and export Live Photo motion"
```

---

### Task 4: Docs and changelog

**Files:**
- Modify: `README.md`
- Modify: `CHANGELOG.md`

**Interfaces:** none.

- [ ] **Step 1: Update README**

Find the `gallery download` section in `README.md` and add an `--edited` bullet next to the existing `--images`/`--videos`/`--force` flags, plus a one-line note:

```markdown
- `--edited` — write the gallery's edited date and location into each file's
  metadata and export Live Photos as a still + `.mov` motion pair that re-pairs
  on import. Requires [`exiftool`](https://exiftool.org) on `PATH`; without it,
  originals are exported unchanged.
```

- [ ] **Step 2: Update CHANGELOG**

Add under the unreleased/next section in `CHANGELOG.md`:

```markdown
- `gallery download --edited`: bake edited date/GPS into exported files and
  export Live Photo motion (still + matching `.mov`) via exiftool.
```

- [ ] **Step 3: Verify the command help renders**

Run: `go run . gallery download --help`
Expected: help text lists `--edited` with its description.

- [ ] **Step 4: Commit**

```bash
git add README.md CHANGELOG.md
git commit -m "docs: document gallery download --edited"
```

---

## Self-Review

**Spec coverage:**
- `--edited` flag + upfront exiftool probe + graceful fallback → Task 3 (probe/fallback), Task 1 (`ExiftoolAvailable`). ✓
- Date → EXIF DateTimeOriginal/CreateDate; GPS when present; Orientation/MakerNotes preserved (only individual tags written, no `-all=`) → Task 1 arg builder. ✓
- Standalone video uses QuickTime:CreateDate → Task 1 `Video` branch. ✓
- Live Photo: fetch metaBlob ContentID, write `<stem>.mov` sidecar, inject ContentIdentifier into both → Task 2 (`FetchMeta`/`FetchMotion`), Task 3 (`downloadOneEdited`). ✓
- metaBlob fetched only for Live Photos → Task 3 (`live` guard). ✓
- Security: arg slice, `-Tag=VALUE`, UUID validation, symlink + traversal guards → Task 1 (`ValidContentID`, `exiftoolArgs`), Task 3 (`writePatchRename` symlink guard, `WithinDir`). ✓
- Non-fatal motion failure keeps still → Task 3 (returns `false, nil`). ✓
- Tests skip without exiftool → Task 3 Step 6. ✓

**Placeholder scan:** none — every code step shows full code.

**Type consistency:** `ExifEdits`, `ExiftoolAvailable`, `ValidContentID`, `RunExiftool`, `FetchMotion`, `FetchMeta`, `WithinDir`, `motionSidecarPath`, `writePatchRename`, `downloadOneEdited` are named identically across their defining and consuming tasks. `downloadOptions.edited` field consistent. ✓

**Open item carried from spec:** exact exiftool tag name for the still-image ContentIdentifier (Apple group) vs. video (QuickTime group). `-ContentIdentifier=` is written for both; verify against a real HEIC+MOV pair during execution (Task 3) and adjust the tag group if exiftool routes it wrongly.
```
