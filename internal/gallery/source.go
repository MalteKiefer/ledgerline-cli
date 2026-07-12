package gallery

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxFileBytes guards reads against pathological inputs; the whole-file upload
// path is bounded by the server's gallery max anyway.
const maxFileBytes = 2 << 30 // 2 GiB

// imageExts and videoExts are the media extensions the uploader accepts. The set
// is deliberately broad — matching the web client, which accepts anything with an
// image/* or video/* MIME type — so phone/camera formats from every vendor go
// through; the server decides what it can actually decode and reports the rest.
var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".jpe": true, ".png": true, ".gif": true, ".webp": true,
	".bmp": true, ".tif": true, ".tiff": true, ".heic": true, ".heif": true, ".heics": true,
	".avif": true, ".jxl": true, ".jp2": true, ".j2k": true, ".dng": true,
	// Common camera RAW formats.
	".cr2": true, ".cr3": true, ".nef": true, ".nrw": true, ".arw": true, ".sr2": true,
	".srf": true, ".raf": true, ".rw2": true, ".orf": true, ".srw": true, ".pef": true,
	".dcr": true, ".kdc": true, ".x3f": true, ".3fr": true, ".mef": true, ".raw": true, ".rwl": true,
}
var videoExts = map[string]bool{
	".mov": true, ".mp4": true, ".m4v": true, ".avi": true, ".webm": true, ".mkv": true,
	".3gp": true, ".3g2": true, ".mts": true, ".m2ts": true, ".ts": true, ".mpg": true,
	".mpeg": true, ".mpe": true, ".wmv": true, ".flv": true, ".f4v": true, ".ogv": true,
	".vob": true, ".mxf": true, ".hevc": true, ".insv": true,
}

// isImageExt reports whether a filename has a known image extension.
func isImageExt(name string) bool { return imageExts[strings.ToLower(filepath.Ext(name))] }

// isVideoExtName reports whether a filename has a known video extension.
func isVideoExtName(name string) bool { return videoExts[strings.ToLower(filepath.Ext(name))] }

// isSupported reports whether a file is an image or video the gallery accepts,
// by extension first and then by sniffing the content for extensionless or
// unusually-named files (mirroring the web's MIME-based acceptance).
func isSupported(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if imageExts[ext] || videoExts[ext] {
		return true
	}
	return sniffedMedia(path)
}

// sniffedMedia peeks a file's first bytes and reports whether it looks like an
// image or a video, so files with missing or unknown extensions are still
// picked up.
func sniffedMedia(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	ct := http.DetectContentType(buf[:n])
	return strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "video/")
}

// CollectFolder walks root (optionally recursively) and returns upload items,
// pairing a still with a same-basename video as its Live Photo motion clip.
func CollectFolder(root string, recursive bool) ([]Item, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", root)
	}

	var files []string
	walk := func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if !recursive && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if isSupported(path) {
			files = append(files, path)
		}
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		return nil, err
	}
	sort.Strings(files)
	return pairItems(files, nil), nil
}

// pairItems groups files by directory+basename so a still and its same-named
// video merge into one Live Photo item. A standalone video becomes its own
// (video) item. takenAt, if non-nil, supplies sidecar capture times by path.
func pairItems(files []string, takenAt map[string]time.Time) []Item {
	type group struct {
		images []string
		videos []string
	}
	groups := map[string]*group{}
	var order []string
	keyOf := func(p string) string {
		dir := filepath.Dir(p)
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		return dir + "/" + strings.ToLower(base)
	}
	for _, f := range files {
		k := keyOf(f)
		g := groups[k]
		if g == nil {
			g = &group{}
			groups[k] = g
			order = append(order, k)
		}
		if isImageExt(f) {
			g.images = append(g.images, f)
		} else {
			g.videos = append(g.videos, f)
		}
	}

	var items []Item
	for _, k := range order {
		g := groups[k]
		switch {
		case len(g.images) > 0:
			// Each still is its own item; the first video (if any) is the motion
			// clip for the first still.
			for i, img := range g.images {
				it := Item{StillPath: img}
				if i == 0 && len(g.videos) > 0 {
					it.MotionPath = g.videos[0]
				}
				if takenAt != nil {
					it.SidecarTaken = takenAt[img]
				}
				items = append(items, it)
			}
		case len(g.videos) > 0:
			// Standalone video(s): upload as video photos.
			for _, v := range g.videos {
				it := Item{StillPath: v}
				if takenAt != nil {
					it.SidecarTaken = takenAt[v]
				}
				items = append(items, it)
			}
		}
	}
	return items
}

