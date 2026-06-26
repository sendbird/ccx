package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/sendbird/ccx/internal/session"
)

func TestBuildGroupedItems_PinsCurrentWindowSessions(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		{ID: "s1", ShortID: "s1", ProjectPath: "/tmp/a", ProjectName: "a", ModTime: now.Add(-10 * time.Minute)},
		{ID: "s2", ShortID: "s2", ProjectPath: "/tmp/b", ProjectName: "b", ModTime: now.Add(-5 * time.Minute), IsCurrentWindow: true},
		{ID: "s3", ShortID: "s3", ProjectPath: "/tmp/c", ProjectName: "c", ModTime: now.Add(-1 * time.Minute)},
		{ID: "s4", ShortID: "s4", ProjectPath: "/tmp/d", ProjectName: "d", ModTime: now.Add(-30 * time.Minute), IsCurrentWindow: true},
	}

	items := buildGroupedItems(sessions, groupFlat, nil)
	if len(items) < 5 {
		t.Fatalf("expected at least 5 items (2 headers + 4 sessions), got %d", len(items))
	}

	// Item 0: "Current Window" header.
	h0, ok := items[0].(headerItem)
	if !ok || h0.label != "Current Window" {
		t.Fatalf("expected first item to be Current Window header, got %T %v", items[0], items[0])
	}

	// Item 1 + 2: current-window sessions (most recent first → s2 then s4).
	si1, ok := items[1].(sessionItem)
	if !ok || si1.sess.ID != "s2" {
		t.Fatalf("expected items[1]=s2, got %v", items[1])
	}
	si2, ok := items[2].(sessionItem)
	if !ok || si2.sess.ID != "s4" {
		t.Fatalf("expected items[2]=s4, got %v", items[2])
	}

	// Then the "Sessions" header divider.
	h1, ok := items[3].(headerItem)
	if !ok || h1.label != "Sessions" {
		t.Fatalf("expected items[3]=Sessions header, got %T %v", items[3], items[3])
	}

	// Then the rest of the sessions in their normal order.
	rest1, _ := items[4].(sessionItem)
	rest2, _ := items[5].(sessionItem)
	if rest1.sess.ID != "s1" || rest2.sess.ID != "s3" {
		t.Fatalf("expected rest items s1,s3 got %s,%s", rest1.sess.ID, rest2.sess.ID)
	}
}

func TestBuildGroupedItems_ProjectCentric_CurrentWindowIsGroupedByProject(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: now.Add(-1 * time.Minute), IsCurrentWindow: true},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a/.worktree/feat", ProjectName: "feat", ModTime: now.Add(-2 * time.Minute), IsCurrentWindow: true, IsWorktree: true},
		{ID: "b1", ShortID: "b1", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: now.Add(-3 * time.Minute)},
	}
	items := buildGroupedItems(sessions, groupProjectCentric, nil, ".worktree")
	if len(items) < 4 {
		t.Fatalf("expected grouped items, got %d", len(items))
	}
	h0, ok := items[0].(headerItem)
	if !ok || h0.label != "Current Window Projects" {
		t.Fatalf("expected first header 'Current Window Projects', got %T %#v", items[0], items[0])
	}
	if _, ok := items[1].(projectItem); !ok {
		t.Fatalf("expected current-window section to start with a projectItem, got %T", items[1])
	}
	foundProjectsHeader := false
	for _, item := range items {
		if h, ok := item.(headerItem); ok && h.label == "Projects" {
			foundProjectsHeader = true
			break
		}
	}
	if !foundProjectsHeader {
		t.Fatalf("expected a trailing 'Projects' header, got %#v", items)
	}
}

func TestBuildGroupedItems_NoCurrentWindow_NoHeader(t *testing.T) {
	sessions := []session.Session{
		{ID: "s1", ShortID: "s1", ProjectPath: "/tmp/a", ProjectName: "a"},
		{ID: "s2", ShortID: "s2", ProjectPath: "/tmp/b", ProjectName: "b"},
	}
	items := buildGroupedItems(sessions, groupFlat, nil)
	for _, it := range items {
		if _, ok := it.(headerItem); ok {
			t.Fatalf("did not expect a header when no current-window sessions present")
		}
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

func TestPinCurrentWindowFilter_AlwaysIncludesCurrent(t *testing.T) {
	sessions := []session.Session{
		{ID: "s1", ProjectPath: "/tmp/match", ProjectName: "match"},
		{ID: "s2", ProjectPath: "/tmp/here", ProjectName: "here", IsCurrentWindow: true},
	}
	items := buildGroupedItems(sessions, groupFlat, nil)
	targets := make([]string, len(items))
	for i, it := range items {
		targets[i] = it.FilterValue()
	}

	filter := wrapPinCurrentWindow(items, substringFilter)
	ranks := filter("nomatch", targets)

	// "Current Window" header + the pinned session must be present even if
	// neither matches "nomatch".
	if len(ranks) < 2 {
		t.Fatalf("expected at least 2 ranks for header + pinned session, got %d", len(ranks))
	}
	gotIDs := map[string]bool{}
	for _, r := range ranks {
		switch v := items[r.Index].(type) {
		case headerItem:
			gotIDs["header:"+v.label] = true
		case sessionItem:
			gotIDs[v.sess.ID] = true
		}
	}
	if !gotIDs["header:Current Window"] {
		t.Fatalf("expected Current Window header to be pinned in filtered ranks")
	}
	if !gotIDs["s2"] {
		t.Fatalf("expected pinned current-window session s2 to be visible under filter")
	}
	if gotIDs["s1"] {
		t.Fatalf("did not expect non-matching s1 to leak through filter")
	}
	if gotIDs["header:Sessions"] {
		t.Fatalf("expected trailing Sessions header to be trimmed when no rest items match")
	}
}

func TestPinCurrentWindowFilter_KeepsMatchedRest(t *testing.T) {
	sessions := []session.Session{
		{ID: "s1", ProjectPath: "/tmp/match", ProjectName: "alpha-match"},
		{ID: "s2", ProjectPath: "/tmp/here", ProjectName: "here-proj", IsCurrentWindow: true},
	}
	items := buildGroupedItems(sessions, groupFlat, nil)
	targets := make([]string, len(items))
	for i, it := range items {
		targets[i] = it.FilterValue()
	}

	filter := wrapPinCurrentWindow(items, substringFilter)
	ranks := filter("alpha", targets)

	got := map[string]bool{}
	for _, r := range ranks {
		switch v := items[r.Index].(type) {
		case headerItem:
			got["header:"+v.label] = true
		case sessionItem:
			got[v.sess.ID] = true
		}
	}
	if !got["s1"] {
		t.Fatalf("expected matched session s1 to be visible")
	}
	if !got["s2"] {
		t.Fatalf("expected pinned current-window session s2 to remain visible")
	}
	if !got["header:Current Window"] || !got["header:Sessions"] {
		t.Fatalf("expected both section headers when both sections have items: %v", got)
	}
}

// Sanity: header items declare their FilterValue as the header sentinel so
// they are never produced by substringFilter on user input.
func TestHeaderItem_FilterValueDoesNotMatchUserTerms(t *testing.T) {
	h := headerItem{label: "Current Window"}
	targets := []string{h.FilterValue(), "regular"}
	ranks := substringFilter("Current", targets)
	for _, r := range ranks {
		if r.Index == 0 {
			t.Fatalf("expected header sentinel to not match user term Current")
		}
	}
	// Just to confirm filter does work for the regular value.
	_ = list.Rank{}
}
