package kitty

import (
	"os"
	"testing"
)

func TestSupportedCachesResult(t *testing.T) {
	// Just verify it doesn't panic and returns a bool
	_ = Supported()
}

func TestDisplayImageMissingFile(t *testing.T) {
	result := DisplayImage("/nonexistent/path.png", 40, 20)
	if result != "" {
		t.Fatalf("expected empty for missing file, got %q", result)
	}
}

func TestDisplayImageValidFile(t *testing.T) {
	f, err := os.CreateTemp("", "kitty-test-*.png")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.Write([]byte("fake png"))
	f.Close()

	result := DisplayImage(f.Name(), 40, 20)
	if result == "" {
		t.Fatal("expected non-empty escape sequence for valid file")
	}
	if !containsStr(result, "\x1b_G") {
		t.Fatalf("expected Kitty escape prefix, got %q", result)
	}
}

func TestPlaceImageEmpty(t *testing.T) {
	if PlaceImage("", 1, 1, 40, 20) != "" {
		t.Fatal("expected empty for empty path")
	}
}

func TestClearImages(t *testing.T) {
	result := ClearImages()
	if !containsStr(result, "\x1b_G") {
		t.Fatalf("expected Kitty escape prefix, got %q", result)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstr(s, substr))
}

func findSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
