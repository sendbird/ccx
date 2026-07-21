package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// stateFilterSessions returns sessions spanning several lifecycle states so the
// state-toggle filter can be exercised: one live, one awaiting input, one done.
func stateFilterSessions() []session.Session {
	now := time.Now()
	return []session.Session{
		{ID: "live1", ShortID: "live1", ProjectPath: "/tmp/live", ProjectName: "live", ModTime: now, MsgCount: 4, IsLive: true},
		{ID: "input1", ShortID: "input1", ProjectPath: "/tmp/input", ProjectName: "input", ModTime: now, MsgCount: 4, IsLive: true, AwaitingInput: true},
		{ID: "done1", ShortID: "done1", ProjectPath: "/tmp/done", ProjectName: "done", ModTime: now.Add(-3 * time.Hour), MsgCount: 9, Todos: []session.TodoItem{{Content: "task", Status: "completed"}}},
	}
}

func newStateFilterApp(t *testing.T) *App {
	t.Helper()
	app := newTestApp(stateFilterSessions())
	app.sessGroupMode = groupFlat
	app.config.SearchQuery = ""
	app.sessionList.ResetFilter()
	app.rebuildSessionList()
	return app
}

func visibleSessionIDs(a *App) []string {
	var ids []string
	for _, item := range a.sessionList.VisibleItems() {
		if si, ok := item.(sessionItem); ok {
			ids = append(ids, si.sess.ID)
		}
	}
	return ids
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestApplyStartupFilterClearsWhenEmpty verifies the auto-applied active-state
// default filter is dropped (rather than leaving a blank browser) when no
// session matches it — the "아무것도 안 보임" case.
func TestApplyStartupFilterClearsWhenEmpty(t *testing.T) {
	// All sessions are plain/done — none live/input/mon.
	now := time.Now()
	sessions := []session.Session{
		{ID: "d1", ShortID: "d1", ProjectPath: "/tmp/d1", ProjectName: "d1", ModTime: now},
		{ID: "d2", ShortID: "d2", ProjectPath: "/tmp/d2", ProjectName: "d2", ModTime: now},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupFlat
	app.rebuildSessionList()

	// Simulate the startup default filter being armed.
	app.config.SearchQuery = defaultActiveStateFilter
	app.autoStateFilter = true
	app.applyStartupFilter()

	if app.config.SearchQuery != "" {
		t.Fatalf("auto filter should be cleared when it hides everything, got %q", app.config.SearchQuery)
	}
	if visibleSessionIDs(app) == nil {
		t.Fatal("expected sessions visible after auto filter cleared")
	}
}

// TestApplyStartupFilterKeepsExplicitEmpty verifies a user's explicit filter is
// NOT auto-cleared even if it matches nothing (only the auto default is).
func TestApplyStartupFilterKeepsExplicitEmpty(t *testing.T) {
	now := time.Now()
	app := newTestApp([]session.Session{{ID: "d1", ShortID: "d1", ProjectName: "d1", ProjectPath: "/tmp/d1", ModTime: now}})
	app.sessGroupMode = groupFlat
	app.rebuildSessionList()
	app.config.SearchQuery = "nomatch-xyz"
	app.autoStateFilter = false
	app.applyStartupFilter()
	if app.config.SearchQuery != "nomatch-xyz" {
		t.Fatalf("explicit filter should be preserved, got %q", app.config.SearchQuery)
	}
}

// TestStateFilterCommaOR verifies the state-toggle menu builds a comma-OR filter
// and that multiple states are shown at once (which plain AND cannot express).
func TestStateFilterCommaOR(t *testing.T) {
	app := newStateFilterApp(t)

	// Toggle live on: only the live sessions show.
	app.toggleStateFilter("l") // is:live
	ids := visibleSessionIDs(app)
	if !containsID(ids, "live1") || !containsID(ids, "input1") {
		t.Fatalf("live filter should show live sessions, got %v", ids)
	}
	if containsID(ids, "done1") {
		t.Fatalf("live filter should hide done session, got %v", ids)
	}

	// Add done on: now live OR done both show (comma-OR).
	app.toggleStateFilter("d") // is:done
	if got := strings.TrimSpace(app.activeFilterValue()); got != "is:live,is:done" {
		t.Fatalf("combined filter = %q, want is:live,is:done", got)
	}
	ids = visibleSessionIDs(app)
	if !containsID(ids, "live1") || !containsID(ids, "done1") {
		t.Fatalf("live+done filter should show both, got %v", ids)
	}

	// Toggle live back off: only done remains.
	app.toggleStateFilter("l")
	if got := strings.TrimSpace(app.activeFilterValue()); got != "is:done" {
		t.Fatalf("after removing live, filter = %q, want is:done", got)
	}
}

// TestStateMenuClearShowsAll verifies the "a" sub-key clears the filter.
func TestStateMenuClearShowsAll(t *testing.T) {
	app := newStateFilterApp(t)
	app.toggleStateFilter("d")
	if strings.TrimSpace(app.activeFilterValue()) == "" {
		t.Fatal("precondition: expected an active filter")
	}
	app.stateMenu = true
	app.handleStateMenu("a")
	if got := strings.TrimSpace(app.activeFilterValue()); got != "" {
		t.Fatalf("after clear, filter = %q, want empty", got)
	}
	if app.stateMenu {
		t.Fatal("state menu should close after a keypress")
	}
}

// TestStateFilterBadge verifies the title-bar badge summarizes the active states.
func TestStateFilterBadge(t *testing.T) {
	app := newStateFilterApp(t)
	if b := app.stateFilterBadge(); b != "" {
		t.Fatalf("no filter should yield empty badge, got %q", b)
	}
	app.toggleStateFilter("d")
	if b := app.stateFilterBadge(); b != "DONE-ONLY" {
		t.Fatalf("single-state badge = %q, want DONE-ONLY", b)
	}
	app.toggleStateFilter("l")
	if b := app.stateFilterBadge(); b != "LIVE·DONE" {
		t.Fatalf("multi-state badge = %q, want LIVE·DONE", b)
	}
}
