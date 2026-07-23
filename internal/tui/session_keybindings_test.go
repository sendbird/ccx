package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newSessionKeybindingApp() *App {
	app := newTestApp(fakeSessions())
	app.sessionsLoading = false
	app.sessSplit.Show = false
	app.sessSplit.Focus = false
	contentH := ContentHeight(app.height)
	app.sessionList = newSessionList(app.sessions, app.sessSplit.ListWidth(app.width, app.splitRatio), contentH, app.sessGroupMode, app.selectedSet, app.hiddenBadges, app.sessFolded, app.sessionRowCache, app.config.WorktreeDir)
	app.sessionList.ResetFilter()
	app.sessSplit.List = &app.sessionList
	return app
}

func TestSessionsGGJumpsToTop(t *testing.T) {
	app := newSessionKeybindingApp()
	if got := len(app.sessionList.VisibleItems()); got < 3 {
		t.Fatalf("expected at least 3 visible items, got %d", got)
	}
	app.sessionList.Select(2)
	if got := app.sessionList.Index(); got != 2 {
		t.Fatalf("expected precondition index 2, got %d", got)
	}

	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	app = m.(*App)
	if app.sessionList.Index() != 2 {
		t.Fatalf("single g should only arm pending jump, got index %d", app.sessionList.Index())
	}
	if !app.sessPendingG {
		t.Fatal("expected pending g after first g")
	}

	m, _ = app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	app = m.(*App)
	if app.sessionList.Index() != 0 {
		t.Fatalf("gg should jump to top, got index %d", app.sessionList.Index())
	}
	if app.sessPendingG {
		t.Fatal("pending g should clear after gg")
	}
}

func TestSessionsGJumpsToEnd(t *testing.T) {
	app := newSessionKeybindingApp()
	app.sessionList.Select(0)

	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	app = m.(*App)
	want := len(app.sessionList.VisibleItems()) - 1
	if app.sessionList.Index() != want {
		t.Fatalf("G should jump to end, got index %d want %d", app.sessionList.Index(), want)
	}
}

func TestSessionsTabCyclesPreviewModeInsteadOfGrouping(t *testing.T) {
	app := newSessionKeybindingApp()
	app.sessPreviewMode = sessPreviewConversation
	startGroup := app.sessGroupMode

	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyTab})
	app = m.(*App)
	if app.sessGroupMode != startGroup {
		t.Fatalf("tab should no longer cycle group mode, changed from %d to %d", startGroup, app.sessGroupMode)
	}
	if !app.sessSplit.Show {
		t.Fatalf("tab should open the preview when hidden")
	}
}

func TestSessionsSpaceDoesNotSelectWhenPreviewFocused(t *testing.T) {
	app := newSessionKeybindingApp()
	app.sessSplit.Show = true
	app.sessSplit.Focus = true
	app.sessPreviewMode = sessPreviewConversation

	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeySpace})
	app = m.(*App)
	if app.hasMultiSelection() {
		t.Fatalf("space in focused preview should not multi-select session, got %v", app.selectedSet)
	}
}

func TestSessionsHelpShowsNavigationAndPreviewTab(t *testing.T) {
	app := newSessionKeybindingApp()
	app.sessSplit.Show = false

	// Footer is concise: core actions + the ?:help affordance. The full key
	// list moved into the "?" overlay.
	help := stripANSI(app.sessHelpLine())
	for _, want := range []string{"↵:open", "?:help", "q:quit"} {
		if !strings.Contains(help, want) {
			t.Fatalf("expected sessions footer to contain %q, got %q", want, help)
		}
	}

	// The context-aware overlay carries the detailed keys.
	overlay := stripANSI(app.renderHelpModal("", 120, 80))
	for _, want := range []string{"Sessions", "Preview", "Multi-select", "Common keys"} {
		if !strings.Contains(overlay, want) {
			t.Fatalf("expected help overlay to contain %q, got %q", want, overlay)
		}
	}
}

// TestSessionShortcutRebinds verifies the #112 shortcut rebinds dispatch to the
// right action: s opens the state menu (was S), V opens the views menu (was v),
// x then e opens the edit menu (was e), and the old top-level s/L/D are gone.
func TestSessionShortcutRebinds(t *testing.T) {
	app := newSessionKeybindingApp()

	// s → state menu (formerly S).
	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	app = m.(*App)
	if !app.stateMenu {
		t.Fatal("s should open the state menu")
	}
	app.stateMenu = false

	// V → views menu (formerly v).
	m, _ = app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'V'}})
	app = m.(*App)
	if !app.viewsMenu {
		t.Fatal("V should open the views menu")
	}
	app.viewsMenu = false

	// x → actions menu, then e → edit menu (formerly top-level e).
	m, _ = app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	app = m.(*App)
	if !app.actionsMenu {
		t.Fatal("x should open the actions menu")
	}
	m, _ = app.handleActionsMenu("e")
	app = m.(*App)
	if !app.editMenu {
		t.Fatal("x → e should open the edit menu")
	}
}
