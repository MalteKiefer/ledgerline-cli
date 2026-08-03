package notes

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// NoteView is a typed read view of a note record. The record shape mirrors the
// web client (resources/js/components/notes.js) byte-for-byte:
//
//	{id, title, content, tags, pinned, trashed, updated}
//
// content is plaintext markdown carried in the record (not a blob).
type NoteView struct {
	ID      string
	Title   string
	Content string
	Tags    []string
	Pinned  bool
	Trashed bool
	Updated string
}

// NoteViews returns the parsed notes (records that fail to parse or lack an id
// are skipped, but their raw bytes are still preserved through a save).
func (s *Store) NoteViews() []NoteView {
	recs := s.Notes()
	out := make([]NoteView, 0, len(recs))
	for _, raw := range recs {
		if v, err := parseNote(raw); err == nil && v.ID != "" {
			out = append(out, v)
		}
	}
	return out
}

// NewNote describes the fields for a new note.
type NewNote struct {
	Title   string
	Content string
	Tags    []string
	Pinned  bool
}

// Add stages a new note and returns its id.
func (s *Store) Add(n NewNote) (string, error) {
	raw, id, err := newNoteRecord(n)
	if err != nil {
		return "", err
	}
	s.AddNote(raw)
	return id, nil
}

// Update stages a field patch on a note and refreshes its "updated" timestamp,
// mirroring the web client (which stamps updated on every edit).
func (s *Store) Update(id string, patch map[string]any) {
	if _, ok := patch["updated"]; !ok {
		patch["updated"] = nowISO()
	}
	s.UpdateNote(id, patch)
}

// Delete stages permanent removal of a note.
func (s *Store) Delete(id string) { s.DeleteNote(id) }

// Trash stages a soft-delete (trashed=true), matching the web trash behaviour.
func (s *Store) Trash(id string) { s.Update(id, map[string]any{"trashed": true}) }

// Restore clears the trashed flag.
func (s *Store) Restore(id string) { s.Update(id, map[string]any{"trashed": false}) }

// parseNote reads the modelled fields from a raw note record.
func parseNote(raw json.RawMessage) (NoteView, error) {
	var r struct {
		ID      string   `json:"id"`
		Title   string   `json:"title"`
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
		Pinned  bool     `json:"pinned"`
		Trashed bool     `json:"trashed"`
		Updated string   `json:"updated"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return NoteView{}, err
	}
	return NoteView(r), nil
}

// newNoteRecord builds a fresh note record matching the web's field set.
func newNoteRecord(n NewNote) (json.RawMessage, string, error) {
	id, err := newID()
	if err != nil {
		return nil, "", err
	}
	if n.Tags == nil {
		n.Tags = []string{}
	}
	rec := map[string]any{
		"id":      id,
		"title":   n.Title,
		"content": n.Content,
		"tags":    n.Tags,
		"pinned":  n.Pinned,
		"trashed": false,
		"updated": nowISO(),
	}
	raw, err := json.Marshal(rec)
	return raw, id, err
}

// newID mirrors the web store's newId(): 16 random bytes as lowercase hex.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// nowISO is the RFC3339 UTC timestamp the web writes into "updated".
func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }
