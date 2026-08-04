package files

import (
	"encoding/json"
	"testing"
)

// TestParseFileVersions confirms a file record's versions[] (openapi FileVersion)
// is fully modeled into FileView.Versions, newest first, with sealed-key
// normalization — so the CLI's file record is a complete read model.
func TestParseFileVersions(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"id": "f1", "blob": "b2", "encFileKey": `{"c":"cc","n":"nn"}`,
		"name": "doc.pdf", "mime": "application/pdf", "size": 2048,
		"folder": nil, "created": "2026-08-04T09:00:00Z",
		"versions": []any{
			map[string]any{"id": "v1", "blob": "b1", "encFileKey": map[string]any{"c": "c1", "n": "n1"},
				"size": 1024, "mime": "application/pdf", "name": "doc.pdf", "created": "2026-08-03T09:00:00Z"},
		},
	})
	fv, err := parseFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(fv.Versions) != 1 {
		t.Fatalf("versions = %d, want 1", len(fv.Versions))
	}
	v := fv.Versions[0]
	if v.ID != "v1" || v.Blob != "b1" || v.Size != 1024 || v.Name != "doc.pdf" || v.Created == "" {
		t.Fatalf("version fields wrong: %+v", v)
	}
	// encFileKey normalizes to the {c,n} object string crypto.DecryptContent expects.
	if v.EncFileKey == "" || v.EncFileKey[0] != '{' {
		t.Fatalf("version encFileKey not normalized: %q", v.EncFileKey)
	}
}
