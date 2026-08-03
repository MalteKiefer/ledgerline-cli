package gallery

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

// immichSearchPageSize is the page size requested from search/metadata. Immich
// caps a page at ~1000 and this stays under that ceiling while keeping the number
// of enumeration round-trips low for a large library.
const immichSearchPageSize = 1000

// ImportOptions tunes an Immich import run. Jobs bounds the per-batch worker pool
// (mirrors gallery upload's --jobs); Batch is how many assets are downloaded and
// sealed before a checkpoint save; DryRun enumerates and counts without any
// download or write.
type ImportOptions struct {
	Jobs   int
	Batch  int
	DryRun bool
	// Total is the library asset count for the progress display (0 = unknown, so
	// progress shows a running count with no percentage). The caller supplies it
	// (via ImmichClient.LibraryCount) so it and any progress bar agree on one
	// number and the count is fetched once.
	Total int
}

// ImportStats is the tally returned by a run. Imported = newly sealed records;
// Skipped = assets already in the ledger (skipped before download); Duplicate =
// content-dedup hits found after download; Failed = per-asset errors that did not
// abort the run.
type ImportStats struct {
	Imported  int
	Skipped   int
	Duplicate int
	Failed    int
}

// ImportProgress is a snapshot of an import's progress, reported after each
// asset is accounted for so a caller can drive a live progress bar. Total is 0
// when the library count is unknown.
type ImportProgress struct {
	Done, Total int
	Current     string
	Stats       ImportStats
}

// RunImmichImport imports an entire Immich library into the gallery. It enumerates
// via search/metadata in descending taken-time windows (§6), drops asset ids
// already in the ledger BEFORE downloading, and processes the rest in batches of
// opts.Batch with a bounded pool of opts.Jobs workers. Each asset's original (and
// any Live Photo motion clip) is downloaded to a per-batch temp dir, its Immich
// exif mapped into an ImportedMeta, and fed through the existing Uploader; after a
// batch the store is checkpointed, the batch's imported ids are recorded in the
// ledger, and the temp dir is removed. A per-asset failure is counted and the run
// continues; a Ledgerline auth-fatal (api 401) or an enumeration error (which is
// where a revoked Immich key surfaces) aborts. Ctx cancellation is honoured
// between batches, leaving a consistent ledger + saved manifest for a resumed
// re-run. The Immich API key is never logged.
func RunImmichImport(ctx context.Context, up *Uploader, store *Store, client *ImmichClient, ledger *ImportLedger, opts ImportOptions, log func(string), progress func(ImportProgress)) (ImportStats, error) {
	if opts.Jobs < 1 {
		opts.Jobs = 1
	}
	if opts.Batch < 1 {
		opts.Batch = 1
	}

	r := &importRun{up: up, store: store, client: client, ledger: ledger, opts: opts, log: log, progress: progress, total: opts.Total}

	// motionIDs collects the livePhotoVideoId of every still seen, so a paired
	// motion asset is never enumerated as a standalone item (belt-and-suspenders on
	// top of the timeline visibility filter, which already hides motion halves).
	motionIDs := make(map[string]bool)

	// seen dedups asset ids across taken-time windows: each new window re-observes
	// the assets sitting exactly at its cursor (takenBefore is inclusive), and this
	// set drops them so nothing is counted or imported twice.
	seen := make(map[string]struct{})

	var batch []ImmichAsset

	// Enumerate in descending taken-time WINDOWS (§6): each fetch carries a
	// takenBefore cursor set to the oldest taken-time seen so far, so the page
	// offset resets per window instead of growing toward the deep-offset slowdown a
	// straight page=1..N sweep hits at ~100k (and a mutating library shifts fewer
	// rows under a cursor than under a raw offset). The cursor moves the window
	// strictly older each step; page only advances by offset to walk a same-taken-
	// time cluster larger than one page, so every asset stays reachable.
	var takenBefore *time.Time
	page := 1
enumerate:
	for {
		if ctx.Err() != nil {
			break
		}
		assets, _, _, err := client.SearchPage(ctx, page, immichSearchPageSize,
			SearchOptions{WithPeople: false, TakenBefore: takenBefore})
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			// A revoked/invalid Immich key (401/403) surfaces here — abort.
			return r.stats, fmt.Errorf("enumerate Immich library: %w", err)
		}

		// Window bookkeeping over the RAW page (before dedup): the oldest taken-time
		// is the next cursor, and a full page means more may lie deeper at this offset.
		var windowOldest *time.Time
		for _, a := range assets {
			if a.LivePhotoVideoID != "" {
				motionIDs[a.LivePhotoVideoID] = true
			}
			if t := immichSortTime(a); !t.IsZero() {
				if windowOldest == nil || t.Before(*windowOldest) {
					tc := t
					windowOldest = &tc
				}
			}
		}
		for _, a := range assets {
			if ctx.Err() != nil {
				break // stop filling batches promptly on cancellation (Ctrl-C)
			}
			if _, dup := seen[a.ID]; dup {
				continue // already handled in a newer window (inclusive-boundary refetch)
			}
			seen[a.ID] = struct{}{}
			if motionIDs[a.ID] {
				continue // a Live Photo motion half — imported with its still, not alone
			}
			if ledger.Has(a.ID) {
				r.stats.Skipped++
				r.reportProgress(a.OriginalFileName)
				continue
			}
			if opts.DryRun {
				r.stats.Imported++ // would-import; no download in a dry run
				r.reportProgress(a.OriginalFileName)
				continue
			}
			batch = append(batch, a)
			if len(batch) >= opts.Batch {
				if err := r.processBatch(ctx, batch); err != nil {
					return r.stats, err
				}
				batch = nil
			}
		}

		full := len(assets) == immichSearchPageSize
		canAdvance := windowOldest != nil && (takenBefore == nil || windowOldest.Before(*takenBefore))
		switch {
		case canAdvance:
			// Move the window to the next-older taken-time; offset resets to page 1.
			takenBefore = windowOldest
			page = 1
		case full:
			// A full page whose cursor can't advance is a same-taken-time cluster
			// larger than one page — dig deeper by offset so no member is skipped.
			page++
		default:
			// A short page with no older taken-time to move to: the sweep is complete.
			break enumerate
		}
	}

	// Final partial batch (skipped on cancellation so a Ctrl-C stops cleanly).
	if !opts.DryRun && len(batch) > 0 && ctx.Err() == nil {
		if err := r.processBatch(ctx, batch); err != nil {
			return r.stats, err
		}
	}

	return r.stats, ctx.Err()
}

