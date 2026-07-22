package gallery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	"github.com/MalteKiefer/ledgerline-cli/internal/vault"
)

func TestFileSig(t *testing.T) {
	a := FileSig([]byte("hello world"))
	b := FileSig([]byte("hello world"))
	c := FileSig([]byte("hello worlD"))
	if a != b {
		t.Fatal("same bytes must yield same signature")
	}
	if a == c {
		t.Fatal("different bytes must differ")
	}
	if !strings.HasPrefix(a, "11:") {
		t.Fatalf("signature should start with the byte length: %s", a)
	}
}

func TestPairItemsLivePhoto(t *testing.T) {
	files := []string{
		"/p/IMG_1.HEIC", "/p/IMG_1.MOV", // Live Photo pair
		"/p/IMG_2.jpg", // lone still
		"/p/clip.mp4",  // lone video
	}
	items := pairItems(files, nil)
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d: %+v", len(items), items)
	}
	byStill := map[string]Item{}
	for _, it := range items {
		byStill[filepath.Base(it.StillPath)] = it
	}
	if got := byStill["IMG_1.HEIC"].MotionPath; got != "/p/IMG_1.MOV" {
		t.Fatalf("Live Photo motion not paired: %q", got)
	}
	if byStill["IMG_2.jpg"].MotionPath != "" {
		t.Fatal("lone still should have no motion")
	}
	if _, ok := byStill["clip.mp4"]; !ok {
		t.Fatal("lone video should upload as its own item")
	}
}

// mockServer emulates the vault + gallery endpoints against an in-memory blob
// store, so the pipeline can be exercised end to end.
type mockServer struct {
	srv     *httptest.Server
	vk      []byte
	mu      sync.Mutex
	blobs   map[string][]byte
	store   string
	version int64
	nextID  int
}

