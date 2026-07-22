package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/sendbird/ccx/internal/session"
)

// TestStartSearchRepopulatesClearedFilter verifies that opening "/" over an
// already-applied filter shows the matching rows even if a prior SetItems left
// filteredItems empty. Regression: the left list went blank when "/" was pressed
// while is:live,is:input,is:mon was applied.
func TestStartSearchRepopulatesClearedFilter(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		{ID: "live1", ShortID: "live1", ProjectPath: "/tmp/a", ProjectName: "a", ModTime: now, IsLive: true, HasRefs: true},
		{ID: "done1", ShortID: "done1", ProjectPath: "/tmp/b", ProjectName: "b", ModTime: now, Todos: []session.TodoItem{{Status: "completed"}}},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupFlat
	app.rebuildSessionList()

	// Apply the filter, then simulate the filteredItems-clearing damage a bare
	// SetItems causes (bubbles nils filteredItems and defers the re-filter cmd).
	applyListFilter(&app.sessionList, "is:live")
	app.sessionList.SetItems(app.sessionList.Items()) // drops the re-filter cmd
	if len(app.sessionList.VisibleItems()) != 0 {
		t.Skip("precondition: SetItems no longer clears filteredItems; nothing to test")
	}

	// Opening the search must repopulate the visible set.
	startListSearch(&app.sessionList)
	if app.sessionList.FilterState() != list.Filtering {
		t.Fatalf("expected Filtering after '/', got %v", app.sessionList.FilterState())
	}
	if len(app.sessionList.VisibleItems()) == 0 {
		t.Fatal("'/' over an applied filter left the list blank")
	}
}
