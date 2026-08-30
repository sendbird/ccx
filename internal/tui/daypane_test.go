package tui

import (
	"reflect"
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
	unfocused := app.sessSplit.Preview.View()
	if !strings.Contains(unfocused, "folds this row") {
		t.Errorf("unfocused pane should still describe the list's Enter, got:\n%s", unfocused)
	}
	// Same trap, the sort key: unfocused, `s` opens the state menu, so offering
	// it here would point at a different action.
	if strings.Contains(unfocused, "s: group by kind") {
		t.Errorf("unfocused pane advertises the sort key it does not own:\n%s", unfocused)
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

// --- Produced ordering ---

// timedRef builds a PR ref that first appeared at ts, which is what the day
// pane's timeline sorts on.
func timedRef(label string, ts time.Time) session.SessionRef {
	return session.SessionRef{
		Kind: session.RefPR, Label: label,
		URL:       "https://github.com/sendbird/ccx/pull/" + label,
		Resolved:  true,
		FirstSeen: ts, FirstSeenUUID: "u-" + label,
	}
}

// TestDayOutputRowsFollowFirstAppearance is the point of the timeline: rows come
// out in the order the outputs first appeared, NOT in the order the sessions
// happen to carry them. A session's Refs are sorted first-seen DESCENDING, so
// without the explicit sort the pane reads backwards inside every session.
func TestDayOutputRowsFollowFirstAppearance(t *testing.T) {
	day := dayOf(0)
	sessions := []session.Session{
		// One session holding two refs — stored newest-first, as SortRefs leaves them.
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: day,
			Refs: []session.SessionRef{
				timedRef("late", day.Add(4*time.Hour)),
				timedRef("early", day.Add(time.Hour)),
			}},
		// A second session whose output lands between the two above, so kind
		// grouping alone could not produce the right answer either.
		{ID: "b1", ShortID: "b1", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: day.Add(-time.Hour),
			Refs: []session.SessionRef{timedRef("middle", day.Add(2*time.Hour))}},
	}
	di := buildDailyItems(sessions, nil)[0].(dayItem)
	rows := buildDayOutputRows(di)

	var got []string
	for _, r := range rows {
		got = append(got, r.out.Title)
	}
	want := []string{"early", "middle", "late"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("timeline order = %v, want %v", got, want)
	}
}

// TestDayOutputTabsKeepOneChronology guards the tab split: filtering by kind
// must not re-order anything — every tab is the same timeline, narrowed.
func TestDayOutputTabsKeepOneChronology(t *testing.T) {
	day := dayOf(0)
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ModTime: day,
		PlanSlugs: []string{"a-plan"},
		Refs: []session.SessionRef{
			timedRef("late", day.Add(4*time.Hour)),
			timedRef("early", day.Add(time.Hour)),
		},
	}}
	di := buildDailyItems(sessions, nil)[0].(dayItem)
	rows := buildDayOutputRows(di)

	prs := filterDayOutputRows(rows, dayOutputTab{label: "PRs", kind: session.OutputPR}, "")
	if len(prs) != 2 {
		t.Fatalf("PR tab = %d rows, want 2", len(prs))
	}
	if prs[0].out.Title != "early" || prs[1].out.Title != "late" {
		t.Errorf("PR tab = %q,%q — want the same chronology the All tab has", prs[0].out.Title, prs[1].out.Title)
	}
	if all := filterDayOutputRows(rows, dayOutputTabAll, ""); len(all) != 3 {
		t.Errorf("All tab = %d rows, want every kind (3)", len(all))
	}
}

// TestDayOutputRowsFallBackToSessionTime covers refs extracted before FirstSeen
// was recorded, and plan slugs, which carry no entry at all. A zero timestamp
// would sink them to the end of the day and read as "produced last"; the
// producing session's own time is the honest approximation.
func TestDayOutputRowsFallBackToSessionTime(t *testing.T) {
	day := dayOf(0)
	untimed := session.SessionRef{
		Kind: session.RefPR, Label: "untimed",
		URL: "https://github.com/sendbird/ccx/pull/9", Resolved: true,
	}
	sessions := []session.Session{
		{ID: "old", ShortID: "old", ProjectPath: "/tmp/repo-a", ModTime: day.Add(-5 * time.Hour),
			Refs: []session.SessionRef{untimed}},
		{ID: "new", ShortID: "new", ProjectPath: "/tmp/repo-b", ModTime: day,
			Refs: []session.SessionRef{timedRef("timed", day.Add(-time.Hour))}},
	}
	di := buildDailyItems(sessions, nil)[0].(dayItem)
	rows := buildDayOutputRows(di)

	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].out.Title != "untimed" {
		t.Errorf("first row = %q, want the untimed ref placed at its session's time (%s), not sunk to the bottom",
			rows[0].out.Title, day.Add(-5*time.Hour).Format("15:04"))
	}
	if rows[0].when.IsZero() {
		t.Error("untimed row kept a zero time — it has nothing to render in the timeline")
	}
}

// TestDayPreviewTabKeySwitchesKind drives the REAL key path: tab is consumed by
// km.Session.Preview long before the focused-preview handlers run, so the day
// pane's kind switch has to live in that case. Driving handleDayPreviewKeys
// directly would pass while the actual keypress does nothing.
func TestDayPreviewTabKeySwitchesKind(t *testing.T) {
	day := dayOf(0)
	// Three PRs and one plan: the PR tab is LONGER than the cursor index used
	// below, so a missing reset cannot hide behind the render's range clamp — it
	// would leave the cursor on a real but different PR.
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: day,
		PlanSlugs: []string{"a-plan"},
		Refs: []session.SessionRef{
			timedRef("pr-1", day.Add(time.Hour)),
			timedRef("pr-2", day.Add(2*time.Hour)),
			timedRef("pr-3", day.Add(3*time.Hour)),
		},
	}}
	app := dayPaneApp(t, sessions)

	if app.dayOutputTabKind != "" {
		t.Fatalf("default tab = %q, want the All timeline", app.dayOutputTabKind)
	}
	if len(app.dayOutputRows) != 4 {
		t.Fatalf("All tab = %d rows, want 4", len(app.dayOutputRows))
	}
	if view := app.sessSplit.Preview.View(); !strings.Contains(view, "[All 4]") {
		t.Errorf("tab bar missing the active All tab with its count:\n%s", view)
	}

	// A stale cursor across a tab switch would act on a different output than the
	// highlighted one, so it must reset.
	app.dayOutputsCursor = 2
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyTab})
	app = m.(*App)

	if app.dayOutputTabKind != session.OutputPR {
		t.Fatalf("tab after one press = %q, want the PR tab", app.dayOutputTabKind)
	}
	if app.dayOutputsCursor != 0 {
		t.Errorf("cursor = %d, want 0 — a stale index acts on a different PR than the one highlighted", app.dayOutputsCursor)
	}
	if len(app.dayOutputRows) != 3 {
		t.Fatalf("PR tab = %d actionable rows, want 3 — the cursor indexes THIS slice", len(app.dayOutputRows))
	}
	for _, r := range app.dayOutputRows {
		if r.out.Kind != session.OutputPR {
			t.Errorf("PR tab leaked a %s row", r.out.Kind)
		}
	}
	if view := app.sessSplit.Preview.View(); strings.Contains(view, "a-plan") {
		t.Errorf("PR tab still renders the plan row:\n%s", view)
	}

	// shift+tab walks back.
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	app = m.(*App)
	if app.dayOutputTabKind != "" {
		t.Errorf("tab after shift+tab = %q, want All back", app.dayOutputTabKind)
	}
}

// TestDayPreviewTabSurvivesTheCacheKey pins the repaint path. cycleDayOutputTab
// clears CacheKey and renders, but the very next updateSessionPreview() (any
// navigation, any refresh tick) recomputes the key — if the tab is not part of
// it, that call sees a "matching" key, returns early, and the pane silently
// reverts to whatever was rendered under the old tab.
func TestDayPreviewTabSurvivesTheCacheKey(t *testing.T) {
	day := dayOf(0)
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: day,
		PlanSlugs: []string{"a-plan"},
		Refs:      []session.SessionRef{timedRef("pr-1", day.Add(time.Hour))},
	}}
	app := dayPaneApp(t, sessions)

	// Render once under All so a stale key would have All's content behind it.
	if len(app.dayOutputRows) != 2 {
		t.Fatalf("All tab = %d rows, want 2", len(app.dayOutputRows))
	}
	keyAll := app.sessSplit.CacheKey

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyTab})
	app = m.(*App)
	if app.dayOutputTabKind != session.OutputPR {
		t.Fatalf("tab = %q, want the PR tab", app.dayOutputTabKind)
	}

	// The next preview pass must NOT think it is already up to date.
	_ = app.updateSessionPreview()
	if app.sessSplit.CacheKey == keyAll {
		t.Fatalf("cache key is unchanged across a tab switch (%q) — the pane will not repaint", keyAll)
	}
	if len(app.dayOutputRows) != 1 {
		t.Errorf("after the refresh the pane holds %d rows, want the PR tab's 1 — it reverted to All", len(app.dayOutputRows))
	}
	if view := app.sessSplit.Preview.View(); strings.Contains(view, "a-plan") {
		t.Errorf("pane reverted to the All tab's content:\n%s", view)
	}
}