func newMockServer(t *testing.T, passphrase string) *mockServer {
	t.Helper()
	m := &mockServer{blobs: map[string][]byte{}}

	// Build a vault: derive a KEK from the passphrase and wrap a fresh VK.
	salt := make([]byte, crypto.SaltBytes)
	for i := range salt {
		salt[i] = byte(i)
	}
	const ops, memBytes = 1, 8 * 1024 * 1024
	kek := crypto.DeriveKEK(passphrase, salt, ops, memBytes)
	m.vk = make([]byte, crypto.KeyBytes)
	for i := range m.vk {
		m.vk[i] = byte(200 - i)
	}
	wrapped, err := crypto.Seal(m.vk, kek)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user":{"id":1,"name":"T"},"usage":{"files":0,"gallery":0}}`))
	})
	mux.HandleFunc("/api/v1/vault", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"configured": true, "salt": base64.StdEncoding.EncodeToString(salt),
			"kdf_ops": ops, "kdf_mem": memBytes,
			"wrapped_vault_key": wrapped.C, "wrap_nonce": wrapped.N,
		})
	})
	mux.HandleFunc("/api/v1/gallery/store", func(w http.ResponseWriter, r *http.Request) {
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
			w.Write([]byte(`{"error":"version_conflict"}`))
			return
		}
		m.store = body.Ciphertext
		m.version++
		json.NewEncoder(w).Encode(map[string]any{"version": m.version})
	})
	mux.HandleFunc("/api/v1/gallery/upload", func(w http.ResponseWriter, r *http.Request) {
		f, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(400)
			return
		}
		defer f.Close()
		data := readAll(f)
		m.mu.Lock()
		m.nextID++
		id := "blob-" + itoa(m.nextID)
		m.blobs[id] = data
		m.mu.Unlock()
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	})
	mux.HandleFunc("/api/v1/gallery/raw/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/gallery/raw/")
		m.mu.Lock()
		data, ok := m.blobs[id]
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Write(data)
	})
	mux.HandleFunc("/api/v1/gallery/process", func(w http.ResponseWriter, r *http.Request) {
		b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
		json.NewEncoder(w).Encode(map[string]any{
			"media_type": "image", "width": 4, "height": 3, "duration": nil, "content_id": nil,
			"exif":  map[string]any{"taken_at": "2021-05-01T10:00:00", "lat": nil, "lon": nil, "camera": nil},
			"place": nil, "embedding": nil, "phash": nil, "faces": []any{},
			"thumb": b64("THUMBNAIL-BYTES"), "medium": b64("MEDIUM-BYTES"), "motion": "",
		})
	})

	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockServer) client(t *testing.T) *api.Client {
	c, err := api.New(m.srv.URL, api.WithHTTPClient(m.srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUploadPipelineEndToEnd(t *testing.T) {
	const pass = "correct horse battery staple"
	m := newMockServer(t, pass)
	client := m.client(t)
	ctx := context.Background()

	vk, err := vault.Unlock(ctx, client, pass)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}

	// A tiny "photo" on disk.
	dir := t.TempDir()
	photoPath := filepath.Join(dir, "IMG_0001.jpg")
	original := []byte("ORIGINAL-PHOTO-BYTES-\x00\x01\x02")
	if err := os.WriteFile(photoPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	up := NewUploader(client, store, vk, true, false, nil)

	outcome, rec, err := up.Upload(ctx, Item{StillPath: photoPath}, original)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if outcome != Uploaded {
		t.Fatalf("outcome = %v", outcome)
	}

	// The original round-trips (this is what --delete verifies).
	if err := up.VerifyOriginal(ctx, rec, original); err != nil {
		t.Fatalf("verify original: %v", err)
	}

	if err := store.Save(ctx); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Re-open the manifest exactly as the web client would and check the record.
	fresh := NewStore(client, vk)
	if err := fresh.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fresh.basePhotos) != 1 {
		t.Fatalf("want 1 photo in reloaded manifest, got %d", len(fresh.basePhotos))
	}
	var got PhotoRecord
	if err := json.Unmarshal(fresh.basePhotos[0], &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "IMG_0001.jpg" || got.MediaType != "image" || got.TakenAt != "2021-05-01T10:00:00" {
		t.Fatalf("record fields wrong: %+v", got)
	}
	if got.ThumbRef == "" || got.MediumRef == "" || got.MetaRef == "" {
		t.Fatalf("derived refs missing: %+v", got)
	}
	// Fast upload leaves ML deferred, matching the web.
	if got.HasFaces != nil || !got.MlPending {
		t.Fatalf("fast upload should be un-analysed: hasFaces=%v mlPending=%v", got.HasFaces, got.MlPending)
	}

	// The thumbnail blob decrypts to the process output.
	thumbBlob, err := client.GetGalleryBlob(ctx, got.ThumbRef)
	if err != nil {
		t.Fatal(err)
	}
	thumb, err := crypto.DecryptContent(thumbBlob, got.ThumbKey, vk)
	if err != nil {
		t.Fatal(err)
	}
	if string(thumb) != "THUMBNAIL-BYTES" {
		t.Fatalf("thumb = %q", thumb)
	}
}

func TestUploadDedup(t *testing.T) {
	const pass = "pw"
	m := newMockServer(t, pass)
	client := m.client(t)
	ctx := context.Background()
	vk, _ := vault.Unlock(ctx, client, pass)

	store := NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	up := NewUploader(client, store, vk, true, false, nil)
	data := []byte("dup-bytes")

	if o, _, err := up.Upload(ctx, Item{StillPath: "/x/a.jpg"}, data); err != nil || o != Uploaded {
		t.Fatalf("first upload: o=%v err=%v", o, err)
	}
	if o, _, err := up.Upload(ctx, Item{StillPath: "/x/b.jpg"}, data); err != nil || o != Duplicate {
		t.Fatalf("second upload should be a duplicate: o=%v err=%v", o, err)
	}
}

func TestParallelUploadIsRaceFree(t *testing.T) {
	const pass = "pw"
	m := newMockServer(t, pass)
	client := m.client(t)
	ctx := context.Background()
	vk, _ := vault.Unlock(ctx, client, pass)

	store := NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	up := NewUploader(client, store, vk, true, false, nil)

	// Upload many distinct items concurrently; the store's added list and sig
	// index must stay consistent (run under -race to catch a regression).
	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf("photo-bytes-%d", i))
			o, _, err := up.Upload(ctx, Item{StillPath: fmt.Sprintf("/x/p%d.jpg", i)}, data)
			if err != nil {
				errs <- err
			} else if o != Uploaded {
				errs <- fmt.Errorf("item %d: outcome %v", i, o)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := store.PendingCount(); got != n {
		t.Fatalf("PendingCount = %d, want %d", got, n)
	}
}

func TestMergeLivePhotosByContentID(t *testing.T) {
	s := NewStore(nil, nil)
	still := &PhotoRecord{ID: "s1", MediaType: "image", OriginalRef: "b-still", contentID: "APPLE-123"}
	video := &PhotoRecord{ID: "v1", MediaType: "video", OriginalRef: "b-video", OriginalKey: "vidkey", contentID: "APPLE-123"}
	lone := &PhotoRecord{ID: "v2", MediaType: "video", OriginalRef: "b-lone", contentID: "OTHER"}
	_ = s.Add(still)
	_ = s.Add(video)
	_ = s.Add(lone)

	if n := s.MergeLivePhotos(); n != 1 {
		t.Fatalf("want 1 merge, got %d", n)
	}
	if still.MotionRef != "b-video" || still.MotionKey != "vidkey" {
		t.Fatalf("still did not gain the video as motion: %+v", still)
	}
	if !video.merged {
		t.Fatal("merged video should be flagged out")
	}
	if lone.merged {
		t.Fatal("unmatched video must remain its own record")
	}
}

func TestDownloadFilter(t *testing.T) {
	all := Filter{Images: true, Videos: true}
	if !all.Includes(PhotoRecord{MediaType: "image", TakenAt: "2021-01-01T00:00:00"}) {
		t.Fatal("image should be included")
	}
	if all.Includes(PhotoRecord{MediaType: "image", Trashed: "2021-01-02T00:00:00"}) {
		t.Fatal("trashed photo must be excluded")
	}

	imagesOnly := Filter{Images: true}
	if imagesOnly.Includes(PhotoRecord{MediaType: "video"}) {
		t.Fatal("--images must exclude videos")
	}
	videosOnly := Filter{Videos: true}
	if videosOnly.Includes(PhotoRecord{MediaType: "image"}) {
		t.Fatal("--videos must exclude images")
	}

	ranged := Filter{
		Images: true, Videos: true,
		From: time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2021, 6, 30, 23, 59, 59, 0, time.UTC),
	}
	if !ranged.Includes(PhotoRecord{MediaType: "image", TakenAt: "2021-06-15T12:00:00"}) {
		t.Fatal("in-range photo should be included")
	}
	if ranged.Includes(PhotoRecord{MediaType: "image", TakenAt: "2021-07-01T00:00:00"}) {
		t.Fatal("out-of-range photo must be excluded")
	}
	if ranged.Includes(PhotoRecord{MediaType: "image", TakenAt: ""}) {
		t.Fatal("undated photo must be excluded when a date bound is set")
	}
}

func TestDownloadPlanDisambiguatesNames(t *testing.T) {
	recs := []PhotoRecord{
		{ID: "aaaaaaaa1111", Name: "IMG_1.jpg", MediaType: "image"},
		{ID: "bbbbbbbb2222", Name: "IMG_1.jpg", MediaType: "image"}, // same name → disambiguate
		{ID: "cccccccc3333", Name: "IMG_2.jpg", MediaType: "image"}, // unique name → kept as-is
		{ID: "dddddddd4444", Name: "trashed.jpg", MediaType: "image", Trashed: "2021-01-01T00:00:00"},
	}
	targets := Plan(recs, "/out", Filter{Images: true, Videos: true})
	if len(targets) != 3 {
		t.Fatalf("want 3 targets (trashed excluded), got %d", len(targets))
	}
	paths := map[string]bool{}
	for _, tg := range targets {
		if paths[tg.Path] {
			t.Fatalf("duplicate target path: %s", tg.Path)
		}
		paths[tg.Path] = true
	}
	if !paths["/out/IMG_2.jpg"] {
		t.Fatal("uniquely-named photo should keep its name")
	}
	if !paths["/out/IMG_1_aaaaaaaa.jpg"] || !paths["/out/IMG_1_bbbbbbbb.jpg"] {
		t.Fatalf("colliding names should be disambiguated by id: %+v", paths)
	}
}

// addBlob encrypts plaintext to the vault key and injects it under a fixed id,
// returning the ref/key a record would carry.
func (m *mockServer) addBlob(t *testing.T, plaintext []byte) (ref, key string) {
	t.Helper()
	blob, encKey, err := crypto.EncryptContent(plaintext, m.vk)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.nextID++
	ref = "inj-" + itoa(m.nextID)
	m.blobs[ref] = blob
	m.mu.Unlock()
	return ref, encKey
}

func TestFetchMotionAndMeta(t *testing.T) {
	const pass = "pw"
	m := newMockServer(t, pass)
	client := m.client(t)
	ctx := context.Background()

	motionRef, motionKey := m.addBlob(t, []byte("MOTION-VIDEO-BYTES"))
	metaRef, metaKey := m.addBlob(t, []byte(`{"content_id":"11112222-3333-4444-5555-666677778888"}`))

	rec := PhotoRecord{
		MotionRef: motionRef, MotionKey: motionKey,
		MetaRef: metaRef, MetaKey: metaKey,
	}

	motion, err := FetchMotion(ctx, client, m.vk, rec)
	if err != nil {
		t.Fatalf("FetchMotion: %v", err)
	}
	if string(motion) != "MOTION-VIDEO-BYTES" {
		t.Fatalf("motion bytes = %q", motion)
	}

	cid, err := FetchMeta(ctx, client, m.vk, rec)
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}
	if cid != "11112222-3333-4444-5555-666677778888" {
		t.Fatalf("content id = %q", cid)
	}
}

func TestFetchMetaNoContentID(t *testing.T) {
	const pass = "pw"
	m := newMockServer(t, pass)
	client := m.client(t)
	metaRef, metaKey := m.addBlob(t, []byte(`{"content_id":null}`))
	cid, err := FetchMeta(context.Background(), client, m.vk,
		PhotoRecord{MetaRef: metaRef, MetaKey: metaKey})
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}
	if cid != "" {
		t.Fatalf("want empty content id, got %q", cid)
	}
}

