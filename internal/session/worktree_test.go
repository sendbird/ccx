package session

import (
	"path/filepath"
	"testing"
)

func TestParseWorktreePorcelain(t *testing.T) {
	input := `worktree /home/user/repo
HEAD abc123def456
branch refs/heads/main

worktree /tmp/repo-feature
HEAD def456abc123
branch refs/heads/feature/login

worktree /tmp/repo-fix
HEAD 111222333444
branch refs/heads/hotfix

`
	wts := ParseWorktreePorcelain(input)

	if len(wts) != 3 {
		t.Fatalf("expected 3 worktrees, got %d", len(wts))
	}

	// First is main
	if !wts[0].IsMain {
		t.Error("first worktree should be main")
	}
	if wts[0].Path != "/home/user/repo" {
		t.Errorf("main path = %q, want /home/user/repo", wts[0].Path)
	}
	if wts[0].Branch != "main" {
		t.Errorf("main branch = %q, want main", wts[0].Branch)
	}

	// Second is not main
	if wts[1].IsMain {
		t.Error("second worktree should not be main")
	}
	if wts[1].Path != "/tmp/repo-feature" {
		t.Errorf("second path = %q", wts[1].Path)
	}
	if wts[1].Branch != "feature/login" {
		t.Errorf("second branch = %q, want feature/login", wts[1].Branch)
	}

	// Third
	if wts[2].Branch != "hotfix" {
		t.Errorf("third branch = %q, want hotfix", wts[2].Branch)
	}
}

func TestParseWorktreePorcelainEmpty(t *testing.T) {
	wts := ParseWorktreePorcelain("")
	if len(wts) != 0 {
		t.Fatalf("expected 0 worktrees, got %d", len(wts))
	}
}

func TestParseWorktreePorcelainDetachedHead(t *testing.T) {
	input := `worktree /home/user/repo
HEAD abc123
branch refs/heads/main

worktree /tmp/detached
HEAD def456
detached

`
	wts := ParseWorktreePorcelain(input)
	if len(wts) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(wts))
	}
	// Detached worktree has no branch
	if wts[1].Branch != "" {
		t.Errorf("detached branch = %q, want empty", wts[1].Branch)
	}
}

func TestMisalignedWorktrees(t *testing.T) {
	repoRoot := "/home/user/repo"
	worktreeDir := ".worktree"

	worktrees := []WorktreeInfo{
		{Path: "/home/user/repo", Branch: "main", IsMain: true},
		{Path: filepath.Join(repoRoot, ".worktree", "feature"), Branch: "feature", IsMain: false},
		{Path: "/tmp/repo-fix", Branch: "hotfix", IsMain: false},
		{Path: "/home/user/other/branch", Branch: "other", IsMain: false},
	}

	misaligned := MisalignedWorktrees(repoRoot, worktrees, worktreeDir)

	if len(misaligned) != 2 {
		t.Fatalf("expected 2 misaligned, got %d", len(misaligned))
	}

	paths := map[string]bool{}
	for _, m := range misaligned {
		paths[m.Path] = true
	}
	if !paths["/tmp/repo-fix"] {
		t.Error("expected /tmp/repo-fix to be misaligned")
	}
	if !paths["/home/user/other/branch"] {
		t.Error("expected /home/user/other/branch to be misaligned")
	}
}

func TestMisalignedWorktreesAllAligned(t *testing.T) {
	repoRoot := "/home/user/repo"
	worktrees := []WorktreeInfo{
		{Path: "/home/user/repo", Branch: "main", IsMain: true},
		{Path: filepath.Join(repoRoot, ".worktree", "a"), Branch: "a", IsMain: false},
		{Path: filepath.Join(repoRoot, ".worktree", "b"), Branch: "b", IsMain: false},
	}

	misaligned := MisalignedWorktrees(repoRoot, worktrees, ".worktree")
	if len(misaligned) != 0 {
		t.Fatalf("expected 0 misaligned, got %d", len(misaligned))
	}
}
