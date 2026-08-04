// Package health implements the zero-knowledge Health module client. Health is a
// Store v3 SINGLE-ROW module (GET/PUT /store/health) whose decrypted object is
//
//	{ healthProfile: HealthProfile, healthEntries: [HealthEntry], healthFasts: [HealthFast] }
//
// healthEntries and healthFasts are collection arrays (handled by manifeststore);
// healthProfile is a singular object stored as a non-collection top-level key.
//
// Health records legitimately carry DECIMAL measurement values (weight, temp,
// glucose, height) — unlike every other single-row module — so this is the one
// place the sealed manifest carries floats. That is supported by
// crypto.SealManifest's float-tolerant canonicalization (the ciphertext is opaque,
// so no cross-client shard hash is affected). Field shapes mirror the web client +
// openapi (HealthProfile/HealthEntry/HealthFast) byte-for-byte.
package health

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/manifeststore"
)

// Manifest keys owned by the Health module.
const (
	collEntries = "healthEntries"
	collFasts   = "healthFasts"
	keyProfile  = "healthProfile"
)

// metric enum values (HealthEntry.metric).
const (
	MetricWeight  = "weight"
	MetricBP      = "bp"
	MetricPulse   = "pulse"
	MetricSpO2    = "spo2"
	MetricTemp    = "temp"
	MetricGlucose = "glucose"
)

// ErrActiveFast is returned by StartFast when a fast is already running (only one
// fast may be active — end=null — across all clients; a cross-client invariant).
var ErrActiveFast = errors.New("health: a fast is already active — end it before starting another")

// Profile is the personal health profile (health store key healthProfile).
type Profile struct {
	Birthdate    string   `json:"birthdate,omitempty"`
	HeightCm     *float64 `json:"heightCm"`
	Sex          string   `json:"sex,omitempty"`
	WeightGoalKg *float64 `json:"weightGoalKg"`
	Units        struct {
		Weight  string `json:"weight,omitempty"`
		Glucose string `json:"glucose,omitempty"`
		Temp    string `json:"temp,omitempty"`
	} `json:"units"`
}

// EntryView is a typed read view of a measurement (health store key healthEntries[]).
type EntryView struct {
	ID     string
	Metric string
	V      float64
	V2     *float64 // diastolic for metric=bp; nil otherwise
	Ts     string
	Note   string
}

// FastView is a typed read view of an intermittent-fasting record.
type FastView struct {
	ID          string
	Start       string
	End         *string // nil while running (the active fast)
	TargetHours float64
	Note        string
}

// Store is a view of the Health module's single sealed row.
type Store struct {
	ms *manifeststore.Store
}

// NewStore builds a health store over healthEntries + healthFasts (arrays) plus
// the healthProfile object.
func NewStore(client *api.Client, vaultKey []byte) *Store {
	return &Store{ms: manifeststore.New(client, vaultKey, "health", "health",
		manifeststore.CollectionSpec{Key: collEntries},
		manifeststore.CollectionSpec{Key: collFasts},
	)}
}

// Load fetches and decrypts the sealed health row.
func (s *Store) Load(ctx context.Context) error { return s.ms.Load(ctx) }

// Dirty reports whether there are unsaved changes.
func (s *Store) Dirty() bool { return s.ms.Dirty() }

// Save writes the sealed row (conflict-safe).
func (s *Store) Save(ctx context.Context) error { return s.ms.Save(ctx) }

// Profile returns the current health profile (a zero Profile when unset).
func (s *Store) Profile() Profile {
	var p Profile
	if raw := s.ms.RawKey(keyProfile); len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

// SetProfile stages a full replacement of the health profile.
func (s *Store) SetProfile(p Profile) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	s.ms.SetRawKey(keyProfile, raw)
	return nil
}

// EntryViews returns the parsed measurements.
func (s *Store) EntryViews() []EntryView {
	recs := s.ms.Records(collEntries)
	out := make([]EntryView, 0, len(recs))
	for _, raw := range recs {
		var r struct {
			ID     string   `json:"id"`
			Metric string   `json:"metric"`
			V      float64  `json:"v"`
			V2     *float64 `json:"v2"`
			Ts     string   `json:"ts"`
			Note   string   `json:"note"`
		}
		if err := json.Unmarshal(raw, &r); err == nil && r.ID != "" {
			out = append(out, EntryView(r))
		}
	}
	return out
}

// FastViews returns the parsed fasts.
func (s *Store) FastViews() []FastView {
	recs := s.ms.Records(collFasts)
	out := make([]FastView, 0, len(recs))
	for _, raw := range recs {
		var r struct {
			ID          string  `json:"id"`
			Start       string  `json:"start"`
			End         *string `json:"end"`
			TargetHours float64 `json:"targetHours"`
			Note        string  `json:"note"`
		}
		if err := json.Unmarshal(raw, &r); err == nil && r.ID != "" {
			out = append(out, FastView(r))
		}
	}
	return out
}

// ActiveFast returns the running fast (end==null), or nil when none is active.
func (s *Store) ActiveFast() *FastView {
	for _, f := range s.FastViews() {
		if f.End == nil {
			fc := f
			return &fc
		}
	}
	return nil
}

// NewEntry describes a new measurement. V2 is only meaningful for metric=bp.
type NewEntry struct {
	Metric string
	V      float64
	V2     *float64
	Ts     string // ISO-8601; defaults to now when empty
	Note   string
}

// AddEntry stages a new measurement and returns its id.
func (s *Store) AddEntry(n NewEntry) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	if n.Ts == "" {
		n.Ts = nowISO()
	}
	rec := map[string]any{"id": id, "metric": n.Metric, "v": n.V, "v2": n.V2, "ts": n.Ts, "note": n.Note}
	raw, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	s.ms.Add(collEntries, raw)
	return id, nil
}

// UpdateEntry stages a field patch on a measurement.
func (s *Store) UpdateEntry(id string, patch map[string]any) { s.ms.Update(collEntries, id, patch) }

// DeleteEntry stages removal of a measurement.
func (s *Store) DeleteEntry(id string) { s.ms.Delete(collEntries, id) }

// StartFast stages a new running fast (end=null). It refuses when a fast is
// already active — but the caller MUST Load()/refresh first to observe a fast
// started on another device (the single-active-fast invariant is enforced
// client-side over the sealed store; §HealthFast).
func (s *Store) StartFast(targetHours float64, note string) (string, error) {
	if s.ActiveFast() != nil {
		return "", ErrActiveFast
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	rec := map[string]any{"id": id, "start": nowISO(), "end": nil, "targetHours": targetHours, "note": note}
	raw, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	s.ms.Add(collFasts, raw)
	return id, nil
}

// EndFast stages ending a fast (sets end to now, or endISO when given).
func (s *Store) EndFast(id string, endISO string) {
	if endISO == "" {
		endISO = nowISO()
	}
	s.ms.Update(collFasts, id, map[string]any{"end": endISO})
}

// UpdateFast stages a field patch on a fast.
func (s *Store) UpdateFast(id string, patch map[string]any) { s.ms.Update(collFasts, id, patch) }

// DeleteFast stages removal of a fast.
func (s *Store) DeleteFast(id string) { s.ms.Delete(collFasts, id) }

// newID mirrors the web store's newId(): 16 random bytes as lowercase hex.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }
