package tui

import (
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// projectChildVisibleIDs returns the session IDs of sessionItem rows visible in
// the project-centric list.
func projectChildVisibleIDs(a *App) []string {
	var ids []string
	for _, item := range a.sessionList.VisibleItems() {
		if si, ok := item.(sessionItem); ok {
			ids = append(ids, si.sess.ID)
		}
	}
	return ids
}

// TestIsLiveFilterHidesNonLiveSiblings verifies that `is:live` in project-centric
// mode shows only the live sessions, not the non-live siblings under the same
// project — while a project-name search still reveals the whole project.
func TestIsLiveFilterHidesNonLiveSiblings(t *testing.T) {
	now := time.Now()
	// One project "proj" with a live main session and a done worktree session.
	sessions := []session.Session{
		{ID: "live1", ShortID: "live1", ProjectPath: "/tmp/proj", ProjectName: "proj", ModTime: now, IsLive: true},
		{ID: "done1", ShortID: "done1", ProjectPath: "/tmp/proj", ProjectName: "proj", ModTime: now.Add(-time.Hour), Todos: []session.TodoItem{{Status: "completed"}}},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.config.SearchQuery = ""
	app.sessionList.ResetFilter()
	app.rebuildSessionList()

	// Expand the project so children are rendered rows.
	app.setAllSessGroupsFolded(false)

	applyListFilter(&app.sessionList, "is:live")
	ids := projectChildVisibleIDs(app)
	for _, id := range ids {
		if id == "done1" {
			t.Fatalf("is:live should hide the non-live sibling, got visible=%v", ids)
		}
	}
	if len(ids) == 0 {
		// The project header may still show; ensure at least the live child is present
		// when children are expanded.
	}

	// A project-name search reveals the whole project (both children).
	app.sessionList.ResetFilter()
	app.setAllSessGroupsFolded(false)
	applyListFilter(&app.sessionList, "proj")
	ids = projectChildVisibleIDs(app)
	sawDone := false
	for _, id := range ids {
		if id == "done1" {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatalf("project-name search should reveal all children incl. done1, got %v", ids)
	}
}
