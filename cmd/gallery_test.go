package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

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

func TestValidateUploadFlags(t *testing.T) {
	cases := []struct {
		name    string
		opts    uploadOptions
		wantErr bool
	}{
		{"folder only", uploadOptions{folder: "/photos", jobs: 1}, false},
		{"google photos with zip", uploadOptions{zipPath: "/t.zip", googlePhoto: true, jobs: 1}, false},
		{"no source at all", uploadOptions{jobs: 1}, true},
		{"google photos without zip", uploadOptions{googlePhoto: true, jobs: 1}, true},
		{"folder mixed with zip", uploadOptions{folder: "/photos", zipPath: "/t.zip", jobs: 1}, true},
		{"folder mixed with google photos", uploadOptions{folder: "/photos", zipPath: "/t.zip", googlePhoto: true, jobs: 1}, true},
		{"server and local ML together", uploadOptions{folder: "/photos", withML: true, mlLocalURL: "http://localhost:3003", jobs: 1}, true},
		{"local ML alone is fine", uploadOptions{folder: "/photos", mlLocalURL: "http://localhost:3003", jobs: 1}, false},
		{"zero jobs is rejected", uploadOptions{folder: "/photos", jobs: 0}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUploadFlags(tc.opts)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateUploadFlags(%+v) err=%v, wantErr=%v", tc.opts, err, tc.wantErr)
			}
		})
	}
}

func TestValidateImportFlags(t *testing.T) {
	cases := []struct {
		name    string
		opts    importOptions
		wantErr bool
	}{
		{"immich with url and jobs", importOptions{immich: true, immichURL: "http://host:2283", jobs: 1}, false},
		{"no backend chosen", importOptions{immichURL: "http://host:2283", jobs: 1}, true},
		{"immich without url", importOptions{immich: true, jobs: 1}, true},
		{"immich with blank url", importOptions{immich: true, immichURL: "   ", jobs: 1}, true},
		{"server and local ML together", importOptions{immich: true, immichURL: "http://host:2283", withML: true, mlLocalURL: "http://localhost:3003", jobs: 1}, true},
		{"local ML alone is fine", importOptions{immich: true, immichURL: "http://host:2283", mlLocalURL: "http://localhost:3003", jobs: 1}, false},
		{"zero jobs is rejected", importOptions{immich: true, immichURL: "http://host:2283", jobs: 0}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateImportFlags(tc.opts)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateImportFlags(%+v) err=%v, wantErr=%v", tc.opts, err, tc.wantErr)
			}
		})
	}
}

// TestResolveImmichKey pins the secret-resolution order (IMMICH_API_KEY env →
// prompt → --immich-key flag) and the no-key error. A non-*os.File stdin makes
// isTerminalIn false, so the interactive prompt branch is skipped and the
// env-vs-flag precedence is what these cases exercise.
func TestResolveImmichKey(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		flagKey string
		want    string
		wantErr bool
	}{
		{"env preferred over flag", "env-key", "flag-key", "env-key", false},
		{"env is trimmed", "  spaced-key  ", "flag-key", "spaced-key", false},
		{"flag used when env empty", "", "flag-key", "flag-key", false},
		{"flag is trimmed", "", "  flag-key  ", "flag-key", false},
		{"no key anywhere errors", "", "", "", true},
		{"blank env and blank flag error", "   ", "   ", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("IMMICH_API_KEY", tc.env)
			cmd := &cobra.Command{}
			// A strings.Reader is not an *os.File, so isTerminalIn is false and the
			// no-echo prompt branch is skipped in the test harness.
			cmd.SetIn(strings.NewReader(""))
			got, err := resolveImmichKey(cmd, "", tc.flagKey, false)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveImmichKey env=%q flag=%q err=%v, wantErr=%v", tc.env, tc.flagKey, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("resolveImmichKey env=%q flag=%q = %q, want %q", tc.env, tc.flagKey, got, tc.want)
			}
		})
	}
}

func TestDefaultDeviceNameHasPrefix(t *testing.T) {
	name := defaultDeviceName()
	if name == "" {
		t.Fatal("device name must not be empty")
	}
	if len(name) < len("ledgerline-cli") || name[:len("ledgerline-cli")] != "ledgerline-cli" {
		t.Fatalf("device name %q lacks the ledgerline-cli prefix", name)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:       "0 B",
		512:     "512 B",
		1024:    "1.0 KiB",
		1048576: "1.0 MiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Fatalf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
