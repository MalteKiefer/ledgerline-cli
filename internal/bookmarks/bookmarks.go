// Package bookmarks implements the zero-knowledge Bookmarks module client:
// reading and writing bookmark records and their (nestable) folders inside the
// module's sealed manifest row, preserving every other module verbatim. It is a
// thin domain wrapper over manifeststore, structurally identical to internal/todo.
//
// Record shapes mirror the web client (resources/js/components/bookmarks.js)
// byte-for-byte: a bookmark is
//
//	{id,url,title,description,tags,folderId,favorite,readLater,read,trashed}
//
// and a folder (collection key "bookmarkFolders") is
//
//	{id,name,parentId,color,icon}
//
// New bookmarks are prepended (the web unshifts); folders append.
package bookmarks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/manifeststore"
)

// Manifest collection keys owned by the Bookmarks module.
const (
	collBookmarks = "bookmarks"
	collFolders   = "bookmarkFolders"
)

// BookmarkView is a typed read view of a bookmark record.
type BookmarkView struct {
	ID          string
	URL         string
	Title       string
	Description string
	Tags        []string
	FolderID    *string
	Favorite    bool
	ReadLater   bool
	Read        bool
	Trashed     bool
}

// FolderView is a typed read view of a bookmark folder (folders nest via ParentID).
type FolderView struct {
	ID       string
	Name     string
	ParentID *string
	Color    string
	Icon     string
}

// Store is a view of the Bookmarks module manifest. manifeststore handles
// decryption, conflict-safe (409 rebase) saves, and verbatim preservation of
// keys owned by other modules and of record fields this parser does not model.
type Store struct {
	ms *manifeststore.Store
}

// NewStore builds a bookmarks store. New bookmarks are prepended (matching the
// web client); folders append.
func NewStore(client *api.Client, vaultKey []byte) *Store {
	return &Store{ms: manifeststore.New(client, vaultKey, "bookmark", "bookmarks",
		manifeststore.CollectionSpec{Key: collBookmarks, PrependAdds: true},
		manifeststore.CollectionSpec{Key: collFolders},
	)}
}

// Load fetches and decrypts the sealed bookmarks row.
func (s *Store) Load(ctx context.Context) error { return s.ms.Load(ctx) }

// Dirty reports whether there are unsaved changes.
func (s *Store) Dirty() bool { return s.ms.Dirty() }

// Save writes the sealed row (conflict-safe).
func (s *Store) Save(ctx context.Context) error { return s.ms.Save(ctx) }

// Bookmarks / Folders return the current raw records (base + pending ops).
func (s *Store) Bookmarks() []json.RawMessage { return s.ms.Records(collBookmarks) }
func (s *Store) Folders() []json.RawMessage    { return s.ms.Records(collFolders) }

// BookmarkViews returns the parsed bookmarks.
func (s *Store) BookmarkViews() []BookmarkView {
	recs := s.Bookmarks()
	out := make([]BookmarkView, 0, len(recs))
	for _, raw := range recs {
		if v, err := parseBookmark(raw); err == nil && v.ID != "" {
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

// NewBookmark describes the fields for a new bookmark.
type NewBookmark struct {
	URL         string
	Title       string
	Description string
	Tags        []string
	FolderID    *string
	Favorite    bool
	ReadLater   bool
}

// Add stages a new bookmark and returns its id.
func (s *Store) Add(n NewBookmark) (string, error) {
	raw, id, err := newBookmarkRecord(n)
	if err != nil {
		return "", err
	}
	s.ms.Add(collBookmarks, raw)
	return id, nil
}

// Update stages a field patch on a bookmark (other fields preserved).
func (s *Store) Update(id string, patch map[string]any) { s.ms.Update(collBookmarks, id, patch) }

// Delete stages permanent removal of a bookmark.
func (s *Store) Delete(id string) { s.ms.Delete(collBookmarks, id) }

// AddFolder stages a new folder (parentID nil = top level) and returns its id.
func (s *Store) AddFolder(name string, parentID *string, color, icon string) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(map[string]any{
		"id": id, "name": name, "parentId": parentID, "color": color, "icon": icon,
	})
	if err != nil {
		return "", err
	}
	s.ms.Add(collFolders, raw)
	return id, nil
}

// RenameFolder stages a folder rename.
func (s *Store) RenameFolder(id, name string) {
	s.ms.Update(collFolders, id, map[string]any{"name": name})
}

// DeleteFolder stages a folder removal, detaching its bookmarks (folderId → nil)
// and reparenting its child folders to top level, mirroring the web behaviour of
// never orphaning a record under a missing folder.
func (s *Store) DeleteFolder(id string) {
	for _, b := range s.BookmarkViews() {
		if b.FolderID != nil && *b.FolderID == id {
			s.Update(b.ID, map[string]any{"folderId": nil})
		}
	}
	for _, f := range s.FolderViews() {
		if f.ParentID != nil && *f.ParentID == id {
			s.ms.Update(collFolders, f.ID, map[string]any{"parentId": nil})
		}
	}
	s.ms.Delete(collFolders, id)
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

// parseBookmark reads the modelled fields from a raw bookmark record.
func parseBookmark(raw json.RawMessage) (BookmarkView, error) {
	var r struct {
		ID          string   `json:"id"`
		URL         string   `json:"url"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		FolderID    *string  `json:"folderId"`
		Favorite    bool     `json:"favorite"`
		ReadLater   bool     `json:"readLater"`
		Read        bool     `json:"read"`
		Trashed     bool     `json:"trashed"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return BookmarkView{}, err
	}
	return BookmarkView(r), nil
}

// parseFolder reads the modelled fields from a raw folder record.
func parseFolder(raw json.RawMessage) (FolderView, error) {
	var r struct {
		ID       string  `json:"id"`
		Name     string  `json:"name"`
		ParentID *string `json:"parentId"`
		Color    string  `json:"color"`
		Icon     string  `json:"icon"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return FolderView{}, err
	}
	return FolderView(r), nil
}

// newBookmarkRecord builds a fresh bookmark record matching the web's field set.
func newBookmarkRecord(n NewBookmark) (json.RawMessage, string, error) {
	id, err := newID()
	if err != nil {
		return nil, "", err
	}
	if n.Tags == nil {
		n.Tags = []string{}
	}
	rec := map[string]any{
		"id":          id,
		"url":         n.URL,
		"title":       n.Title,
		"description": n.Description,
		"tags":        n.Tags,
		"folderId":    n.FolderID,
		"favorite":    n.Favorite,
		"readLater":   n.ReadLater,
		"read":        false,
		"trashed":     false,
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
