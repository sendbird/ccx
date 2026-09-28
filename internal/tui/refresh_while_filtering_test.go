package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// TestBackgroundRefreshKeepsListPopulatedWhileFiltering reproduces the blank-list
// regression seen when an async ref-status resolve lands while the user is
// actively editing the filter ("/" pressed → FilterState == Filtering).
//
// The resolve calls syncSessionRefsToList → setListItemsPreservingFilter, which
// used to skip the synchronous re-filter for any non-FilterApplied state. In
// Filtering it just called SetItems — which nils filteredItems and returns a
// re-filter tea.Cmd that the caller drops. Result: VisibleItems() == 0 and the
// left list renders blank while the search box is still open (the screenshot
// bug).
func TestBackgroundRefreshKeepsListPopulatedWhileFiltering(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		{ID: "live1", ShortID: "live1", ProjectPath: "/tmp/a", ProjectName: "a", ModTime: now, IsLive: true, HasRefs: true},
		{ID: "done1", ShortID: "done1", ProjectPath: "/tmp/b", ProjectName: "b", ModTime: now, Todos: []session.TodoItem{{Status: "completed"}}},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupFlat
	app.rebuildSessionList()

	// Apply a filter, then open it for editing so the list is in Filtering.
	applyListFilter(&app.sessionList, "is:live")
	startListSearch(&app.sessionList)
	if app.sessionList.FilterState() != list.Filtering {
		t.Fatalf("precondition: expected Filtering after '/', got %v", app.sessionList.FilterState())
	}
	if len(app.sessionList.VisibleItems()) == 0 {
		t.Fatal("precondition: list already blank before refresh")
	}

	// An async ref-status resolve lands while the user is still editing the
	// filter — the real path that produced the screenshot.
	app.sessions[0].Refs = []session.SessionRef{{URL: "https://example.test/pr/1", Resolved: true}}
	app.sessions[0].RefsResolved = true
	app.syncSessionRefsToList("live1")

	// The search box must stay open AND the matching rows must remain visible.
	if app.sessionList.FilterState() != list.Filtering {
		t.Fatalf("refresh closed the search box: state=%v", app.sessionList.FilterState())
	}
	if len(app.sessionList.VisibleItems()) == 0 {
		t.Fatal("background refresh during active filtering left the list blank")
	}
}

// TestBackgroundRefreshKeepsCaretWhileFiltering covers the other half of the
// same path: the list survived, but the caret did not.
//
// setListItemsPreservingFilter re-applies the query with SetFilterText, and
// restores Filtering with SetFilterState — and *both* call
// FilterInput.CursorEnd(). This runs on the async ref-resolve, which fires
// repeatedly over several seconds, so every press of left/right was undone a
// moment later and arrow keys looked like they did nothing in the search box.
func TestBackgroundRefreshKeepsCaretWhileFiltering(t *testing.T) {
	app := newTestApp(fakeSessions())
	send := func(m tea.KeyMsg) { x, _ := app.Update(m); app = x.(*App) }
	typeRune := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

	send(typeRune("/"))
	for _, c := range []string{"p", "r", "o", "j"} {
		send(typeRune(c))
	}
	send(tea.KeyMsg{Type: tea.KeyLeft})
	send(tea.KeyMsg{Type: tea.KeyLeft})
	want := app.sessionList.FilterInput.Position()
	if want != 2 {
		t.Fatalf("precondition: caret should be at 2 after two lefts, got %d", want)
	}

	setListItemsPreservingFilter(&app.sessionList, app.sessionList.Items())

	if got := app.sessionList.FilterInput.Position(); got != want {
		t.Errorf("background refresh moved the caret %d -> %d; typing mid-string becomes impossible", want, got)
	}
}

// TestRebuildWhileFilteringKeepsSearchBoxOpen guards the mid-edit case in
// rebuildSessionList itself. Most tick/refresh callers guard on !isFiltering(),
// but several do not (remote-liveness refresh, :commands, fold toggles), and a
// rebuild used to capture the query only in FilterApplied — so landing mid-edit
// closed the search box and threw away what the user had typed.
func TestRebuildWhileFilteringKeepsSearchBoxOpen(t *testing.T) {
	app := newTestApp(fakeSessions())
	send := func(m tea.KeyMsg) { x, _ := app.Update(m); app = x.(*App) }
	typeRune := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

	send(typeRune("/"))
	for _, c := range []string{"p", "r", "o", "j"} {
		send(typeRune(c))
	}
	send(tea.KeyMsg{Type: tea.KeyLeft})
	wantCaret := app.sessionList.FilterInput.Position()

	app.rebuildSessionList()

	if got := app.sessionList.FilterState(); got != list.Filtering {
		t.Errorf("rebuild closed the search box: state=%v, want Filtering", got)
	}
	if got := app.sessionList.FilterInput.Value(); got != "proj" {
		t.Errorf("rebuild discarded the in-progress query: %q", got)
	}
	if got := app.sessionList.FilterInput.Position(); got != wantCaret {
		t.Errorf("rebuild moved the caret %d -> %d", wantCaret, got)
	}
}

// TestDismissedAutoFilterStaysDismissed covers the state filter reappearing on
// its own. The active-state default lives in config.SearchQuery, which is
// separate from the list's filter, and rebuildSessionList re-applies it
// whenever no filter is active. Dismissing the filter with Esc therefore only
// cleared the list — the next live tick put "is:live,is:input,is:mon" straight
// back, so the filter looked like it kept switching itself on.
func TestDismissedAutoFilterStaysDismissed(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		{ID: "live1", ShortID: "live1", ProjectPath: "/tmp/a", ProjectName: "a", ModTime: now, IsLive: true},
		{ID: "done1", ShortID: "done1", ProjectPath: "/tmp/b", ProjectName: "b", ModTime: now,
			Todos: []session.TodoItem{{Status: "completed"}}},
	}
	app := NewApp(sessions, Config{})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	app = m.(*App)
	app.state = viewSessions
	app.sessionsLoading = false

	if app.sessionList.FilterInput.Value() != defaultActiveStateFilter {
		t.Fatalf("precondition: expected the auto state filter on startup, got %q",
			app.sessionList.FilterInput.Value())
	}

	send := func(msg tea.KeyMsg) { x, _ := app.Update(msg); app = x.(*App) }
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	send(tea.KeyMsg{Type: tea.KeyEsc})

	if app.config.SearchQuery != "" {
		t.Errorf("dismissing the filter left SearchQuery=%q, so a rebuild will restore it",
			app.config.SearchQuery)
	}

	app.rebuildSessionList() // the live tick
	if got := app.sessionList.FilterInput.Value(); got != "" {
		t.Errorf("auto filter came back after being dismissed: %q", got)
	}
}
