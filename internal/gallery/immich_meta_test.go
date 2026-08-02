package gallery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/canonicaljson"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
	"github.com/MalteKiefer/ledgerline-cli/internal/vault"
)

// TestImmichMetaInjectionNoProcess covers the §4.1 metadata-injection seam: an
// Immich exifInfo is mapped into an ImportedMeta and, with /process AND ML off,
// the pipeline still writes a cold meta blob and promotes the injected
// GPS/camera/dims/taken onto the record — while keeping thumbPending/mlPending set
// and never touching the transient-plaintext /process endpoint. It also pins the
// taken_at precedence: the injected TakenAt wins over the SidecarTaken fallback.
func TestImmichMetaInjectionNoProcess(t *testing.T) {
	const pass = "correct horse battery staple"
	m := newMockServer(t, pass)
	client := m.client(t)
	ctx := context.Background()

	vk, err := vault.Unlock(ctx, client, pass)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}

	// --- exifInfo -> ImportedMeta mapping half ---
	taken := time.Date(2020, 6, 15, 9, 30, 0, 0, time.UTC)
	lat, lon := 48.137154, 11.576124
	asset := ImmichAsset{
		ID:               "immich-asset-1",
		OriginalFileName: "IMG_4242.jpg",
		Checksum:         "c2hh",
		Type:             "IMAGE",
		FileCreatedAt:    time.Date(2020, 6, 15, 9, 30, 0, 0, time.UTC),
		LocalDateTime:    time.Date(2020, 6, 15, 11, 30, 0, 0, time.UTC),
		Duration:         "0:00:12.500000",
		IsFavorite:       true,
		Exif: &ImmichExif{
			Make:             "Canon",
			Model:            "EOS 5D",
			DateTimeOriginal: &taken,
			Latitude:         &lat,
			Longitude:        &lon,
			ExifImageWidth:   6000,
			ExifImageHeight:  4000,
		},
	}

	im := mapImportedMeta(asset)
	if im == nil {
		t.Fatal("mapImportedMeta returned nil")
	}
	if !im.TakenAt.Equal(taken) {
		t.Fatalf("mapped TakenAt = %v, want %v", im.TakenAt, taken)
	}
	if im.Lat == nil || im.Lon == nil || *im.Lat != lat || *im.Lon != lon {
		t.Fatalf("mapped GPS = %v/%v, want %v/%v", im.Lat, im.Lon, lat, lon)
	}
	if im.CameraMake != "Canon" || im.CameraModel != "EOS 5D" {
		t.Fatalf("mapped camera = %q/%q", im.CameraMake, im.CameraModel)
	}
	if im.Width != 6000 || im.Height != 4000 {
		t.Fatalf("mapped dims = %dx%d, want 6000x4000", im.Width, im.Height)
	}
	if im.DurationSec == nil || *im.DurationSec != 12.5 {
		t.Fatalf("mapped duration = %v, want 12.5", im.DurationSec)
	}
	if !im.Favorite {
		t.Fatal("mapped favorite = false, want true")
	}

	// --- meta-blob injection half (process off, ML off) ---
	dir := t.TempDir()
	photoPath := filepath.Join(dir, "IMG_4242.jpg")
	original := []byte("IMMICH-ORIGINAL-BYTES-\x00\x01\x02")
	if err := os.WriteFile(photoPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	up := NewUploader(client, store, vk, false, false, nil) // process off, no ML

	// A DIFFERENT sidecar time proves the injected TakenAt takes precedence over
	// the fallback (§4.1: injected > /process EXIF > sidecar/created).
	sidecar := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	item := Item{StillPath: photoPath, SidecarTaken: sidecar, Imported: im}

	outcome, rec, err := up.Upload(ctx, item, original)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if outcome != Uploaded || rec == nil {
		t.Fatalf("outcome = %v, rec = %v", outcome, rec)
	}

	// No /process call: the no-egress import path must not hit the transient-
	// plaintext endpoint.
	m.mu.Lock()
	pc := m.processCalls
	m.mu.Unlock()
	if pc != 0 {
		t.Fatalf("/process was called %d times, want 0 (no plaintext egress)", pc)
	}

	// A cold meta blob was written even though ML/process are off.
	if rec.MetaRef == "" || rec.MetaKey == "" {
		t.Fatalf("expected a meta blob from item.Imported, got metaRef=%q", rec.MetaRef)
	}

	// Thumb + ML stay pending for a GUI client to backfill.
	if !rec.ThumbPending {
		t.Fatal("thumbPending must stay true (no rendition produced)")
	}
	if !rec.MlPending || rec.HasFaces != nil {
		t.Fatalf("ML must stay pending: mlPending=%v hasFaces=%v", rec.MlPending, rec.HasFaces)
	}
	// No renditions were produced (no plaintext transform ran).
	if rec.ThumbRef != "" || rec.MediumRef != "" {
		t.Fatalf("no rendition should exist: thumbRef=%q mediumRef=%q", rec.ThumbRef, rec.MediumRef)
	}

	// taken_at precedence: the injected TakenAt (not the 1999 sidecar) wins.
	wantTaken := taken.UTC().Format(time.RFC3339)
	if rec.TakenAt != wantTaken {
		t.Fatalf("taken_at = %q, want injected %q (not sidecar %q)",
			rec.TakenAt, wantTaken, sidecar.Format(time.RFC3339))
	}

	// Injected GPS/camera/dims promoted onto the hot record.
	wantLat := canonicaljson.FormatDecimal(lat)
	wantLng := canonicaljson.FormatDecimal(lon)
	if rec.Lat == nil || rec.Lng == nil || *rec.Lat != wantLat || *rec.Lng != wantLng {
		t.Fatalf("record GPS = %v/%v, want %q/%q", rec.Lat, rec.Lng, wantLat, wantLng)
	}
	if rec.Camera == nil || *rec.Camera != "Canon EOS 5D" {
		t.Fatalf("record camera = %v, want \"Canon EOS 5D\"", rec.Camera)
	}
	if rec.Width != 6000 || rec.Height != 4000 {
		t.Fatalf("record dims = %dx%d, want 6000x4000", rec.Width, rec.Height)
	}

	// The cold meta blob decrypts and carries the injected exif + favorite + dims.
	blob, err := client.GetGalleryBlob(ctx, rec.MetaRef)
	if err != nil {
		t.Fatalf("fetch meta blob: %v", err)
	}
	metaJSON, err := crypto.DecryptContent(blob, rec.MetaKey, vk)
	if err != nil {
		t.Fatalf("decrypt meta blob: %v", err)
	}
	var meta struct {
		Exif struct {
			TakenAt string   `json:"taken_at"`
			Lat     *float64 `json:"lat"`
			Lon     *float64 `json:"lon"`
			Camera  *string  `json:"camera"`
		} `json:"exif"`
		Width    int      `json:"width"`
		Height   int      `json:"height"`
		Duration *float64 `json:"duration"`
		Favorite *bool    `json:"favorite"`
	}
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		t.Fatalf("unmarshal meta blob: %v", err)
	}
	if meta.Exif.TakenAt != wantTaken {
		t.Fatalf("meta exif taken_at = %q, want %q", meta.Exif.TakenAt, wantTaken)
	}
	if meta.Exif.Lat == nil || *meta.Exif.Lat != lat || meta.Exif.Lon == nil || *meta.Exif.Lon != lon {
		t.Fatalf("meta exif GPS = %v/%v, want %v/%v (floats preserved in cold blob)",
			meta.Exif.Lat, meta.Exif.Lon, lat, lon)
	}
	if meta.Exif.Camera == nil || *meta.Exif.Camera != "Canon EOS 5D" {
		t.Fatalf("meta exif camera = %v, want \"Canon EOS 5D\"", meta.Exif.Camera)
	}
	if meta.Width != 6000 || meta.Height != 4000 {
		t.Fatalf("meta dims = %dx%d, want 6000x4000", meta.Width, meta.Height)
	}
	if meta.Duration == nil || *meta.Duration != 12.5 {
		t.Fatalf("meta duration = %v, want 12.5", meta.Duration)
	}
	if meta.Favorite == nil || !*meta.Favorite {
		t.Fatalf("meta favorite = %v, want true", meta.Favorite)
	}
}

