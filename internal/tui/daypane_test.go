package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sendbird/ccx/internal/session"
)

// The day pane's "Produced" digest answers what a day produced; these tests
// guard the two ways out of a row — Enter into the conversation at the moment
// the output first appeared, and `o` to the output itself.

// dayPaneApp builds a daily-view App with the day row selected and the preview
// focused, which is the state every key assertion below starts from.
func dayPaneApp(t *testing.T, sessions []session.Session) *App {
	t.Helper()
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()
	app.sessionList.Select(0)
	app.sessSplit.Show = true
	app.sessSplit.Focus = true
	_ = app.updateSessionPreview()
	return app
}

// TestDayPreviewOpenSendsPRToBrowser guards the reported "PR open is broken"
// bug: openSelectedDayOutput ignored o.URL entirely, so a PR row in the day
// digest had no path to the browser at all — `o` opened the conversation just
// like Enter did. The refs pane and the per-session digest both open the URL;
// the day pane must too.
func TestDayPreviewOpenSendsPRToBrowser(t *testing.T) {
	sessions := []session.Session{{
		ID: "maker", ShortID: "maker", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
		Refs: []session.SessionRef{{
			Kind: session.RefPR, Label: "sendbird/ccx#5",
			URL: "https://github.com/sendbird/ccx/pull/5", Resolved: true,
		}},
	}}
	app := dayPaneApp(t, sessions)

	var opened string
	app.openURL = func(u string) error { opened = u; return nil }

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	got := m.(*App)

	if opened != "https://github.com/sendbird/ccx/pull/5" {
		t.Errorf("expected the PR URL to open in the browser, got %q", opened)
	}
	if got.state != viewSessions {
		t.Errorf("o should stay in the sessions view, got state %v", got.state)
	}
}

// TestDayPreviewEnterJumpsToFirstMention guards the second reported bug: Enter
// dumped you at the top of the session instead of the message where the output
// first appeared. The row's uuid comes from the ref, which now records the
// entry it was first seen in.
func TestDayPreviewEnterJumpsToFirstMention(t *testing.T) {
	// Three entries; the PR is first mentioned in the middle one.
	path := writeTranscript(t,
		`{"type":"user","uuid":"u1","timestamp":"2026-08-13T01:00:00Z","message":{"role":"user","content":"open a PR for this"}}`,
		`{"type":"assistant","uuid":"u2","timestamp":"2026-08-13T01:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"opened https://github.com/sendbird/ccx/pull/5"}]}}`,
		`{"type":"user","uuid":"u3","timestamp":"2026-08-13T01:02:00Z","message":{"role":"user","content":"thanks"}}`,
	)
	sessions := []session.Session{{
		ID: "maker", ShortID: "maker", FilePath: path,
		ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0),
		Refs: []session.SessionRef{{
			Kind: session.RefPR, Label: "sendbird/ccx#5",
			URL: "https://github.com/sendbird/ccx/pull/5", Resolved: true,
			FirstSeen: time.Date(2026, 8, 13, 1, 1, 0, 0, time.UTC), FirstSeenUUID: "u2",
		}},
	}}
	app := dayPaneApp(t, sessions)

	if len(app.dayOutputRows) != 1 {
		t.Fatalf("expected 1 output row, got %d", len(app.dayOutputRows))
	}
	if app.dayOutputRows[0].out.MessageUUID != "u2" {
		t.Fatalf("row lost the ref's first-seen uuid: %q", app.dayOutputRows[0].out.MessageUUID)
	}

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(*App)

	if got.state != viewConversation {
		t.Fatalf("expected Enter to enter the conversation view, got state %v", got.state)
	}
	if got.currentSess.ID != "maker" {
		t.Fatalf("expected the producing session, got %q", got.currentSess.ID)
	}
	// The jump landed if the selected conversation row covers entry u2 — and
	// jumpToSessionEntry says so out loud when it does not.
	if strings.Contains(got.copiedMsg, "not found") {
		t.Fatalf("jump missed the entry: %q", got.copiedMsg)
	}
	if uuid := selectedConvEntryUUID(got); uuid != "u2" {
		t.Errorf("cursor landed on entry %q, want the first mention u2", uuid)
	}
}

