package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/sendbird/ccx/internal/session"
)

// TestBackgroundRefreshKeepsListPopulatedWhileFiltering reproduces the blank-list
// regression seen when an async ref-status resolve lands while the user is
// actively editing the filter ("/" pressed → FilterState == Filtering).
//
// The resolve calls syncSessionRefsToList → setListItemsPreservingFilter, which
// used to skip the synchronous re-filter for any non-FilterApplied state. In
// Filtering it just called SetItems — which nils filteredItems and returns a
// re-filter tea.Cmd that the caller drops. Result: VisibleItems() == 0 and the
// left list renders blank while the search box is still open (the screenshot
// bug).
func TestBackgroundRefreshKeepsListPopulatedWhileFiltering(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		{ID: "live1", ShortID: "live1", ProjectPath: "/tmp/a", ProjectName: "a", ModTime: now, IsLive: true, HasRefs: true},
		{ID: "done1", ShortID: "done1", ProjectPath: "/tmp/b", ProjectName: "b", ModTime: now, Todos: []session.TodoItem{{Status: "completed"}}},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupFlat
	app.rebuildSessionList()

	// Apply a filter, then open it for editing so the list is in Filtering.
	applyListFilter(&app.sessionList, "is:live")
	startListSearch(&app.sessionList)
	if app.sessionList.FilterState() != list.Filtering {
		t.Fatalf("precondition: expected Filtering after '/', got %v", app.sessionList.FilterState())
	}
	if len(app.sessionList.VisibleItems()) == 0 {
		t.Fatal("precondition: list already blank before refresh")
	}

	// An async ref-status resolve lands while the user is still editing the
	// filter — the real path that produced the screenshot.
	app.sessions[0].Refs = []session.SessionRef{{URL: "https://example.test/pr/1", Resolved: true}}
	app.sessions[0].RefsResolved = true
	app.syncSessionRefsToList("live1")

	// The search box must stay open AND the matching rows must remain visible.
	if app.sessionList.FilterState() != list.Filtering {
		t.Fatalf("refresh closed the search box: state=%v", app.sessionList.FilterState())
	}
	if len(app.sessionList.VisibleItems()) == 0 {
		t.Fatal("background refresh during active filtering left the list blank")
	}
}
