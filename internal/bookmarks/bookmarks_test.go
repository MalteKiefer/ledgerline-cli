package bookmarks

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// TestNewBookmarkRecordShape locks the exact JSON key set the web contract
// expects (resources/js/components/bookmarks.js) so a CLI-written record parses
// identically on the web/iOS clients.
func TestNewBookmarkRecordShape(t *testing.T) {
	folder := "fld1"
	raw, id, err := newBookmarkRecord(NewBookmark{
		URL: "https://x", Title: "X", Description: "d", Tags: []string{"a"},
		FolderID: &folder, Favorite: true, ReadLater: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || len(id) != 32 {
		t.Fatalf("id = %q, want 32 hex chars", id)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"description", "favorite", "folderId", "id", "read", "readLater", "tags", "title", "trashed", "url"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record keys = %v, want %v", got, want)
	}
	if m["read"] != false || m["trashed"] != false {
		t.Fatalf("new record must have read=false trashed=false, got read=%v trashed=%v", m["read"], m["trashed"])
	}
}

// TestNewBookmarkTagsDefault ensures a nil Tags serializes as [] (not null), as
// the web client always carries an array.
func TestNewBookmarkTagsDefault(t *testing.T) {
	raw, _, err := newBookmarkRecord(NewBookmark{URL: "https://x"})
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Tags     []string `json:"tags"`
		FolderID *string  `json:"folderId"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Tags == nil {
		t.Fatal("tags must serialize as [], got null")
	}
	if m.FolderID != nil {
		t.Fatalf("folderId must be null when unset, got %q", *m.FolderID)
	}
}

// TestParseBookmarkRoundTrip confirms parse reads every modelled field back.
func TestParseBookmarkRoundTrip(t *testing.T) {
	folder := "fld1"
	raw, id, err := newBookmarkRecord(NewBookmark{
		URL: "https://x", Title: "T", Description: "D", Tags: []string{"a", "b"},
		FolderID: &folder, Favorite: true, ReadLater: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := parseBookmark(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != id || v.URL != "https://x" || v.Title != "T" || v.Description != "D" ||
		len(v.Tags) != 2 || v.FolderID == nil || *v.FolderID != "fld1" ||
		!v.Favorite || !v.ReadLater || v.Read || v.Trashed {
		t.Fatalf("round-trip mismatch: %+v", v)
	}
}

// TestParseFolderRoundTrip covers the nestable folder shape.
func TestParseFolderRoundTrip(t *testing.T) {
	parent := "p1"
	raw, _ := json.Marshal(map[string]any{
		"id": "f1", "name": "N", "parentId": parent, "color": "#fff", "icon": "folder",
	})
	v, err := parseFolder(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "f1" || v.Name != "N" || v.ParentID == nil || *v.ParentID != "p1" || v.Color != "#fff" || v.Icon != "folder" {
		t.Fatalf("folder round-trip mismatch: %+v", v)
	}
}
