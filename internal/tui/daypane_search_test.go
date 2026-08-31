package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

func outRow(kind session.OutputKind, title, detail, project string) dayOutputRow {
	return dayOutputRow{
		out:     session.SessionOutput{Kind: kind, Title: title, Detail: detail},
		project: project,
		shortID: "abc1234",
		when:    time.Now(),
	}
}

func rowTitles(rows []dayOutputRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.out.Title)
	}
	return out
}

func TestDayOutputQueryFiltersRows(t *testing.T) {
	rows := []dayOutputRow{
		outRow(session.OutputPR, "sendbird/ops-k8s#42", "argocd chart bump", "ops-k8s"),
		outRow(session.OutputJira, "CPLAT-11790", "search resume", "ccx"),
		outRow(session.OutputPR, "sendbird/ccx#162", "live state in search", "ccx"),
	}

	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"sendbird/ops-k8s#42", "CPLAT-11790", "sendbird/ccx#162"}},
		{"cplat", []string{"CPLAT-11790"}},
		{"CPLAT", []string{"CPLAT-11790"}}, // case-insensitive
		{"search", []string{"CPLAT-11790", "sendbird/ccx#162"}},
		{"ccx search", []string{"CPLAT-11790", "sendbird/ccx#162"}}, // AND across fields
		{"argocd", []string{"sendbird/ops-k8s#42"}},                 // matches Detail
		{"ops-k8s", []string{"sendbird/ops-k8s#42"}},                // matches project
		{"nothingmatches", nil},
	}
	for _, c := range cases {
		got := rowTitles(filterDayOutputRows(rows, dayOutputTabAll, c.query))
		if len(got) != len(c.want) {
			t.Errorf("query %q: got %v, want %v", c.query, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("query %q: got %v, want %v", c.query, got, c.want)
				break
			}
		}
	}
}

// The kind tab and the text query are independent narrowings and must compose.
func TestDayOutputQueryComposesWithKindTab(t *testing.T) {
	rows := []dayOutputRow{
		outRow(session.OutputPR, "pr-search", "", "ccx"),
		outRow(session.OutputJira, "jira-search", "", "ccx"),
		outRow(session.OutputPR, "pr-other", "", "ccx"),
	}
	prTab := dayOutputTab{label: "PRs", kind: session.OutputPR}

	got := rowTitles(filterDayOutputRows(rows, prTab, "search"))
	if len(got) != 1 || got[0] != "pr-search" {
		t.Errorf("kind+query = %v, want [pr-search]", got)
	}
}

// daySearchApp reuses the daily-view harness and stocks the pane with rows to
// filter (dayPaneApp lives in daypane_test.go).
func daySearchApp(t *testing.T) *App {
	t.Helper()
	a := dayPaneApp(t, fakeSessions())
	a.dayOutputRows = []dayOutputRow{
		outRow(session.OutputPR, "pr-one", "", "p"),
		outRow(session.OutputJira, "CPLAT-1", "", "p"),
	}
	return a
}

// "/" in the day pane must search the day pane, not steal focus to the session
// list — the two panes answer different questions.
func TestSlashInDayPaneOpensItsOwnSearch(t *testing.T) {
	a := daySearchApp(t)
	sp := &a.sessSplit

	_, _, handled := a.handleDayPreviewKeys(sp, "/")
	if !handled {
		t.Fatal("day pane did not handle /")
	}
	if !a.dayOutputSearching {
		t.Error("/ did not open the day pane's own search")
	}
	if !sp.Focus {
		t.Error("/ moved focus away from the day pane")
	}
}

func TestDayOutputSearchAppliesAsYouType(t *testing.T) {
	a := daySearchApp(t)
	a.startDayOutputSearch()

	for _, r := range "cplat" {
		m, _ := a.handleDayOutputSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		a = m.(*App)
	}
	if a.dayOutputQuery != "cplat" {
		t.Errorf("query = %q, want %q", a.dayOutputQuery, "cplat")
	}

	m, _ := a.handleDayOutputSearch(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)
	if a.dayOutputSearching {
		t.Error("enter did not close the search input")
	}
	if a.dayOutputQuery != "cplat" {
		t.Errorf("query after enter = %q, want %q", a.dayOutputQuery, "cplat")
	}
}

// Esc cancels the edit, not the filter — otherwise a stray keypress loses the
// narrowing the user built up.
func TestDayOutputSearchEscKeepsAppliedQuery(t *testing.T) {
	a := daySearchApp(t)
	a.applyDayOutputQuery("cplat")
	a.startDayOutputSearch()

	m, _ := a.handleDayOutputSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	a = m.(*App)
	m, _ = a.handleDayOutputSearch(tea.KeyMsg{Type: tea.KeyEsc})
	a = m.(*App)

	if a.dayOutputSearching {
		t.Error("esc did not close the input")
	}
	if a.dayOutputQuery != "cplat" {
		t.Errorf("esc dropped the applied query: %q, want %q", a.dayOutputQuery, "cplat")
	}
}

// Changing the filter must reset the cursor: every row action resolves through
// dayOutputsCursor into the FILTERED slice, so a kept index would act on a
// different output than the highlighted one.
func TestDayOutputQueryResetsCursor(t *testing.T) {
	a := daySearchApp(t)
	a.dayOutputsCursor = 1
	a.applyDayOutputQuery("cplat")
	if a.dayOutputsCursor != 0 {
		t.Errorf("cursor = %d after filter change, want 0", a.dayOutputsCursor)
	}
}

// The heading must say the count is filtered, or a narrowed list reads as
// "this day produced 3 things".
func TestDayPaneHeadingShowsQuery(t *testing.T) {
	a := daySearchApp(t)
	a.width, a.height = 200, 50
	all := []dayOutputRow{
		outRow(session.OutputPR, "pr-one", "", "p"),
		outRow(session.OutputJira, "CPLAT-1", "", "p"),
	}
	a.dayOutputQuery = "cplat"

	view := stripANSI(a.renderOutputsPane("Today", "", "1 session", time.Now(), all, 120))
	if !strings.Contains(view, "of 2") {
		t.Errorf("heading does not show the unfiltered total:\n%s", view)
	}
	if !strings.Contains(view, "cplat") {
		t.Errorf("heading does not show the active query:\n%s", view)
	}
}

// An empty result under a query is a different answer than an empty day.
func TestDayPaneEmptyQueryResultIsDistinct(t *testing.T) {
	a := daySearchApp(t)
	a.width, a.height = 200, 50
	all := []dayOutputRow{outRow(session.OutputPR, "pr-one", "", "p")}
	a.dayOutputQuery = "zzzznotfound"

	view := stripANSI(a.renderOutputsPane("Today", "", "1 session", time.Now(), all, 120))
	if !strings.Contains(view, "nothing matching") {
		t.Errorf("empty query result is not distinguished from an empty day:\n%s", view)
	}
}

// The left pane keeps its own search: "/" with the list focused must still open
// the session filter, not the day pane's.
func TestSlashInSessionListStillFiltersSessions(t *testing.T) {
	a := daySearchApp(t)
	a.sessSplit.Focus = false // focus on the list, not the pane

	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	got := m.(*App)

	if got.dayOutputSearching {
		t.Error("/ on the session list opened the day pane's search")
	}
	if !got.isFiltering() {
		t.Error("/ on the session list did not open the session filter")
	}
}

// The two queries are independent: filtering one pane must not touch the other.
func TestPaneSearchesAreIndependent(t *testing.T) {
	a := daySearchApp(t)
	a.applyDayOutputQuery("cplat")

	if a.sessionList.FilterInput.Value() != "" {
		t.Error("day pane query leaked into the session list filter")
	}

	a.sessionList.SetFilterText("proj-a")
	if a.dayOutputQuery != "cplat" {
		t.Errorf("session filter clobbered the day pane query: %q", a.dayOutputQuery)
	}
}

// Moving to a different day must drop the query — carrying it would silently
// hide the new day's outputs behind a filter the user is no longer thinking of.
func TestDayOutputQueryResetsOnScopeChange(t *testing.T) {
	a := daySearchApp(t)
	a.applyDayOutputQuery("cplat")
	a.dayOutputsCacheID = "some-other-day"

	di := dayItem{dayKey: "2026-08-31", sessions: fakeSessions()}
	a.updateDayPreview(di)

	if a.dayOutputQuery != "" {
		t.Errorf("query survived a day change: %q", a.dayOutputQuery)
	}
}
