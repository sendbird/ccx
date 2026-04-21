package session

import (
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractImageToTempJPEGReencodesAsStablePNG verifies two invariants of
// the image cache pipeline that Kitty rendering depends on:
//  1. JPEG source data is re-encoded as PNG on disk, because the Kitty f=100
//     protocol only accepts PNG file transfers.
//  2. The output path is stable across calls (same pasteID → same file),
//     so repeated renders don't pile up new temp files and confuse the
//     terminal's image cache.
func TestExtractImageToTempJPEGReencodesAsStablePNG(t *testing.T) {
	home := t.TempDir()
	sessID := "test-session"

	// Build a 2x2 JPEG and base64-encode it.
	jpegBytes := makeJPEG(t, 2, 2)
	encoded := base64.StdEncoding.EncodeToString(jpegBytes)

	// Emit a single-line JSONL session entry with the image block.
	entryJSON, err := json.Marshal(map[string]any{
		"imagePasteIds": []int{42},
		"message": map[string]any{
			"content": []map[string]any{
				{
					"type": "image",
					"source": map[string]any{
						"data":       encoded,
						"media_type": "image/jpeg",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}

	sessionFile := filepath.Join(home, "session.jsonl")
	if err := os.WriteFile(sessionFile, append(entryJSON, '\n'), 0644); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	path1, err := ExtractImageToTemp(home, sessionFile, sessID, 42)
	if err != nil {
		t.Fatalf("extract first call: %v", err)
	}
	if !strings.HasSuffix(path1, ".png") {
		t.Fatalf("expected .png suffix, got %q", path1)
	}
	if !strings.Contains(path1, filepath.Join("image-cache", sessID)) {
		t.Fatalf("expected cache-dir path, got %q", path1)
	}

	// The re-encoded file on disk must be actual PNG bytes so Kitty's f=100
	// transfer succeeds.
	onDisk, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("read cached file: %v", err)
	}
	if _, err := png.Decode(strings.NewReader(string(onDisk))); err != nil {
		t.Fatalf("cached file is not valid PNG: %v", err)
	}

	// Second call must return the same stable path; this is the property
	// that kept the picker from flickering: every render otherwise produced
	// a different temp path.
	path2, err := ExtractImageToTemp(home, sessionFile, sessID, 42)
	if err != nil {
		t.Fatalf("extract second call: %v", err)
	}
	if path1 != path2 {
		t.Fatalf("expected stable path across calls, got %q then %q", path1, path2)
	}
}

func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 50), uint8(y * 50), 200, 255})
		}
	}
	var buf strings.Builder
	if err := jpeg.Encode(asWriteString(&buf), img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return []byte(buf.String())
}

type writeString struct{ *strings.Builder }

func (w writeString) Write(p []byte) (int, error) { return w.Builder.Write(p) }

func asWriteString(b *strings.Builder) writeString { return writeString{b} }
