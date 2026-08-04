package passwords

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

type pwMock struct {
	mu         sync.Mutex
	srv        *httptest.Server
	store      string
	version    int64
	blobs      map[string][]byte
	seq        int
	lastShards []string
	lastCounts map[string]int
}

func newPwMock(t *testing.T) *pwMock {
	t.Helper()
	m := &pwMock{blobs: map[string][]byte{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/passwords/store", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{"ciphertext": m.store, "version": m.version})
			return
		}
		var body struct {
			Ciphertext string         `json:"ciphertext"`
			Version    int64          `json:"version"`
			Shards     []string       `json:"shards"`
			Counts     map[string]int `json:"counts"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Version != m.version {
			w.WriteHeader(http.StatusConflict)
			return
		}
		m.lastShards, m.lastCounts, m.store = body.Shards, body.Counts, body.Ciphertext
		m.version++
		json.NewEncoder(w).Encode(map[string]any{"version": m.version})
	})
	mux.HandleFunc("/api/v1/passwords/upload", func(w http.ResponseWriter, r *http.Request) {
		f, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(400)
			return
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		m.mu.Lock()
		m.seq++
		id := "b" + strconv.Itoa(m.seq)
		m.blobs[id] = data
		m.mu.Unlock()
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	})
	mux.HandleFunc("/api/v1/passwords/raw/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/passwords/raw/")
		m.mu.Lock()
		data, ok := m.blobs[id]
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Write(data)
	})
	mux.HandleFunc("/api/v1/passwords/raw-batch", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Blobs []string `json:"blobs"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		defer m.mu.Unlock()
		var out []byte
		for _, id := range body.Blobs {
			data, ok := m.blobs[id]
			if !ok {
				continue
			}
			out = binary.LittleEndian.AppendUint32(out, uint32(len(id)))
			out = append(out, id...)
			out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
			out = append(out, data...)
		}
		w.Write(out)
	})

	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *pwMock) client(t *testing.T) *api.Client {
	c, err := api.New(m.srv.URL, api.WithHTTPClient(m.srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testVK() []byte {
	vk := make([]byte, 32)
	for i := range vk {
		vk[i] = byte(11 + i)
	}
	return vk
}

// TestPasswordsRoundTrip proves the files-shaped sharded engine: secrets (sharded)
// + a secretFolders collection blob round-trip through seal/upload/save/reload,
// the folder link survives, and the counts map covers both slices.
func TestPasswordsRoundTrip(t *testing.T) {
	m := newPwMock(t)
	client := m.client(t)
	vk := testVK()

	store := NewStore(client, vk)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	fid, err := store.AddFolder("Work", "")
	if err != nil {
		t.Fatal(err)
	}
	fields := json.RawMessage(`{"username":"u","password":"p"}`)
	if _, err := store.Add(NewSecret{Type: "login", Title: "GitHub", Folder: &fid, Fields: fields, Tags: []string{"dev"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(NewSecret{Type: "password", Title: "Server"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.lastCounts["secrets"] != 2 || m.lastCounts["secretFolders"] != 1 {
		t.Fatalf("counts = %v, want secrets=2 secretFolders=1", m.lastCounts)
	}
	if len(m.lastShards) < 2 { // >=1 record shard + 1 folders collection blob
		t.Fatalf("shards[] must list record shard(s) + the folders blob, got %v", m.lastShards)
	}

	store2 := NewStore(client, vk)
	if err := store2.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	secs := store2.SecretViews()
	if len(secs) != 2 {
		t.Fatalf("reloaded %d secrets, want 2", len(secs))
	}
	if len(store2.FolderViews()) != 1 || store2.ResolveFolder(fid) != "Work" {
		t.Fatalf("folder lost: %v", store2.FolderViews())
	}
	var gh *SecretView
	for i := range secs {
		if secs[i].Title == "GitHub" {
			gh = &secs[i]
		}
	}
	if gh == nil || gh.Type != "login" || gh.Folder == nil || *gh.Folder != fid || len(gh.Tags) != 1 {
		t.Fatalf("GitHub secret round-trip mismatch: %+v", gh)
	}
	// fields survives (canonical JSON sorts object keys — compare semantically).
	var fm map[string]string
	if err := json.Unmarshal(gh.Fields, &fm); err != nil || fm["username"] != "u" || fm["password"] != "p" {
		t.Fatalf("fields lost: %s (%v)", gh.Fields, err)
	}
}

// TestPasswordsUpdatePreservesFieldsAndStampsUpdated confirms a patch keeps
// unmodeled/structured fields (versions/custom) and refreshes "updated".
func TestPasswordsUpdatePreservesFieldsAndStampsUpdated(t *testing.T) {
	m := newPwMock(t)
	client := m.client(t)
	vk := testVK()

	store := NewStore(client, vk)
	store.Load(context.Background())
	raw, _ := json.Marshal(map[string]any{
		"id": "s1", "type": "login", "title": "T", "favorite": false, "folder": nil,
		"tags": []string{}, "custom": []any{}, "icon": "", "fields": map[string]any{"password": "old"},
		"created": "2020-01-01T00:00:00Z", "updated": "2020-01-01T00:00:00Z",
		"versions": []any{map[string]any{"at": "2019", "title": "T"}}, "trashed": nil,
	})
	store.AddSecret(raw)
	if err := store.Save(context.Background()); err != nil {
		t.Fatal(err)
	}

	store2 := NewStore(client, vk)
	store2.Load(context.Background())
	store2.Update("s1", map[string]any{"title": "T2"})
	if err := store2.Save(context.Background()); err != nil {
		t.Fatal(err)
	}

	store3 := NewStore(client, vk)
	store3.Load(context.Background())
	var rec map[string]any
	json.Unmarshal(store3.Secrets()[0], &rec)
	if rec["title"] != "T2" {
		t.Fatalf("title not patched: %v", rec["title"])
	}
	if rec["updated"] == "2020-01-01T00:00:00Z" {
		t.Fatal("updated not refreshed")
	}
	// versions (structured, unmodeled by the patch) must survive.
	vs, _ := rec["versions"].([]any)
	if len(vs) != 1 {
		t.Fatalf("versions lost on patch: %v", rec["versions"])
	}
}
