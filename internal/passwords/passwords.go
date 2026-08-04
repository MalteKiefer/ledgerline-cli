package passwords

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// SecretView is a typed read view of a password/secret record. The shape mirrors
// the web client (resources/js/components/passwords.js) byte-for-byte:
//
//	{id,type,title,favorite,folder,tags,custom,icon,fields,created,updated,versions}
//
// type is the secret kind (login|password|card|wifi|…). fields, custom and
// versions are structured JSON kept verbatim (the CLI/GUI reads and rewrites them
// but the engine does not need to model their internals).
type SecretView struct {
	ID       string
	Type     string
	Title    string
	Favorite bool
	Folder   *string
	Tags     []string
	Icon     string
	Created  string
	Updated  string
	Fields   json.RawMessage
	Custom   json.RawMessage
	Versions json.RawMessage
}

// FolderView is a typed read view of a secretFolders folder record.
type FolderView struct {
	ID   string
	Name string
	Role string
}

// SecretViews returns the parsed secrets.
func (s *Store) SecretViews() []SecretView {
	recs := s.Secrets()
	out := make([]SecretView, 0, len(recs))
	for _, raw := range recs {
		if v, err := parseSecret(raw); err == nil && v.ID != "" {
			out = append(out, v)
		}
	}
	return out
}

// FolderViews returns the parsed folders.
func (s *Store) FolderViews() []FolderView {
	recs := s.Folders()
	out := make([]FolderView, 0, len(recs))
	for _, raw := range recs {
		if v, err := parseFolder(raw); err == nil && v.ID != "" {
			out = append(out, v)
		}
	}
	return out
}

// NewSecret describes the fields for a new secret. Fields/Custom may be nil (they
// default to an empty object / empty array).
type NewSecret struct {
	Type     string
	Title    string
	Favorite bool
	Folder   *string
	Tags     []string
	Icon     string
	Fields   json.RawMessage
	Custom   json.RawMessage
}

// Add stages a new secret and returns its id.
func (s *Store) Add(n NewSecret) (string, error) {
	raw, id, err := newSecretRecord(n)
	if err != nil {
		return "", err
	}
	s.AddSecret(raw)
	return id, nil
}

// Update stages a field patch on a secret and refreshes "updated" (matching the
// web, which stamps updated on every edit).
func (s *Store) Update(id string, patch map[string]any) {
	if _, ok := patch["updated"]; !ok {
		patch["updated"] = nowISO()
	}
	s.UpdateSecret(id, patch)
}

// Delete stages permanent removal of a secret.
func (s *Store) Delete(id string) { s.DeleteSecret(id) }

// Trash / Restore toggle the soft-delete flag (the web uses trashed:true|null).
func (s *Store) Trash(id string)   { s.Update(id, map[string]any{"trashed": true}) }
func (s *Store) Restore(id string) { s.Update(id, map[string]any{"trashed": nil}) }

// AddFolder stages a new folder (role defaults to "manage") and returns its id.
func (s *Store) AddFolder(name, role string) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	if role == "" {
		role = "manage"
	}
	raw, err := json.Marshal(map[string]any{"id": id, "name": name, "role": role})
	if err != nil {
		return "", err
	}
	s.addFolderRaw(raw)
	return id, nil
}

// RenameFolder stages a folder rename.
func (s *Store) RenameFolder(id, name string) { s.UpdateFolder(id, map[string]any{"name": name}) }

// DeleteFolderAndDetach removes a folder and detaches its secrets (folder → nil).
func (s *Store) DeleteFolderAndDetach(id string) {
	for _, sec := range s.SecretViews() {
		if sec.Folder != nil && *sec.Folder == id {
			s.Update(sec.ID, map[string]any{"folder": nil})
		}
	}
	s.DeleteFolder(id)
}

// ResolveFolder returns a folder's name, or "" when the id is empty/unknown.
func (s *Store) ResolveFolder(id string) string {
	if id == "" {
		return ""
	}
	for _, f := range s.FolderViews() {
		if f.ID == id {
			return f.Name
		}
	}
	return ""
}

func parseSecret(raw json.RawMessage) (SecretView, error) {
	var r struct {
		ID       string          `json:"id"`
		Type     string          `json:"type"`
		Title    string          `json:"title"`
		Favorite bool            `json:"favorite"`
		Folder   *string         `json:"folder"`
		Tags     []string        `json:"tags"`
		Icon     string          `json:"icon"`
		Created  string          `json:"created"`
		Updated  string          `json:"updated"`
		Fields   json.RawMessage `json:"fields"`
		Custom   json.RawMessage `json:"custom"`
		Versions json.RawMessage `json:"versions"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return SecretView{}, err
	}
	return SecretView(r), nil
}

func parseFolder(raw json.RawMessage) (FolderView, error) {
	var r struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return FolderView{}, err
	}
	return FolderView(r), nil
}

// newSecretRecord builds a fresh secret record matching the web's field set.
func newSecretRecord(n NewSecret) (json.RawMessage, string, error) {
	id, err := newID()
	if err != nil {
		return nil, "", err
	}
	if n.Tags == nil {
		n.Tags = []string{}
	}
	now := nowISO()
	rec := map[string]any{
		"id":       id,
		"type":     n.Type,
		"title":    n.Title,
		"favorite": n.Favorite,
		"folder":   n.Folder,
		"tags":     n.Tags,
		"custom":   rawOr(n.Custom, "[]"),
		"icon":     n.Icon,
		"fields":   rawOr(n.Fields, "{}"),
		"created":  now,
		"updated":  now,
		"versions": json.RawMessage("[]"),
		"trashed":  nil,
	}
	raw, err := json.Marshal(rec)
	return raw, id, err
}

// rawOr returns rm when non-empty, else the given JSON literal default.
func rawOr(rm json.RawMessage, def string) json.RawMessage {
	if len(rm) == 0 {
		return json.RawMessage(def)
	}
	return rm
}

// newID mirrors the web store's newId(): 16 random bytes as lowercase hex.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }
