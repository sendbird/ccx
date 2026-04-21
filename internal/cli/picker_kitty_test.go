package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sendbird/ccx/internal/extract"
)

// TestKittyImageLayerOnlyForImagesKind verifies the layer never draws for
// non-image pickers (it may emit a safe clear sequence but never a=T place).
func TestKittyImageLayerOnlyForImagesKind(t *testing.T) {
	t.Setenv("CCX_KITTY", "1")
	m := newPickerModel("urls", []PickerItem{
		{Item: extract.Item{URL: "https://example.com", Label: "example", Category: "url"}},
	})
	m.width = 120
	m.height = 40
	out := m.kittyImageLayer(m.height-2, m.listWidth(), m.previewWidth())
	if strings.Contains(out, "a=T") {
		t.Fatalf("expected no draw sequence for non-images picker, got %q", out)
	}
}

// TestKittyImageLayerClearsWhenPreviewFocused verifies we hide the image while
// the user is scrolling the preview viewport, so Kitty graphics don't cover
// the scrollable text.
func TestKittyImageLayerClearsWhenPreviewFocused(t *testing.T) {
	t.Setenv("CCX_KITTY", "1")
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "img.png")
	writePNG(t, imagePath)

	m := newPickerModel("images", []PickerItem{
		{Item: extract.Item{URL: imagePath, Label: "#1", Category: "image"}},
	})
	m.width = 120
	m.height = 40
	m.previewFocused = true
	out := m.kittyImageLayer(m.height-2, m.listWidth(), m.previewWidth())
	if !strings.Contains(out, "a=d") {
		t.Fatalf("expected clear sequence when preview focused, got %q", out)
	}
	if strings.Contains(out, "a=T") {
		t.Fatalf("did not expect draw sequence when preview focused, got %q", out)
	}
}

// TestKittyImageLayerEmitsDrawForImage asserts the happy path: a cached image
// file at the focused cursor produces a Kitty a=T place sequence.
func TestKittyImageLayerEmitsDrawForImage(t *testing.T) {
	t.Setenv("CCX_KITTY", "1")
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "img.png")
	writePNG(t, imagePath)

	m := newPickerModel("images", []PickerItem{
		{Item: extract.Item{URL: imagePath, Label: "#1", Category: "image"}},
	})
	m.width = 120
	m.height = 40
	out := m.kittyImageLayer(m.height-2, m.listWidth(), m.previewWidth())
	if !strings.Contains(out, "a=T") {
		t.Fatalf("expected place image sequence, got %q", out)
	}
}

// TestKittyImageLayerClearsWhenFileMissing covers the case where the cached
// image file has been garbage-collected between extraction and render.
func TestKittyImageLayerClearsWhenFileMissing(t *testing.T) {
	t.Setenv("CCX_KITTY", "1")
	m := newPickerModel("images", []PickerItem{
		{Item: extract.Item{URL: "/nonexistent/path.png", Label: "#1", Category: "image"}},
	})
	m.width = 120
	m.height = 40
	out := m.kittyImageLayer(m.height-2, m.listWidth(), m.previewWidth())
	if !strings.Contains(out, "a=d") {
		t.Fatalf("expected clear sequence when file missing, got %q", out)
	}
	if strings.Contains(out, "a=T") {
		t.Fatalf("did not expect draw sequence when file missing, got %q", out)
	}
}

// TestKittyImageLayerClearsWhenTermBlurred verifies we hide the image when
// the tmux window is not focused so it doesn't leak into unrelated windows.
func TestKittyImageLayerClearsWhenTermBlurred(t *testing.T) {
	t.Setenv("CCX_KITTY", "1")
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "img.png")
	writePNG(t, imagePath)

	m := newPickerModel("images", []PickerItem{
		{Item: extract.Item{URL: imagePath, Label: "#1", Category: "image"}},
	})
	m.width = 120
	m.height = 40
	m.termFocused = false
	out := m.kittyImageLayer(m.height-2, m.listWidth(), m.previewWidth())
	if !strings.Contains(out, "a=d") {
		t.Fatalf("expected clear sequence when term blurred, got %q", out)
	}
	if strings.Contains(out, "a=T") {
		t.Fatalf("did not expect draw sequence when term blurred, got %q", out)
	}
}

// writePNG emits a minimal 1x1 PNG so kitty.ImageSize can decode it.
func writePNG(t *testing.T, path string) {
	t.Helper()
	// 1x1 transparent PNG
	data := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write png: %v", err)
	}
}
