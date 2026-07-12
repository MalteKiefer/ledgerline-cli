// Package files implements the zero-knowledge Files module client: reading and
// writing the file tree inside the shared workspace manifest, uploading and
// downloading encrypted content blobs, and a bidirectional sync engine.
//
// File and folder records are kept as raw JSON and edited by patching individual
// keys, so fields the CLI does not model (favorite, note, tags, future fields)
// are always preserved when a record is rewritten.
package files

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
)

// FileView is a typed read-only view of a file record's fields the client uses.
type FileView struct {
	ID         string
	Blob       string
	EncFileKey string
	Name       string
	Mime       string
	Size       int64
	Folder     *string // parent folder id, nil = root
	Created    string
	Trashed    string
}

// FolderView is a typed read-only view of a folder record.
type FolderView struct {
	ID      string
	Name    string
	Parent  *string
	Trashed string
}

// parseFile reads the modelled fields from a raw file record. It is tolerant of
// field-type variance in the manifest (size as int or float, trashed as string,
// bool or null) so a single odd field never drops a whole record.
func parseFile(raw json.RawMessage) (FileView, error) {
	var r struct {
		ID         string          `json:"id"`
		Blob       string          `json:"blob"`
		EncFileKey json.RawMessage `json:"encFileKey"`
		Name       string          `json:"name"`
		Mime       string          `json:"mime"`
		Size       json.Number     `json:"size"`
		Folder     *string         `json:"folder"`
		Created    string          `json:"created"`
		Trashed    json.RawMessage `json:"trashed"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return FileView{}, err
	}
	size, _ := r.Size.Int64()
	return FileView{
		ID: r.ID, Blob: r.Blob, EncFileKey: normalizeSealed(r.EncFileKey), Name: r.Name, Mime: r.Mime,
		Size: size, Folder: r.Folder, Created: r.Created, Trashed: interpretTrashed(r.Trashed),
	}, nil
}

// normalizeSealed returns the {"c","n"} JSON string for a file's wrapped key,
// accepting either a JSON string (the web stores JSON.stringify({c,n})) or a bare
// object. The result is what crypto.DecryptContent expects.
func normalizeSealed(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if s[0] == '"' {
		var inner string
		if err := json.Unmarshal(raw, &inner); err == nil {
			return inner
		}
	}
	return s
}

// parseFolder reads the modelled fields from a raw folder record (tolerant of a
// bool/null/string trashed field).
func parseFolder(raw json.RawMessage) (FolderView, error) {
	var r struct {
		ID      string          `json:"id"`
		Name    string          `json:"name"`
		Parent  *string         `json:"parent"`
		Trashed json.RawMessage `json:"trashed"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return FolderView{}, err
	}
	return FolderView{ID: r.ID, Name: r.Name, Parent: r.Parent, Trashed: interpretTrashed(r.Trashed)}, nil
}

// interpretTrashed reduces a trashed field (which may be null, false, "" or an
// ISO timestamp string) to a marker string: empty means "not trashed".
func interpretTrashed(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "", "null", "false", `""`, "0":
		return ""
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str // an ISO timestamp (or any non-empty string)
	}
	return "trashed" // truthy non-string (e.g. bool true)
}

// recordID extracts the id from any raw record.
func recordID(raw json.RawMessage) string {
	var r struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &r)
	return r.ID
}

// patchRecord applies key/value updates to a raw record, preserving every other
// field (modelled or not). A nil value deletes the key.
func patchRecord(raw json.RawMessage, patch map[string]any) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, err
		}
	}
	for k, v := range patch {
		if v == nil {
			delete(obj, k)
			continue
		}
		enc, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		obj[k] = enc
	}
	return json.Marshal(obj)
}

// newFileRecord builds a fresh file record and returns it with its id. folder is
// the parent id (nil = root).
func newFileRecord(name, mime string, size int64, blob, encFileKey string, folder *string, createdISO string) (json.RawMessage, string, error) {
	id, err := newUUID()
	if err != nil {
		return nil, "", err
	}
	rec := map[string]any{
		"id":         id,
		"blob":       blob,
		"encFileKey": encFileKey,
		"name":       name,
		"mime":       mime,
		"size":       size,
		"folder":     folder,
		"created":    createdISO,
		"versions":   []any{},
	}
	raw, err := json.Marshal(rec)
	return raw, id, err
}

// newFolderRecord builds a fresh folder record under parent (nil = root).
func newFolderRecord(name string, parent *string) (json.RawMessage, string, error) {
	id, err := newUUID()
	if err != nil {
		return nil, "", err
	}
	rec := map[string]any{"id": id, "name": name, "parent": parent}
	raw, err := json.Marshal(rec)
	return raw, id, err
}

// newUUID returns a random RFC 4122 v4 UUID string (matches crypto.randomUUID()).
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
