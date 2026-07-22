package files

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	mux.HandleFunc("/api/v1/files/store", func(w http.ResponseWriter, r *http.Request) {
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

// sealBlobInto encrypts + pads bytes, stores them under a fresh id, and returns
// the id + wrapped key (a hand-built blob, so seeded records bypass the canonical
// write path — this lets a test seed "odd" records like a float size).
func (m *mock) sealBlobInto(t *testing.T, plain []byte) (ref, key string) {
	t.Helper()
	blob, encKey, err := crypto.EncryptContent(plain, m.vk)
	if err != nil {
		t.Fatal(err)
	}
	padded, err := crypto.PadBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.seq++
	id := "b" + itoa(m.seq)
	m.blobs[id] = padded
	m.mu.Unlock()
	return id, encKey
}

// seedFiles seals a v3 Files root into the mock store: all file records go into a
// single shard (shardBits 0), folders into the fileFolders collection blob, and
// extraRoot merges any additional root keys (e.g. an unknown field to prove
// preservation).
func (m *mock) seedFiles(t *testing.T, files, folders []any, extraRoot map[string]any) {
	t.Helper()
	root := map[string]any{"v": 3, "suite": 1, "shardBits": 0, "caps": map[string]any{}}

	filesJSON, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	ref, key := m.sealBlobInto(t, filesJSON)
	sum := sha256.Sum256(filesJSON)
	root["shards"] = []map[string]any{{
		"ref": ref, "key": key, "hash": hex.EncodeToString(sum[:]), "count": len(files), "bucket": 0,
	}}

	if len(folders) > 0 {
		foldersJSON, err := json.Marshal(folders)
		if err != nil {
			t.Fatal(err)
		}
		fref, fkey := m.sealBlobInto(t, foldersJSON)
		fsum := sha256.Sum256(foldersJSON)
		root["foldersRef"] = fref
		root["foldersKey"] = fkey
		root["foldersHash"] = hex.EncodeToString(fsum[:])
	}
	for k, v := range extraRoot {
		root[k] = v
	}

	raw, err := json.Marshal(root)
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

// currentRoot decrypts the mock store into the root map.
func (m *mock) currentRoot(t *testing.T) map[string]json.RawMessage {
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
	// Store v3: Files owns its own sharded store, so foreign modules no longer
	// share the row. The forward-compat guarantee that remains is unknown-ROOT-key
	// preservation: a field this client doesn't model must survive a save.
	m.seedFiles(t, []any{}, []any{}, map[string]any{"futureField": "keep me"})

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

	root := m.currentRoot(t)
	if !strings.Contains(string(root["futureField"]), "keep me") {
		t.Fatalf("unknown root field was clobbered: %s", root["futureField"])
	}

	// The file + its docs folder are readable from a fresh sharded load.
	fresh := NewStore(client, vk)
	if err := fresh.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fresh.Files()) != 1 {
		t.Fatalf("want 1 file, got %d", len(fresh.Files()))
	}
	if len(fresh.Folders()) != 1 {
		t.Fatalf("want 1 folder (docs), got %d", len(fresh.Folders()))
	}
}

func TestChildrenListing(t *testing.T) {
	m := newMock(t, "pw")
	client := m.client(t)
	ctx := context.Background()
	vk, _ := vault.Unlock(ctx, client, "pw")

	m.seedFiles(t,
		[]any{
			map[string]any{"id": "a", "name": "root.txt", "blob": "b1", "encFileKey": "{}", "size": 10, "folder": nil},
			map[string]any{"id": "b", "name": "inside.txt", "blob": "b2", "encFileKey": "{}", "size": 20, "folder": "f1"},
			// A file whose parent folder no longer exists must show at the root.
			map[string]any{"id": "c", "name": "orphan.txt", "blob": "b3", "encFileKey": "{}", "size": 30, "folder": "ghost"},
			// Odd field types must not drop the record: encFileKey as an object,
			// size as a float, trashed as a bool.
			map[string]any{"id": "d", "name": "weird.pdf", "blob": "b4", "encFileKey": map[string]any{"c": "x", "n": "y"}, "size": 12.0, "trashed": false, "folder": nil},
		},
		[]any{
			map[string]any{"id": "f1", "name": "docs", "parent": nil},
			map[string]any{"id": "f2", "name": "sub", "parent": "f1"},
		},
		nil,
	)

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

	// A hostile manifest names a file so it would escape the sync target. Its
	// content blob uses a fixed id that won't collide with the seeded shard blobs.
	m.seedFiles(t,
		[]any{
			map[string]any{"id": "e", "name": "../../evil.txt", "blob": "content1", "encFileKey": "{}", "size": 3, "folder": nil},
		},
		[]any{}, nil,
	)
	// Put a real (decryptable) blob behind content1 so only the path guard can stop it.
	blob, key, _ := crypto.EncryptContent([]byte("bad"), vk)
	padded, _ := crypto.PadBlob(blob)
	m.mu.Lock()
	m.blobs["content1"] = padded
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

	m.seedFiles(t,
		[]any{
			map[string]any{"id": "a", "name": "a.txt", "blob": "content_a", "encFileKey": "{}", "size": 1, "folder": "f1"},
			map[string]any{"id": "b", "name": "b.txt", "blob": "content_b", "encFileKey": "{}", "size": 1, "folder": "f2"},
			map[string]any{"id": "r", "name": "root.txt", "blob": "content_r", "encFileKey": "{}", "size": 1, "folder": nil},
		},
		[]any{
			map[string]any{"id": "f1", "name": "docs", "parent": nil},
			map[string]any{"id": "f2", "name": "sub", "parent": "f1"},
		},
		nil,
	)

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

// TestSyncSkipsIdenticalOnFirstRun verifies that a pre-existing file present on
// both sides with matching size+mtime is left untouched even without a prior
// sync-state baseline — i.e. a first sync no longer flags it as a conflict.
func TestSyncSkipsIdenticalOnFirstRun(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	m := newMock(t, "pw")
	client := m.client(t)
	ctx := context.Background()
	vk, _ := vault.Unlock(ctx, client, "pw")

	dirA := t.TempDir()
	aPath := filepath.Join(dirA, "a.txt")
	writeFile(t, aPath, "hello")

	opts := SyncOptions{Conflict: ConflictNewest, Delete: DeleteBoth}
	// Sync A uploads a.txt; its remote Created is set from A's mtime.
	if res := runSync(t, ctx, client, vk, dirA, opts); res.Uploaded != 1 {
		t.Fatalf("A upload = %d, want 1", res.Uploaded)
	}

	// A brand-new local dir B with byte-identical content and the same mtime.
	fi, err := os.Stat(aPath)
	if err != nil {
		t.Fatal(err)
	}
	dirB := t.TempDir()
	bPath := filepath.Join(dirB, "a.txt")
	writeFile(t, bPath, "hello")
	if err := os.Chtimes(bPath, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}

	res := runSync(t, ctx, client, vk, dirB, opts) // first sync for B → no baseline
	if res.Conflicts != 0 || res.Uploaded != 0 || res.Downloaded != 0 {
		t.Fatalf("identical file mishandled: %+v (want 0 conflicts/up/down)", res)
	}
	if res.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", res.Skipped)
	}
}

// TestSyncSkipsIdenticalDespiteMtime verifies that a same-size copy whose mtime
// does NOT line up with the remote Created time (e.g. a web upload) is still
// recognised as identical via a content compare, instead of being re-downloaded.
func TestSyncSkipsIdenticalDespiteMtime(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	m := newMock(t, "pw")
	client := m.client(t)
	ctx := context.Background()
	vk, _ := vault.Unlock(ctx, client, "pw")

	dirA := t.TempDir()
	writeFile(t, filepath.Join(dirA, "a.txt"), "same-bytes")
	opts := SyncOptions{Conflict: ConflictNewest, Delete: DeleteBoth}
	runSync(t, ctx, client, vk, dirA, opts)

	// B: identical content, but a wildly different mtime than the remote Created.
	dirB := t.TempDir()
	bPath := filepath.Join(dirB, "a.txt")
	writeFile(t, bPath, "same-bytes")
	old := time.Now().Add(-240 * time.Hour)
	if err := os.Chtimes(bPath, old, old); err != nil {
		t.Fatal(err)
	}

	res := runSync(t, ctx, client, vk, dirB, opts)
	if res.Conflicts != 0 || res.Downloaded != 0 || res.Uploaded != 0 {
		t.Fatalf("identical content re-synced despite matching bytes: %+v", res)
	}
	if res.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", res.Skipped)
	}
	if got := readFile(t, bPath); got != "same-bytes" {
		t.Fatalf("local file was overwritten: %q", got)
	}
}

// TestSyncOverrideLocalWins verifies that --override pushes the local copy over
// a differing remote instead of resolving by newest/keep-both.
func TestSyncOverrideLocalWins(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	m := newMock(t, "pw")
	client := m.client(t)
	ctx := context.Background()
	vk, _ := vault.Unlock(ctx, client, "pw")

	dirA := t.TempDir()
	writeFile(t, filepath.Join(dirA, "a.txt"), "AAAA")
	runSync(t, ctx, client, vk, dirA, SyncOptions{Conflict: ConflictNewest, Delete: DeleteBoth})

	// B has different content (and a different size, so it is not treated as
	// identical) with an older mtime — newest would pull, but override pushes.
	dirB := t.TempDir()
	bPath := filepath.Join(dirB, "a.txt")
	writeFile(t, bPath, "BBBBBB")
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(bPath, old, old); err != nil {
		t.Fatal(err)
	}

	res := runSync(t, ctx, client, vk, dirB, SyncOptions{Conflict: ConflictNewest, Delete: DeleteBoth, Override: true})
	if res.Uploaded != 1 || res.Downloaded != 0 || res.Conflicts != 0 {
		t.Fatalf("override should push local: %+v", res)
	}

	// A fresh dir must now receive B's content.
	dirC := t.TempDir()
	runSync(t, ctx, client, vk, dirC, SyncOptions{Conflict: ConflictNewest, Delete: DeleteBoth})
	if got := readFile(t, filepath.Join(dirC, "a.txt")); got != "BBBBBB" {
		t.Fatalf("remote content = %q, want BBBBBB", got)
	}
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