// TestDayPreviewTabDoesNotTouchSessionPreviewMode pins the other half of that
// interception: on a day row, tab must NOT rotate sessPreviewMode. It used to,
// silently — the pane ignores the mode, so the rotation was invisible state
// corruption that surfaced only after moving to a session row.
func TestDayPreviewTabDoesNotTouchSessionPreviewMode(t *testing.T) {
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
		Refs:    []session.SessionRef{timedRef("pr-1", dayOf(0))},
	}}
	app := dayPaneApp(t, sessions)
	before := app.sessPreviewMode

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.(*App).sessPreviewMode; got != before {
		t.Errorf("sessPreviewMode = %v, want it untouched at %v — a day row has no preview modes", got, before)
	}
}

// TestDayPreviewTabStaysStickyOnEmptyKind covers walking dates under a filter:
// a day that produced nothing of the selected kind shows an empty state under
// that same tab. Falling back to All would break the date-to-date comparison
// the sticky tab exists for.
func TestDayPreviewTabStaysStickyOnEmptyKind(t *testing.T) {
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0), PlanSlugs: []string{"a-plan"},
	}}
	app := dayPaneApp(t, sessions)
	app.dayOutputTabKind = session.OutputPR // as if carried over from another day
	app.sessSplit.CacheKey = ""
	_ = app.updateSessionPreview()

	if app.dayOutputTabKind != session.OutputPR {
		t.Errorf("tab = %q, want it kept so dates stay comparable", app.dayOutputTabKind)
	}
	if len(app.dayOutputRows) != 0 {
		t.Errorf("expected no rows under the PR tab, got %d", len(app.dayOutputRows))
	}
	if view := app.sessSplit.Preview.View(); !strings.Contains(view, "nothing of this kind") {
		t.Errorf("empty kind tab is missing its own empty state:\n%s", view)
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

// TestDayPaneActionsMenuActsOnTheRow guards the day pane's half of the x-actions
// work: with the pane focused, `x` must act on the OUTPUT under the cursor, not
// on the day's sessions. A day-scoped project row is the trap — the plain `x`
// path multi-selects the whole project, which is an action about the list while
// the user is looking at the pane.
func TestDayPaneActionsMenuActsOnTheRow(t *testing.T) {
	sessions := []session.Session{{
		ID: "maker", ShortID: "maker", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
		Refs: []session.SessionRef{{
			Kind: session.RefPR, Label: "sendbird/ccx#5",
			URL: "https://github.com/sendbird/ccx/pull/5", Resolved: true,
		}},
	}}
	app := dayPaneApp(t, sessions)

	if !app.outputsPreviewActionsActive() {
		t.Fatal("a focused day pane must own the x actions menu")
	}

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	got := m.(*App)
	if !got.actionsMenu {
		t.Fatal("x did not open the actions menu on the focused day pane")
	}
	if len(got.selectedSet) > 0 {
		t.Errorf("x multi-selected %d sessions instead of acting on the row", len(got.selectedSet))
	}
	box := got.renderActionsHintBox()
	if !strings.Contains(box, "sendbird/ccx#5") {
		t.Errorf("the menu does not name the row it acts on:\n%s", box)
	}
	if strings.Contains(box, ":delete") {
		t.Errorf("the session actions menu rendered over the day pane:\n%s", box)
	}

	var opened string
	got.openURL = func(u string) error { opened = u; return nil }
	m, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if opened != "https://github.com/sendbird/ccx/pull/5" {
		t.Errorf("x→o opened %q, want the row's PR", opened)
	}
	if m.(*App).actionsMenu {
		t.Error("the menu should close after a key is picked")
	}
}

// TestDayPaneActionsAnchorToTheRowsSession pins the day pane's extra field: the
// row's anchor session, which the per-session digest does not need. Without it,
// the uuid-less fallback ("open the conversation") could not be offered.
func TestDayPaneActionsAnchorToTheRowsSession(t *testing.T) {
	app := newTestApp(nil)
	app.sessGroupMode = groupDaily
	app.dayOutputRows = []dayOutputRow{{
		out:    session.SessionOutput{Kind: session.OutputPlan, Title: "some-plan"},
		sessID: "planner",
	}}
	app.dayOutputsCursor = 0

	acts := app.outputActionsFor(app.dayOutputRows[0].out, app.dayOutputRows[0].sessID != "")
	var kinds []outputActionKind
	for _, a := range acts {
		kinds = append(kinds, a.kind)
	}
	if len(kinds) != 1 || kinds[0] != outputActionSession {
		t.Fatalf("a uuid-less plan slug with an anchor should offer exactly the conversation, got %+v", acts)
	}
}

// TestDayPaneActionsOnProjectRow pins the day-scoped project row, which owns the
// same pane (selectedOwnsDayPane) but is a projectItem — the arm the plain `x`
// path would otherwise route into.
func TestDayPaneActionsOnProjectRow(t *testing.T) {
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
		Refs: []session.SessionRef{{
			Kind: session.RefPR, Label: "sendbird/ccx#1",
			URL: "https://github.com/sendbird/ccx/pull/1", Resolved: true,
		}},
	}}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()

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

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	got := m.(*App)
	if len(got.selectedSet) > 0 {
		t.Errorf("x on a focused day-project pane multi-selected %d sessions instead of acting on the row", len(got.selectedSet))
	}
	if box := got.renderActionsHintBox(); !strings.Contains(box, "sendbird/ccx#1") {
		t.Errorf("the menu does not name the row it acts on:\n%s", box)
	}
}