// TestParseImmichDuration covers the HH:MM:SS.ffffff → seconds parse (design §8),
// including every reject branch: empty/blank, wrong part count, a non-numeric
// component, and a still's zero-length "0:00:00.00000" (which maps to nil, not 0),
// plus the hours-accumulation term.
func TestParseImmichDuration(t *testing.T) {
	cases := []struct {
		in   string
		want *float64
	}{
		{"", nil},              // empty
		{"   ", nil},           // blank
		{"1:02", nil},          // too few parts
		{"1:02:03:04", nil},    // too many parts
		{"aa:bb:cc", nil},      // non-numeric components
		{"0:00:00.00000", nil}, // a still: zero duration → nil, not 0
		{"0:00:12.500000", f64(12.5)},
		{"0:01:00", f64(60)},
		{"1:02:03", f64(3723)}, // hours term: 3600 + 120 + 3
	}
	for _, tc := range cases {
		got := parseImmichDuration(tc.in)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("parseImmichDuration(%q) = %v, want nil", tc.in, *got)
		case tc.want != nil && got == nil:
			t.Errorf("parseImmichDuration(%q) = nil, want %v", tc.in, *tc.want)
		case tc.want != nil && got != nil && *got != *tc.want:
			t.Errorf("parseImmichDuration(%q) = %v, want %v", tc.in, *got, *tc.want)
		}
	}
}
