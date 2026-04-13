package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAggregateStats_GroupsCustomWorktreeDirsByRepo(t *testing.T) {
	tmpDir := t.TempDir()

	mainFile := filepath.Join(tmpDir, "main.jsonl")
	if err := os.WriteFile(mainFile, []byte("{\"timestamp\":\"2026-04-13T10:00:00Z\",\"message\":{\"role\":\"assistant\",\"model\":\"claude-sonnet-4-20250514\",\"usage\":{\"input_tokens\":10,\"output_tokens\":20},\"content\":\"ok\"}}\n"), 0644); err != nil {
		t.Fatalf("write main session: %v", err)
	}
	wtFile := filepath.Join(tmpDir, "wt.jsonl")
	if err := os.WriteFile(wtFile, []byte("{\"timestamp\":\"2026-04-13T11:00:00Z\",\"message\":{\"role\":\"assistant\",\"model\":\"claude-sonnet-4-20250514\",\"usage\":{\"input_tokens\":10,\"output_tokens\":30},\"content\":\"ok\"}}\n"), 0644); err != nil {
		t.Fatalf("write worktree session: %v", err)
	}

	sessions := []Session{
		{FilePath: mainFile, ProjectPath: "/Users/me/src/sendbird/platform-tools", ProjectName: "platform-tools"},
		{FilePath: wtFile, ProjectPath: "/Users/me/src/sendbird/platform-tools/.worktrees/build-agent/agent", ProjectName: "agent"},
	}

	for _, dirs := range [][]string{nil, {".worktree"}, {".worktrees"}} {
		stats := AggregateStats(sessions, dirs...)
		if len(stats.ProjectStats) != 1 {
			t.Fatalf("worktreeDirs=%v: expected 1 repo entry, got %#v", dirs, stats.ProjectStats)
		}

		ps := stats.ProjectStats[0]
		if ps.RepoPath != "/Users/me/src/sendbird/platform-tools" {
			t.Fatalf("worktreeDirs=%v: RepoPath = %q, want main repo path", dirs, ps.RepoPath)
		}
		if ps.SessionCount != 2 {
			t.Fatalf("worktreeDirs=%v: SessionCount = %d, want 2", dirs, ps.SessionCount)
		}
		if ps.TotalOutputTokens != 50 {
			t.Fatalf("worktreeDirs=%v: TotalOutputTokens = %d, want 50", dirs, ps.TotalOutputTokens)
		}
		if ps.ProjectPath != "/Users/me/src/sendbird/platform-tools" {
			t.Fatalf("worktreeDirs=%v: ProjectPath = %q, want first aggregated project path", dirs, ps.ProjectPath)
		}
	}
}
