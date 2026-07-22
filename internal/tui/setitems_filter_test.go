package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
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

// TestSetItemsPreservesCursor verifies an in-place row update (fired repeatedly
// as async ref statuses resolve) does not snap the selection back to the top.
// The reset only happens under an applied filter (SetFilterText calls GoToStart),
// so the list is filtered here.
func TestSetItemsPreservesCursor(t *testing.T) {
	now := time.Now()
	var sessions []session.Session
	for i := 0; i < 6; i++ {
		id := string(rune('a' + i))
		sessions = append(sessions, session.Session{
			ID: id, ShortID: id, ProjectPath: "/tmp/" + id, ProjectName: id,
			ModTime: now.Add(-time.Duration(i) * time.Hour), HasRefs: true, IsLive: true,
		})
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupFlat
	app.rebuildSessionList()
	applyListFilter(&app.sessionList, "is:live") // all six match

	// Move the cursor off the top.
	app.sessionList.Select(3)
	if app.sessionList.Index() != 3 {
		t.Fatalf("precondition: cursor should be at 3, got %d", app.sessionList.Index())
	}

	// A ref-status resolve lands for a row → in-place SetItems.
	app.sessions[3].Refs = []session.SessionRef{{URL: "https://example.test/pr/9", Resolved: true}}
	app.sessions[3].RefsResolved = true
	app.syncSessionRefsToList(app.sessions[3].ID)

	if got := app.sessionList.Index(); got != 3 {
		t.Fatalf("ref sync reset the cursor: got %d, want 3", got)
	}
}

// TestSetItemsDoesNotCloseActiveSearch verifies that an in-place row update
// (async ref-status resolve) does NOT force the search box shut while the user
// is actively typing a filter — state must stay Filtering, not flip to
// FilterApplied.
func TestSetItemsDoesNotCloseActiveSearch(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		{ID: "a", ShortID: "a", ProjectPath: "/tmp/a", ProjectName: "a", ModTime: now, HasRefs: true},
		{ID: "b", ShortID: "b", ProjectPath: "/tmp/b", ProjectName: "b", ModTime: now},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupFlat
	app.rebuildSessionList()

	// Open the search (state → Filtering) and type a character, as a user does.
	startListSearch(&app.sessionList)
	app.sessionList.FilterInput.SetValue("a")
	if app.sessionList.FilterState() != list.Filtering {
		t.Fatalf("precondition: expected Filtering state, got %v", app.sessionList.FilterState())
	}

	// A ref-status resolve lands mid-typing.
	app.sessions[0].Refs = []session.SessionRef{{URL: "https://example.test/pr/1", Resolved: true}}
	app.sessions[0].RefsResolved = true
	app.syncSessionRefsToList("a")

	if got := app.sessionList.FilterState(); got != list.Filtering {
		t.Fatalf("ref sync closed the active search: state=%v, want Filtering", got)
	}
}