// CollectGooglePhotos extracts a Google Photos (Takeout) .zip into destDir and
// returns upload items, using the per-file JSON sidecars for capture times.
func CollectGooglePhotos(zipPath, destDir string) ([]Item, error) {
	if err := unzip(zipPath, destDir); err != nil {
		return nil, err
	}

	var media []string
	sidecars := map[string]time.Time{}
	walk := func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch {
		case isSupported(path):
			media = append(media, path)
		case strings.HasSuffix(strings.ToLower(path), ".json"):
			if t, mediaPath, ok := parseTakeoutSidecar(path); ok {
				sidecars[mediaPath] = t
			}
		}
		return nil
	}
	if err := filepath.WalkDir(destDir, walk); err != nil {
		return nil, err
	}
	sort.Strings(media)

	// Map sidecar capture times onto their media files by resolved path.
	takenAt := map[string]time.Time{}
	for _, m := range media {
		if t, ok := sidecars[m]; ok {
			takenAt[m] = t
		}
	}
	return pairItems(media, takenAt), nil
}

// parseTakeoutSidecar reads a Takeout metadata JSON and returns its capture time
// and the media file it describes (…/photo.jpg.json → …/photo.jpg).
func parseTakeoutSidecar(jsonPath string) (time.Time, string, bool) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return time.Time{}, "", false
	}
	var meta struct {
		PhotoTakenTime struct {
			Timestamp string `json:"timestamp"`
		} `json:"photoTakenTime"`
	}
	if err := json.Unmarshal(data, &meta); err != nil || meta.PhotoTakenTime.Timestamp == "" {
		return time.Time{}, "", false
	}
	secs, err := strconv.ParseInt(meta.PhotoTakenTime.Timestamp, 10, 64)
	if err != nil {
		return time.Time{}, "", false
	}
	// Strip the metadata suffix to recover the media path. Takeout uses either
	// "<media>.json" or "<media>.supplemental-metadata.json".
	base := strings.TrimSuffix(jsonPath, ".json")
	base = strings.TrimSuffix(base, ".supplemental-metadata")
	return time.Unix(secs, 0).UTC(), base, true
}

// unzip extracts a zip archive into dest, guarding against path traversal.
func unzip(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		target := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry escapes destination: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := extractOne(f, target); err != nil {
			return err
		}
	}
	return nil
}

// extractOne writes a single zip entry to target.
func extractOne(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, io.LimitReader(rc, maxFileBytes))
	return err
}

// readFileCapped reads a file, refusing anything larger than maxFileBytes.
func readFileCapped(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("%s is too large (%d bytes)", filepath.Base(path), info.Size())
	}
	return os.ReadFile(path)
}

// mimeFor maps a filename extension to a MIME type for the record and, crucially,
// for the process request's declared content type — the server keys "is this a
// video?" off it. Unknown-but-classified extensions fall back to a generic type
// of the right class so a video is never mistaken for an image.
func mimeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".jpe":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".heic", ".heics":
		return "image/heic"
	case ".heif":
		return "image/heif"
	case ".avif":
		return "image/avif"
	case ".jxl":
		return "image/jxl"
	case ".jp2", ".j2k":
		return "image/jp2"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".bmp":
		return "image/bmp"
	case ".dng":
		return "image/x-adobe-dng"
	case ".mov":
		return "video/quicktime"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".avi":
		return "video/x-msvideo"
	case ".webm":
		return "video/webm"
	case ".mkv":
		return "video/x-matroska"
	case ".3gp":
		return "video/3gpp"
	case ".3g2":
		return "video/3gpp2"
	case ".mts", ".m2ts", ".ts":
		return "video/mp2t"
	case ".mpg", ".mpeg", ".mpe":
		return "video/mpeg"
	case ".wmv":
		return "video/x-ms-wmv"
	case ".flv", ".f4v":
		return "video/x-flv"
	case ".ogv":
		return "video/ogg"
	default:
		if isVideoExtName(name) {
			return "video/mp4"
		}
		if isImageExt(name) {
			return "image/jpeg"
		}
		return "application/octet-stream"
	}
}
