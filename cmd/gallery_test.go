package cmd

import "testing"

func TestValidateUploadFlags(t *testing.T) {
	cases := []struct {
		name        string
		folder      string
		zip         string
		googlePhoto bool
		wantErr     bool
	}{
		{"folder only", "/photos", "", false, false},
		{"folder recursive is fine", "/photos", "", false, false},
		{"google photos with zip", "", "/t.zip", true, false},
		{"no source at all", "", "", false, true},
		{"google photos without zip", "", "", true, true},
		{"folder mixed with zip", "/photos", "/t.zip", false, true},
		{"folder mixed with google photos", "/photos", "/t.zip", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUploadFlags(tc.folder, tc.zip, tc.googlePhoto)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateUploadFlags(%q,%q,%v) err=%v, wantErr=%v",
					tc.folder, tc.zip, tc.googlePhoto, err, tc.wantErr)
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
