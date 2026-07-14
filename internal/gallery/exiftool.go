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
