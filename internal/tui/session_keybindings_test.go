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
	app.sessionList = newSessionList(app.sessions, app.sessSplit.ListWidth(app.width, app.splitRatio), contentH, app.sessGroupMode, app.selectedSet, app.hiddenBadges, app.config.WorktreeDir)
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

func TestSessionsTabStillCyclesGroupMode(t *testing.T) {
	app := newSessionKeybindingApp()
	start := app.sessGroupMode

	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyTab})
	app = m.(*App)
	if app.sessGroupMode == start {
		t.Fatalf("tab should still cycle group mode, stayed at %d", start)
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

func TestSessionsHelpShowsNavigationAndTabGrouping(t *testing.T) {
	app := newSessionKeybindingApp()
	app.sessSplit.Show = false

	help := stripANSI(app.sessHelpLine())
	for _, want := range []string{"g/G:top/end", "tab/S-tab:group"} {
		if !strings.Contains(help, want) {
			t.Fatalf("expected sessions help to contain %q, got %q", want, help)
		}
	}
}
