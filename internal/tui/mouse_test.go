package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSessionSingleClickSelectsVisibleFilteredItem(t *testing.T) {
	app := newTestApp(fakeSessions())
	app.rebuildSessionList()
	applyListFilter(&app.sessionList, "proj-b")

	if len(app.sessionList.VisibleItems()) != 1 {
		t.Fatalf("expected exactly one visible session after filter, got %d", len(app.sessionList.VisibleItems()))
	}

	m, _ := app.handleMouseClick(tea.MouseMsg{
		X:      1,
		Y:      1,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	app = m.(*App)

	sess, ok := app.selectedSession()
	if !ok {
		t.Fatal("expected a selected session after click")
	}
	if sess.ProjectName != "proj-b" {
		t.Fatalf("expected clicked filtered session proj-b, got %q", sess.ProjectName)
	}
}
