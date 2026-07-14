package gallery

import (
	"context"
	"os"
	"os/exec"
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

func TestRunExiftoolRejectsBadContentID(t *testing.T) {
	err := RunExiftool(context.Background(), "/tmp/does-not-matter.jpg", ExifEdits{ContentID: "not-a-uuid"})
	if err == nil {
		t.Fatal("RunExiftool must reject a malformed ContentID before spawning exiftool")
	}
}

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
