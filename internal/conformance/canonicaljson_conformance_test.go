// Package conformance holds the §17 cross-client conformance tests: the CLI's
// write/serialize/crypto paths are validated against the shared fixture set
// (mirrored from the web repo's resources/js/__tests__/fixtures/store-v3). No
// client may write a real library until these pass.
package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/canonicaljson"
)

// fixture reads and returns the raw bytes of a §17 fixture file.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "store-v3", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// TestCanonicalJSONFixture gates internal/canonicaljson against the shared
// canonical-json.json fixture (spec §5.2 / §17).
func TestCanonicalJSONFixture(t *testing.T) {
	var cases []struct {
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
		Expected string          `json:"expected"`
	}
	if err := json.Unmarshal(fixture(t, "canonical-json.json"), &cases); err != nil {
		t.Fatalf("parse canonical-json.json: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("no canonical-json fixture cases")
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := canonicaljson.Canonicalize(tc.Input)
			if err != nil {
				t.Fatalf("Canonicalize: %v", err)
			}
			if string(got) != tc.Expected {
				t.Fatalf("got %q want %q", got, tc.Expected)
			}
		})
	}
}