// importRun holds the shared state for one import: the pipeline handles, the
// ledger, and the run tally guarded by mu (workers mutate it concurrently).
type importRun struct {
	up     *Uploader
	store  *Store
	client *ImmichClient
	ledger *ImportLedger
	opts   ImportOptions
	log    func(string)

	total    int                  // whole-library asset count (0 = unknown)
	progress func(ImportProgress) // optional live progress callback (may be nil)

	mu       sync.Mutex
	stats    ImportStats
	pending  []ledgerMark // successes to record in the ledger AFTER the checkpoint save
	fatalErr error        // a Ledgerline auth-fatal that must abort the run
}

// processed is how many assets the run has accounted for (imported, duplicate,
// already-in-ledger, or failed) — the numerator for progress against total.
func (r *importRun) processed() int {
	return r.stats.Imported + r.stats.Duplicate + r.stats.Skipped + r.stats.Failed
}

// reportProgress fires the progress callback with the current tallies. It holds
// mu across the callback so concurrent workers serialize (the callback drives a
// non-thread-safe progress bar).
func (r *importRun) reportProgress(current string) {
	if r.progress == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress(ImportProgress{Done: r.processed(), Total: r.total, Current: current, Stats: r.stats})
}

// progressSuffix renders " — N/M (P%)" against the known library total, or "" when
// the total is unknown (e.g. a dry run that never captured it).
func (r *importRun) progressSuffix() string {
	if r.total <= 0 {
		return ""
	}
	done := r.processed()
	pct := done * 100 / r.total
	return fmt.Sprintf(" — %d/%d (%d%%)", done, r.total, pct)
}

// ledgerMark is one imported asset to append to the ledger once the batch that
// produced it has been durably saved.
type ledgerMark struct {
	assetID, checksum, recordID string
}

