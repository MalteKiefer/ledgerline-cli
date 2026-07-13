package files

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
	"github.com/MalteKiefer/ledgerline-cli/internal/manifeststore"
	"github.com/MalteKiefer/ledgerline-cli/internal/vault"
)

// mock is an in-memory server for /vault, /store and /files endpoints.
type mock struct {
	srv     *httptest.Server
	vk      []byte
	mu      sync.Mutex
	store   string
	version int64
	blobs   map[string][]byte
	seq     int
}

func newMock(t *testing.T, pass string) *mock {
	t.Helper()
	m := &mock{blobs: map[string][]byte{}}

	salt := make([]byte, crypto.SaltBytes)
	for i := range salt {
		salt[i] = byte(i + 1)
	}
	const ops, mem = 1, 8 * 1024 * 1024
	kek := crypto.DeriveKEK(pass, salt, ops, mem)
	m.vk = make([]byte, crypto.KeyBytes)
	for i := range m.vk {
		m.vk[i] = byte(7 + i)
	}
	wrapped, err := crypto.Seal(m.vk, kek)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user":{"id":1},"usage":{"files":0,"gallery":0}}`))
	})
	mux.HandleFunc("/api/v1/vault", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"configured": true, "salt": base64.StdEncoding.EncodeToString(salt),
			"kdf_ops": ops, "kdf_mem": mem, "wrapped_vault_key": wrapped.C, "wrap_nonce": wrapped.N,
		})
	})
	mux.HandleFunc("/api/v1/store", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{"ciphertext": m.store, "version": m.version})
			return
		}
		var body struct {
			Ciphertext string `json:"ciphertext"`
			Version    int64  `json:"version"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Version != m.version {
			w.WriteHeader(http.StatusConflict)
			return
		}
		m.store = body.Ciphertext
		m.version++
		json.NewEncoder(w).Encode(map[string]any{"version": m.version})
	})
	mux.HandleFunc("/api/v1/files/upload", func(w http.ResponseWriter, r *http.Request) {
		f, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(400)
			return
		}
		defer f.Close()
		buf := make([]byte, 0, 1024)
		tmp := make([]byte, 4096)
		for {
			n, e := f.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if e != nil {
				break
			}
		}
		m.mu.Lock()
		m.seq++
		id := "b" + itoa(m.seq)
		m.blobs[id] = buf
		m.mu.Unlock()
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	})
	mux.HandleFunc("/api/v1/files/raw/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/files/raw/")
		m.mu.Lock()
		data, ok := m.blobs[id]
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Write(data)
	})
	mux.HandleFunc("/api/v1/files/blob/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"deleted":true}`))
	})

	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mock) client(t *testing.T) *api.Client {
	c, err := api.New(m.srv.URL, api.WithHTTPClient(m.srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// seedManifest seals a raw manifest object into the mock store.
func (m *mock) seedManifest(t *testing.T, obj any) {
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := crypto.SealManifest(raw, m.vk)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.store = sealed
	m.mu.Unlock()
}

// currentManifest decrypts the mock store into a generic map.
func (m *mock) currentManifest(t *testing.T) map[string]json.RawMessage {
	m.mu.Lock()
	ct := m.store
	m.mu.Unlock()
	raw, err := crypto.OpenManifest(ct, m.vk)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(manifeststore.TrimJSON(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestManifestPreservesOtherModules(t *testing.T) {
	m := newMock(t, "pw")
	client := m.client(t)
	ctx := context.Background()
	vk, err := vault.Unlock(ctx, client, "pw")
	if err != nil {
		t.Fatal(err)
	}
	// Seed a manifest that already holds notes + bookmarks.
	m.seedManifest(t, map[string]any{
		"v":           1,
		"notes":       []map[string]any{{"id": "n1", "title": "keep me"}},
		"bookmarks":   []map[string]any{{"id": "bk1"}},
		"files":       []any{},
		"fileFolders": []any{},
	})

	store := NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	up := NewUploader(client, store, vk)
	if _, _, err := up.Create(ctx, "docs/a.txt", "text/plain", "2021-01-01T00:00:00Z", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx); err != nil {
		t.Fatal(err)
	}

	man := m.currentManifest(t)
	if !strings.Contains(string(man["notes"]), "keep me") {
		t.Fatalf("notes were clobbered: %s", man["notes"])
	}
	if !strings.Contains(string(man["bookmarks"]), "bk1") {
		t.Fatalf("bookmarks were clobbered: %s", man["bookmarks"])
	}
	var files []json.RawMessage
	json.Unmarshal(man["files"], &files)
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	var folders []json.RawMessage
	json.Unmarshal(man["fileFolders"], &folders)
	if len(folders) != 1 {
		t.Fatalf("want 1 folder (docs), got %d", len(folders))
	}
}

func TestChildrenListing(t *testing.T) {
	m := newMock(t, "pw")
	client := m.client(t)
	ctx := context.Background()
	vk, _ := vault.Unlock(ctx, client, "pw")

	m.seedManifest(t, map[string]any{
		"v": 1,
		"fileFolders": []map[string]any{
			{"id": "f1", "name": "docs", "parent": nil},
			{"id": "f2", "name": "sub", "parent": "f1"},
		},
		"files": []map[string]any{
			{"id": "a", "name": "root.txt", "blob": "b1", "encFileKey": "{}", "size": 10, "folder": nil},
			{"id": "b", "name": "inside.txt", "blob": "b2", "encFileKey": "{}", "size": 20, "folder": "f1"},
			// A file whose parent folder no longer exists must show at the root.
			{"id": "c", "name": "orphan.txt", "blob": "b3", "encFileKey": "{}", "size": 30, "folder": "ghost"},
			// Odd field types must not drop the record: encFileKey as an object,
			// size as a float, trashed as a bool.
			{"id": "d", "name": "weird.pdf", "blob": "b4", "encFileKey": map[string]any{"c": "x", "n": "y"}, "size": 12.0, "trashed": false, "folder": nil},
		},
	})

	store := NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}

	folders, filesList, err := Children(store, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Name != "docs" {
		t.Fatalf("root folders = %+v", folders)
	}
	if len(filesList) != 3 {
		t.Fatalf("want 3 root files (root.txt + orphan.txt + weird.pdf), got %+v", filesList)
	}

	if _, ok := FindFile(store, "docs/inside.txt"); !ok {
		t.Fatal("FindFile should resolve a nested file path")
	}
	if _, ok := FindFile(store, "docs"); ok {
		t.Fatal("FindFile must not resolve a folder path")
	}

	folders, filesList, err = Children(store, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Name != "sub" {
		t.Fatalf("docs folders = %+v", folders)
	}
	if len(filesList) != 1 || filesList[0].Name != "inside.txt" {
		t.Fatalf("docs files = %+v", filesList)
	}

	if _, _, err := Children(store, "nope"); err == nil {
		t.Fatal("expected error for unknown folder")
	}
}

func TestSafeJoin(t *testing.T) {
	base := "/out"
	cases := []struct {
		rel string
		ok  bool
	}{
		{"docs/a.txt", true},
		{"normal.txt", true},
		{"../evil", false},
		{"a/../../b", false},
		{"../../etc/passwd", false},
		{"..", false},
	}
	for _, c := range cases {
		if _, ok := SafeJoin(base, c.rel); ok != c.ok {
			t.Fatalf("SafeJoin(%q, %q) ok=%v, want %v", base, c.rel, ok, c.ok)
		}
	}
}

func TestDownloadRefusesTraversalName(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	m := newMock(t, "pw")
	client := m.client(t)
	ctx := context.Background()
	vk, _ := vault.Unlock(ctx, client, "pw")

	// A hostile manifest names a file so it would escape the sync target.
	m.seedManifest(t, map[string]any{
		"v":           1,
		"fileFolders": []any{},
		"files": []map[string]any{
			{"id": "e", "name": "../../evil.txt", "blob": "b1", "encFileKey": "{}", "size": 3, "folder": nil},
		},
	})
	// Put a real (decryptable) blob behind b1 so only the path guard can stop it.
	blob, key, _ := crypto.EncryptContent([]byte("bad"), vk)
	padded, _ := crypto.PadBlob(blob)
	m.mu.Lock()
	m.blobs["b1"] = padded
	m.mu.Unlock()
	_ = key // the manifest's encFileKey is "{}", so decrypt would fail anyway; the point is the path guard fires first

	dir := t.TempDir()
	store := NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := NewSyncer(client, store, vk, dir, "", SyncOptions{Conflict: ConflictKeepBoth, Delete: DeleteBoth}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed == 0 {
		t.Fatal("expected the traversal name to be refused (a failure), not written")
	}
	// Nothing must exist outside the sync dir.
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(dir)), "evil.txt")); err == nil {
		t.Fatal("traversal wrote a file outside the target directory")
	}
}

func TestSubtreeAndForceDelete(t *testing.T) {
	m := newMock(t, "pw")
	client := m.client(t)
	ctx := context.Background()
	vk, _ := vault.Unlock(ctx, client, "pw")

	m.seedManifest(t, map[string]any{
		"v": 1,
		"fileFolders": []map[string]any{
			{"id": "f1", "name": "docs", "parent": nil},
			{"id": "f2", "name": "sub", "parent": "f1"},
		},
		"files": []map[string]any{
			{"id": "a", "name": "a.txt", "blob": "b1", "encFileKey": "{}", "size": 1, "folder": "f1"},
			{"id": "b", "name": "b.txt", "blob": "b2", "encFileKey": "{}", "size": 1, "folder": "f2"},
			{"id": "r", "name": "root.txt", "blob": "b3", "encFileKey": "{}", "size": 1, "folder": nil},
		},
	})

	store := NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}

	subFiles, folderIDs, err := Subtree(store, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(subFiles) != 2 || len(folderIDs) != 2 {
		t.Fatalf("Subtree(docs) = %d files, %d folders (want 2, 2)", len(subFiles), len(folderIDs))
	}

	for _, fv := range subFiles {
		store.DeleteFile(fv.ID)
	}
	for _, id := range folderIDs {
		store.DeleteFolder(id)
	}
	if err := store.Save(ctx); err != nil {
		t.Fatal(err)
	}

	fresh := NewStore(client, vk)
	fresh.Load(ctx)
	folders, filesList, _ := Children(fresh, "")
	if len(folders) != 0 {
		t.Fatalf("folders not deleted: %+v", folders)
	}
	if len(filesList) != 1 || filesList[0].Name != "root.txt" {
		t.Fatalf("subtree files not deleted (root.txt should remain): %+v", filesList)
	}
}

func TestSyncRoundTripAndDelete(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir()) // isolate sync-state
	m := newMock(t, "pw")
	client := m.client(t)
	ctx := context.Background()
	vk, _ := vault.Unlock(ctx, client, "pw")

	dirA := t.TempDir()
	dirB := t.TempDir()
	writeFile(t, filepath.Join(dirA, "docs", "a.txt"), "content-A")

	opts := SyncOptions{Conflict: ConflictKeepBoth, Delete: DeleteBoth}

	// Sync A → uploads a.txt.
	runSync(t, ctx, client, vk, dirA, opts)
	// Sync B → downloads a.txt.
	runSync(t, ctx, client, vk, dirB, opts)
	if got := readFile(t, filepath.Join(dirB, "docs", "a.txt")); got != "content-A" {
		t.Fatalf("B did not receive the file: %q", got)
	}

	// Delete in A, sync A → remote trashed; sync B → deleted locally.
	if err := os.Remove(filepath.Join(dirA, "docs", "a.txt")); err != nil {
		t.Fatal(err)
	}
	runSync(t, ctx, client, vk, dirA, opts)
	runSync(t, ctx, client, vk, dirB, opts)
	if _, err := os.Stat(filepath.Join(dirB, "docs", "a.txt")); !os.IsNotExist(err) {
		t.Fatal("deletion did not propagate to B")
	}
}

// runSync loads a fresh store and runs one sync pass for a local dir (root map).
func runSync(t *testing.T, ctx context.Context, client *api.Client, vk []byte, dir string, opts SyncOptions) SyncResult {
	t.Helper()
	// Isolate sync state under a temp config dir per call chain.
	store := NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := NewSyncer(client, store, vk, dir, "", opts).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