// TestDayProjectPaneSurvivesResize guards a bug found while wiring the actions
// menu: the resize branch in renderSessionSplit guarded only selectedDay(), so
// on a day-scoped PROJECT row — which owns the same pane — a window resize fell
// through to the per-session digest path and overwrote the "Produced" list with
// an arbitrary child session's outputs.
func TestDayProjectPaneSurvivesResize(t *testing.T) {
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
		Refs: []session.SessionRef{{
			Kind: session.RefPR, Label: "sendbird/ccx#1",
			URL: "https://github.com/sendbird/ccx/pull/1", Resolved: true,
		}},
	}}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	// The digest mode is what makes the resize branch reachable; a day-scoped
	// row renders the day pane regardless, which is exactly the mismatch.
	app.sessPreviewMode = sessPreviewOutputs
	app.rebuildSessionList()

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

	if before := app.sessSplit.Preview.View(); !strings.Contains(before, "Produced") {
		t.Fatalf("fixture should start on the day-project pane, got:\n%s", before)
	}

	m, _ := app.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	got := m.(*App)
	_ = got.View() // the resize branch lives in the render path

	if after := got.sessSplit.Preview.View(); !strings.Contains(after, "Produced") {
		t.Errorf("resize replaced the day-project pane with a session digest:\n%s", after)
	}
}

// timedRefOf is timedRef for a kind other than PR — the digit tests need a bar
// with several kinds on it, which one ref kind cannot produce.
func timedRefOf(kind session.RefKind, label string, ts time.Time) session.SessionRef {
	return session.SessionRef{
		Kind: kind, Label: label,
		URL:       "https://example.test/" + label,
		Resolved:  true,
		FirstSeen: ts, FirstSeenUUID: "u-" + label,
	}
}

// dayPaneTabDigitsApp builds the shape the digits were reported broken on: a day
// whose bar reads All / PRs / Jira / Plans — no Artifacts, because the day
// produced none. That gap is the whole point: the digits are POSITIONAL over the
// bar as rendered, so "4" must reach Plans even though Plans is 5th in
// dayOutputTabOrder.
func dayPaneTabDigitsApp(t *testing.T) *App {
	t.Helper()
	day := dayOf(0)
	return dayPaneApp(t, []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: day,
		PlanSlugs: []string{"a-plan"},
		Refs: []session.SessionRef{
			timedRef("pr-1", day.Add(time.Hour)),
			timedRefOf(session.RefJira, "CPLAT-1", day.Add(2*time.Hour)),
		},
	}})
}

