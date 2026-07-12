package todo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
)

const maxSaveRetries = 5

// Store is a view of the Todos portion of the shared workspace manifest. It
// keeps the full manifest so other modules are preserved verbatim, and records
// edits as logical operations for conflict-safe saves.
type Store struct {
	client *api.Client
	vk     []byte

	version   int64
	manifest  map[string]json.RawMessage
	baseTodos []json.RawMessage
	baseLists []json.RawMessage
	ops       []op
}

type op struct {
	coll  collection
	kind  opKind
	id    string
	raw   json.RawMessage
	patch map[string]any
}

type collection int

const (
	collTodos collection = iota
	collLists
)

type opKind int

const (
	opAdd opKind = iota
	opUpdate
	opDelete
)

// NewStore builds a todo store.
func NewStore(client *api.Client, vaultKey []byte) *Store {
	return &Store{client: client, vk: vaultKey}
}

// Load fetches and decrypts the workspace manifest and extracts todos + lists.
func (s *Store) Load(ctx context.Context) error {
	sealed, err := s.client.Store(ctx)
	if err != nil {
		return err
	}
	s.version = sealed.Version
	s.ops = nil
	s.manifest = map[string]json.RawMessage{}
	if sealed.Ciphertext != "" {
		raw, derr := crypto.OpenManifest(sealed.Ciphertext, s.vk)
		if derr != nil {
			return errors.New("decrypt workspace manifest failed (wrong passphrase?)")
		}
		if err := json.Unmarshal(trimJSON(raw), &s.manifest); err != nil {
			return err
		}
	}
	s.baseTodos = decodeArray(s.manifest["todos"])
	s.baseLists = decodeArray(s.manifest["todoLists"])
	return nil
}

// Todos returns the current todo records (base + pending ops).
func (s *Store) Todos() []json.RawMessage { return applyOps(s.baseTodos, s.ops, collTodos) }

// Lists returns the current list records (base + pending ops).
func (s *Store) Lists() []json.RawMessage { return applyOps(s.baseLists, s.ops, collLists) }

// TodoViews returns the parsed todos.
func (s *Store) TodoViews() []TodoView {
	out := make([]TodoView, 0, len(s.baseTodos))
	for _, raw := range s.Todos() {
		if v, err := parseTodo(raw); err == nil && v.ID != "" {
			out = append(out, v)
		}
	}
	return out
}

// ListViews returns the parsed lists.
func (s *Store) ListViews() []ListView {
	out := make([]ListView, 0, len(s.baseLists))
	for _, raw := range s.Lists() {
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
	s.ops = append(s.ops, op{coll: collTodos, kind: opAdd, id: id, raw: raw})
	return id, nil
}

// Update stages a field patch on a todo.
func (s *Store) Update(id string, patch map[string]any) {
	s.ops = append(s.ops, op{coll: collTodos, kind: opUpdate, id: id, patch: patch})
}

// Delete stages permanent removal of a todo.
func (s *Store) Delete(id string) {
	s.ops = append(s.ops, op{coll: collTodos, kind: opDelete, id: id})
}

// AddList stages a new list and returns its id.
func (s *Store) AddList(name string) (string, error) {
	raw, id, err := newListRecord(name)
	if err != nil {
		return "", err
	}
	s.ops = append(s.ops, op{coll: collLists, kind: opAdd, id: id, raw: raw})
	return id, nil
}

// RenameList stages a list rename.
func (s *Store) RenameList(id, name string) {
	s.ops = append(s.ops, op{coll: collLists, kind: opUpdate, id: id, patch: map[string]any{"name": name}})
}

// DeleteList stages a list removal and detaches its todos to no list.
func (s *Store) DeleteList(id string) {
	for _, v := range s.TodoViews() {
		if v.ListID != nil && *v.ListID == id {
			s.Update(v.ID, map[string]any{"listId": nil})
		}
	}
	s.ops = append(s.ops, op{coll: collLists, kind: opDelete, id: id})
}

// Dirty reports whether there are unsaved changes.
func (s *Store) Dirty() bool { return len(s.ops) > 0 }

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

// Save seals the manifest with the todo changes applied and PUTs it, retrying on
// a version conflict by reloading and re-applying the staged operations.
func (s *Store) Save(ctx context.Context) error {
	if len(s.ops) == 0 {
		return nil
	}
	for attempt := 0; attempt < maxSaveRetries; attempt++ {
		if err := s.saveOnce(ctx); err != nil {
			if errors.Is(err, api.ErrVersionConflict) {
				ops := s.ops
				if rerr := s.Load(ctx); rerr != nil {
					return rerr
				}
				s.ops = ops
				continue
			}
			return err
		}
		s.ops = nil
		return nil
	}
	return errors.New("todo: manifest kept conflicting; try again")
}

func (s *Store) saveOnce(ctx context.Context) error {
	todos := applyOps(s.baseTodos, s.ops, collTodos)
	lists := applyOps(s.baseLists, s.ops, collLists)

	todosRaw, err := json.Marshal(todos)
	if err != nil {
		return err
	}
	listsRaw, err := json.Marshal(lists)
	if err != nil {
		return err
	}
	s.manifest["todos"] = todosRaw
	s.manifest["todoLists"] = listsRaw
	if _, ok := s.manifest["v"]; !ok {
		s.manifest["v"] = json.RawMessage("1")
	}

	manifestJSON, err := json.Marshal(s.manifest)
	if err != nil {
		return err
	}
	sealed, err := crypto.SealManifest(manifestJSON, s.vk)
	if err != nil {
		return err
	}
	newVersion, err := s.client.SaveStore(ctx, sealed, s.version)
	if err != nil {
		return err
	}
	s.version = newVersion
	s.baseTodos = todos
	s.baseLists = lists
	return nil
}

// applyOps returns base with the ops for one collection applied.
func applyOps(base []json.RawMessage, ops []op, coll collection) []json.RawMessage {
	deleted := map[string]bool{}
	patches := map[string]map[string]any{}
	var adds []json.RawMessage
	for _, o := range ops {
		if o.coll != coll {
			continue
		}
		switch o.kind {
		case opAdd:
			adds = append(adds, o.raw)
		case opDelete:
			deleted[o.id] = true
		case opUpdate:
			if patches[o.id] == nil {
				patches[o.id] = map[string]any{}
			}
			for k, v := range o.patch {
				patches[o.id][k] = v
			}
		}
	}
	// New todos go to the front (the web unshifts); lists append. Patches and
	// deletes apply across BOTH base and same-session adds.
	var combined []json.RawMessage
	if coll == collTodos {
		combined = append(combined, adds...)
		combined = append(combined, base...)
	} else {
		combined = append(combined, base...)
		combined = append(combined, adds...)
	}

	out := make([]json.RawMessage, 0, len(combined))
	for _, raw := range combined {
		id := recordID(raw)
		if deleted[id] {
			continue
		}
		if p := patches[id]; p != nil {
			if patched, err := patchRecord(raw, p); err == nil {
				raw = patched
			}
		}
		out = append(out, raw)
	}
	return out
}

func decodeArray(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	return arr
}

func trimJSON(b []byte) []byte {
	i := len(b)
	for i > 0 {
		switch b[i-1] {
		case ' ', '\n', '\t', '\r', 0:
			i--
			continue
		}
		break
	}
	return b[:i]
}
