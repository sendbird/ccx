package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

func TestZoomedInspectorMouseTreatsFullWidthAsPreview(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app.openInspector(inspectorConversation, session.ScopeNode, true)
	selected := app.convList.Index()
	borderX := app.conv.split.ListWidth(app.width, app.splitRatio)

	if app.tryStartDragResize(tea.MouseMsg{X: borderX, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}) {
		t.Fatal("zoomed inspector started hidden divider resize")
	}
	if app.dragResizing {
		t.Fatal("zoomed inspector left drag resize active")
	}

	m, _ := app.handleMouseClick(tea.MouseMsg{X: 1, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	app = m.(*App)
	if !app.conv.split.Focus || app.convList.Index() != selected {
		t.Fatalf("left-side zoom click escaped inspector: focus=%t selected=%d want=%d", app.conv.split.Focus, app.convList.Index(), selected)
	}

	m, _ = app.handleMouseScroll(tea.MouseMsg{X: 1, Y: 5, Button: tea.MouseButtonWheelDown})
	app = m.(*App)
	if !app.conv.split.Focus || app.convList.Index() != selected {
		t.Fatalf("left-side zoom scroll changed hidden list: focus=%t selected=%d want=%d", app.conv.split.Focus, app.convList.Index(), selected)
	}

	app.lastClickY = 6
	app.lastClickTime = time.Now()
	m, _ = app.handleMouseClick(tea.MouseMsg{X: 1, Y: 6, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	app = m.(*App)
	if app.convList.Index() != selected || !app.conv.inspector.Zoom {
		t.Fatalf("zoom double-click escaped to hidden list: selected=%d want=%d zoom=%t", app.convList.Index(), selected, app.conv.inspector.Zoom)
	}
}

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