// TestDayPreviewDigitsSelectTabsPositionally is the reported bug: on a day row
// the number keys are bound to preview modes the pane cannot show, so they were
// swallowed and 1/2/3/4 did nothing at all. They now address the tab bar.
func TestDayPreviewDigitsSelectTabsPositionally(t *testing.T) {
	app := dayPaneTabDigitsApp(t)

	tabs := dayOutputTabsFor(app.currentDayOutputRows(), app.dayOutputTabKind)
	var labels []string
	for _, tb := range tabs {
		labels = append(labels, tb.label)
	}
	if want := []string{"All", "PRs", "Jira", "Plans"}; !reflect.DeepEqual(labels, want) {
		t.Fatalf("tab bar = %v, want %v — the digit mapping is defined against this bar", labels, want)
	}

	cases := []struct {
		key  rune
		want session.OutputKind
		rows int
	}{
		{'2', session.OutputPR, 1},
		{'3', session.OutputJira, 1},
		{'4', session.OutputPlan, 1},
		{'1', "", 3},
	}
	for _, tc := range cases {
		m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tc.key}})
		app = m.(*App)
		if app.dayOutputTabKind != tc.want {
			t.Errorf("key %q selected kind %q, want %q", tc.key, app.dayOutputTabKind, tc.want)
		}
		if len(app.dayOutputRows) != tc.rows {
			t.Errorf("key %q left %d actionable rows, want %d", tc.key, len(app.dayOutputRows), tc.rows)
		}
		if app.dayOutputsCursor != 0 {
			t.Errorf("key %q left the cursor at %d — a stale index acts on a different output", tc.key, app.dayOutputsCursor)
		}
	}
}

// TestDayPreviewDigitsFollowTheStickyBar pins the one trap in a positional
// mapping: the active tab stays in the bar even on a day that produced none of
// that kind (dayOutputTabsFor), which shifts every later tab's position. The
// digits must follow what is on screen, not a fixed kind table.
func TestDayPreviewDigitsFollowTheStickyBar(t *testing.T) {
	day := dayOf(0)
	app := dayPaneApp(t, []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: day,
		PlanSlugs: []string{"a-plan"},
	}})
	// Carried over from another date: Jira has no rows here but keeps its tab,
	// so the bar reads All / Jira / Plans and Plans sits at position 3.
	app.dayOutputTabKind = session.OutputJira
	app.sessSplit.CacheKey = ""
	_ = app.updateSessionPreview()

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	app = m.(*App)
	if app.dayOutputTabKind != session.OutputPlan {
		t.Errorf("key 3 selected %q, want the Plans tab that the sticky bar puts in that slot", app.dayOutputTabKind)
	}
}

// TestDayPreviewOutOfRangeDigitIsSwallowed keeps the original guarantee: a digit
// with no tab behind it must not fall through to the list, where it would scroll
// the cursor instead.
func TestDayPreviewOutOfRangeDigitIsSwallowed(t *testing.T) {
	app := dayPaneTabDigitsApp(t)
	before := app.sessionList.Index()

	for _, r := range []rune{'0', '9'} {
		m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		app = m.(*App)
		if app.dayOutputTabKind != "" {
			t.Errorf("key %q changed the tab to %q — it addresses no tab", r, app.dayOutputTabKind)
		}
		if app.sessionList.Index() != before {
			t.Errorf("key %q moved the list cursor to %d, want it left at %d", r, app.sessionList.Index(), before)
		}
	}
}

// TestSessionRowDigitsStillSwitchPreviewMode guards the other side of the
// re-point: only a day-pane row loses the preview-mode digits. On a session row
// they must still do what they always did.
func TestSessionRowDigitsStillSwitchPreviewMode(t *testing.T) {
	app := dayPaneTabDigitsApp(t)
	for i := 0; i < len(app.sessionList.VisibleItems()); i++ {
		app.sessionList.Select(i)
		if !app.selectedOwnsDayPane() {
			break
		}
	}
	if app.selectedOwnsDayPane() {
		t.Fatal("could not land on a session row")
	}
	app.sessPreviewMode = sessPreviewConversation

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	if got := m.(*App).sessPreviewMode; got != sessPreviewRefs {
		t.Errorf("sessPreviewMode = %v, want the refs preview 5 is bound to", got)
	}
}
