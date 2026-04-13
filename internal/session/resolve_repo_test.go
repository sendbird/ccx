package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveBaseRepo_PathFallback(t *testing.T) {
	// Without a real git repo, falls back to path-based resolution
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/me/src/repo/.worktree/feat-x", "/Users/me/src/repo"},
		{"/Users/me/src/repo/.worktrees/feat-x/service", "/Users/me/src/repo"},
		{"/Users/me/src/repo", "/Users/me/src/repo"},
		{"/tmp/plain-dir", "/tmp/plain-dir"},
	}
	for _, tt := range tests {
		got := ResolveBaseRepo(tt.input)
		if got != tt.want {
			t.Errorf("ResolveBaseRepo(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolveBaseRepo_GitWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create a real git repo with a worktree
	// Resolve symlinks (macOS /var -> /private/var)
	mainDir, _ := filepath.EvalSymlinks(t.TempDir())
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %s\n%s", args, err, out)
		}
	}

	run(mainDir, "init")
	run(mainDir, "config", "user.email", "test@test.com")
	run(mainDir, "config", "user.name", "test")

	// Create an initial commit
	f := filepath.Join(mainDir, "README.md")
	os.WriteFile(f, []byte("hello"), 0644)
	run(mainDir, "add", ".")
	run(mainDir, "commit", "-m", "init")

	// Create a worktree
	wtDir := filepath.Join(mainDir, ".worktree", "feat-x")
	os.MkdirAll(filepath.Dir(wtDir), 0755)
	run(mainDir, "worktree", "add", wtDir, "-b", "feat-x")

	// ResolveBaseRepo on the worktree should return the main repo
	got := ResolveBaseRepo(wtDir)
	if got != mainDir {
		t.Errorf("ResolveBaseRepo(%q) = %q, want %q", wtDir, got, mainDir)
	}

	// ResolveBaseRepo on the main repo should return itself
	got = ResolveBaseRepo(mainDir)
	if got != mainDir {
		t.Errorf("ResolveBaseRepo(%q) = %q, want %q (self)", mainDir, got, mainDir)
	}
}

func TestResolveBaseRepo_ExternalWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create a repo with worktree in a different directory (not under .worktree/)
	mainDir, _ := filepath.EvalSymlinks(t.TempDir())
	externalWT, _ := filepath.EvalSymlinks(t.TempDir())
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %s\n%s", args, err, out)
		}
	}

	run(mainDir, "init")
	run(mainDir, "config", "user.email", "test@test.com")
	run(mainDir, "config", "user.name", "test")
	os.WriteFile(filepath.Join(mainDir, "file.txt"), []byte("x"), 0644)
	run(mainDir, "add", ".")
	run(mainDir, "commit", "-m", "init")

	wtPath := filepath.Join(externalWT, "my-wt")
	run(mainDir, "worktree", "add", wtPath, "-b", "external-branch")

	// Git-based resolution should find the main repo even for external worktrees
	got := ResolveBaseRepo(wtPath)
	if got != mainDir {
		t.Errorf("ResolveBaseRepo(%q) = %q, want %q (git-based)", wtPath, got, mainDir)
	}
}
