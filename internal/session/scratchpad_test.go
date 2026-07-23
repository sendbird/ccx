package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeScratchpadFixture writes the given files into the session's scratchpad
// directory under ScratchpadBase() (which honors $TMPDIR, set by the caller),
// returning the project path + session ID pair LoadScratchpadFiles expects.
func writeScratchpadFixture(t *testing.T, projectPath, sessionID string, files map[string]string) (string, string) {
	t.Helper()
	dir := filepath.Join(ScratchpadBase(), EncodeProjectPath(projectPath), sessionID, "scratchpad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return projectPath, sessionID
}

func TestLoadScratchpadFiles_MissingDir(t *testing.T) {
	if got := LoadScratchpadFiles("/nonexistent/project", "no-such-session"); got != nil {
		t.Fatalf("expected nil for missing dir, got %d files", len(got))
	}
}

func TestLoadScratchpadFiles_EmptyArgs(t *testing.T) {
	if got := LoadScratchpadFiles("", "sid"); got != nil {
		t.Fatalf("expected nil for empty projectPath, got %d", len(got))
	}
	if got := LoadScratchpadFiles("/p", ""); got != nil {
		t.Fatalf("expected nil for empty sessionID, got %d", len(got))
	}
}

func TestLoadScratchpadFiles_LoadsTextSorted(t *testing.T) {
	base := t.TempDir()
	orig := scratchpadBaseOverride
	scratchpadBaseOverride = base
	defer func() { scratchpadBaseOverride = orig }()

	projectPath := "/Users/me/src/repo"
	sessionID := "sess-uuid-1"
	writeScratchpadFixture(t, projectPath, sessionID, map[string]string{
		"notes.md":  "# plan\nsome scratch text\n",
		"alpha.txt": "first file\n",
	})

	files := LoadScratchpadFiles(projectPath, sessionID)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Name != "alpha.txt" {
		t.Errorf("expected sorted: alpha.txt first, got %q", files[0].Name)
	}
	if !files[0].IsText {
		t.Errorf("alpha.txt should be text")
	}
	if files[0].Body != "first file\n" {
		t.Errorf("unexpected body %q", files[0].Body)
	}
	if files[1].Name != "notes.md" || !strings.Contains(files[1].Body, "scratch text") {
		t.Errorf("notes.md body mismatch: %q", files[1].Body)
	}
	// Path is absolute and lives under the fake base.
	if !filepath.IsAbs(files[0].Path) {
		t.Errorf("expected absolute path, got %q", files[0].Path)
	}
}

func TestLoadScratchpadFiles_BinaryPlaceholder(t *testing.T) {
	base := t.TempDir()
	orig := scratchpadBaseOverride
	scratchpadBaseOverride = base
	defer func() { scratchpadBaseOverride = orig }()

	projectPath := "/Users/me/src/repo2"
	sessionID := "sess-bin"
	dir := filepath.Join(ScratchpadBase(), EncodeProjectPath(projectPath), sessionID, "scratchpad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// NUL byte → not valid text.
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	files := LoadScratchpadFiles(projectPath, sessionID)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].IsText {
		t.Errorf("blob.bin should not be text")
	}
	if files[0].Body != "(binary file)" {
		t.Errorf("expected binary placeholder, got %q", files[0].Body)
	}
}

func TestLoadScratchpadFiles_TruncatesLargeText(t *testing.T) {
	base := t.TempDir()
	orig := scratchpadBaseOverride
	scratchpadBaseOverride = base
	defer func() { scratchpadBaseOverride = orig }()

	projectPath := "/Users/me/src/big"
	sessionID := "sess-big"
	dir := filepath.Join(ScratchpadBase(), EncodeProjectPath(projectPath), sessionID, "scratchpad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	big := make([]byte, scratchpadMaxBody+1024)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), big, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	files := LoadScratchpadFiles(projectPath, sessionID)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if !files[0].IsText {
		t.Errorf("big.txt should be text")
	}
	if !files[0].Truncated {
		t.Errorf("expected Truncated=true for oversized file")
	}
	if int64(len(files[0].Body)) > scratchpadMaxBody {
		t.Errorf("body exceeds cap: %d", len(files[0].Body))
	}
	if files[0].Size != int64(len(big)) {
		t.Errorf("Size should reflect full file: got %d want %d", files[0].Size, len(big))
	}
}
