package gallery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/vault"
)

// immichLib emulates the subset of the Immich REST API the importer speaks:
// the health ping, search/metadata enumeration (single page), and the original
// download. It counts /original GETs per asset so a test can assert a resumed
// run downloads nothing. The x-api-key header is verified (and never echoed).
type immichLib struct {
	srv    *httptest.Server
	apiKey string

	mu         sync.Mutex
	assets     []ImmichAsset     // timeline-enumerated stills (motion halves excluded)
	originals  map[string][]byte // assetID (incl. motion) -> original bytes
	downloads  int               // total /original GETs
	dlByID     map[string]int    // per-asset /original GETs
	onDownload func(count int)   // test hook fired after each successful /original write
}

func newImmichLib(t *testing.T, apiKey string, assets []ImmichAsset, originals map[string][]byte) *immichLib {
	t.Helper()
	m := &immichLib{apiKey: apiKey, assets: assets, originals: originals, dlByID: map[string]int{}}

	mux := http.NewServeMux()
	// Every request must carry the API key; a wrong/absent key is a 401 (also
	// proves the key is actually being sent on the header, never in the URL).
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("x-api-key") != m.apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}

	mux.HandleFunc("/api/server/ping", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Write([]byte(`{"res":"pong"}`))
	})
	mux.HandleFunc("/api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		// The whole (small) library fits in one page; nextPage:null ends the loop.
		m.mu.Lock()
		items := m.assets
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"assets": map[string]any{"items": items, "nextPage": nil},
		})
	})
	mux.HandleFunc("/api/assets/", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		// Path is /api/assets/{id}/original.
		rest := strings.TrimPrefix(r.URL.Path, "/api/assets/")
		id := strings.TrimSuffix(rest, "/original")
		m.mu.Lock()
		data, ok := m.originals[id]
		if ok {
			m.downloads++
			m.dlByID[id]++
		}
		count := m.downloads
		hook := m.onDownload
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(data)
		if hook != nil {
			hook(count) // e.g. cancel the run after the Nth asset is fetched
		}
	})

	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *immichLib) downloadCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.downloads
}

// setOnDownload installs (or clears with nil) the per-download test hook.
func (m *immichLib) setOnDownload(fn func(count int)) {
	m.mu.Lock()
	m.onDownload = fn
	m.mu.Unlock()
}

// importFixture wires the two halves an importer test needs: the Immich source
// mock and the Ledgerline vault+gallery mock with a loaded store/uploader (the
// no-egress import path: no /process, no ML). Config + temp dirs are test-scoped
// (t.TempDir + TMPDIR) so per-server ledgers and per-batch temp dirs never leak
// between tests. The import ledger is opened per run by each test (it is Close-d
// and reopened across a resume), so it is not held here.
type importFixture struct {
	imm    *immichLib
	m      *mockServer
	client *ImmichClient
	api    *api.Client
	vk     []byte
	store  *Store
	up     *Uploader
}

