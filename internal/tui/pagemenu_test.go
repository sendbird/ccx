package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/sendbird/ccx/internal/session"
)

// The `p` preview-page combo existed, but only while the PREVIEW pane had
// focus. The normal position is the cursor in the LIST wanting to change what
// the right pane shows — there `p` did nothing at all, which is why it read as
// "the combo does not exist".

// TestPageMenuOpensFromTheList is the reported gap.
func TestPageMenuOpensFromTheList(t *testing.T) {
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
	}}
	app := newTestApp(sessions)
	app.sessSplit.Show = true
	app.sessSplit.Focus = false // cursor in the list — the normal position

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	got := m.(*App)
	if !got.sessPageMenu {
		t.Fatal("p from the list did not open the preview-page menu")
	}

	// The next key picks a mode.
	m, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	got = m.(*App)
	if got.sessPageMenu {
		t.Error("the menu should close after a mode is picked")
	}
	if got.sessPreviewMode != sessPreviewOutputs {
		t.Errorf("p→o left the preview mode at %v, want outputs", got.sessPreviewMode)
	}
}

// TestPageMenuStillOpensFromTheFocusedPreview pins the behavior that already
// worked — moving the binding out of the focus block must not lose it.
func TestPageMenuStillOpensFromTheFocusedPreview(t *testing.T) {
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
	}}
	app := newTestApp(sessions)
	app.sessSplit.Show = true
	app.sessSplit.Focus = true

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !m.(*App).sessPageMenu {
		t.Fatal("p from the focused preview stopped opening the page menu")
	}
}

// TestPageMenuSuppressedOnDayRows keeps the two mechanisms for one thing in
// agreement: a date row (and a day-scoped project row) always renders that
// scope's outputs, so it has no preview modes — the number keys already refuse
// there (rowSupportsPreviewModes), and the p menu must refuse identically
// rather than offering eleven entries that all no-op.
func TestPageMenuSuppressedOnDayRows(t *testing.T) {
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
	}}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()
	app.sessionList.Select(0) // the date row
	app.sessSplit.Show = true

	if !app.selectedOwnsDayPane() {
		t.Fatal("fixture should put the cursor on a day row")
	}
	before := app.sessPreviewMode

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	got := m.(*App)
	if got.sessPageMenu {
		t.Error("the page menu opened on a row that cannot honor any preview mode")
	}
	if got.sessPreviewMode != before {
		t.Errorf("preview mode changed to %v on a day row", got.sessPreviewMode)
	}
	if got.copiedMsg == "" {
		t.Error("p was swallowed silently — the user gets no reason why")
	}
}

// TestPageMenuDoesNotSwallowSearchInput pins the letter's other consumer: while
// the "/" filter is active, p is text, not a command.
func TestPageMenuDoesNotSwallowSearchInput(t *testing.T) {
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
	}}
	app := newTestApp(sessions)
	app.sessSplit.Show = true
	app.sessionList.SetFilterState(list.Filtering)

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	got := m.(*App)
	if got.sessPageMenu {
		t.Fatal("p opened the page menu while the search filter was taking input")
	}
	if v := got.sessionList.FilterValue(); v != "p" {
		t.Errorf("the filter input received %q, want the typed p", v)
	}
}

// TestPageMenuCountsAsAnOverlay guards the digit shortcuts: with the page menu
// open, a number key belongs to the menu's dismissal, not to the top-level
// preview-mode shortcuts firing behind it.
func TestPageMenuCountsAsAnOverlay(t *testing.T) {
	app := newTestApp(nil)
	app.sessPageMenu = true
	if !app.isInOverlay() {
		t.Error("an open page menu must count as an overlay")
	}
}

func TestPageMenuOpensWithThePreviewClosed(t *testing.T) {
	// ccx can start with the split hidden, and `p` is exactly the key you reach
	// for to say "show me something in the preview". Before the fix the binding
	// sat behind `sp.Focus && sp.Show`, so a closed preview swallowed it — the
	// pane did not even open.
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: time.Now(), MsgCount: 5},
	}
	app := newTestApp(sessions)
	app.sessSplit.Show = false
	app.sessSplit.Focus = false
	app.rebuildSessionList()

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	a := m.(*App)
	if !a.sessPageMenu {
		t.Fatal("expected p to open the page menu with the preview closed")
	}

	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	b := m.(*App)
	if b.sessPreviewMode != sessPreviewStats {
		t.Fatalf("expected the stats preview, got %d", b.sessPreviewMode)
	}
	// Picking a mode from a closed preview has to open the pane too — otherwise
	// the mode changes behind a hidden pane and nothing visible happens.
	if !b.sessSplit.Show {
		t.Fatal("expected picking a mode to open the preview pane")
	}
}
