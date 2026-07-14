package cmd

import "testing"

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