// TestDayPreviewEnterFallsBackWithoutUUID pins the fallback: outputs with no
// recorded entry (a plan slug inherited from a parent session) must still open
// the conversation rather than doing nothing.
func TestDayPreviewEnterFallsBackWithoutUUID(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"user","uuid":"u1","timestamp":"2026-08-13T01:00:00Z","message":{"role":"user","content":"work from the plan"}}`,
	)
	sessions := []session.Session{{
		ID: "planner", ShortID: "planner", FilePath: path,
		ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0),
		PlanSlugs: []string{"some-plan"},
	}}
	app := dayPaneApp(t, sessions)

	if len(app.dayOutputRows) != 1 || app.dayOutputRows[0].out.MessageUUID != "" {
		t.Fatalf("fixture should give one uuid-less row, got %+v", app.dayOutputRows)
	}

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(*App)

	if got.state != viewConversation || got.currentSess.ID != "planner" {
		t.Fatalf("expected the conversation to open anyway, got state %v session %q", got.state, got.currentSess.ID)
	}
}

// TestDayPreviewUUIDBelongsToAnchorSession guards the collapse trap: the same
// PR referenced from two sessions renders as ONE row anchored to the earliest
// session. The uuid must come from that anchor — a uuid from the later session
// names an entry that does not exist in the anchor's transcript, so the jump
// would silently miss.
func TestDayPreviewUUIDBelongsToAnchorSession(t *testing.T) {
	pr := session.SessionRef{
		Kind: session.RefPR, Label: "sendbird/ccx#5",
		URL: "https://github.com/sendbird/ccx/pull/5", Resolved: true,
	}
	first, later := pr, pr
	first.FirstSeenUUID = "first-entry"
	later.FirstSeenUUID = "later-entry"

	sessions := []session.Session{
		{ID: "first", ShortID: "first", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
			ModTime: dayOf(0).Add(-3 * time.Hour), Refs: []session.SessionRef{first}},
		{ID: "later", ShortID: "later", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b",
			ModTime: dayOf(0).Add(-time.Hour), Refs: []session.SessionRef{later}},
	}
	di := buildDailyItems(sessions, nil)[0].(dayItem)
	rows := buildDayOutputRows(di)

	if len(rows) != 1 {
		t.Fatalf("expected the repeated PR to collapse to one row, got %d", len(rows))
	}
	if rows[0].sessID != "first" {
		t.Fatalf("expected the earliest session as the anchor, got %q", rows[0].sessID)
	}
	if rows[0].out.MessageUUID != "first-entry" {
		t.Errorf("jump uuid = %q, want the anchor session's %q — a later session's uuid does not exist in the anchor's transcript",
			rows[0].out.MessageUUID, "first-entry")
	}
}

// TestDayPreviewCursorMovesOnProjectRow guards a bug found while fixing the
// above: the cursor-move branch only re-rendered via selectedDay(), so on a
// day-scoped PROJECT row (which owns the same pane — see selectedOwnsDayPane)
// the highlight never moved, making the pane look frozen.
func TestDayPreviewCursorMovesOnProjectRow(t *testing.T) {
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
		Refs: []session.SessionRef{
			{Kind: session.RefPR, Label: "sendbird/ccx#1", URL: "https://github.com/sendbird/ccx/pull/1", Resolved: true},
			{Kind: session.RefPR, Label: "sendbird/ccx#2", URL: "https://github.com/sendbird/ccx/pull/2", Resolved: true},
		},
	}}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()

	// Walk to the day-scoped project row.
	idx := -1
	for i, item := range app.sessionList.VisibleItems() {
		if pi, ok := item.(projectItem); ok && pi.dayKey != "" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("expected a day-scoped project row in the daily tree")
	}
	app.sessionList.Select(idx)
	app.sessSplit.Show = true
	app.sessSplit.Focus = true
	_ = app.updateSessionPreview()

	if len(app.dayOutputRows) < 2 {
		t.Fatalf("expected the project row's pane to list both PRs, got %d rows", len(app.dayOutputRows))
	}
	before := app.sessSplit.Preview.View()

	if _, _, handled := app.handleDayPreviewKeys(&app.sessSplit, "down"); !handled {
		t.Fatal("expected the day pane to handle down on a project row")
	}
	if app.dayOutputsCursor != 1 {
		t.Fatalf("cursor = %d, want 1", app.dayOutputsCursor)
	}
	if after := app.sessSplit.Preview.View(); after == before {
		t.Error("pane content did not change after the cursor moved — the highlight is frozen on day-scoped project rows")
	}
}

// TestDayPreviewHintsMatchTheFocusedKeys guards the hint line: with the pane
// focused, Enter and `o` belong to the pane (jump / open), not to the list's
// fold. The old footer advertised "↵/o folds this row" in both states, which
// pointed at an action the focused pane does not perform.
func TestDayPreviewHintsMatchTheFocusedKeys(t *testing.T) {
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
		Refs: []session.SessionRef{{
			Kind: session.RefPR, Label: "sendbird/ccx#1",
			URL: "https://github.com/sendbird/ccx/pull/1", Resolved: true,
		}},
	}}
	app := dayPaneApp(t, sessions)

	focused := app.sessSplit.Preview.View()
	if strings.Contains(focused, "folds this row") {
		t.Errorf("focused pane still advertises the list's fold action:\n%s", focused)
	}
	for _, want := range []string{"first appeared", "opens it"} {
		if !strings.Contains(focused, want) {
			t.Errorf("focused hint missing %q, got:\n%s", want, focused)
		}
	}

	app.sessSplit.Focus = false
	app.sessSplit.CacheKey = ""
	_ = app.updateSessionPreview()
	if unfocused := app.sessSplit.Preview.View(); !strings.Contains(unfocused, "folds this row") {
		t.Errorf("unfocused pane should still describe the list's Enter, got:\n%s", unfocused)
	}
}

// TestDayPreviewCopyFallsBackToPath mirrors the per-session digest: `y` copies
// the URL when there is one and the path otherwise. Previously a path-only row
// (a plan file) reported "No URL for this output" and copied nothing.
func TestDayPreviewCopyFallsBackToPath(t *testing.T) {
	app := newTestApp(nil)
	app.dayOutputRows = []dayOutputRow{{
		out:    session.SessionOutput{Kind: session.OutputPlan, Title: "some-plan", Path: "/tmp/plans/some-plan.md"},
		sessID: "a1",
	}}
	app.dayOutputsCursor = 0

	m, _, _ := app.copySelectedDayOutput()
	if got := m.(*App).copiedMsg; !strings.Contains(got, "some-plan") {
		t.Errorf("copiedMsg = %q, want the plan copied via its path", got)
	}
}

// TestDayPreviewKeysIgnoreUnrelatedKeys pins key ownership: the day pane must
// not swallow keys it has no business handling (the list still owns them).
func TestDayPreviewKeysIgnoreUnrelatedKeys(t *testing.T) {
	app := newTestApp(nil)
	app.dayOutputRows = []dayOutputRow{{
		out:    session.SessionOutput{Kind: session.OutputPR, Title: "sendbird/ccx#1"},
		sessID: "a1",
	}}
	for _, key := range []string{"D", "r", "x", "tab"} {
		if _, _, handled := app.handleDayPreviewKeys(&app.sessSplit, key); handled {
			t.Errorf("day pane swallowed %q", key)
		}
	}
}

// selectedConvEntryUUID returns the uuid of the first transcript entry covered
// by the conversation row under the cursor.
func selectedConvEntryUUID(a *App) string {
	items := a.convList.VisibleItems()
	idx := a.convList.Index()
	if idx < 0 || idx >= len(items) {
		return ""
	}
	ci, ok := items[idx].(convItem)
	if !ok || ci.kind != convMsg {
		return ""
	}
	if ci.merged.startIdx < 0 || ci.merged.startIdx >= len(a.conv.messages) {
		return ""
	}
	return a.conv.messages[ci.merged.startIdx].UUID
}