// processBatch downloads and seals one batch with a bounded worker pool, then
// checkpoints the store and records the batch's imported ids in the ledger. The
// ledger is marked only AFTER a successful save so a crash between them re-imports
// the batch rather than silently dropping it (design §4.3). The per-batch temp dir
// is always removed on the way out.
func (r *importRun) processBatch(ctx context.Context, batch []ImmichAsset) error {
	tmpDir, err := os.MkdirTemp("", "ledgerline-immich-")
	if err != nil {
		return fmt.Errorf("create temp batch dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	r.mu.Lock()
	r.pending = r.pending[:0]
	r.mu.Unlock()

	var wg sync.WaitGroup
	sem := make(chan struct{}, r.opts.Jobs)
	for i, a := range batch {
		if ctx.Err() != nil {
			break
		}
		// A latched auth-fatal means every further upload would 401 too; stop
		// launching workers and let the batch barrier below carry the abort.
		r.mu.Lock()
		fatal := r.fatalErr != nil
		r.mu.Unlock()
		if fatal {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, asset ImmichAsset) {
			defer wg.Done()
			defer func() { <-sem }()
			r.one(ctx, tmpDir, idx, asset)
		}(i, a)
	}
	wg.Wait()

	// Checkpoint: persist the sealed manifest even if ctx was cancelled mid-batch,
	// so completed work survives a Ctrl-C.
	if err := r.store.Save(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("save gallery: %w", err)
	}

	// The save succeeded — now durably record the batch's imported ids.
	r.mu.Lock()
	pending := append([]ledgerMark(nil), r.pending...)
	r.pending = r.pending[:0]
	fatal := r.fatalErr
	r.mu.Unlock()
	for _, m := range pending {
		if err := r.ledger.Mark(m.assetID, m.checksum, m.recordID); err != nil {
			return fmt.Errorf("record import in ledger: %w", err)
		}
	}
	r.logf("checkpoint: %d imported, %d duplicate, %d skipped, %d failed%s",
		r.stats.Imported, r.stats.Duplicate, r.stats.Skipped, r.stats.Failed, r.progressSuffix())

	if fatal != nil {
		return fatal
	}
	return nil
}

// one downloads and seals a single asset. Download/seal failures are counted and
// the worker returns (the run continues); a Ledgerline auth-fatal is latched to
// abort the run; a cancellation is treated as an interruption, not a failure.
func (r *importRun) one(ctx context.Context, tmpDir string, idx int, a ImmichAsset) {
	if ctx.Err() != nil {
		return
	}

	// Each asset gets its own subdir so the original keeps its real filename (which
	// becomes the record's Name) without colliding with a same-named sibling.
	dir := filepath.Join(tmpDir, strconv.Itoa(idx))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.fail(a, "prepare temp dir", err)
		return
	}
	stillPath := filepath.Join(dir, importFileName(a.OriginalFileName))

	if err := r.client.DownloadOriginal(ctx, a.ID, stillPath); err != nil {
		if ctx.Err() != nil {
			return // interrupted, not a failure
		}
		r.fail(a, "download", err)
		return
	}
	plain, err := readFileCapped(stillPath)
	if err != nil {
		r.fail(a, "read original", err)
		return
	}

	// Live Photo motion clip: an explicit pairing from Immich. If the motion half
	// can't be fetched, fail the WHOLE asset rather than importing a bare still: the
	// asset is then not ledgered, so a re-run re-fetches the pair. Importing the
	// still alone would ledger it (and register its signature), and the re-run's
	// re-uploaded still would content-dedup to Duplicate — dropping the motion
	// permanently. A Live Photo is therefore imported atomically or not at all.
	var motionPath string
	if a.LivePhotoVideoID != "" {
		mp := filepath.Join(dir, "motion.mov")
		if err := r.client.DownloadOriginal(ctx, a.LivePhotoVideoID, mp); err != nil {
			if ctx.Err() != nil {
				return // interrupted, not a failure
			}
			r.fail(a, "motion download", err)
			return
		}
		motionPath = mp
	}

	// Immich's own preview JPEG: the thumbnail/medium + local-ML input, so the
	// import never routes plaintext through the Ledgerline /process transform
	// (which cannot decode HEIC/RAW/video and some servers reject). Best-effort —
	// a preview failure just leaves the record's thumb/ML pending for a GUI
	// client to backfill, and the asset still imports with its injected metadata.
	renditionPath := filepath.Join(dir, "preview.jpg")
	if err := r.client.DownloadPreview(ctx, a.ID, renditionPath); err != nil {
		if ctx.Err() != nil {
			return // interrupted, not a failure
		}
		renditionPath = ""
	}

	sidecar := a.LocalDateTime
	if sidecar.IsZero() {
		sidecar = a.FileCreatedAt
	}
	item := Item{
		StillPath:     stillPath,
		MotionPath:    motionPath,
		SidecarTaken:  sidecar,
		Imported:      mapImportedMeta(a),
		RenditionPath: renditionPath,
	}

	outcome, rec, uerr := r.up.Upload(ctx, item, plain)
	switch {
	case uerr != nil && ctx.Err() != nil:
		return // interrupted on the way out
	case uerr != nil && api.Status(uerr) == http.StatusUnauthorized:
		// The Ledgerline token is dead — latch a fatal error so the run aborts
		// after this batch's barrier (a re-run resumes from the ledger).
		r.mu.Lock()
		r.stats.Failed++
		if r.fatalErr == nil {
			r.fatalErr = fmt.Errorf("Ledgerline authentication failed: %w", uerr)
		}
		r.mu.Unlock()
	case uerr != nil:
		r.fail(a, "import", uerr)
	case outcome == Duplicate:
		r.mu.Lock()
		r.stats.Duplicate++
		r.pending = append(r.pending, ledgerMark{assetID: a.ID, checksum: a.Checksum})
		r.mu.Unlock()
		r.logf("%s: duplicate, skipped", a.OriginalFileName)
	default:
		recordID := ""
		if rec != nil {
			recordID = rec.ID
		}
		r.mu.Lock()
		r.stats.Imported++
		r.pending = append(r.pending, ledgerMark{assetID: a.ID, checksum: a.Checksum, recordID: recordID})
		r.mu.Unlock()
		r.logf("%s: imported", a.OriginalFileName)
	}
	r.reportProgress(a.OriginalFileName)
}

// fail counts a per-asset failure and logs it (without the API key, which never
// reaches an error here in any case).
func (r *importRun) fail(a ImmichAsset, stage string, err error) {
	r.mu.Lock()
	r.stats.Failed++
	r.mu.Unlock()
	r.logf("%s: %s failed: %v", a.OriginalFileName, stage, err)
}

// logf forwards a formatted line to the caller's logger when one is set,
// serialising the call so lines from concurrent workers do not interleave and the
// caller's logger need not be concurrency-safe.
func (r *importRun) logf(format string, args ...any) {
	if r.log == nil {
		return
	}
	line := fmt.Sprintf(format, args...)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log(line)
}

// importFileName sanitises an Immich-supplied filename down to a base name (a
// hostile server could send a path), falling back to a generic name when empty so
// the temp file is always creatable.
func importFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "asset"
	}
	return name
}

