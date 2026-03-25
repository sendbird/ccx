package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WorktreeInfo from `git worktree list --porcelain`.
type WorktreeInfo struct {
	Path   string
	Branch string
	IsMain bool
}

// ListWorktrees parses `git worktree list --porcelain` output for the given repo root.
func ListWorktrees(repoRoot string) ([]WorktreeInfo, error) {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return ParseWorktreePorcelain(string(out)), nil
}

// ParseWorktreePorcelain parses the porcelain output of `git worktree list --porcelain`.
// Blocks are separated by blank lines. The first block is the main worktree.
func ParseWorktreePorcelain(output string) []WorktreeInfo {
	var worktrees []WorktreeInfo
	blocks := splitPorcelainBlocks(output)

	for i, block := range blocks {
		wt := parseWorktreeBlock(block)
		if wt.Path == "" {
			continue
		}
		wt.IsMain = (i == 0)
		worktrees = append(worktrees, wt)
	}
	return worktrees
}

// splitPorcelainBlocks splits porcelain output into blocks separated by blank lines.
func splitPorcelainBlocks(output string) [][]string {
	var blocks [][]string
	var current []string

	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			if len(current) > 0 {
				blocks = append(blocks, current)
				current = nil
			}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		blocks = append(blocks, current)
	}
	return blocks
}

// parseWorktreeBlock parses a single worktree block from porcelain output.
func parseWorktreeBlock(lines []string) WorktreeInfo {
	var wt WorktreeInfo
	for _, line := range lines {
		if strings.HasPrefix(line, "worktree ") {
			wt.Path = strings.TrimPrefix(line, "worktree ")
		} else if strings.HasPrefix(line, "branch ") {
			ref := strings.TrimPrefix(line, "branch ")
			// Strip refs/heads/ prefix to get the short branch name.
			wt.Branch = strings.TrimPrefix(ref, "refs/heads/")
		}
	}
	return wt
}

// MisalignedWorktrees returns worktrees whose paths are not under repoRoot/worktreeDir.
// The main worktree is always excluded.
func MisalignedWorktrees(repoRoot string, worktrees []WorktreeInfo, worktreeDir string) []WorktreeInfo {
	expectedPrefix := filepath.Join(repoRoot, worktreeDir) + string(os.PathSeparator)
	var misaligned []WorktreeInfo
	for _, wt := range worktrees {
		if wt.IsMain {
			continue
		}
		if !strings.HasPrefix(wt.Path+string(os.PathSeparator), expectedPrefix+string(os.PathSeparator)) &&
			!strings.HasPrefix(wt.Path, expectedPrefix) {
			misaligned = append(misaligned, wt)
		}
	}
	return misaligned
}

// AlignWorktree moves a worktree to repoRoot/worktreeDir/branchName.
// Returns the new path on success.
func AlignWorktree(repoRoot, oldPath, worktreeDir string) (string, error) {
	// Derive the leaf name from the branch or from the existing directory name.
	baseName := filepath.Base(oldPath)

	newPath := filepath.Join(repoRoot, worktreeDir, baseName)

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		return "", fmt.Errorf("create dir: %w", err)
	}

	// Use `git worktree move` to relocate the worktree.
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "move", oldPath, newPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree move: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return newPath, nil
}
