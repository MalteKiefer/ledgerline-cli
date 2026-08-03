package notes

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

// notesMock is a minimal in-memory server for the sharded /notes endpoints.
type notesMock struct {
	mu         sync.Mutex
	srv        *httptest.Server
	store      string
	version    int64
	blobs      map[string][]byte
	seq        int
	lastShards []string
	lastCounts map[string]int
}

func newNotesMock(t *testing.T) *notesMock {
	t.Helper()
	m := &notesMock{blobs: map[string][]byte{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/notes/store", func(w http.ResponseWriter, r *http.Request) {
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

	mux.HandleFunc("/api/v1/notes/upload", func(w http.ResponseWriter, r *http.Request) {
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

	mux.HandleFunc("/api/v1/notes/raw/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/notes/raw/")
		m.mu.Lock()
		data, ok := m.blobs[id]
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Write(data)
	})

	mux.HandleFunc("/api/v1/notes/raw-batch", func(w http.ResponseWriter, r *http.Request) {
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

func (m *notesMock) client(t *testing.T) *api.Client {
	c, err := api.New(m.srv.URL, api.WithHTTPClient(m.srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testVK() []byte {
	vk := make([]byte, 32)
	for i := range vk {
		vk[i] = byte(7 + i)
	}
	return vk
}

// TestNotesRoundTrip proves the sharded engine end-to-end: add notes, seal +
// upload shards + save the root, then a fresh store reloads, decrypts and parses
// the same notes, and the anomaly-scan counts map is complete.
func TestNotesRoundTrip(t *testing.T) {
	m := newNotesMock(t)
	client := m.client(t)
	vk := testVK()

	store := NewStore(client, vk)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	titles := []string{"Alpha", "Beta", "Gamma"}
	for _, tt := range titles {
		if _, err := store.Add(NewNote{Title: tt, Content: "# " + tt + "\nbody", Tags: []string{"x"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.lastCounts["notes"] != 3 {
		t.Fatalf("counts[notes] = %d, want 3", m.lastCounts["notes"])
	}
	if len(m.lastShards) == 0 {
		t.Fatal("shards[] guard must be sent on save")
	}

	// Fresh store, cold reload from the server.
	store2 := NewStore(client, vk)
	if err := store2.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := store2.NoteViews()
	if len(got) != 3 {
		t.Fatalf("reloaded %d notes, want 3", len(got))
	}
	names := make([]string, 0, 3)
	for _, n := range got {
		names = append(names, n.Title)
		if n.Content == "" || len(n.Tags) != 1 || n.Tags[0] != "x" || n.Updated == "" {
			t.Fatalf("note %q lost fields: %+v", n.Title, n)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "Alpha,Beta,Gamma" {
		t.Fatalf("titles = %v", names)
	}
}

// TestNotesUpdateAndTrashPreserveUnknownFields verifies a patch keeps unmodeled
// record fields (round-trip) and stamps "updated".
func TestNotesUpdateAndTrashPreserveUnknownFields(t *testing.T) {
	m := newNotesMock(t)
	client := m.client(t)
	vk := testVK()

	store := NewStore(client, vk)
	store.Load(context.Background())
	// Seed a note carrying an unmodeled field ("color") directly.
	raw, _ := json.Marshal(map[string]any{
		"id": "n1", "title": "T", "content": "c", "tags": []string{}, "pinned": false,
		"trashed": false, "updated": "2020-01-01T00:00:00Z", "color": "#abc",
	})
	store.AddNote(raw)
	if err := store.Save(context.Background()); err != nil {
		t.Fatal(err)
	}

	store2 := NewStore(client, vk)
	store2.Load(context.Background())
	store2.Update("n1", map[string]any{"title": "T2"})
	store2.Trash("n1")
	if err := store2.Save(context.Background()); err != nil {
		t.Fatal(err)
	}

	store3 := NewStore(client, vk)
	store3.Load(context.Background())
	notes := store3.Notes()
	if len(notes) != 1 {
		t.Fatalf("want 1 note, got %d", len(notes))
	}
	var rec map[string]any
	json.Unmarshal(notes[0], &rec)
	if rec["title"] != "T2" || rec["trashed"] != true || rec["color"] != "#abc" {
		t.Fatalf("patch/round-trip failed: %+v", rec)
	}
	if rec["updated"] == "2020-01-01T00:00:00Z" {
		t.Fatal("updated timestamp was not refreshed on edit")
	}
}
