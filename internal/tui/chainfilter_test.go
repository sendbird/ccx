package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/sendbird/ccx/internal/session"
)

func TestChainAwareFilter_ParentMatchIncludesChildren(t *testing.T) {
	items := []list.Item{
		sessionItem{sess: session.Session{ID: "p1", ProjectPath: "/proj-alpha", ProjectName: "alpha"}, treeDepth: 0},
		sessionItem{sess: session.Session{ID: "c1", ProjectPath: "/proj-alpha", ProjectName: "alpha"}, treeDepth: 1},
		sessionItem{sess: session.Session{ID: "c2", ProjectPath: "/proj-alpha", ProjectName: "alpha"}, treeDepth: 1, treeLast: true},
		sessionItem{sess: session.Session{ID: "p2", ProjectPath: "/proj-beta", ProjectName: "beta"}, treeDepth: 0},
	}

	targets := make([]string, len(items))
	for i, item := range items {
		targets[i] = item.(sessionItem).FilterValue()
	}

	filter := buildChainAwareFilter(items)
	ranks := filter("alpha", targets)

	// Should include p1, c1, c2 (parent + children) but not p2
	indices := make(map[int]bool)
	for _, r := range ranks {
		indices[r.Index] = true
	}

	if !indices[0] {
		t.Error("expected parent p1 (index 0) to match")
	}
	if !indices[1] {
		t.Error("expected child c1 (index 1) included via parent")
	}
	if !indices[2] {
		t.Error("expected child c2 (index 2) included via parent")
	}
	if indices[3] {
		t.Error("expected p2 (index 3) to be excluded")
	}
}

func TestChainAwareFilter_ChildMatchIncludesParent(t *testing.T) {
	items := []list.Item{
		sessionItem{sess: session.Session{ID: "p1", ProjectPath: "/proj-x", ProjectName: "x"}, treeDepth: 0},
		sessionItem{sess: session.Session{ID: "c1", ProjectPath: "/proj-x", ProjectName: "x", FirstPrompt: "unique-child-query"}, treeDepth: 1, treeLast: true},
		sessionItem{sess: session.Session{ID: "p2", ProjectPath: "/proj-y", ProjectName: "y"}, treeDepth: 0},
	}

	targets := make([]string, len(items))
	for i, item := range items {
		targets[i] = item.(sessionItem).FilterValue()
	}

	filter := buildChainAwareFilter(items)
	ranks := filter("unique-child-query", targets)

	indices := make(map[int]bool)
	for _, r := range ranks {
		indices[r.Index] = true
	}

	if !indices[0] {
		t.Error("expected parent p1 (index 0) included via child match")
	}
	if !indices[1] {
		t.Error("expected child c1 (index 1) to match")
	}
	if indices[2] {
		t.Error("expected p2 (index 2) to be excluded")
	}
}

func TestChainAwareFilter_NoMatchReturnsEmpty(t *testing.T) {
	items := []list.Item{
		sessionItem{sess: session.Session{ID: "p1", ProjectPath: "/proj-a"}, treeDepth: 0},
		sessionItem{sess: session.Session{ID: "c1", ProjectPath: "/proj-a"}, treeDepth: 1, treeLast: true},
	}

	targets := make([]string, len(items))
	for i, item := range items {
		targets[i] = item.(sessionItem).FilterValue()
	}

	filter := buildChainAwareFilter(items)
	ranks := filter("nonexistent", targets)

	if len(ranks) != 0 {
		t.Errorf("expected 0 results, got %d", len(ranks))
	}
}

func TestChainAwareFilter_StandaloneFiltersNormally(t *testing.T) {
	now := time.Now()
	items := []list.Item{
		sessionItem{sess: session.Session{ID: "s1", ProjectPath: "/standalone-a", ProjectName: "standalone-a", ModTime: now}, treeDepth: 0},
		sessionItem{sess: session.Session{ID: "s2", ProjectPath: "/standalone-b", ProjectName: "standalone-b", ModTime: now}, treeDepth: 0},
	}

	targets := make([]string, len(items))
	for i, item := range items {
		targets[i] = item.(sessionItem).FilterValue()
	}

	filter := buildChainAwareFilter(items)
	ranks := filter("standalone-a", targets)

	if len(ranks) != 1 || ranks[0].Index != 0 {
		t.Errorf("expected only index 0, got %v", ranks)
	}
}
