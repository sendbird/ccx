package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMainProjectPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/me/src/repo/.worktree/feat-x", "/Users/me/src/repo"},
		{"/Users/me/src/repo/.worktree/deep/nested", "/Users/me/src/repo"},
		{"/Users/me/src/repo", "/Users/me/src/repo"},
		{"/Users/me/.worktree/name", "/Users/me"},
	}
	for _, tt := range tests {
		got := ResolveMainProjectPath(tt.input)
		if got != tt.want {
			t.Errorf("ResolveMainProjectPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestHasProjectMemoryFallsBackToMainRepoForWorktree(t *testing.T) {
	home := t.TempDir()
	mainProject := "/Users/gavin.jeong/src/keyolk/ccproxy"
	worktreeProject := "/Users/gavin.jeong/src/keyolk/ccproxy/.worktree/rebuild"
	memDir := filepath.Join(home, ".claude", "projects", EncodeProjectPath(mainProject), "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("mkdir memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "state.md"), []byte("memory"), 0o644); err != nil {
		t.Fatalf("write memory file: %v", err)
	}
	if !hasProjectMemory(worktreeProject, home) {
		t.Fatalf("expected worktree project to inherit memory from main repo")
	}
}

func TestListMemoryDir(t *testing.T) {
	dir := t.TempDir()

	// Create some files
	os.WriteFile(filepath.Join(dir, "user_role.md"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(dir, "feedback.md"), []byte("test2"), 0644)
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("index"), 0644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("skip"), 0644)

	files, err := listMemoryDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 2 {
		t.Fatalf("expected 2 files (excluding MEMORY.md and .txt), got %d", len(files))
	}

	names := map[string]bool{}
	for _, f := range files {
		names[f.Name] = true
	}
	if !names["user_role.md"] || !names["feedback.md"] {
		t.Errorf("unexpected files: %v", files)
	}
}

func TestListMemoryDir_Empty(t *testing.T) {
	dir := t.TempDir()
	files, err := listMemoryDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestListMemoryDir_NotExist(t *testing.T) {
	files, err := listMemoryDir("/nonexistent/path")
	if err != nil {
		t.Fatal(err)
	}
	if files != nil {
		t.Errorf("expected nil for nonexistent dir, got %v", files)
	}
}
