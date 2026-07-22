package tui

import (
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// TestSetItemsPreservesFilterVisibility reproduces the "[filtered] / No items"
// regression: with a filter applied, a ref-status sync (which calls SetItems to
// mutate a row in place) must not blank the visible list. bubbles' SetItems
// clears filteredItems and defers the re-filter to a tea.Cmd the caller drops;
// setListItemsPreservingFilter re-filters synchronously so VisibleItems() stays
// correct.
func TestSetItemsPreservesFilterVisibility(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		{ID: "live1", ShortID: "live1", ProjectPath: "/tmp/a", ProjectName: "a", ModTime: now, IsLive: true, HasRefs: true},
		{ID: "done1", ShortID: "done1", ProjectPath: "/tmp/b", ProjectName: "b", ModTime: now, Todos: []session.TodoItem{{Status: "completed"}}},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupFlat
	app.rebuildSessionList()

	// Apply a filter that matches the live session.
	applyListFilter(&app.sessionList, "is:live")
	before := len(app.sessionList.VisibleItems())
	if before == 0 {
		t.Fatal("precondition: filter should match the live session")
	}

	// Simulate an async ref-status resolve landing for the live session, which
	// mutates its row and calls SetItems under the hood.
	app.sessions[0].Refs = []session.SessionRef{{URL: "https://example.test/pr/1", Resolved: true}}
	app.sessions[0].RefsResolved = true
	app.syncSessionRefsToList("live1")

	after := len(app.sessionList.VisibleItems())
	if after != before {
		t.Fatalf("ref sync blanked the filtered list: before=%d after=%d (filterState=%v)",
			before, after, app.sessionList.FilterState())
	}
}
