package todo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/manifeststore"
)

// Manifest collection keys owned by the Todos module.
const (
	collTodos = "todos"
	collLists = "todoLists"
)

// Store is a view of the Todos portion of the shared workspace manifest. It is a
// thin domain wrapper over manifeststore, which handles decryption, conflict-safe
// saves and verbatim preservation of keys owned by other modules.
type Store struct {
	ms *manifeststore.Store
}

// NewStore builds a todo store. New todos are unshifted to the front (matching
// the web client); lists append.
func NewStore(client *api.Client, vaultKey []byte) *Store {
	return &Store{ms: manifeststore.New(client, vaultKey, "todo", "todos",
		manifeststore.CollectionSpec{Key: collTodos, PrependAdds: true},
		manifeststore.CollectionSpec{Key: collLists},
	)}
}

// Load fetches and decrypts the workspace manifest and extracts todos + lists.
func (s *Store) Load(ctx context.Context) error { return s.ms.Load(ctx) }

// Todos returns the current todo records (base + pending ops).
func (s *Store) Todos() []json.RawMessage { return s.ms.Records(collTodos) }

// Lists returns the current list records (base + pending ops).
func (s *Store) Lists() []json.RawMessage { return s.ms.Records(collLists) }

// TodoViews returns the parsed todos.
func (s *Store) TodoViews() []TodoView {
	todos := s.Todos()
	out := make([]TodoView, 0, len(todos))
	for _, raw := range todos {
		if v, err := parseTodo(raw); err == nil && v.ID != "" {
			out = append(out, v)
		}
	}
	return out
}

// ListViews returns the parsed lists.
func (s *Store) ListViews() []ListView {
	lists := s.Lists()
	out := make([]ListView, 0, len(lists))
	for _, raw := range lists {
		if v, err := parseList(raw); err == nil && v.ID != "" {
			out = append(out, v)
		}
	}
	return out
}

// Add stages a new todo and returns its id.
func (s *Store) Add(n NewTodo) (string, error) {
	raw, id, err := newTodoRecord(n)
	if err != nil {
		return "", err
	}
	s.ms.Add(collTodos, raw)
	return id, nil
}

// Update stages a field patch on a todo.
func (s *Store) Update(id string, patch map[string]any) { s.ms.Update(collTodos, id, patch) }

// Delete stages permanent removal of a todo.
func (s *Store) Delete(id string) { s.ms.Delete(collTodos, id) }

// AddList stages a new list and returns its id.
func (s *Store) AddList(name string) (string, error) {
	raw, id, err := newListRecord(name)
	if err != nil {
		return "", err
	}
	s.ms.Add(collLists, raw)
	return id, nil
}

// RenameList stages a list rename.
func (s *Store) RenameList(id, name string) {
	s.ms.Update(collLists, id, map[string]any{"name": name})
}

// DeleteList stages a list removal and detaches its todos to no list.
func (s *Store) DeleteList(id string) {
	for _, v := range s.TodoViews() {
		if v.ListID != nil && *v.ListID == id {
			s.Update(v.ID, map[string]any{"listId": nil})
		}
	}
	s.ms.Delete(collLists, id)
}

// Dirty reports whether there are unsaved changes.
func (s *Store) Dirty() bool { return s.ms.Dirty() }

// Save seals the manifest with the todo changes applied and PUTs it, retrying on
// a version conflict.
func (s *Store) Save(ctx context.Context) error { return s.ms.Save(ctx) }

// ResolveTodo resolves a full or unambiguous prefix id (or, if unique, a title
// substring) to a single todo id.
func (s *Store) ResolveTodo(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("no todo given")
	}
	var byPrefix []string
	for _, v := range s.TodoViews() {
		if v.ID == ref {
			return v.ID, nil
		}
		if strings.HasPrefix(v.ID, strings.ToLower(ref)) {
			byPrefix = append(byPrefix, v.ID)
		}
	}
	switch len(byPrefix) {
	case 1:
		return byPrefix[0], nil
	case 0:
		return "", errors.New("no todo matches: " + ref)
	default:
		return "", errors.New("ambiguous id prefix: " + ref)
	}
}

// ResolveListByName resolves a list name (case-insensitive) to its id.
func (s *Store) ResolveListByName(name string) (string, bool) {
	for _, v := range s.ListViews() {
		if strings.EqualFold(v.Name, name) {
			return v.ID, true
		}
	}
	return "", false
}
