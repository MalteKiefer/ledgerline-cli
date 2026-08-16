package cmd

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func meHandler(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte(`{"user":{"id":9,"name":"Grace","email":"grace@example.com"},"usage":{"files":0,"gallery":0}}`))
}

func TestFilesRenameCommand(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/files/entries/5/show", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"file":{"id":5,"name":"old.txt","version":1}}`))
	})
	mux.HandleFunc("/api/v1/files/entries/5", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Fatalf("method = %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"file":{"id":5,"name":"new.txt","version":2}}`))
	})
	loggedInRoot(t, mux)

	out := run(t, "files", "rename", "5", "new.txt")
	if !strings.Contains(out, `Renamed to "new.txt"`) {
		t.Fatalf("output = %q", out)
	}
}

func TestFilesMvCommand(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/files/entries/5/show", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"file":{"id":5,"name":"a.txt","version":1}}`))
	})
	var putBody string
	mux.HandleFunc("/api/v1/files/entries/5", func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		putBody = string(buf)
		_, _ = w.Write([]byte(`{"file":{"id":5,"name":"a.txt","version":2}}`))
	})
	loggedInRoot(t, mux)

	out := run(t, "files", "mv", "5", "--to", "3")
	if !strings.Contains(out, "Moved 1 file(s).") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(putBody, `"file_folder_id":3`) {
		t.Fatalf("PUT body = %q", putBody)
	}
}

func TestFilesFolderRenameCommand(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/files/folders/3", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Fatalf("method = %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"folder":{"id":3,"name":"renamed"}}`))
	})
	loggedInRoot(t, mux)

	out := run(t, "files", "folder", "rename", "3", "renamed")
	if !strings.Contains(out, `Renamed to "renamed"`) {
		t.Fatalf("output = %q", out)
	}
}

func TestFilesTrashLsAndRestore(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/files/trash", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[{"id":5,"name":"a.txt"}],"folders":[]}`))
	})
	mux.HandleFunc("/api/v1/files/entries/5/restore", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"file":{"id":5,"name":"a.txt"}}`))
	})
	loggedInRoot(t, mux)

	out := run(t, "files", "trash", "ls")
	if !strings.Contains(out, "a.txt") {
		t.Fatalf("ls output = %q", out)
	}
	out = run(t, "files", "trash", "restore", "5")
	if !strings.Contains(out, "Restored 1 item(s).") {
		t.Fatalf("restore output = %q", out)
	}
}

func TestFilesLabelsLsAndCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/files/labels", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"labels":[{"id":1,"name":"urgent","color":"#ff0000"}]}`))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"label":{"id":2,"name":"green","color":"#00ff00"}}`))
	})
	loggedInRoot(t, mux)

	out := run(t, "files", "labels", "ls")
	if !strings.Contains(out, "urgent") {
		t.Fatalf("ls output = %q", out)
	}
	out = run(t, "files", "labels", "create", "green", "--color", "#00ff00")
	if !strings.Contains(out, `Created label "green" (id 2).`) {
		t.Fatalf("create output = %q", out)
	}
}

func TestFilesSearchStatsActivityCommands(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/files/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "invoice" {
			t.Fatalf("q = %q", r.URL.Query().Get("q"))
		}
		_, _ = w.Write([]byte(`{"files":[{"id":1,"name":"invoice.pdf","size":100}]}`))
	})
	mux.HandleFunc("/api/v1/files/stats", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"used":1024,"by_type":{"application/pdf":1024},"duplicates":[]}`))
	})
	mux.HandleFunc("/api/v1/files/activity", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"activity":[{"id":1,"action":"upload","file_name":"invoice.pdf","created_at":"2026-08-16T00:00:00Z"}]}`))
	})
	loggedInRoot(t, mux)

	out := run(t, "files", "search", "invoice")
	if !strings.Contains(out, "invoice.pdf") {
		t.Fatalf("search output = %q", out)
	}
	out = run(t, "files", "stats")
	if !strings.Contains(out, "application/pdf") {
		t.Fatalf("stats output = %q", out)
	}
	out = run(t, "files", "activity")
	if !strings.Contains(out, "upload") || !strings.Contains(out, "invoice.pdf") {
		t.Fatalf("activity output = %q", out)
	}
}