func newImportFixture(t *testing.T, assets []ImmichAsset, originals map[string][]byte) *importFixture {
	t.Helper()
	const (
		pass   = "correct horse battery staple"
		apiKey = "immich-secret-key"
	)
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())

	imm := newImmichLib(t, apiKey, assets, originals)
	client, err := NewImmichClient(imm.srv.URL, apiKey, imm.srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	m := newMockServer(t, pass)
	apiClient := m.client(t)
	ctx := context.Background()
	vk, err := vault.Unlock(ctx, apiClient, pass)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	store := NewStore(apiClient, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	up := NewUploader(apiClient, store, vk, false, false, nil) // no /process, no ML
	return &importFixture{imm: imm, m: m, client: client, api: apiClient, vk: vk, store: store, up: up}
}

// reloadRecords re-opens the gallery manifest exactly as a fresh client would and
// returns its photo records, so a test can assert what actually landed on disk.
func (f *importFixture) reloadRecords(t *testing.T) []PhotoRecord {
	t.Helper()
	fresh := NewStore(f.api, f.vk)
	if err := fresh.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	recs := make([]PhotoRecord, 0, len(fresh.basePhotos))
	for _, raw := range fresh.basePhotos {
		var rec PhotoRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
	}
	return recs
}

// still builds a minimal timeline IMAGE asset with a distinct checksum.
func still(id, name string) ImmichAsset {
	return ImmichAsset{ID: id, OriginalFileName: name, Checksum: "chk-" + id, Type: "IMAGE"}
}

// TestImmichImportPerAssetFailureRetries drives the resume-critical partial-failure
// branch: one asset's original is missing (404), so its download fails. It asserts
// the failure is counted, the siblings still import, and — crucially — the failed
// asset is NOT ledgered, so a re-run (once its bytes exist) retries only it.
func TestImmichImportPerAssetFailureRetries(t *testing.T) {
	assets := []ImmichAsset{
		still("asset-0", "IMG_0.jpg"),
		still("asset-1", "IMG_1.jpg"),
		still("asset-2", "IMG_2.jpg"),
	}
	originals := map[string][]byte{
		"asset-0": []byte("BYTES-0"),
		// asset-1 has no original → its download 404s (a per-asset failure).
		"asset-2": []byte("BYTES-2"),
	}
	f := newImportFixture(t, assets, originals)

	ledger, err := OpenImportLedger(f.imm.srv.URL)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	stats, err := RunImmichImport(context.Background(), f.up, f.store, f.client, ledger,
		ImportOptions{Jobs: 1, Batch: 3}, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stats.Imported != 2 || stats.Failed != 1 || stats.Skipped != 0 || stats.Duplicate != 0 {
		t.Fatalf("run 1 stats = %+v, want Imported=2 Failed=1", stats)
	}
	if !ledger.Has("asset-0") || !ledger.Has("asset-2") {
		t.Fatal("successful assets must be recorded in the ledger")
	}
	if ledger.Has("asset-1") {
		t.Fatal("a failed asset must NOT be recorded in the ledger (so a re-run retries it)")
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(f.reloadRecords(t)); got != 2 {
		t.Fatalf("reloaded manifest has %d records, want 2", got)
	}

	// Resume: give the previously-missing asset its bytes; a re-run retries ONLY it.
	f.imm.mu.Lock()
	f.imm.originals["asset-1"] = []byte("BYTES-1")
	f.imm.mu.Unlock()

	ledger2, err := OpenImportLedger(f.imm.srv.URL)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	defer ledger2.Close()
	stats2, err := RunImmichImport(context.Background(), f.up, f.store, f.client, ledger2,
		ImportOptions{Jobs: 1, Batch: 3}, nil)
	if err != nil {
		t.Fatalf("import (run 2): %v", err)
	}
	if stats2.Imported != 1 || stats2.Skipped != 2 || stats2.Failed != 0 {
		t.Fatalf("run 2 stats = %+v, want Imported=1 Skipped=2", stats2)
	}
	if got := len(f.reloadRecords(t)); got != 3 {
		t.Fatalf("after resume the manifest has %d records, want 3", got)
	}
}

// TestImmichImportLedgerlineAuthFatalAborts covers the Ledgerline auth-fatal path
// (design §7): a 401 from the uploader mid-run latches a fatal error that aborts
// the run after the batch barrier, while the batch that completed before it stays
// in the ledger so a re-run resumes.
func TestImmichImportLedgerlineAuthFatalAborts(t *testing.T) {
	assets := []ImmichAsset{
		still("asset-0", "IMG_0.jpg"),
		still("asset-1", "IMG_1.jpg"),
	}
	originals := map[string][]byte{
		"asset-0": []byte("BYTES-0"),
		"asset-1": []byte("BYTES-1"),
	}
	f := newImportFixture(t, assets, originals)

	// 401 every Ledgerline upload once the store has been saved once: batch 0
	// (asset-0) imports cleanly, then batch 1 (asset-1) hits the auth-fatal.
	f.m.mu.Lock()
	f.m.failUploadAfterSave = true
	f.m.mu.Unlock()

	ledger, err := OpenImportLedger(f.imm.srv.URL)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer ledger.Close()
	stats, err := RunImmichImport(context.Background(), f.up, f.store, f.client, ledger,
		ImportOptions{Jobs: 1, Batch: 1}, nil)
	if err == nil {
		t.Fatal("a Ledgerline 401 mid-run must abort with an error")
	}
	if api.Status(err) != http.StatusUnauthorized {
		t.Fatalf("abort error = %v, want a Ledgerline 401", err)
	}
	if stats.Imported != 1 || stats.Failed != 1 {
		t.Fatalf("stats = %+v, want Imported=1 (batch 0) Failed=1 (the 401 asset)", stats)
	}
	if !ledger.Has("asset-0") {
		t.Fatal("the batch completed before the 401 must remain in the ledger (resume)")
	}
	if ledger.Has("asset-1") {
		t.Fatal("the 401 asset must not be ledgered")
	}
	if got := len(f.reloadRecords(t)); got != 1 {
		t.Fatalf("manifest has %d records, want 1 (only the pre-401 batch)", got)
	}
}

// TestImmichImportDuplicateAfterDownload covers the content-dedup Duplicate outcome
// (distinct from a ledger Skip): two assets with byte-identical originals. The
// second is a Duplicate — counted, not re-recorded as a photo — but still ledgered
// (with an empty record id) so a re-run treats it as done, not as new work.
func TestImmichImportDuplicateAfterDownload(t *testing.T) {
	assets := []ImmichAsset{
		still("asset-0", "IMG_0.jpg"),
		still("asset-1", "IMG_1.jpg"),
	}
	same := []byte("IDENTICAL-CONTENT-BYTES")
	originals := map[string][]byte{
		"asset-0": append([]byte(nil), same...),
		"asset-1": append([]byte(nil), same...), // byte-identical → content dedup
	}
	f := newImportFixture(t, assets, originals)

	ledger, err := OpenImportLedger(f.imm.srv.URL)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	// Jobs=1 so asset-0 registers its signature before asset-1 is processed.
	stats, err := RunImmichImport(context.Background(), f.up, f.store, f.client, ledger,
		ImportOptions{Jobs: 1, Batch: 2}, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stats.Imported != 1 || stats.Duplicate != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want Imported=1 Duplicate=1", stats)
	}
	if !ledger.Has("asset-0") || !ledger.Has("asset-1") {
		t.Fatal("both the original and its content-duplicate must be ledgered")
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(f.reloadRecords(t)); got != 1 {
		t.Fatalf("manifest has %d records, want 1 (the duplicate was skipped)", got)
	}

	// A re-run is a pure no-op: both ids are in the ledger, nothing is downloaded.
	dlBefore := f.imm.downloadCount()
	ledger2, err := OpenImportLedger(f.imm.srv.URL)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	defer ledger2.Close()
	stats2, err := RunImmichImport(context.Background(), f.up, f.store, f.client, ledger2,
		ImportOptions{Jobs: 1, Batch: 2}, nil)
	if err != nil {
		t.Fatalf("import (run 2): %v", err)
	}
	if stats2.Skipped != 2 || stats2.Imported != 0 || stats2.Duplicate != 0 {
		t.Fatalf("run 2 stats = %+v, want Skipped=2", stats2)
	}
	if got := f.imm.downloadCount() - dlBefore; got != 0 {
		t.Fatalf("run 2 downloaded %d, want 0", got)
	}
}

// TestImmichImportDryRun covers the --dry-run path: it enumerates and counts new
// vs already-imported assets without downloading anything or saving the store. A
// pre-ledgered asset counts as Skipped; the rest count as would-import (Imported).
func TestImmichImportDryRun(t *testing.T) {
	assets := []ImmichAsset{
		still("asset-0", "IMG_0.jpg"),
		still("asset-1", "IMG_1.jpg"),
		still("asset-2", "IMG_2.jpg"),
	}
	originals := map[string][]byte{
		"asset-0": []byte("B0"), "asset-1": []byte("B1"), "asset-2": []byte("B2"),
	}
	f := newImportFixture(t, assets, originals)

	// Pre-mark one asset as already imported so the dry run reports it as skipped.
	ledger, err := OpenImportLedger(f.imm.srv.URL)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	if err := ledger.Mark("asset-0", "chk-asset-0", "rec-0"); err != nil {
		t.Fatal(err)
	}

	f.m.mu.Lock()
	verBefore := f.m.version
	f.m.mu.Unlock()

	stats, err := RunImmichImport(context.Background(), f.up, f.store, f.client, ledger,
		ImportOptions{Jobs: 2, Batch: 2, DryRun: true}, nil)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if stats.Imported != 2 || stats.Skipped != 1 || stats.Failed != 0 || stats.Duplicate != 0 {
		t.Fatalf("dry-run stats = %+v, want Imported=2 (would-import) Skipped=1 (ledger hit)", stats)
	}
	if got := f.imm.downloadCount(); got != 0 {
		t.Fatalf("dry run downloaded %d assets, want 0", got)
	}
	f.m.mu.Lock()
	verAfter := f.m.version
	f.m.mu.Unlock()
	if verAfter != verBefore {
		t.Fatalf("dry run saved the store %d times, want 0", verAfter-verBefore)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(f.reloadRecords(t)); got != 0 {
		t.Fatalf("dry run wrote %d records, want 0", got)
	}
}

// TestImmichImportExcludesEnumeratedMotionHalf covers the belt-and-suspenders
// exclusion (design §4.2): if a server also enumerates a Live Photo's motion half
// as a standalone timeline asset, the importer skips it — the motion is folded into
// its still, never imported as its own photo.
func TestImmichImportExcludesEnumeratedMotionHalf(t *testing.T) {
	live := still("asset-0", "IMG_LIVE.HEIC")
	live.LivePhotoVideoID = "motion-1"
	motion := still("motion-1", "motion.mov")
	motion.Type = "VIDEO"
	assets := []ImmichAsset{live, motion} // the motion half wrongly enumerated too
	originals := map[string][]byte{
		"asset-0":  []byte("LIVE-STILL-BYTES"),
		"motion-1": []byte("MOTION-VIDEO-BYTES"),
	}
	f := newImportFixture(t, assets, originals)

	ledger, err := OpenImportLedger(f.imm.srv.URL)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer ledger.Close()
	stats, err := RunImmichImport(context.Background(), f.up, f.store, f.client, ledger,
		ImportOptions{Jobs: 1, Batch: 2}, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stats.Imported != 1 || stats.Skipped != 0 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want Imported=1 (the still only)", stats)
	}
	if ledger.Has("motion-1") {
		t.Fatal("the motion half must not be imported/ledgered as a standalone asset")
	}
	recs := f.reloadRecords(t)
	if len(recs) != 1 {
		t.Fatalf("manifest has %d records, want 1 (motion half excluded)", len(recs))
	}
	if recs[0].MotionRef == "" {
		t.Fatal("the Live Photo still should carry its motion clip")
	}
}

// TestImmichImportMotionFailureFailsWholeAsset covers the Live-Photo atomicity fix:
// when a still's motion half can't be downloaded, the WHOLE asset fails (is not
// ledgered) rather than importing a bare still — so a re-run re-fetches the pair
// instead of the motion being lost to content-dedup on the re-uploaded still.
func TestImmichImportMotionFailureFailsWholeAsset(t *testing.T) {
	live := still("asset-0", "IMG_LIVE.HEIC")
	live.LivePhotoVideoID = "motion-1"
	assets := []ImmichAsset{live}
	originals := map[string][]byte{
		"asset-0": []byte("LIVE-STILL-BYTES"),
		// motion-1 is absent → its download 404s.
	}
	f := newImportFixture(t, assets, originals)

	ledger, err := OpenImportLedger(f.imm.srv.URL)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	stats, err := RunImmichImport(context.Background(), f.up, f.store, f.client, ledger,
		ImportOptions{Jobs: 1, Batch: 1}, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stats.Imported != 0 || stats.Failed != 1 {
		t.Fatalf("stats = %+v, want Imported=0 Failed=1 (the still must not import without its motion)", stats)
	}
	if ledger.Has("asset-0") {
		t.Fatal("a Live Photo whose motion failed must not be ledgered (so a re-run retries the pair)")
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(f.reloadRecords(t)); got != 0 {
		t.Fatalf("a bare still must not have been written: %d records", got)
	}

	// Resume: once the motion exists, the whole Live Photo imports atomically.
	f.imm.mu.Lock()
	f.imm.originals["motion-1"] = []byte("MOTION-VIDEO-BYTES")
	f.imm.mu.Unlock()

	ledger2, err := OpenImportLedger(f.imm.srv.URL)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	defer ledger2.Close()
	stats2, err := RunImmichImport(context.Background(), f.up, f.store, f.client, ledger2,
		ImportOptions{Jobs: 1, Batch: 1}, nil)
	if err != nil {
		t.Fatalf("import (run 2): %v", err)
	}
	if stats2.Imported != 1 || stats2.Failed != 0 {
		t.Fatalf("run 2 stats = %+v, want Imported=1", stats2)
	}
	recs := f.reloadRecords(t)
	if len(recs) != 1 || recs[0].MotionRef == "" {
		t.Fatalf("resumed Live Photo should carry its motion clip: %+v", recs)
	}
}

// TestImmichImportCancellationIsDurableAndResumable covers the named design
// requirement (§7): Ctrl-C leaves a consistent ledger + saved manifest. Cancelling
// once the second asset is fetched lands the cancellation while batch 1 is in
// flight, so the post-cancel checkpoint save (context.WithoutCancel) is the
// property under test — reverting it to a plain ctx would make batch 1's Save fail
// and the run return that error instead of context.Canceled, and would drop the
// durable record. It then asserts the interrupted run resumes cleanly.
func TestImmichImportCancellationIsDurableAndResumable(t *testing.T) {
	assets := []ImmichAsset{
		still("asset-0", "IMG_0.jpg"),
		still("asset-1", "IMG_1.jpg"),
		still("asset-2", "IMG_2.jpg"),
	}
	originals := map[string][]byte{
		"asset-0": []byte("BYTES-0"), "asset-1": []byte("BYTES-1"), "asset-2": []byte("BYTES-2"),
	}
	f := newImportFixture(t, assets, originals)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel once the SECOND original has been fetched: batch 0 (asset-0) is by then
	// fully imported + checkpoint-saved, and the cancellation lands during batch 1.
	f.imm.setOnDownload(func(count int) {
		if count >= 2 {
			cancel()
		}
	})

	ledger, err := OpenImportLedger(f.imm.srv.URL)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	stats, err := RunImmichImport(ctx, f.up, f.store, f.client, ledger,
		ImportOptions{Jobs: 1, Batch: 1}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run returned err = %v, want context.Canceled", err)
	}
	if stats.Imported != 1 {
		t.Fatalf("stats = %+v, want Imported=1 (only the pre-cancel batch)", stats)
	}
	if !ledger.Has("asset-0") {
		t.Fatal("the completed batch must survive cancellation in the ledger")
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(f.reloadRecords(t)); got != 1 {
		t.Fatalf("post-cancel manifest has %d records, want 1 (durable checkpoint)", got)
	}

	// Resume with a fresh context: the remaining assets import, asset-0 is skipped.
	f.imm.setOnDownload(nil)
	ledger2, err := OpenImportLedger(f.imm.srv.URL)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	defer ledger2.Close()
	stats2, err := RunImmichImport(context.Background(), f.up, f.store, f.client, ledger2,
		ImportOptions{Jobs: 1, Batch: 1}, nil)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if stats2.Imported != 2 || stats2.Skipped != 1 {
		t.Fatalf("resume stats = %+v, want Imported=2 Skipped=1", stats2)
	}
	if got := len(f.reloadRecords(t)); got != 3 {
		t.Fatalf("after resume the manifest has %d records, want 3", got)
	}
}

// TestRunImmichImportBatchesResumeAndLivePhoto drives RunImmichImport end to end
// against the Immich mock and the existing gallery mock server: N stills (one a
// Live Photo with a paired motion clip) are imported in batches of B, and then a
// second run is a pure no-op driven off the ledger. It asserts:
//   - exactly ceil(N/B) checkpoint saves (one store PUT per batch),
//   - every still (and the motion half) is downloaded once, none twice,
//   - the per-batch temp dirs are cleaned up,
//   - all N records land in the reloaded manifest with the Live Photo carrying
//     a MotionRef,
//   - the second run downloads nothing and skips all N (ledger resume).
//
// Run under -race to catch a regression in the worker pool / shared tally.
func TestRunImmichImportBatchesResumeAndLivePhoto(t *testing.T) {
	const (
		pass   = "correct horse battery staple"
		apiKey = "immich-secret-key"
		N      = 5
		B      = 2
	)
	wantSaves := (N + B - 1) / B // ceil(N/B) = 3

	// Isolate the import ledger and the per-batch temp dirs into test-scoped dirs.
	// TMPDIR redirects os.MkdirTemp("") so we can assert the batch dirs are gone.
	cfgDir := t.TempDir()
	tmpRoot := t.TempDir()
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", cfgDir)
	t.Setenv("TMPDIR", tmpRoot)

	// Build N stills; asset 0 is a Live Photo referencing a hidden motion clip.
	taken := time.Date(2021, 5, 1, 10, 0, 0, 0, time.UTC)
	lat, lon := 48.2, 16.4
	assets := make([]ImmichAsset, 0, N)
	originals := map[string][]byte{}
	for i := 0; i < N; i++ {
		id := "asset-" + itoa(i)
		a := ImmichAsset{
			ID:               id,
			OriginalFileName: "IMG_" + itoa(i) + ".jpg",
			Checksum:         "chk-" + itoa(i),
			Type:             "IMAGE",
			FileCreatedAt:    taken,
			LocalDateTime:    taken,
			Exif: &ImmichExif{
				Make: "TestCam", Model: "T1000",
				DateTimeOriginal: &taken, Latitude: &lat, Longitude: &lon,
				ExifImageWidth: 4, ExifImageHeight: 3,
			},
		}
		// Distinct bytes per asset so content-dedup never collapses two stills.
		originals[id] = []byte("STILL-BYTES-" + itoa(i))
		if i == 0 {
			a.OriginalFileName = "IMG_LIVE.HEIC"
			a.LivePhotoVideoID = "motion-1"
			originals["motion-1"] = []byte("MOTION-VIDEO-BYTES")
		}
		assets = append(assets, a)
	}

	imm := newImmichLib(t, apiKey, assets, originals)
	client, err := NewImmichClient(imm.srv.URL, apiKey, imm.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// Ledgerline side: the shared vault + gallery mock server.
	m := newMockServer(t, pass)
	apiClient := m.client(t)
	ctx := context.Background()
	vk, err := vault.Unlock(ctx, apiClient, pass)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}

	store := NewStore(apiClient, vk)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	up := NewUploader(apiClient, store, vk, false, false, nil) // no /process, no ML

	ledger, err := OpenImportLedger(imm.srv.URL)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}

	stats, err := RunImmichImport(ctx, up, store, client, ledger,
		ImportOptions{Jobs: 3, Batch: B}, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	if stats.Imported != N || stats.Skipped != 0 || stats.Duplicate != 0 || stats.Failed != 0 {
		t.Fatalf("run 1 stats = %+v, want Imported=%d and the rest 0", stats, N)
	}

	// Exactly ceil(N/B) checkpoint saves: the mock bumps its store version on
	// every successful PUT, and only Store.Save (once per batch) PUTs.
	m.mu.Lock()
	saves := m.version
	m.mu.Unlock()
	if saves != int64(wantSaves) {
		t.Fatalf("checkpoint saves = %d, want ceil(%d/%d) = %d", saves, N, B, wantSaves)
	}

	// Every still downloaded once; the motion half downloaded once; none twice.
	if got := imm.downloadCount(); got != N+1 {
		t.Fatalf("run 1 downloads = %d, want %d (N stills + 1 motion)", got, N+1)
	}
	imm.mu.Lock()
	for id, c := range imm.dlByID {
		if c != 1 {
			imm.mu.Unlock()
			t.Fatalf("asset %s downloaded %d times, want exactly 1", id, c)
		}
	}
	imm.mu.Unlock()

	// The per-batch temp dirs (os.MkdirTemp under TMPDIR) were all removed.
	assertNoTempBatchDirs(t, tmpRoot)

	// All N records landed, and the Live Photo carries a motion clip.
	fresh := NewStore(apiClient, vk)
	if err := fresh.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fresh.basePhotos) != N {
		t.Fatalf("reloaded manifest has %d records, want %d", len(fresh.basePhotos), N)
	}
	motion := 0
	for _, raw := range fresh.basePhotos {
		var rec PhotoRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		if rec.MotionRef != "" {
			motion++
		}
	}
	if motion != 1 {
		t.Fatalf("want exactly 1 record with a MotionRef (the Live Photo), got %d", motion)
	}

	// SECOND RUN: same server + ledger. Every asset is already in the ledger, so
	// nothing is downloaded and nothing is saved — a pure resume no-op.
	dlBefore := imm.downloadCount()
	m.mu.Lock()
	verBefore := m.version
	m.mu.Unlock()

	ledger2, err := OpenImportLedger(imm.srv.URL)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	defer ledger2.Close()
	stats2, err := RunImmichImport(ctx, up, store, client, ledger2,
		ImportOptions{Jobs: 3, Batch: B}, nil)
	if err != nil {
		t.Fatalf("import (run 2): %v", err)
	}
	if stats2.Skipped != N || stats2.Imported != 0 || stats2.Duplicate != 0 || stats2.Failed != 0 {
		t.Fatalf("run 2 stats = %+v, want Skipped=%d and the rest 0", stats2, N)
	}
	if got := imm.downloadCount(); got != dlBefore {
		t.Fatalf("run 2 performed %d downloads, want 0 (ledger resume)", got-dlBefore)
	}
	m.mu.Lock()
	verAfter := m.version
	m.mu.Unlock()
	if verAfter != verBefore {
		t.Fatalf("run 2 saved the store %d times, want 0", verAfter-verBefore)
	}
}

// assertNoTempBatchDirs fails if any "ledgerline-immich-*" batch dir remains
// under root, proving processBatch cleaned up every per-batch temp dir.
func assertNoTempBatchDirs(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "ledgerline-immich-") {
			t.Fatalf("leftover temp batch dir: %s", filepath.Join(root, e.Name()))
		}
	}
}
