package tui

import (
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// TestPinnedCurrentWindowKeepsProjectHeader verifies that a current-window
// session in project-centric mode keeps its project header row visible under a
// filter — the session must not render orphaned without its project line.
func TestPinnedCurrentWindowKeepsProjectHeader(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		// Current-window session under project "cur" — matches neither is:done.
		{ID: "cur1", ShortID: "cur1", ProjectPath: "/tmp/cur", ProjectName: "cur", ModTime: now, IsCurrentWindow: true, IsLive: true},
		// A done session in another project so the filter has something to match.
		{ID: "done1", ShortID: "done1", ProjectPath: "/tmp/other", ProjectName: "other", ModTime: now.Add(-time.Hour), Todos: []session.TodoItem{{Status: "completed"}}},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.config.SearchQuery = ""
	app.sessionList.ResetFilter()
	app.rebuildSessionList()
	app.setAllSessGroupsFolded(false) // expand so children render

	// Filter to is:done — the current-window session's project does NOT match on
	// identity, but the session is pinned; its project header must come along.
	applyListFilter(&app.sessionList, "is:done")

	// Walk the visible rows; the pinned current session must be immediately
	// preceded (somewhere above, within its section) by its project header.
	var sawCurProject bool
	var sawCurSession bool
	for _, item := range app.sessionList.VisibleItems() {
		switch v := item.(type) {
		case projectItem:
			if v.basePath == "/tmp/cur" || v.displayName == "cur" {
				sawCurProject = true
			}
		case sessionItem:
			if v.sess.ID == "cur1" {
				sawCurSession = true
				if !sawCurProject {
					t.Fatal("current-window session rendered before/without its project header")
				}
			}
		}
	}
	if !sawCurSession {
		t.Fatal("pinned current-window session not visible under filter")
	}
	if !sawCurProject {
		t.Fatal("project header for the pinned current-window session is missing")
	}
}
