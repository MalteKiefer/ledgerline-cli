// Package ml runs machine-learning inference (CLIP embeddings and face
// detection) on a local immich-machine-learning instance, so a gallery upload
// can attach search embeddings and face data without using the server's ML
// service. The client speaks immich-ml's /predict endpoint directly; the models
// and detection threshold are configurable so the instance can be tuned.
package ml

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// isTransientMLErr reports whether a POST /predict transport error is worth
// retrying: a net timeout/connection error, an EOF, or a reused-then-closed
// keep-alive connection.
func isTransientMLErr(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"closed network connection", "connection reset", "broken pipe", "unexpected EOF", "EOF"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// predictTimeout bounds a single /predict call; model inference can be slow on
// CPU-only instances.
const predictTimeout = 3 * time.Minute

// cropPad expands each detected face box by this fraction on every side so the
// stored crop shows a little context around the face.
const cropPad = 0.15

// cropJPEGQuality is the quality of the re-encoded face crop.
const cropJPEGQuality = 90

// maxPredictResponseBytes bounds the /predict JSON so a misbehaving or hostile
// ML endpoint cannot stream an unbounded body and exhaust memory.
const maxPredictResponseBytes = 64 << 20 // 64 MiB

// maxImagePixels bounds the decoded rendition (a decompression-bomb guard): the
// pixel buffer is allocated from the header dimensions before decoding.
const maxImagePixels = 100 << 20 // 100 megapixels

// Face is one detected face: its detector score, box in analyzed-image pixels
// ([x1, y1, x2, y2]), recognition embedding, and a cropped JPEG of the face.
type Face struct {
	Score     float64
	Box       []float64
	Embedding []float64
	CropJPEG  []byte
}

// Result is the inference output for one image.
type Result struct {
	Embedding []float64 // CLIP visual embedding (nil when CLIP was not requested)
	Faces     []Face
}

// Analyzer runs inference on a JPEG rendition and returns embeddings and faces.
type Analyzer interface {
	Analyze(ctx context.Context, jpegData []byte) (Result, error)
}

// Immich talks to an immich-machine-learning instance.
type Immich struct {
	baseURL   string
	clipModel string
	faceModel string
	minScore  float64
	http      *http.Client
}

// NewImmich builds a client for the instance at baseURL. An empty clipModel or
// faceModel disables that task, so a tuned instance can run only what it serves.
func NewImmich(baseURL, clipModel, faceModel string, minScore float64) (*Immich, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid --ml-local URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("--ml-local URL must be http(s): %s", baseURL)
	}
	if clipModel == "" && faceModel == "" {
		return nil, fmt.Errorf("--ml-local needs at least one of --ml-clip-model or --ml-face-model")
	}
	return &Immich{
		baseURL:   u.String(),
		clipModel: clipModel,
		faceModel: faceModel,
		minScore:  minScore,
		http: &http.Client{
			Timeout: predictTimeout,
			// Refuse redirects: the request carries a decrypted image rendition,
			// which must not be replayed to a different host the user did not name.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("refusing redirect from the ML endpoint")
			},
		},
	}, nil
}

// entries builds the immich-ml "entries" pipeline description for the enabled
// tasks. The shape is task -> type -> {modelName, options}.
func (m *Immich) entries() map[string]any {
	e := map[string]any{}
	if m.clipModel != "" {
		e["clip"] = map[string]any{
			"visual": map[string]any{"modelName": m.clipModel},
		}
	}
	if m.faceModel != "" {
		e["facial-recognition"] = map[string]any{
			"detection":   map[string]any{"modelName": m.faceModel, "options": map[string]any{"minScore": m.minScore}},
			"recognition": map[string]any{"modelName": m.faceModel},
		}
	}
	return e
}

// predictResponse is immich-ml's /predict JSON, keyed by task. immich-ml returns
// embeddings as a string holding a JSON float array (e.g. "[0.1,-1.5,…]") and
// bounding-box coordinates as floats.
type predictResponse struct {
	Clip  json.RawMessage `json:"clip"`
	Faces []struct {
		BoundingBox struct {
			X1 float64 `json:"x1"`
			Y1 float64 `json:"y1"`
			X2 float64 `json:"x2"`
			Y2 float64 `json:"y2"`
		} `json:"boundingBox"`
		Embedding json.RawMessage `json:"embedding"`
		Score     float64         `json:"score"`
	} `json:"facial-recognition"`
}

// Analyze runs the configured tasks on jpegData and returns the embeddings and
// faces, cropping each detected face out of the same image.
func (m *Immich) Analyze(ctx context.Context, jpegData []byte) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, predictTimeout)
	defer cancel()

	entriesJSON, err := json.Marshal(m.entries())
	if err != nil {
		return Result{}, err
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("entries", string(entriesJSON)); err != nil {
		return Result{}, err
	}
	part, err := w.CreateFormFile("image", "image.jpg")
	if err != nil {
		return Result{}, err
	}
	if _, err := part.Write(jpegData); err != nil {
		return Result{}, err
	}
	if err := w.Close(); err != nil {
		return Result{}, err
	}

	bodyBytes := buf.Bytes()
	contentType := w.FormDataContentType()

	// The ML service keeps HTTP/1.1 keep-alive connections that it may close
	// between requests during a long import; a reused-then-closed connection
	// surfaces as "use of closed network connection". Retry such transport
	// failures with backoff (the request body is replayed from bodyBytes).
	const attempts = 4
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/predict", bytes.NewReader(bodyBytes))
		if rerr != nil {
			return Result{}, rerr
		}
		req.Header.Set("Content-Type", contentType)

		var derr error
		resp, derr = m.http.Do(req)
		if derr == nil {
			break
		}
		if attempt >= attempts-1 || ctx.Err() != nil || !isTransientMLErr(derr) {
			return Result{}, fmt.Errorf("call local ML: %w", derr)
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("local ML returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPredictResponseBytes))
	if err != nil {
		return Result{}, fmt.Errorf("read local ML response: %w", err)
	}
	var pr predictResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return Result{}, fmt.Errorf("decode local ML response: %w", err)
	}

	res := Result{Embedding: parseEmbedding(pr.Clip)}
	if len(pr.Faces) > 0 {
		// Face crops are best-effort: the ML service decodes formats the Go
		// client cannot (e.g. HEIC/HEIF from Apple devices). If the original
		// won't decode here, keep each face's box + embedding (the parts that
		// power search and clustering) and simply omit the crop image, rather
		// than failing the whole asset's import.
		img, derr := decodeImageBounded(jpegData)
		for _, f := range pr.Faces {
			emb := parseEmbedding(f.Embedding)
			if len(emb) == 0 {
				continue
			}
			bb := f.BoundingBox
			if !finiteBox(bb.X1, bb.Y1, bb.X2, bb.Y2) {
				continue
			}
			box := []float64{bb.X1, bb.Y1, bb.X2, bb.Y2}
			var crop []byte
			if derr == nil {
				if c, cerr := cropFace(img, int(bb.X1), int(bb.Y1), int(bb.X2), int(bb.Y2)); cerr == nil {
					crop = c
				}
			}
			res.Faces = append(res.Faces, Face{Score: f.Score, Box: box, Embedding: emb, CropJPEG: crop})
		}
	}
	return res, nil
}

// decodeImageBounded decodes a JPEG after rejecting one whose header declares an
// implausibly large pixel count (a decompression bomb allocates from the header
// dimensions before the pixels are read).
func decodeImageBounded(data []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return nil, fmt.Errorf("rendition too large: %dx%d", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// finiteBox reports whether every coordinate is a finite number, so a NaN/Inf
// from the ML endpoint never reaches the integer conversion in cropFace.
func finiteBox(vals ...float64) bool {
	for _, v := range vals {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// parseEmbedding decodes an immich-ml embedding. Current builds return a string
// holding a JSON float array (e.g. "[0.1,-1.5,…]"); a bare JSON array and a
// base64 float32 string are also accepted for forward/backward compatibility. It
// returns nil when absent or unparseable.
func parseEmbedding(raw json.RawMessage) []float64 {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil
		}
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "[") {
			return decodeFloatArray([]byte(s))
		}
		emb, _ := decodeFloat32B64(s)
		return emb
	}
	return decodeFloatArray(raw)
}

// decodeFloatArray unmarshals a JSON number array into float64s (nil on error).
func decodeFloatArray(b []byte) []float64 {
	var nums []float64
	if err := json.Unmarshal(b, &nums); err != nil {
		return nil
	}
	return nums
}

// decodeFloat32B64 decodes a base64 little-endian float32 array into float64s.
func decodeFloat32B64(s string) ([]float64, bool) {
	if s == "" {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b)%4 != 0 {
		return nil, false
	}
	out := make([]float64, len(b)/4)
	for i := range out {
		bits := binary.LittleEndian.Uint32(b[i*4:])
		out[i] = float64(math.Float32frombits(bits))
	}
	return out, true
}

// cropFace crops the face box (padded and clamped to the image) and returns it
// as a JPEG.
func cropFace(img image.Image, x1, y1, x2, y2 int) ([]byte, error) {
	b := img.Bounds()
	padX := int(float64(x2-x1) * cropPad)
	padY := int(float64(y2-y1) * cropPad)
	x1, y1 = clamp(x1-padX, b.Min.X, b.Max.X), clamp(y1-padY, b.Min.Y, b.Max.Y)
	x2, y2 = clamp(x2+padX, b.Min.X, b.Max.X), clamp(y2+padY, b.Min.Y, b.Max.Y)
	if x2 <= x1 || y2 <= y1 {
		return nil, fmt.Errorf("degenerate face box")
	}

	// Every image the standard JPEG decoder produces supports SubImage, giving a
	// zero-copy crop.
	sub, ok := img.(interface {
		SubImage(r image.Rectangle) image.Image
	})
	if !ok {
		return nil, fmt.Errorf("image type does not support cropping")
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, sub.SubImage(image.Rect(x1, y1, x2, y2)), &jpeg.Options{Quality: cropJPEGQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// clamp constrains v to [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
