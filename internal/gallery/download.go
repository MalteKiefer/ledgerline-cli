package gallery

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
)

// Filter restricts which photos a download covers.
type Filter struct {
	From   time.Time // inclusive lower bound on taken_at (zero = no bound)
	To     time.Time // inclusive upper bound on taken_at (zero = no bound)
	Images bool      // include images
	Videos bool      // include videos
}

// Includes reports whether a record passes the filter (trashed photos and
// photos outside the type/date bounds are excluded).
func (f Filter) Includes(rec PhotoRecord) bool {
	if rec.Trashed != "" {
		return false
	}
	if rec.MediaType == "video" && !f.Videos {
		return false
	}
	if rec.MediaType != "video" && !f.Images {
		return false
	}
	if f.From.IsZero() && f.To.IsZero() {
		return true
	}
	taken := parseTaken(rec.TakenAt)
	if taken.IsZero() {
		// No known date — keep it only when no date bound is set (handled above);
		// with a bound, an undated photo is excluded so the range stays honest.
		return false
	}
	if !f.From.IsZero() && taken.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && taken.After(f.To) {
		return false
	}
	return true
}

// Target is one photo to download and the local path it maps to.
type Target struct {
	Rec  PhotoRecord
	Path string
	When time.Time // taken_at, applied as the file's mod time when known
}

// Plan filters records and resolves collision-free local paths under outDir.
// Records sharing an original filename are disambiguated deterministically with
// a short id suffix, so a re-run maps each photo to the same path.
func Plan(records []PhotoRecord, outDir string, f Filter) []Target {
	nameCount := map[string]int{}
	var kept []PhotoRecord
	for _, rec := range records {
		if f.Includes(rec) {
			kept = append(kept, rec)
			nameCount[strings.ToLower(cleanName(rec))]++
		}
	}

	targets := make([]Target, 0, len(kept))
	for _, rec := range kept {
		name := cleanName(rec)
		if nameCount[strings.ToLower(name)] > 1 {
			name = disambiguate(name, rec.ID)
		}
		targets = append(targets, Target{
			Rec:  rec,
			Path: filepath.Join(outDir, name),
			When: parseTaken(rec.TakenAt),
		})
	}
	return targets
}

// FetchOriginal downloads and decrypts a photo's original bytes.
func FetchOriginal(ctx context.Context, client *api.Client, vaultKey []byte, rec PhotoRecord) ([]byte, error) {
	if rec.OriginalRef == "" || rec.OriginalKey == "" {
		return nil, fmt.Errorf("photo %s has no original blob", rec.ID)
	}
	blob, err := client.GetGalleryBlob(ctx, rec.OriginalRef)
	if err != nil {
		return nil, err
	}
	return crypto.DecryptContent(blob, rec.OriginalKey, vaultKey)
}

// cleanName returns a safe local filename for a record, falling back to the
// photo id plus an extension guessed from its mime when the stored name is
// missing or unsafe.
func cleanName(rec PhotoRecord) string {
	name := filepath.Base(strings.TrimSpace(rec.Name))
	if name == "" || name == "." || name == string(filepath.Separator) || strings.ContainsAny(name, "/\\") {
		return rec.ID + extForMime(rec.Mime)
	}
	return name
}

// disambiguate inserts a short id before the extension: photo.jpg → photo_1a2b3c4d.jpg.
func disambiguate(name, id string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	short := id
	if len(short) > 8 {
		short = short[:8]
	}
	return stem + "_" + short + ext
}

// extForMime returns a file extension for a mime type (used when a record has no
// usable name).
func extForMime(mime string) string {
	switch {
	case strings.Contains(mime, "jpeg"):
		return ".jpg"
	case strings.Contains(mime, "png"):
		return ".png"
	case strings.Contains(mime, "heic"):
		return ".heic"
	case strings.Contains(mime, "quicktime"):
		return ".mov"
	case strings.Contains(mime, "mp4"):
		return ".mp4"
	default:
		return ".bin"
	}
}

// parseTaken parses a record's taken_at, tolerating the common ISO variants the
// server emits (with or without a timezone).
func parseTaken(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
