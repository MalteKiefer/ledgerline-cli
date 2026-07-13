package ml

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// float32B64 encodes floats as base64 little-endian float32, the way immich-ml
// serialises embeddings.
func float32B64(vals ...float32) string {
	buf := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// sampleJPEG returns a small solid JPEG for cropping tests.
func sampleJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImmichAnalyze(t *testing.T) {
	jpg := sampleJPEG(t, 200, 200)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/predict" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse form: %v", err)
		}
		// The entries field must describe both tasks.
		var entries map[string]any
		if err := json.Unmarshal([]byte(r.FormValue("entries")), &entries); err != nil {
			t.Errorf("entries json: %v", err)
		}
		if _, ok := entries["clip"]; !ok {
			t.Errorf("entries missing clip: %v", entries)
		}
		if _, ok := entries["facial-recognition"]; !ok {
			t.Errorf("entries missing facial-recognition: %v", entries)
		}
		resp := map[string]any{
			"clip": float32B64(0.1, 0.2, 0.3),
			"facial-recognition": []map[string]any{{
				"boundingBox": map[string]int{"x1": 40, "y1": 50, "x2": 120, "y2": 150},
				"embedding":   float32B64(1, 2, 3, 4),
				"score":       0.97,
			}},
			"imageHeight": 200,
			"imageWidth":  200,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	m, err := NewImmich(srv.URL, "ViT-B-32__openai", "buffalo_l", 0.7)
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Analyze(context.Background(), jpg)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	if len(res.Embedding) != 3 || math.Abs(res.Embedding[1]-0.2) > 1e-6 {
		t.Fatalf("clip embedding = %v", res.Embedding)
	}
	if len(res.Faces) != 1 {
		t.Fatalf("want 1 face, got %d", len(res.Faces))
	}
	f := res.Faces[0]
	if f.Score != 0.97 {
		t.Fatalf("score = %v", f.Score)
	}
	if len(f.Embedding) != 4 || math.Abs(f.Embedding[3]-4) > 1e-6 {
		t.Fatalf("face embedding = %v", f.Embedding)
	}
	wantBox := []float64{40, 50, 120, 150}
	for i, v := range wantBox {
		if f.Box[i] != v {
			t.Fatalf("box = %v, want %v", f.Box, wantBox)
		}
	}
	// The crop must be a valid JPEG bounded by the (padded) box within the image.
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(f.CropJPEG))
	if err != nil {
		t.Fatalf("crop is not a valid jpeg: %v", err)
	}
	if cfg.Width <= 0 || cfg.Width > 200 || cfg.Height <= 0 || cfg.Height > 200 {
		t.Fatalf("crop dimensions out of range: %dx%d", cfg.Width, cfg.Height)
	}
}

func TestParseEmbedding(t *testing.T) {
	if emb := parseEmbedding(json.RawMessage(`[1.5, 2.5]`)); len(emb) != 2 || emb[0] != 1.5 {
		t.Fatalf("array form: %v", emb)
	}
	b64, _ := json.Marshal(float32B64(3, 4))
	if emb := parseEmbedding(json.RawMessage(b64)); len(emb) != 2 || emb[1] != 4 {
		t.Fatalf("base64 form: %v", emb)
	}
	if emb := parseEmbedding(json.RawMessage(`null`)); emb != nil {
		t.Fatalf("null should be nil: %v", emb)
	}
}

func TestNewImmichValidation(t *testing.T) {
	if _, err := NewImmich("ftp://x", "clip", "", 0.7); err == nil {
		t.Fatal("non-http scheme should be rejected")
	}
	if _, err := NewImmich("http://localhost:3003", "", "", 0.7); err == nil {
		t.Fatal("no models should be rejected")
	}
	if _, err := NewImmich("http://localhost:3003", "clip", "", 0.7); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}
