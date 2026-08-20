package files

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

// scopedServer serves a remote tree with two folders, so a sync scoped to one
// of them can be told apart from a sync of everything.
type scopedServer struct {
	folders  []api.FileFolder
	files    []api.FileEntry
	raw      map[int64]string
	uploaded []uploadRecord
	created  []string // folder names created during the run
}

type uploadRecord struct {
	name     string
	folderID string
}

func (s *scopedServer) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/files/data", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"folders": s.folders, "files": s.files, "usage": api.FilesUsage{},
		})
	})
	mux.HandleFunc("/api/v1/files/folders", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name     string `json:"name"`
			ParentID *int64 `json:"file_folder_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.created = append(s.created, body.Name)
		id := int64(500 + len(s.created))
		folder := api.FileFolder{ID: id, Name: body.Name, ParentID: body.ParentID}
		s.folders = append(s.folders, folder)
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"folder": folder})
	})
	mux.HandleFunc("/api/v1/files/entries", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		_, hdr, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		s.uploaded = append(s.uploaded, uploadRecord{name: hdr.Filename, folderID: r.FormValue("file_folder_id")})
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"file": api.FileEntry{ID: 999, Name: hdr.Filename}})
	})
	mux.HandleFunc("/api/v1/files/entries/", func(w http.ResponseWriter, r *http.Request) {
		var id int64
		if _, err := fmt.Sscanf(r.URL.Path, "/api/v1/files/entries/%d/raw", &id); err != nil {
			http.NotFound(w, r)
			return
		}
		if body, ok := s.raw[id]; ok {
			_, _ = io.WriteString(w, body)
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

// TestSyncScopedToRemoteFolder is the behaviour several sync pairs depend on:
// a pair must see only its own remote folder. Without scoping, a second local
// directory could only ever be a second copy of the whole tree.
func TestSyncScopedToRemoteFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.txt"), []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, diary, loose := sha("report"), sha("diary"), sha("loose")
	work := api.FileFolder{ID: 10, Name: "Work"}
	personal := api.FileFolder{ID: 20, Name: "Personal"}
	workID, personalID := work.ID, personal.ID

	ss := &scopedServer{
		folders: []api.FileFolder{work, personal},
		files: []api.FileEntry{
			{ID: 1, Name: "report.txt", FileFolderID: &workID, Sha256: &report, Size: 6},
			{ID: 2, Name: "diary.txt", FileFolderID: &personalID, Sha256: &diary, Size: 5},
			{ID: 3, Name: "loose.txt", Sha256: &loose, Size: 5},
		},
		raw: map[int64]string{1: "report", 2: "diary", 3: "loose"},
	}
	srv := httptest.NewServer(ss.handler(t))
	defer srv.Close()

	res, err := Sync(context.Background(), newClient(t, srv), dir,
		SyncOptions{RemoteRoot: "Work"}, io.Discard, false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Only the file inside Work is pulled: the other folder and the root file
	// belong to different pairs, or to none.
	if _, err := os.Stat(filepath.Join(dir, "report.txt")); err != nil {
		t.Fatalf("scoped file not pulled: %v", err)
	}
	for _, name := range []string{"diary.txt", "loose.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was pulled into a scoped sync", name)
		}
	}
	if res.Pulled != 1 {
		t.Fatalf("pulled = %d, want 1", res.Pulled)
	}

	// The local file lands inside the scope, not at the remote root.
	if len(ss.uploaded) != 1 || ss.uploaded[0].name != "local.txt" {
		t.Fatalf("uploads = %+v", ss.uploaded)
	}
	if ss.uploaded[0].folderID != "10" {
		t.Fatalf("uploaded into folder %q, want the scoped folder 10", ss.uploaded[0].folderID)
	}
	if len(ss.created) != 0 {
		t.Fatalf("created folders %v for a scope that already existed", ss.created)
	}
}

// TestSyncCreatesTheRemoteRootWhenMissing covers adding a pair for a folder
// that does not exist on the server yet.
func TestSyncCreatesTheRemoteRootWhenMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	ss := &scopedServer{raw: map[int64]string{}}
	srv := httptest.NewServer(ss.handler(t))
	defer srv.Close()

	if _, err := Sync(context.Background(), newClient(t, srv), dir,
		SyncOptions{RemoteRoot: "Archive/2026"}, io.Discard, false); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if len(ss.created) != 2 || ss.created[0] != "Archive" || ss.created[1] != "2026" {
		t.Fatalf("created = %v, want the chain Archive then 2026", ss.created)
	}
	if len(ss.uploaded) != 1 || ss.uploaded[0].folderID == "" {
		t.Fatalf("uploads = %+v, want one inside the created folder", ss.uploaded)
	}
}

// TestSyncRejectsAnEscapingRemoteRoot: a pair must not be able to climb out of
// the folder it was given.
func TestSyncRejectsAnEscapingRemoteRoot(t *testing.T) {
	ss := &scopedServer{raw: map[int64]string{}}
	srv := httptest.NewServer(ss.handler(t))
	defer srv.Close()

	_, err := Sync(context.Background(), newClient(t, srv), t.TempDir(),
		SyncOptions{RemoteRoot: "Work/../../etc"}, io.Discard, false)
	if err == nil {
		t.Fatal("an escaping remote root was accepted")
	}
}
