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
