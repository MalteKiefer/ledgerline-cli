package settings

import (
	"testing"
	"time"
)

func TestUpsertIsIdempotentByLocal(t *testing.T) {
	var s Settings
	s.Upsert(Mapping{Remote: "Aleph Alpha", Local: "/backup/aa"})
	s.Upsert(Mapping{Remote: "Renamed", Local: "/backup/aa"}) // same local → update
	if len(s.Sync) != 1 {
		t.Fatalf("want 1 mapping, got %d", len(s.Sync))
	}
	if s.Sync[0].Remote != "Renamed" {
		t.Fatalf("want updated remote, got %q", s.Sync[0].Remote)
	}
	s.Upsert(Mapping{Remote: "Other", Local: "/backup/bb"})
	if len(s.Sync) != 2 {
		t.Fatalf("want 2 mappings, got %d", len(s.Sync))
	}
}

func TestParseDurationOrFallsBack(t *testing.T) {
	if got := ParseDurationOr("30s", time.Minute); got != 30*time.Second {
		t.Fatalf("want 30s, got %v", got)
	}
	if got := ParseDurationOr("", time.Minute); got != time.Minute {
		t.Fatalf("want default 1m, got %v", got)
	}
	if got := ParseDurationOr("garbage", time.Minute); got != time.Minute {
		t.Fatalf("want default on bad input, got %v", got)
	}
}

func TestLegacyMappingStillDecodes(t *testing.T) {
	// A pre-existing settings.json with only remote/local must still load.
	const legacy = `{"sync":[{"remote":"Docs","local":"/home/me/docs"}]}`
	var s Settings
	if err := s.unmarshal([]byte(legacy)); err != nil {
		t.Fatalf("legacy decode: %v", err)
	}
	if len(s.Sync) != 1 || s.Sync[0].Local != "/home/me/docs" {
		t.Fatalf("legacy mapping lost: %+v", s.Sync)
	}
}
