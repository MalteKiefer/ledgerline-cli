package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

// healthMock is a minimal in-memory server for the single-row /store/health.
type healthMock struct {
	mu      sync.Mutex
	srv     *httptest.Server
	store   string
	version int64
}

func newHealthMock(t *testing.T) *healthMock {
	t.Helper()
	m := &healthMock{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/store/health", func(w http.ResponseWriter, r *http.Request) {
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
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *healthMock) client(t *testing.T) *api.Client {
	c, err := api.New(m.srv.URL, api.WithHTTPClient(m.srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testVK() []byte {
	vk := make([]byte, 32)
	for i := range vk {
		vk[i] = byte(23 + i)
	}
	return vk
}

func fptr(f float64) *float64 { return &f }

// TestHealthFloatMeasurementsRoundTrip is the crux: a decimal measurement value
// (72.5 kg, 36.6 °C) and a decimal profile height must seal (float-tolerant
// canonicalization) and reload intact — proving the canonicaljson change works
// end-to-end for the one single-row module that carries floats.
func TestHealthFloatMeasurementsRoundTrip(t *testing.T) {
	m := newHealthMock(t)
	client := m.client(t)
	vk := testVK()

	store := NewStore(client, vk)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddEntry(NewEntry{Metric: MetricWeight, V: 72.5, Ts: "2026-08-04T08:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddEntry(NewEntry{Metric: MetricTemp, V: 36.6}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddEntry(NewEntry{Metric: MetricBP, V: 120, V2: fptr(80)}); err != nil {
		t.Fatal(err)
	}
	p := Profile{Birthdate: "1990-05-01", HeightCm: fptr(175.5), Sex: "m", WeightGoalKg: fptr(70.0)}
	p.Units.Weight, p.Units.Glucose, p.Units.Temp = "kg", "mgdl", "c"
	if err := store.SetProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background()); err != nil {
		t.Fatalf("save with float measurements must succeed (float-tolerant seal): %v", err)
	}

	// Cold reload.
	store2 := NewStore(client, vk)
	if err := store2.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := store2.EntryViews()
	if len(entries) != 3 {
		t.Fatalf("reloaded %d entries, want 3", len(entries))
	}
	var weight, temp, bp *EntryView
	for i := range entries {
		switch entries[i].Metric {
		case MetricWeight:
			weight = &entries[i]
		case MetricTemp:
			temp = &entries[i]
		case MetricBP:
			bp = &entries[i]
		}
	}
	if weight == nil || weight.V != 72.5 {
		t.Fatalf("weight lost its decimal: %+v", weight)
	}
	if temp == nil || temp.V != 36.6 {
		t.Fatalf("temp lost its decimal: %+v", temp)
	}
	if bp == nil || bp.V != 120 || bp.V2 == nil || *bp.V2 != 80 {
		t.Fatalf("bp systolic/diastolic wrong: %+v", bp)
	}
	prof := store2.Profile()
	if prof.HeightCm == nil || *prof.HeightCm != 175.5 || prof.Sex != "m" ||
		prof.WeightGoalKg == nil || *prof.WeightGoalKg != 70 || prof.Units.Weight != "kg" {
		t.Fatalf("profile round-trip mismatch: %+v", prof)
	}
}

// TestHealthSingleActiveFast enforces the one-active-fast invariant.
func TestHealthSingleActiveFast(t *testing.T) {
	m := newHealthMock(t)
	client := m.client(t)
	vk := testVK()

	store := NewStore(client, vk)
	store.Load(context.Background())
	id, err := store.StartFast(16, "dinner→lunch")
	if err != nil {
		t.Fatal(err)
	}
	if store.ActiveFast() == nil {
		t.Fatal("fast should be active")
	}
	if _, err := store.StartFast(18, ""); err != ErrActiveFast {
		t.Fatalf("second StartFast must return ErrActiveFast, got %v", err)
	}
	store.EndFast(id, "")
	if store.ActiveFast() != nil {
		t.Fatal("fast should be ended")
	}
	// A new fast is allowed once the previous ended.
	if _, err := store.StartFast(18, ""); err != nil {
		t.Fatalf("StartFast after EndFast must succeed: %v", err)
	}
	if err := store.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
}
