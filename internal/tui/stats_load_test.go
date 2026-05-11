package tui

import (
	"testing"

	"github.com/sendbird/ccx/internal/session"
)

func TestGlobalStatsMsg_DoesNotForceViewAfterEscape(t *testing.T) {
	app := newTestApp(fakeSessions())
	app.state = viewSessions

	// Simulate a stats scan that was in flight when the user left the view.
	app.globalStatsLoading = true

	msg := globalStatsMsg(session.AggregateStats(app.sessions, ""))
	m, _ := app.Update(msg)
	got := m.(*App)

	if got.state != viewSessions {
		t.Fatalf("stats msg must not yank user back to stats view, got state=%v", got.state)
	}
	if got.globalStatsLoading {
		t.Fatalf("globalStatsLoading should be cleared even when we discard the result")
	}
	if got.globalStatsCache == nil {
		t.Fatalf("globalStatsCache should be populated so re-entering stats is instant")
	}
}

func TestGlobalStatsMsg_StillUpdatesWhenStillOnStatsView(t *testing.T) {
	app := newTestApp(fakeSessions())
	app.state = viewGlobalStats
	app.globalStatsLoading = true

	msg := globalStatsMsg(session.AggregateStats(app.sessions, ""))
	m, _ := app.Update(msg)
	got := m.(*App)

	if got.state != viewGlobalStats {
		t.Fatalf("expected stats view to stay active, got %v", got.state)
	}
	if got.globalStatsLoading {
		t.Fatalf("globalStatsLoading must be cleared after data arrives")
	}
	if got.globalStatsCache == nil {
		t.Fatalf("expected globalStatsCache to be populated")
	}
}
