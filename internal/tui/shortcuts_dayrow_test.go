package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// dailyShortcutApp builds a daily-view app whose list is
// day → project → sessions, so each tier can be put under the cursor.
func dailyShortcutApp(t *testing.T) *App {
	t.Helper()
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: day, MsgCount: 10},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: day.Add(-time.Hour), MsgCount: 5},
		{ID: "b1", ShortID: "b1", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: day.Add(-2 * time.Hour), MsgCount: 7},
	}
	a := newTestApp(sessions)
	a.sessGroupMode = groupDaily
	a.rebuildSessionList()
	return a
}

// selectRowKind moves the cursor to the first row of the requested kind.
func selectRowKind(t *testing.T, a *App, kind string) {
	t.Helper()
	for i, it := range a.sessionList.VisibleItems() {
		var got string
		switch v := it.(type) {
		case dayItem:
			got = "day"
		case projectItem:
			got = "project"
			if v.dayKey == "" {
				got = "plainproject"
			}
		case sessionItem:
			got = "session"
		}
		if got == kind {
			a.sessionList.Select(i)
			return
		}
	}
	t.Fatalf("no %s row in the list", kind)
}

// A date row always renders the day-outputs pane, so the preview-mode digits
// cannot apply to it. Before the fix they mutated sessPreviewMode invisibly.
func TestShortcutDigitsIgnoredOnDayRow(t *testing.T) {
	a := dailyShortcutApp(t)
	selectRowKind(t, a, "day")

	a.sessPreviewMode = sessPreviewConversation
	_, _, handled := a.handleShortcutKey("3")

	if !handled {
		t.Fatal("the digit must be swallowed on a day row, not fall through to the list cursor")
	}
	if a.sessPreviewMode != sessPreviewConversation {
		t.Fatalf("preview mode changed on a day row: got %v, want %v (unchanged)",
			a.sessPreviewMode, sessPreviewConversation)
	}
}

// A day-scoped project row drives the same pane as its date row.
func TestShortcutDigitsIgnoredOnDayProjectRow(t *testing.T) {
	a := dailyShortcutApp(t)
	selectRowKind(t, a, "project")

	if !a.selectedOwnsDayPane() {
		t.Fatal("fixture is wrong: the selected project row should own the day pane")
	}
	a.sessPreviewMode = sessPreviewConversation
	_, _, handled := a.handleShortcutKey("5")

	if !handled {
		t.Fatal("the digit must be swallowed on a day-scoped project row")
	}
	if a.sessPreviewMode != sessPreviewConversation {
		t.Fatalf("preview mode changed on a day-scoped project row: got %v, want %v (unchanged)",
			a.sessPreviewMode, sessPreviewConversation)
	}
}

// The hint is the footer's promise about what the digits do. On a row with no
// preview modes it must promise nothing.
func TestShortcutHintDropsPreviewModesOnDayRow(t *testing.T) {
	a := dailyShortcutApp(t)
	selectRowKind(t, a, "day")

	hint := a.shortcutHint()
	for _, advertised := range []string{"agents", "conv", "refs", "stats", "live"} {
		if strings.Contains(hint, advertised) {
			t.Fatalf("hint on a day row advertises %q, which that row cannot render: %q", advertised, hint)
		}
	}
}

// The counterpart: ordinary session rows must keep every digit working.
func TestShortcutDigitsStillSwitchPreviewOnSessionRow(t *testing.T) {
	a := dailyShortcutApp(t)
	selectRowKind(t, a, "session")

	a.sessPreviewMode = sessPreviewConversation
	_, _, handled := a.handleShortcutKey("3")

	if !handled {
		t.Fatal("digit shortcuts must stay live on a session row")
	}
	if a.sessPreviewMode != sessPreviewAgents {
		t.Fatalf("key 3 on a session row: got mode %v, want %v (agents)",
			a.sessPreviewMode, sessPreviewAgents)
	}
	if hint := a.shortcutHint(); !strings.Contains(hint, "3:agents") {
		t.Fatalf("session row hint must still advertise the preview modes, got %q", hint)
	}
}

// A project head in the non-daily browser is deliberately left alone: its
// preview falls back to the project's most-recent session, so the digits do
// change what is on screen there.
func TestShortcutDigitsStillWorkOnPlainProjectRow(t *testing.T) {
	a := newTestApp([]session.Session{
		{ID: "x", ShortID: "x", ProjectPath: "/tmp/repo-x", ProjectName: "repo-x", ModTime: time.Now(), MsgCount: 1},
	})
	a.sessGroupMode = groupProjectCentric
	a.rebuildSessionList()
	selectRowKind(t, a, "plainproject")

	a.sessPreviewMode = sessPreviewConversation
	if _, _, handled := a.handleShortcutKey("3"); !handled {
		t.Fatal("digit shortcuts must stay live on a plain project row")
	}
	if a.sessPreviewMode != sessPreviewAgents {
		t.Fatalf("key 3 on a plain project row: got mode %v, want %v (agents)",
			a.sessPreviewMode, sessPreviewAgents)
	}
}
