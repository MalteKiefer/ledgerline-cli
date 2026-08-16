package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFilesLabelsCRUD(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/files/labels":
			_, _ = w.Write([]byte(`{"labels":[{"id":1,"name":"urgent","color":"#ff0000"}]}`))
		case r.Method == "POST" && r.URL.Path == "/api/v1/files/labels":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["color"] != "#00ff00" {
				t.Fatalf("body = %v", body)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"label":{"id":2,"name":"green","color":"#00ff00"}}`))
		case r.Method == "PUT" && r.URL.Path == "/api/v1/files/labels/2":
			_, _ = w.Write([]byte(`{"label":{"id":2,"name":"renamed","color":"#00ff00"}}`))
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/files/labels/2":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := testClient(t, srv)
	labels, err := c.FilesLabels(context.Background())
	if err != nil || len(labels) != 1 {
		t.Fatalf("labels=%+v err=%v", labels, err)
	}
	label, err := c.CreateLabel(context.Background(), "green", "#00ff00")
	if err != nil || label.ID != 2 {
		t.Fatalf("label=%+v err=%v", label, err)
	}
	label, err = c.UpdateLabel(context.Background(), 2, "renamed", "")
	if err != nil || label.Name != "renamed" {
		t.Fatalf("label=%+v err=%v", label, err)
	}
	if err := c.DeleteLabel(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 4 {
		t.Fatalf("seen = %v", seen)
	}
}

func TestSetFileLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/files/entries/5/labels" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		ids := body["label_ids"].([]any)
		if len(ids) != 2 {
			t.Fatalf("body = %v", body)
		}
		_, _ = w.Write([]byte(`{"file":{"id":5,"labels":[{"id":1,"name":"a"},{"id":2,"name":"b"}]}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	f, err := c.SetFileLabels(context.Background(), 5, []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Labels) != 2 {
		t.Fatalf("file = %+v", f)
	}
}
