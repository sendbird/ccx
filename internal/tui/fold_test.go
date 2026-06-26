package tui

import (
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

func TestBuildGroupedItems_ProjectGroupFold(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/a", ProjectName: "a", ModTime: now.Add(-1 * time.Minute)},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/a", ProjectName: "a", ModTime: now.Add(-5 * time.Minute)},
		{ID: "a3", ShortID: "a3", ProjectPath: "/tmp/a", ProjectName: "a", ModTime: now.Add(-7 * time.Minute)},
		{ID: "b1", ShortID: "b1", ProjectPath: "/tmp/b", ProjectName: "b", ModTime: now.Add(-2 * time.Minute)},
	}

	expanded := buildGroupedItems(sessions, groupProject, nil)
	// 3 items in group "a" + 1 standalone "b" = 4 rows
	if got := len(expanded); got != 4 {
		t.Fatalf("expanded: expected 4 items, got %d", got)
	}
	header, ok := expanded[0].(sessionItem)
	if !ok {
		t.Fatalf("first item is not a sessionItem: %T", expanded[0])
	}
	if header.groupKey != "proj:/tmp/a" {
		t.Fatalf("expected groupKey 'proj:/tmp/a', got %q", header.groupKey)
	}
	if header.groupChildren != 2 {
		t.Fatalf("expected 2 children, got %d", header.groupChildren)
	}
	if header.groupFolded {
		t.Fatal("expected expanded by default")
	}

	folded := buildGroupedItems(sessions, groupProject, map[string]bool{"proj:/tmp/a": true})
	// only header for "a" (no children) + standalone "b" = 2 rows
	if got := len(folded); got != 2 {
		t.Fatalf("folded: expected 2 items, got %d", got)
	}
	foldedHeader, ok := folded[0].(sessionItem)
	if !ok {
		t.Fatalf("first item is not a sessionItem: %T", folded[0])
	}
	if !foldedHeader.groupFolded {
		t.Fatal("expected groupFolded=true on the folded header")
	}
	if foldedHeader.groupChildren != 2 {
		t.Fatalf("expected child count to survive folding, got %d", foldedHeader.groupChildren)
	}
}

func TestBuildGroupedItems_ProjectCentric(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		{ID: "m1", ShortID: "m1", ProjectPath: "/repo-a", ProjectName: "repo-a", ModTime: now.Add(-1 * time.Minute)},
		{ID: "w1", ShortID: "w1", ProjectPath: "/repo-a/.worktree/b1", ProjectName: "b1", ModTime: now.Add(-2 * time.Minute), IsWorktree: true},
		{ID: "n1", ShortID: "n1", ProjectPath: "/repo-b", ProjectName: "repo-b", ModTime: now.Add(-3 * time.Minute)},
	}

	expanded := buildGroupedItems(sessions, groupProjectCentric, nil, ".worktree")
	// repo-a row + 2 children + repo-b row + 1 child = 5
	if got := len(expanded); got != 5 {
		t.Fatalf("expanded: expected 5 items, got %d", got)
	}
	first, ok := expanded[0].(projectItem)
	if !ok {
		t.Fatalf("first item should be projectItem, got %T", expanded[0])
	}
	if first.basePath != "/repo-a" {
		t.Fatalf("expected first project basePath /repo-a, got %q", first.basePath)
	}
	if !first.expanded {
		t.Fatal("expected first project to start expanded")
	}
	if len(first.sessions) != 2 {
		t.Fatalf("expected 2 sessions under repo-a, got %d", len(first.sessions))
	}
	if first.worktrees != 1 {
		t.Fatalf("expected 1 worktree under repo-a, got %d", first.worktrees)
	}

	folded := buildGroupedItems(sessions, groupProjectCentric, map[string]bool{"repo:/repo-a": true}, ".worktree")
	// folded repo-a (1) + repo-b (1) + repo-b child (1) = 3
	if got := len(folded); got != 3 {
		t.Fatalf("folded: expected 3 items, got %d", got)
	}
	foldedPI := folded[0].(projectItem)
	if foldedPI.expanded {
		t.Fatal("expected repo-a to be folded")
	}
}

func TestBuildGroupedItems_BaseProjectGroupFold(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		{ID: "m1", ShortID: "m1", ProjectPath: "/repo", ProjectName: "repo", ModTime: now.Add(-1 * time.Minute)},
		{ID: "w1", ShortID: "w1", ProjectPath: "/repo/.worktree/branch1", ProjectName: "branch1", ModTime: now.Add(-2 * time.Minute), IsWorktree: true},
		{ID: "w2", ShortID: "w2", ProjectPath: "/repo/.worktree/branch2", ProjectName: "branch2", ModTime: now.Add(-3 * time.Minute), IsWorktree: true},
	}

	expanded := buildGroupedItems(sessions, groupBaseProject, nil, ".worktree")
	if got := len(expanded); got != 3 {
		t.Fatalf("expanded: expected 3 items, got %d", got)
	}

	folded := buildGroupedItems(sessions, groupBaseProject, map[string]bool{"repo:/repo": true}, ".worktree")
	if got := len(folded); got != 1 {
		t.Fatalf("folded: expected only the header (1 item), got %d", got)
	}
	header := folded[0].(sessionItem)
	if !header.groupFolded {
		t.Fatal("expected groupFolded=true")
	}
	if header.groupChildren != 2 {
		t.Fatalf("expected groupChildren=2, got %d", header.groupChildren)
	}
}