// immichSortTime is the taken-time Immich orders and windows the library by — the
// asset's localDateTime, falling back to its file-created time. It drives the
// descending taken-time cursor (which Immich's takenBefore filters on the same
// field); a zero value means the asset carries no datable time.
func immichSortTime(a ImmichAsset) time.Time {
	if !a.LocalDateTime.IsZero() {
		return a.LocalDateTime
	}
	return a.FileCreatedAt
}

// mapImportedMeta maps an Immich asset's exif (plus its asset-level duration and
// favorite flag) into the authoritative ImportedMeta the pipeline folds into the
// cold meta blob. It is nil-safe on a missing exifInfo and always returns a
// non-nil meta so a no-exif asset still records its favorite/duration.
func mapImportedMeta(a ImmichAsset) *ImportedMeta {
	im := &ImportedMeta{
		Favorite:    a.IsFavorite,
		DurationSec: parseImmichDuration(string(a.Duration)),
	}
	if e := a.Exif; e != nil {
		if e.DateTimeOriginal != nil {
			im.TakenAt = e.DateTimeOriginal.UTC()
		}
		im.Lat = e.Latitude
		im.Lon = e.Longitude
		im.CameraMake = e.Make
		im.CameraModel = e.Model
		im.Width = e.ExifImageWidth
		im.Height = e.ExifImageHeight
	}
	return im
}

// parseImmichDuration parses an Immich duration ("HH:MM:SS.ffffff") to seconds,
// returning nil for an empty, unparseable, or zero-length duration (a still's
// duration is "0:00:00.00000").
func parseImmichDuration(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// A bare number (no colons) is a duration already expressed in seconds.
	if !strings.Contains(s, ":") {
		total, err := strconv.ParseFloat(s, 64)
		if err != nil || total <= 0 {
			return nil
		}
		return &total
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return nil
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	sec, err3 := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return nil
	}
	total := float64(h)*3600 + float64(m)*60 + sec
	if total <= 0 {
		return nil
	}
	return &total
}
