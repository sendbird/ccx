package tui

import (
	"strings"
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

func TestConversationFixedHeaderAndBodyMouseClicks(t *testing.T) {
	app := setupFixedContextConvApp(t, 110, 20)
	_ = app.renderConvSplit() // establish the effective clamped header inset
	if app.conv.split.headerInset < len(app.conv.contextItems) {
		t.Fatalf("header inset = %d, want at least %d context rows", app.conv.split.headerInset, len(app.conv.contextItems))
	}

	// Screen Y includes the title bar and the PINNED section label, so Y=3
	// targets context row index 1.
	m, _ := app.handleMouseClick(tea.MouseMsg{
		X:      1,
		Y:      3,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	app = m.(*App)
	item, ok := app.selectedConversationItem()
	if !ok || !app.conv.contextActive || item.sessionMeta != "memory" {
		t.Fatalf("header click selected active=%t item=%#v", app.conv.contextActive, item)
	}
	if !strings.Contains(stripANSI(app.conv.split.Preview.View()), "fixed todo") {
		t.Fatal("header click did not update the Session Memory preview")
	}

	// The first body row starts after title bar + the complete fixed inset.
	bodyY := 1 + app.conv.split.headerInset
	m, _ = app.handleMouseClick(tea.MouseMsg{
		X:      1,
		Y:      bodyY,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	app = m.(*App)
	item, ok = app.selectedConversationItem()
	if !ok || app.conv.contextActive || item.kind != convMsg || app.convList.Index() != 0 {
		t.Fatalf("body click selected active=%t idx=%d item=%#v", app.conv.contextActive, app.convList.Index(), item)
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
