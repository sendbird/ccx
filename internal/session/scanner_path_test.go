package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncodeProjectPath(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"/Users/test/project", "-Users-test-project"},
		{"/Users/test/my.project", "-Users-test-my-project"},
		{"/tmp/a/b/c", "-tmp-a-b-c"},
		{"", ""},
	}
	for _, tt := range tests {
		got := EncodeProjectPath(tt.input)
		if got != tt.want {
			t.Errorf("EncodeProjectPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestShortenPath(t *testing.T) {
	tests := []struct {
		path, home, want string
	}{
		{"/Users/test/project", "/Users/test", "~/project"},
		{"/Users/test", "/Users/test", "~"},
		{"/other/path", "/Users/test", "/other/path"},
		{"", "/Users/test", ""},
	}
	for _, tt := range tests {
		got := ShortenPath(tt.path, tt.home)
		if got != tt.want {
			t.Errorf("ShortenPath(%q, %q) = %q, want %q", tt.path, tt.home, got, tt.want)
		}
	}
}

func TestDecodeDirName(t *testing.T) {
	tests := []struct {
		dirName, home, want string
	}{
		{"-Users-test-project", "/Users/test", "~/project"},
		{"plain-dir", "/Users/test", "plain-dir"},
		{"-tmp-something", "/Users/test", "/tmp/something"},
	}
	for _, tt := range tests {
		got := decodeDirName(tt.dirName, tt.home)
		if got != tt.want {
			t.Errorf("decodeDirName(%q, %q) = %q, want %q", tt.dirName, tt.home, got, tt.want)
		}
	}
}

// TestMoveSessionOnlyMovesOneSession proves that MoveSession (used by
// `ccx move --session <id>`) relocates only the named session's transcript,
// leaving a sibling session under the same old project path untouched —
// unlike MoveProject, which renames the whole project directory.
func TestMoveSessionOnlyMovesOneSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldPath := "/Users/test/project-a"
	newPath := "/Users/test/project-b"
	oldEncoded := EncodeProjectPath(oldPath)
	newEncoded := EncodeProjectPath(newPath)

	oldDir := filepath.Join(home, ".claude", "projects", oldEncoded)
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatal(err)
	}

	movingID := "session-moving"
	stayingID := "session-staying"
	movingContent := `{"cwd":"` + oldPath + `","other":"a"}` + "\n"
	stayingContent := `{"cwd":"` + oldPath + `","other":"b"}` + "\n"

	if err := os.WriteFile(filepath.Join(oldDir, movingID+".jsonl"), []byte(movingContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, stayingID+".jsonl"), []byte(stayingContent), 0644); err != nil {
		t.Fatal(err)
	}

	if err := MoveSession(oldPath, newPath, movingID); err != nil {
		t.Fatalf("MoveSession: %v", err)
	}

	newDir := filepath.Join(home, ".claude", "projects", newEncoded)

	if _, err := os.Stat(filepath.Join(oldDir, movingID+".jsonl")); !os.IsNotExist(err) {
		t.Errorf("moving session's old file should be gone, stat err = %v", err)
	}
	movedData, err := os.ReadFile(filepath.Join(newDir, movingID+".jsonl"))
	if err != nil {
		t.Fatalf("moved session file not found: %v", err)
	}
	if want := `{"cwd":"` + newPath + `","other":"a"}` + "\n"; string(movedData) != want {
		t.Errorf("moved session cwd not rewritten: got %q, want %q", movedData, want)
	}

	stayingData, err := os.ReadFile(filepath.Join(oldDir, stayingID+".jsonl"))
	if err != nil {
		t.Fatalf("sibling session should remain in old project dir: %v", err)
	}
	if string(stayingData) != stayingContent {
		t.Errorf("sibling session content should be untouched: got %q, want %q", stayingData, stayingContent)
	}
	if _, err := os.Stat(filepath.Join(newDir, stayingID+".jsonl")); !os.IsNotExist(err) {
		t.Errorf("sibling session should NOT have been moved, stat err = %v", err)
	}
}

func TestEncodeDecodeRoundtrip(t *testing.T) {
	// EncodeProjectPath replaces / and . with -
	// decodeDirName replaces - with / (simple decode)
	// This is a lossy transform, but we can verify encode produces expected output
	paths := []string{
		"/Users/gavin/src/project",
		"/home/user/workspace",
		"/tmp/test",
	}
	for _, p := range paths {
		encoded := EncodeProjectPath(p)
		if encoded == "" {
			t.Errorf("EncodeProjectPath(%q) returned empty", p)
		}
		if encoded[0] != '-' {
			t.Errorf("encoded path should start with -, got %q", encoded)
		}
	}
}
