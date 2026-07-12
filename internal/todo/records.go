// Package todo implements the zero-knowledge Todos module client: reading and
// writing todo items and lists inside the shared workspace manifest, preserving
// every other module (notes, bookmarks, files, contacts) verbatim.
package todo

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
)

// Priority values, matching the web client.
const (
	PriorityHigh   = "high"
	PriorityNormal = "normal"
	PriorityLow    = "low"
)

// TodoView is a typed read view of a todo record.
type TodoView struct {
	ID          string
	Title       string
	Description string
	URL         string
	Tags        []string
	Priority    string
	Marked      bool
	Due         string
	Done        bool
	ListID      *string
	Trashed     bool
}

// ListView is a typed read view of a todo list.
type ListView struct {
	ID   string
	Name string
}

// parseTodo reads the modelled fields from a raw todo record.
func parseTodo(raw json.RawMessage) (TodoView, error) {
	var r struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		URL         string   `json:"url"`
		Tags        []string `json:"tags"`
		Priority    string   `json:"priority"`
		Marked      bool     `json:"marked"`
		Due         string   `json:"due"`
		Done        bool     `json:"done"`
		ListID      *string  `json:"listId"`
		Trashed     bool     `json:"trashed"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return TodoView{}, err
	}
	if r.Priority == "" {
		r.Priority = PriorityNormal
	}
	return TodoView(r), nil
}

// parseList reads the modelled fields from a raw list record.
func parseList(raw json.RawMessage) (ListView, error) {
	var r struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return ListView{}, err
	}
	return ListView(r), nil
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
// field. A nil value deletes the key.
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

// NewTodo describes the fields for a new todo item.
type NewTodo struct {
	Title       string
	Description string
	URL         string
	Tags        []string
	Priority    string
	Marked      bool
	Due         string
	ListID      *string
}

// newTodoRecord builds a fresh todo record (matching the web's field set).
func newTodoRecord(n NewTodo) (json.RawMessage, string, error) {
	id, err := newID()
	if err != nil {
		return nil, "", err
	}
	if n.Priority == "" {
		n.Priority = PriorityNormal
	}
	if n.Tags == nil {
		n.Tags = []string{}
	}
	rec := map[string]any{
		"id":          id,
		"title":       n.Title,
		"description": n.Description,
		"url":         n.URL,
		"tags":        n.Tags,
		"priority":    n.Priority,
		"marked":      n.Marked,
		"due":         n.Due,
		"done":        false,
		"listId":      n.ListID,
		"trashed":     false,
	}
	raw, err := json.Marshal(rec)
	return raw, id, err
}

// newListRecord builds a fresh list record.
func newListRecord(name string) (json.RawMessage, string, error) {
	id, err := newID()
	if err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(map[string]any{"id": id, "name": name})
	return raw, id, err
}

// newID mirrors LLStore.newId(): 16 random bytes as lowercase hex.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
