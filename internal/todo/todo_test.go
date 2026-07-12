package todo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
	"github.com/MalteKiefer/ledgerline-cli/internal/vault"
)

type mock struct {
	srv     *httptest.Server
	vk      []byte
	mu      sync.Mutex
	store   string
	version int64
}

func newMock(t *testing.T, pass string) *mock {
	t.Helper()
	m := &mock{}
	salt := make([]byte, crypto.SaltBytes)
	for i := range salt {
		salt[i] = byte(i + 3)
	}
	const ops, mem = 1, 8 * 1024 * 1024
	kek := crypto.DeriveKEK(pass, salt, ops, mem)
	m.vk = make([]byte, crypto.KeyBytes)
	for i := range m.vk {
		m.vk[i] = byte(i)
	}
	wrapped, _ := crypto.Seal(m.vk, kek)

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
		var b struct {
			Ciphertext string `json:"ciphertext"`
			Version    int64  `json:"version"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		if b.Version != m.version {
			w.WriteHeader(http.StatusConflict)
			return
		}
		m.store = b.Ciphertext
		m.version++
		json.NewEncoder(w).Encode(map[string]any{"version": m.version})
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mock) seed(t *testing.T, obj any) {
	raw, _ := json.Marshal(obj)
	sealed, err := crypto.SealManifest(raw, m.vk)
	if err != nil {
		t.Fatal(err)
	}
	m.store = sealed
}

func (m *mock) manifest(t *testing.T) map[string]json.RawMessage {
	raw, err := crypto.OpenManifest(m.store, m.vk)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	json.Unmarshal(trimJSON(raw), &out)
	return out
}

func TestTodoCRUDPreservesOtherModules(t *testing.T) {
	m := newMock(t, "pw")
	client, _ := api.New(m.srv.URL, api.WithHTTPClient(m.srv.Client()))
	ctx := context.Background()
	vk, err := vault.Unlock(ctx, client, "pw")
	if err != nil {
		t.Fatal(err)
	}
	m.seed(t, map[string]any{
		"v":         1,
		"notes":     []map[string]any{{"id": "n1", "title": "keep"}},
		"todos":     []any{},
		"todoLists": []any{},
	})

	store := NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := store.Add(NewTodo{Title: "Buy milk", Priority: PriorityHigh, Tags: []string{"home"}})
	if err != nil {
		t.Fatal(err)
	}
	store.Update(id, map[string]any{"done": true})
	if err := store.Save(ctx); err != nil {
		t.Fatal(err)
	}

	// Notes preserved.
	if !strings.Contains(string(m.manifest(t)["notes"]), "keep") {
		t.Fatal("notes clobbered")
	}

	// Reload and verify the todo.
	fresh := NewStore(client, vk)
	if err := fresh.Load(ctx); err != nil {
		t.Fatal(err)
	}
	todos := fresh.TodoViews()
	if len(todos) != 1 {
		t.Fatalf("want 1 todo, got %d", len(todos))
	}
	if !todos[0].Done || todos[0].Title != "Buy milk" || todos[0].Priority != PriorityHigh {
		t.Fatalf("todo wrong: %+v", todos[0])
	}

	// Resolve by prefix, trash it.
	rid, err := fresh.ResolveTodo(todos[0].ID[:6])
	if err != nil {
		t.Fatalf("resolve prefix: %v", err)
	}
	fresh.Update(rid, map[string]any{"trashed": true})
	if err := fresh.Save(ctx); err != nil {
		t.Fatal(err)
	}
	again := NewStore(client, vk)
	again.Load(ctx)
	if !again.TodoViews()[0].Trashed {
		t.Fatal("trash did not persist")
	}
}

func TestTodoListLifecycle(t *testing.T) {
	m := newMock(t, "pw")
	client, _ := api.New(m.srv.URL, api.WithHTTPClient(m.srv.Client()))
	ctx := context.Background()
	vk, _ := vault.Unlock(ctx, client, "pw")
	m.seed(t, map[string]any{"v": 1, "todos": []any{}, "todoLists": []any{}})

	store := NewStore(client, vk)
	store.Load(ctx)
	lid, _ := store.AddList("Work")
	store.Add(NewTodo{Title: "Task", ListID: &lid})
	if err := store.Save(ctx); err != nil {
		t.Fatal(err)
	}

	fresh := NewStore(client, vk)
	fresh.Load(ctx)
	if id, ok := fresh.ResolveListByName("work"); !ok || id != lid {
		t.Fatal("list not found by name")
	}
	// Delete the list — its todo detaches to no list.
	fresh.DeleteList(lid)
	if err := fresh.Save(ctx); err != nil {
		t.Fatal(err)
	}
	after := NewStore(client, vk)
	after.Load(ctx)
	if len(after.ListViews()) != 0 {
		t.Fatal("list not deleted")
	}
	if after.TodoViews()[0].ListID != nil {
		t.Fatal("todo not detached from deleted list")
	}
}