// helpers

func readAll(r interface{ Read([]byte) (int, error) }) []byte {
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			return out
		}
	}
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

// TestPartialUploadNoEgress verifies the §8.1/§8.2 default: with derivation off,
// the CLI writes a partial record (basics + thumbPending, no renditions/meta) and
// never calls /process — no plaintext leaves the machine. The record round-trips
// through the v3 sharded store.
func TestPartialUploadNoEgress(t *testing.T) {
	const pass = "correct horse battery staple"
	m := newMockServer(t, pass)
	client := m.client(t)
	ctx := context.Background()

	vk, err := vault.Unlock(ctx, client, pass)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}

	dir := t.TempDir()
	photoPath := filepath.Join(dir, "IMG_9999.jpg")
	original := []byte("PARTIAL-PHOTO-BYTES")
	if err := os.WriteFile(photoPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	up := NewUploader(client, store, vk, false, false, nil) // process off, no ML

	outcome, rec, err := up.Upload(ctx, Item{StillPath: photoPath}, original)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if outcome != Uploaded {
		t.Fatalf("outcome = %v", outcome)
	}
	if !rec.ThumbPending {
		t.Fatal("partial record must set thumbPending")
	}
	if rec.ThumbRef != "" || rec.MediumRef != "" || rec.MetaRef != "" {
		t.Fatalf("partial record must have no derived refs: %+v", rec)
	}

	if err := store.Save(ctx); err != nil {
		t.Fatalf("save: %v", err)
	}

	fresh := NewStore(client, vk)
	if err := fresh.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fresh.basePhotos) != 1 {
		t.Fatalf("want 1 photo, got %d", len(fresh.basePhotos))
	}
	// The persisted partial record carries thumbPending:true and the basics, but
	// none of the derived fields (they are absent, not null).
	var raw map[string]any
	if err := json.Unmarshal(fresh.basePhotos[0], &raw); err != nil {
		t.Fatal(err)
	}
	if raw["thumbPending"] != true {
		t.Fatalf("reloaded partial missing thumbPending:true, got %v", raw["thumbPending"])
	}
	for _, k := range []string{"thumbRef", "metaRef", "lat", "camera", "hasFaces"} {
		if _, present := raw[k]; present {
			t.Fatalf("partial record should omit %q, got %v", k, raw[k])
		}
	}
	if raw["media_type"] != "image" || raw["sig"] == "" {
		t.Fatalf("partial record lost its basics: %v", raw)
	}
}
