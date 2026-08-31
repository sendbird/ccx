package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// dayOf builds a timestamp at 12:00 on the given offset from today, so tests
// never straddle a midnight boundary while running.
func dayOf(daysAgo int) time.Time {
	now := time.Now()
	y, m, d := now.AddDate(0, 0, -daysAgo).Date()
	return time.Date(y, m, d, 12, 0, 0, 0, now.Location())
}

func TestBuildDailyItemsGroupsByLastActivityDay(t *testing.T) {
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0), MsgCount: 5},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(0).Add(-2 * time.Hour), MsgCount: 3},
		{ID: "b1", ShortID: "b1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(1), MsgCount: 7},
	}
	items := buildDailyItems(sessions, nil)

	var days []dayItem
	for _, it := range items {
		if di, ok := it.(dayItem); ok {
			days = append(days, di)
		}
	}
	if len(days) != 2 {
		t.Fatalf("expected 2 day rows, got %d", len(days))
	}
	// Newest day first.
	if !days[0].day.After(days[1].day) {
		t.Fatalf("expected days newest-first, got %s then %s", days[0].dayKey, days[1].dayKey)
	}
	if len(days[0].sessions) != 2 {
		t.Fatalf("expected today to hold 2 sessions, got %d", len(days[0].sessions))
	}
	if days[0].projects != 2 {
		t.Fatalf("expected today to span 2 projects, got %d", days[0].projects)
	}
	if days[0].totalMsgs != 8 {
		t.Fatalf("expected today to total 8 messages, got %d", days[0].totalMsgs)
	}
	// Within a day, most-recent-first.
	if days[0].sessions[0].ID != "a1" {
		t.Fatalf("expected a1 first within the day, got %s", days[0].sessions[0].ID)
	}
}

func TestBuildDailyItemsEmitsEachSessionOnce(t *testing.T) {
	// A session in the current tmux window must NOT also appear under a
	// duplicated "Current Window" date row — two rows would share one fold key.
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ModTime: dayOf(0), IsCurrentWindow: true},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a", ModTime: dayOf(0).Add(-time.Hour)},
	}
	items := buildGroupedItems(sessions, groupDaily, nil)

	seen := map[string]int{}
	dayRows := map[string]int{}
	for _, it := range items {
		switch v := it.(type) {
		case sessionItem:
			seen[v.sess.ID]++
		case dayItem:
			dayRows[v.dayKey]++
		case headerItem:
			t.Fatalf("daily view should not emit section headers, got %q", v.label)
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("session %s appeared %d times, want 1", id, n)
		}
	}
	for key, n := range dayRows {
		if n != 1 {
			t.Fatalf("day %s appeared %d times, want 1", key, n)
		}
	}
}

func TestBuildDailyItemsRespectsFold(t *testing.T) {
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ModTime: dayOf(0)},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a", ModTime: dayOf(0).Add(-time.Hour)},
	}
	key := dayOf(0).Format("2006-01-02")
	items := buildDailyItems(sessions, map[string]bool{dayFoldKey(key): true})

	if len(items) != 1 {
		t.Fatalf("expected only the folded day row, got %d items", len(items))
	}
	di, ok := items[0].(dayItem)
	if !ok {
		t.Fatalf("expected a dayItem, got %T", items[0])
	}
	if di.expanded {
		t.Fatal("expected the day row to render as folded")
	}
}

func TestDailyDayRowRollsUpOutputs(t *testing.T) {
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ModTime: dayOf(0),
		PlanSlugs: []string{"some-plan"},
		Refs: []session.SessionRef{
			{Kind: session.RefPR, Label: "sendbird/ccx#1", URL: "https://github.com/sendbird/ccx/pull/1", State: session.RefStateOpen, Resolved: true},
			{Kind: session.RefJira, Label: "CPLAT-1", URL: "https://sendbird.atlassian.net/browse/CPLAT-1", Resolved: true},
			{Kind: session.RefArtifact, Label: "artifact:abcd1234", URL: "https://claude.ai/code/artifact/abcd1234", Resolved: true},
		},
	}}
	items := buildDailyItems(sessions, nil)
	di, ok := items[0].(dayItem)
	if !ok {
		t.Fatalf("expected a dayItem, got %T", items[0])
	}
	if di.prs != 1 || di.jiras != 1 || di.artifacts != 1 || di.plans != 1 {
		t.Fatalf("unexpected rollup: prs=%d jiras=%d artifacts=%d plans=%d",
			di.prs, di.jiras, di.artifacts, di.plans)
	}
}

func TestDayLabelUsesRelativeNames(t *testing.T) {
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.Local)
	today := time.Date(2026, 8, 13, 0, 0, 0, 0, time.Local)
	if got := dayLabel(today, now); got != "Today" {
		t.Fatalf("dayLabel(today) = %q, want Today", got)
	}
	if got := dayLabel(today.AddDate(0, 0, -1), now); got != "Yesterday" {
		t.Fatalf("dayLabel(yesterday) = %q, want Yesterday", got)
	}
	older := today.AddDate(0, 0, -5)
	if got, want := dayLabel(older, now), older.Format("Mon Jan 2"); got != want {
		t.Fatalf("dayLabel(-5d) = %q, want %q", got, want)
	}
}

func TestDailyPreviewShowsDaySummary(t *testing.T) {
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0), MsgCount: 5, FirstPrompt: "add the daily view",
			Refs: []session.SessionRef{{Kind: session.RefPR, Label: "sendbird/ccx#7", URL: "https://github.com/sendbird/ccx/pull/7", State: session.RefStateOpen, Resolved: true}}},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(0).Add(-time.Hour), MsgCount: 2},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()

	items := app.sessionList.VisibleItems()
	if len(items) == 0 {
		t.Fatal("expected visible items")
	}
	if _, ok := items[0].(dayItem); !ok {
		t.Fatalf("expected first row to be a dayItem, got %T", items[0])
	}
	app.sessionList.Select(0)
	app.sessSplit.Show = true
	if cmd := app.updateSessionPreview(); cmd != nil {
		t.Fatal("expected the day preview to be synchronous (no transcript reads)")
	}
	content := app.sessSplit.Preview.View()
	// The pane is the day's outputs with a session anchor on each — not a
	// session listing, and no project breakdown either (the list itself nests
	// day → project → session, so repeating it here would say it twice).
	for _, want := range []string{"Today", "Produced (1)", "sendbird/ccx#7", "a1 · repo-a"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected day preview to contain %q, got:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"add the daily view", "Where the time went"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("expected the day preview to omit %q, got:\n%s", unwanted, content)
		}
	}
}

func TestDailyEnterTogglesDayFold(t *testing.T) {
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ModTime: dayOf(0)},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a", ModTime: dayOf(0).Add(-time.Hour)},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()
	app.sessionList.Select(0)

	before := len(app.sessionList.VisibleItems())
	di, ok := app.selectedDay()
	if !ok {
		t.Fatal("expected the cursor to be on a day row")
	}
	app.toggleDayFold(di)
	if got := len(app.sessionList.VisibleItems()); got >= before {
		t.Fatalf("expected folding to hide children: before=%d after=%d", before, got)
	}
	if _, ok := app.selectedDay(); !ok {
		t.Fatal("expected the cursor to stay on the day row after folding")
	}
	app.toggleDayFold(di)
	if got := len(app.sessionList.VisibleItems()); got != before {
		t.Fatalf("expected unfolding to restore children: want %d, got %d", before, got)
	}
}

func TestDailyFilterKeepsDayRowForMatchingChild(t *testing.T) {
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0)},
		{ID: "b1", ShortID: "b1", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(0).Add(-time.Hour)},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()
	app.setSessionListFilter("repo-a")

	sawDay, sawMatch, sawOther := false, false, false
	for _, it := range app.sessionList.VisibleItems() {
		switch v := it.(type) {
		case dayItem:
			sawDay = true
		case sessionItem:
			if v.sess.ID == "a1" {
				sawMatch = true
			}
			if v.sess.ID == "b1" {
				sawOther = true
			}
		}
	}
	if !sawDay {
		t.Fatal("expected the day row to stay visible for its matching child")
	}
	if !sawMatch {
		t.Fatal("expected the matching session to stay visible")
	}
	if sawOther {
		t.Fatal("expected the non-matching session to be filtered out")
	}
}

// --- Outputs digest -------------------------------------------------------

// writeTranscript writes a minimal JSONL transcript with the given raw lines
// and returns its path.
func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func toolUseLine(uuid, ts, tool, input string) string {
	return `{"type":"assistant","uuid":"` + uuid + `","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"` + tool + `","input":` + input + `}]}}`
}

func TestCollectSessionOutputsSeparatesMemoryPlansAndChanges(t *testing.T) {
	home := t.TempDir()
	path := writeTranscript(t,
		toolUseLine("u1", "2026-08-13T01:00:00Z", "Write", `{"file_path":"/repo/internal/tui/daily.go","content":"x"}`),
		toolUseLine("u2", "2026-08-13T02:00:00Z", "Edit", `{"file_path":"/repo/internal/tui/daily.go","old_string":"a","new_string":"b"}`),
		toolUseLine("u3", "2026-08-13T03:00:00Z", "Write", `{"file_path":"`+home+`/.claude/projects/p/memory/note.md","content":"y"}`),
		toolUseLine("u4", "2026-08-13T04:00:00Z", "ExitPlanMode", `{"planFilePath":"`+home+`/.claude/plans/daily-view.md","plan":"do it"}`),
		toolUseLine("u5", "2026-08-13T05:00:00Z", "Read", `{"file_path":"/repo/README.md"}`),
	)
	outs := session.CollectSessionOutputs(session.Session{ID: "s1", FilePath: path}, home)

	byKind := map[session.OutputKind][]session.SessionOutput{}
	for _, o := range outs {
		byKind[o.Kind] = append(byKind[o.Kind], o)
	}
	if n := len(byKind[session.OutputChange]); n != 1 {
		t.Fatalf("expected 1 changed file (Read must not count), got %d", n)
	}
	change := byKind[session.OutputChange][0]
	if change.Title != "daily.go" {
		t.Fatalf("expected the change titled daily.go, got %q", change.Title)
	}
	if change.Count != 2 {
		t.Fatalf("expected 2 write occurrences collapsed into one row, got %d", change.Count)
	}
	if change.MessageUUID != "u1" {
		t.Fatalf("expected the jump target to be the FIRST write (u1), got %q", change.MessageUUID)
	}
	if n := len(byKind[session.OutputMemory]); n != 1 {
		t.Fatalf("expected 1 memory note, got %d", n)
	}
	if n := len(byKind[session.OutputPlan]); n != 1 {
		t.Fatalf("expected 1 plan, got %d", n)
	}
	if got := byKind[session.OutputPlan][0].Title; got != "daily-view" {
		t.Fatalf("expected the plan slug daily-view, got %q", got)
	}
}

func TestSortOutputsPutsResultsBeforeWorkingMaterial(t *testing.T) {
	outs := []session.SessionOutput{
		{Kind: session.OutputScratchpad, Title: "scratch.txt"},
		{Kind: session.OutputChange, Title: "app.go"},
		{Kind: session.OutputPlan, Title: "plan"},
		{Kind: session.OutputPR, Title: "sendbird/ccx#1"},
	}
	session.SortOutputs(outs)
	want := []session.OutputKind{session.OutputPR, session.OutputPlan, session.OutputChange, session.OutputScratchpad}
	for i, k := range want {
		if outs[i].Kind != k {
			t.Fatalf("position %d: got %s, want %s", i, outs[i].Kind, k)
		}
	}
}

func TestOutputsPreviewRendersSectionsAndRefs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeTranscript(t,
		toolUseLine("u1", "2026-08-13T01:00:00Z", "Write", `{"file_path":"/repo/internal/tui/daily.go","content":"x"}`),
	)
	sessions := []session.Session{{
		ID: "s1", ShortID: "s1", FilePath: path, ProjectPath: "/repo", ProjectName: "repo",
		ModTime: dayOf(0), RefsResolved: true, HasRefs: true,
		Refs: []session.SessionRef{
			{Kind: session.RefPR, Label: "sendbird/ccx#9", URL: "https://github.com/sendbird/ccx/pull/9", State: session.RefStateOpen, Resolved: true},
		},
	}}
	app := newTestApp(sessions)
	app.sessGroupMode = groupFlat
	app.rebuildSessionList()
	app.sessionList.Select(0)
	app.sessSplit.Show = true
	app.sessPreviewMode = sessPreviewOutputs

	// The transcript scan is async: the first update dispatches it, and the
	// digest only fills in once outputsCollectedMsg lands. tea.Batch collapses
	// to the single command when there is only one, so handle both shapes.
	cmd := app.updateSessionOutputsPreview(sessions[0])
	if cmd == nil {
		t.Fatal("expected an async collection command")
	}
	msgs := []tea.Msg{cmd()}
	if batch, ok := msgs[0].(tea.BatchMsg); ok {
		msgs = nil
		for _, c := range batch {
			msgs = append(msgs, c())
		}
	}
	delivered := false
	for _, msg := range msgs {
		if collected, ok := msg.(outputsCollectedMsg); ok {
			m, _ := app.Update(collected)
			app = m.(*App)
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("expected an outputsCollectedMsg, got %#v", msgs)
	}

	content := app.sessSplit.Preview.View()
	for _, want := range []string{"Outputs", "Pull Requests", "sendbird/ccx#9", "Files Changed", "daily.go"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected the outputs digest to contain %q, got:\n%s", want, content)
		}
	}
}

func TestOutputsPreviewIgnoresStaleCollection(t *testing.T) {
	// A collection that lands after the user moved to another session must not
	// overwrite the digest — it belongs to a session no longer on screen.
	app := newTestApp(nil)
	app.sessPreviewMode = sessPreviewOutputs
	app.sessOutputsCacheID = "current"
	app.sessOutputs = []session.SessionOutput{{Kind: session.OutputChange, Title: "kept.go"}}
	app.sessOutputsCollected = "current:1"

	m, _ := app.Update(outputsCollectedMsg{
		id:      "other",
		dataKey: "other:1",
		outputs: []session.SessionOutput{{Kind: session.OutputChange, Title: "stale.go"}},
	})
	app = m.(*App)

	if len(app.sessOutputs) != 1 || app.sessOutputs[0].Title != "kept.go" {
		t.Fatalf("expected the stale collection to be ignored, got %+v", app.sessOutputs)
	}
	if app.outputsInFlight["other"] {
		t.Fatal("expected the in-flight latch to clear even for a stale result")
	}
}

func TestOutputsPreviewKeysIgnoreUnrelatedKeys(t *testing.T) {
	// Adding the digest's keys must not swallow navigation keys it does not
	// own — the caller still needs to see them as unhandled.
	app := newTestApp(nil)
	app.sessPreviewMode = sessPreviewOutputs
	app.sessOutputsRows = []session.SessionOutput{
		{Kind: session.OutputChange, Title: "a.go", Path: "/repo/a.go", MessageUUID: "u1"},
		{Kind: session.OutputChange, Title: "b.go", Path: "/repo/b.go", MessageUUID: "u2"},
	}
	sp := &app.sessSplit

	for _, key := range []string{"x", "R", "tab", "esc"} {
		if _, _, handled := app.handleOutputsPreviewKeys(sp, key); handled {
			t.Fatalf("expected the outputs digest to leave %q unhandled", key)
		}
	}
	if _, _, handled := app.handleOutputsPreviewKeys(sp, "down"); !handled {
		t.Fatal("expected the outputs digest to handle down")
	}
	if app.sessOutputsCursor != 1 {
		t.Fatalf("expected the cursor to move to 1, got %d", app.sessOutputsCursor)
	}
}

func TestOutputsCopyPutsURLBeforePath(t *testing.T) {
	app := newTestApp(nil)
	app.sessPreviewMode = sessPreviewOutputs
	app.sessOutputsRows = []session.SessionOutput{
		{Kind: session.OutputPR, Title: "sendbird/ccx#3", URL: "https://github.com/sendbird/ccx/pull/3"},
	}
	if _, _, handled := app.copySelectedOutput(); !handled {
		t.Fatal("expected copy to be handled")
	}
	if !strings.Contains(app.copiedMsg, "sendbird/ccx#3") {
		t.Fatalf("expected a copy confirmation naming the ref, got %q", app.copiedMsg)
	}
}

func TestOutputsLayoutRefreshNeverArmsInFlight(t *testing.T) {
	// The render path cannot dispatch a tea.Cmd, so it must not arm the
	// collection latch — a latch with no command in flight would strand the
	// digest on "scanning transcript…" with nothing left to clear it.
	path := writeTranscript(t,
		toolUseLine("u1", "2026-08-13T01:00:00Z", "Write", `{"file_path":"/repo/a.go","content":"x"}`),
	)
	sess := session.Session{ID: "s1", ShortID: "s1", FilePath: path, ProjectPath: "/repo", ModTime: dayOf(0)}
	app := newTestApp([]session.Session{sess})
	app.sessSplit.Show = true
	app.sessPreviewMode = sessPreviewOutputs

	app.refreshOutputsPreviewLayout(sess)
	if app.outputsInFlight[sess.ID] {
		t.Fatal("layout refresh must not arm the collection latch")
	}
	if app.refsInFlight[sess.ID] {
		t.Fatal("layout refresh must not arm the refs latch")
	}
	// The dispatching variant still does its job.
	if cmd := app.updateSessionOutputsPreview(sess); cmd == nil {
		t.Fatal("expected the dispatching variant to return work")
	}
	if !app.outputsInFlight[sess.ID] {
		t.Fatal("expected the dispatching variant to arm the latch")
	}
}

func TestInvalidateOutputsClearsInFlightLatch(t *testing.T) {
	sess := session.Session{ID: "s1", ShortID: "s1", ProjectPath: "/repo", ModTime: dayOf(0)}
	app := newTestApp([]session.Session{sess})
	app.sessSplit.Show = true
	app.sessPreviewMode = sessPreviewOutputs
	app.sessionList.Select(0)
	app.outputsInFlight[sess.ID] = true
	app.sessOutputsCollected = "stale"

	app.invalidateOpenPreviewCaches()

	if app.outputsInFlight[sess.ID] {
		t.Fatal("expected refresh to drop the in-flight latch so a collection can re-dispatch")
	}
	if app.sessOutputsCollected != "" {
		t.Fatal("expected refresh to drop the collected marker")
	}
}

func TestResolveVisibleRefsCoversSessionsUnderDayRows(t *testing.T) {
	// The day rollup badges are the daily view's headline, and they are summed
	// from child refs — so a folded day must still get its sessions extracted.
	path := writeTranscript(t,
		`{"type":"assistant","uuid":"u1","timestamp":"2026-08-13T01:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"https://github.com/sendbird/ccx/pull/1"}]}}`,
	)
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", FilePath: path, ProjectPath: "/tmp/repo-a", ModTime: dayOf(0), HasRefs: true},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.sessFolded = map[string]bool{dayFoldKey(dayOf(0).Format("2006-01-02")): true}
	app.rebuildSessionList()

	for _, it := range app.sessionList.VisibleItems() {
		if _, ok := it.(sessionItem); ok {
			t.Fatal("expected the day to be folded (no session rows visible)")
		}
	}
	if cmd := app.resolveVisibleRefsCmd(); cmd == nil {
		t.Fatal("expected an extract to be dispatched for the folded day's sessions")
	}
	if !app.refsInFlight["a1"] {
		t.Fatal("expected the child session's extract to be armed")
	}
}

func TestDayPreviewCollapsesRepeatedOutputs(t *testing.T) {
	// The same PR gets referenced from several of a day's sessions. The row is
	// the PR, not each mention — otherwise a busy day's other results are buried.
	pr := session.SessionRef{Kind: session.RefPR, Label: "sendbird/ccx#154", URL: "https://github.com/sendbird/ccx/pull/154", State: session.RefStateOpen, Resolved: true}
	sessions := []session.Session{
		{ID: "first", ShortID: "first", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0).Add(-3 * time.Hour), Refs: []session.SessionRef{pr}},
		{ID: "later", ShortID: "later", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(0).Add(-time.Hour), Refs: []session.SessionRef{pr}},
	}
	di := buildDailyItems(sessions, nil)[0].(dayItem)
	rows := buildDayOutputRows(di)

	if len(rows) != 1 {
		t.Fatalf("expected the repeated PR to collapse to one row, got %d", len(rows))
	}
	if rows[0].sessions != 2 {
		t.Fatalf("expected the row to record 2 touching sessions, got %d", rows[0].sessions)
	}
	// The anchor is the EARLIEST session — where the work happened, not where it
	// was later quoted.
	if rows[0].sessID != "first" {
		t.Fatalf("expected the earliest session as the anchor, got %q", rows[0].sessID)
	}
}

// TestDayPreviewTabsCoverEveryKindProduced replaces the old kind-ordering test:
// kinds are no longer sections in one list, they are tabs, and the bar must
// offer exactly the kinds the scope produced (All plus those).
func TestDayPreviewTabsCoverEveryKindProduced(t *testing.T) {
	sessions := []session.Session{{
		ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ModTime: dayOf(0),
		PlanSlugs: []string{"a-plan"},
		Refs: []session.SessionRef{
			{Kind: session.RefArtifact, Label: "artifact:abcd1234", Title: "Report", URL: "https://claude.ai/code/artifact/abcd1234", Resolved: true},
			{Kind: session.RefPR, Label: "sendbird/ccx#1", URL: "https://github.com/sendbird/ccx/pull/1", Resolved: true},
		},
	}}
	di := buildDailyItems(sessions, nil)[0].(dayItem)
	rows := buildDayOutputRows(di)

	tabs := dayOutputTabsFor(rows, "")
	var got []string
	for _, tb := range tabs {
		got = append(got, tb.label)
	}
	// All first, then the produced kinds in outputKindRank order. Jira is absent
	// because the day produced none — a tab that can only ever be empty would
	// make the bar say more than the day does.
	want := []string{"All", "PRs", "Artifacts", "Plans"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tabs = %v, want %v", got, want)
	}
	for _, tb := range tabs {
		if tb.kind == "" {
			continue
		}
		for _, r := range filterDayOutputRows(rows, tb, "") {
			if r.out.Kind != tb.kind {
				t.Errorf("tab %q leaked a %s row", tb.label, r.out.Kind)
			}
		}
	}
}

func TestDayPreviewEnterOpensProducingSession(t *testing.T) {
	// Two sessions, and the one that produced the output is NOT the day's
	// representative (selectedSession returns di.sessions[0], the most recent).
	// Without the day-pane branch in the Open handler, Enter would open the
	// representative instead of the row's anchor and the test would pass by
	// coincidence.
	makerPath := writeTranscript(t,
		`{"type":"user","uuid":"u1","timestamp":"2026-08-13T01:00:00Z","message":{"role":"user","content":"make the PR"}}`,
	)
	sessions := []session.Session{
		{ID: "newest", ShortID: "newest", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(0)},
		{ID: "maker", ShortID: "maker", FilePath: makerPath, ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0).Add(-2 * time.Hour),
			Refs: []session.SessionRef{{Kind: session.RefPR, Label: "sendbird/ccx#5", URL: "https://github.com/sendbird/ccx/pull/5", Resolved: true}}},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()
	app.sessionList.Select(0)
	app.sessSplit.Show = true
	app.sessSplit.Focus = true
	_ = app.updateSessionPreview()

	if len(app.dayOutputRows) != 1 {
		t.Fatalf("expected 1 output row, got %d", len(app.dayOutputRows))
	}
	if app.dayOutputRows[0].sessID != "maker" {
		t.Fatalf("expected the row anchored to the producing session, got %q", app.dayOutputRows[0].sessID)
	}
	// Drive the real key dispatch, not the handler directly: Enter is
	// km.Session.Open and is consumed by the top-level switch before the
	// focused-preview handlers ever run.
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = m.(*App)
	if app.currentSess.ID != "maker" {
		t.Fatalf("expected the producing session to be opened, got %q", app.currentSess.ID)
	}
	if app.state != viewConversation {
		t.Fatalf("expected Enter to enter the conversation view, got state %d", app.state)
	}
}

func TestDayPreviewCursorKeepsScrollPosition(t *testing.T) {
	// A real day can hold hundreds of outputs. Recreating the viewport on every
	// cursor move would snap the pane back to the top, making everything below
	// the fold unreachable.
	var refs []session.SessionRef
	for i := 0; i < 60; i++ {
		refs = append(refs, session.SessionRef{
			Kind:  session.RefPR,
			Label: fmt.Sprintf("sendbird/ccx#%d", i),
			URL:   fmt.Sprintf("https://github.com/sendbird/ccx/pull/%d", i),
		})
	}
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ModTime: dayOf(0), Refs: refs},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()
	app.sessionList.Select(0)
	app.sessSplit.Show = true
	app.sessSplit.Focus = true
	_ = app.updateSessionPreview()

	app.sessSplit.Preview.SetYOffset(20)
	before := app.sessSplit.Preview.YOffset
	if before == 0 {
		t.Fatal("expected the pane to be scrollable for this fixture")
	}
	if _, _, handled := app.handleDayPreviewKeys(&app.sessSplit, "down"); !handled {
		t.Fatal("expected the day pane to handle down")
	}
	if got := app.sessSplit.Preview.YOffset; got < before {
		t.Fatalf("expected the cursor move to keep (or advance) the scroll position: %d → %d", before, got)
	}
}

func TestDailyViewTogglesWithSingleKey(t *testing.T) {
	// The daily view is an axis you flip while reading, so it must round-trip
	// without the command palette — and land back where the user was.
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0)},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.preDailyGroupMode = groupProjectCentric
	app.rebuildSessionList()

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	app = m.(*App)
	if app.sessGroupMode != groupDaily {
		t.Fatalf("expected D to enter the daily view, got mode %d", app.sessGroupMode)
	}
	if _, ok := app.sessionList.VisibleItems()[0].(dayItem); !ok {
		t.Fatal("expected a date row after toggling into the daily view")
	}

	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	app = m.(*App)
	if app.sessGroupMode != groupProjectCentric {
		t.Fatalf("expected D to restore the previous grouping, got mode %d", app.sessGroupMode)
	}
}

func TestDayRollupFillsFromExtractAlone(t *testing.T) {
	// A date row's badges and Produced list are built from the refs themselves,
	// so they must fill in when the offline extract lands — waiting for
	// refStatusMsg leaves a day with many PRs reporting zero.
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ModTime: dayOf(0), HasRefs: true},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()
	app.sessionList.Select(0)
	app.sessSplit.Show = true
	_ = app.updateSessionPreview()

	if di, _ := app.selectedDay(); di.prs != 0 {
		t.Fatalf("expected no PRs before the extract lands, got %d", di.prs)
	}

	m, _ := app.Update(refsExtractedMsg{id: "a1", refs: []session.SessionRef{
		{Kind: session.RefPR, Label: "sendbird/ccx#154", URL: "https://github.com/sendbird/ccx/pull/154"},
	}})
	app = m.(*App)

	di, ok := app.selectedDay()
	if !ok {
		t.Fatal("expected the cursor to still be on the day row")
	}
	if di.prs != 1 {
		t.Fatalf("expected the day rollup to count the extracted PR, got %d", di.prs)
	}
	if len(app.dayOutputRows) != 1 {
		t.Fatalf("expected the Produced list to show the PR, got %d rows", len(app.dayOutputRows))
	}
}

// A day with hundreds of outputs re-renders on every cursor keypress. Measure
// that a keypress stays well inside a frame budget.
func TestDayCursorMoveCost(t *testing.T) {
	var refs []session.SessionRef
	for i := 0; i < 500; i++ {
		refs = append(refs, session.SessionRef{
			Kind: session.RefPR, Label: fmt.Sprintf("sendbird/ccx#%d", i),
			URL: fmt.Sprintf("https://github.com/sendbird/ccx/pull/%d", i),
		})
	}
	var sessions []session.Session
	for i := 0; i < 250; i++ {
		s := session.Session{ID: fmt.Sprintf("s%d", i), ShortID: fmt.Sprintf("s%d", i),
			ProjectPath: "/tmp/repo", ProjectName: "repo", ModTime: dayOf(0).Add(-time.Duration(i) * time.Minute)}
		if i == 0 {
			s.Refs = refs
		}
		sessions = append(sessions, s)
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()
	app.sessionList.Select(0)
	app.sessSplit.Show = true
	app.sessSplit.Focus = true
	_ = app.updateSessionPreview()
	t.Logf("rows=%d", len(app.dayOutputRows))

	t0 := time.Now()
	const n = 100
	for i := 0; i < n; i++ {
		app.handleDayPreviewKeys(&app.sessSplit, "down")
	}
	per := time.Since(t0) / n
	t.Logf("per cursor move: %s", per.Round(time.Microsecond))
	if per > 16*time.Millisecond {
		t.Errorf("cursor move costs %s — above a 60fps frame budget", per)
	}
}

func TestVisibleRefsSpreadsParentFanoutAndConverges(t *testing.T) {
	// A date row can own hundreds of sessions. Extracting all of them the
	// moment the view opens is what made the daily view feel slow, so each pass
	// takes only the next slice — but the rollup must still reach the exact
	// count, not settle for an approximation.
	var sessions []session.Session
	for i := 0; i < 30; i++ {
		path := writeTranscript(t,
			`{"type":"assistant","uuid":"u1","timestamp":"2026-08-13T01:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"https://github.com/sendbird/ccx/pull/`+fmt.Sprint(100+i)+`"}]}}`,
		)
		sessions = append(sessions, session.Session{
			ID: fmt.Sprintf("s%02d", i), ShortID: fmt.Sprintf("s%02d", i), FilePath: path,
			ProjectPath: "/tmp/repo", ProjectName: "repo",
			ModTime: dayOf(0).Add(-time.Duration(i) * time.Minute), HasRefs: true,
		})
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.sessFolded = map[string]bool{dayFoldKey(dayOf(0).Format("2006-01-02")): true}
	app.rebuildSessionList()
	app.sessionList.Select(0)

	first := 0
	passes := 0
	for passes < 25 {
		cmd := app.resolveVisibleRefsCmd()
		if cmd == nil {
			break
		}
		n := 0
		if b, ok := cmd().(tea.BatchMsg); ok {
			n = len(b)
			for _, c := range b {
				m, _ := app.Update(c())
				app = m.(*App)
			}
		} else {
			n = 1
			m, _ := app.Update(cmd())
			app = m.(*App)
		}
		if passes == 0 {
			first = n
		}
		passes++
	}

	if first == 0 || first >= len(sessions) {
		t.Fatalf("expected the first pass to take a slice, not everything: got %d of %d", first, len(sessions))
	}
	// Converged: every session's refs extracted, so the rollup is exact.
	di, ok := app.selectedDay()
	if !ok {
		t.Fatal("expected the cursor to be on the day row")
	}
	if di.prs != len(sessions) {
		t.Fatalf("expected the rollup to converge to %d PRs, got %d after %d passes", len(sessions), di.prs, passes)
	}
}

func TestDailyToggleWorksWithPreviewFocused(t *testing.T) {
	// Which grouping the list uses is independent of which pane holds the
	// cursor, so D must work with the preview focused too.
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0)},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.preDailyGroupMode = groupProjectCentric
	app.rebuildSessionList()
	app.sessSplit.Show = true
	app.sessSplit.Focus = true

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	app = m.(*App)
	if app.sessGroupMode != groupDaily {
		t.Fatalf("expected D to work with the preview focused, got mode %d", app.sessGroupMode)
	}
}

func TestPrevGroupModePersists(t *testing.T) {
	// A user whose saved grouping is daily must still get their own view back
	// on toggle-out, not the built-in default.
	app := newTestApp(nil)
	app.sessGroupMode = groupDaily
	app.preDailyGroupMode = groupTree

	prefs := app.capturePreferences()
	if prefs.PrevGroupMode != "tree" {
		t.Fatalf("expected prev_group_mode to persist as tree, got %q", prefs.PrevGroupMode)
	}

	restored := newTestApp(nil)
	restored.preDailyGroupMode = groupProjectCentric
	restored.applyPreferences(Preferences{GroupMode: "daily", PrevGroupMode: "tree"})
	if restored.preDailyGroupMode != groupTree {
		t.Fatalf("expected the restored return target to be tree, got %d", restored.preDailyGroupMode)
	}
}

func TestDailyBuildsDayProjectSessionTree(t *testing.T) {
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0), MsgCount: 5},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0).Add(-time.Hour), MsgCount: 3},
		{ID: "b1", ShortID: "b1", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(0).Add(-2 * time.Hour), MsgCount: 7},
	}
	items := buildDailyItems(sessions, nil)

	var kinds []string
	for _, it := range items {
		switch v := it.(type) {
		case dayItem:
			kinds = append(kinds, "day")
		case projectItem:
			if v.dayKey == "" {
				t.Fatal("expected a project row in the daily tree to carry its day key")
			}
			if v.treeDepth != 1 {
				t.Fatalf("expected project depth 1, got %d", v.treeDepth)
			}
			kinds = append(kinds, "proj:"+v.displayName)
		case sessionItem:
			if v.treeDepth != 2 {
				t.Fatalf("expected session depth 2 under a project, got %d", v.treeDepth)
			}
			kinds = append(kinds, "sess:"+v.sess.ID)
		}
	}
	want := []string{"day", "proj:repo-a", "sess:a1", "sess:a2", "proj:repo-b", "sess:b1"}
	if len(kinds) != len(want) {
		t.Fatalf("expected tree %v, got %v", want, kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("position %d: got %q, want %q (full: %v)", i, kinds[i], want[i], kinds)
		}
	}
}

func TestDayProjectRowAggregatesThatDayOnly(t *testing.T) {
	// A project row is the middle tier: it must aggregate only the slice of
	// work that happened in that project on that day.
	sessions := []session.Session{
		{ID: "today1", ShortID: "today1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0), MsgCount: 5,
			Refs: []session.SessionRef{{Kind: session.RefPR, Label: "x/y#1", URL: "https://github.com/x/y/pull/1"}}},
		{ID: "today2", ShortID: "today2", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0).Add(-time.Hour), MsgCount: 3},
		{ID: "yday", ShortID: "yday", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(1), MsgCount: 99,
			Refs: []session.SessionRef{{Kind: session.RefPR, Label: "x/y#2", URL: "https://github.com/x/y/pull/2"}}},
	}
	items := buildDailyItems(sessions, nil)

	var today projectItem
	for _, it := range items {
		if p, ok := it.(projectItem); ok && p.dayKey == dayOf(0).Format("2006-01-02") {
			today = p
			break
		}
	}
	if len(today.sessions) != 2 {
		t.Fatalf("expected today's project row to hold 2 sessions, got %d", len(today.sessions))
	}
	if today.totalMsgs != 8 {
		t.Fatalf("expected 8 messages for today only, got %d", today.totalMsgs)
	}
}

func TestDayProjectFoldIsScopedToItsDay(t *testing.T) {
	// The same project appears under every day it was worked on; one shared
	// fold key would collapse it everywhere at once.
	sessions := []session.Session{
		{ID: "t", ShortID: "t", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0)},
		{ID: "y", ShortID: "y", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(1)},
	}
	folded := map[string]bool{dayProjectFoldKey(dayOf(0).Format("2006-01-02"), "/tmp/repo-a"): true}
	items := buildDailyItems(sessions, folded)

	var visibleSessions []string
	for _, it := range items {
		if si, ok := it.(sessionItem); ok {
			visibleSessions = append(visibleSessions, si.sess.ID)
		}
	}
	if len(visibleSessions) != 1 || visibleSessions[0] != "y" {
		t.Fatalf("expected only yesterday's session visible, got %v", visibleSessions)
	}
}

func TestDailyAndBrowserKeepSeparatePreviewModes(t *testing.T) {
	// The daily view is about results and the project browser about sessions;
	// sharing one preview choice means every swap lands on the wrong pane.
	app := newTestApp(nil)
	app.sessGroupMode = groupProjectCentric
	app.preDailyGroupMode = groupProjectCentric
	app.sessPreviewMode = sessPreviewConversation
	app.browserPreviewMode = sessPreviewConversation
	app.dailyPreviewMode = sessPreviewOutputs

	app.toggleDailyView()
	if app.sessPreviewMode != sessPreviewOutputs {
		t.Fatalf("expected the daily view to open on its own preview, got %d", app.sessPreviewMode)
	}
	// Change the mode while in the daily view, then swap back and forth.
	app.sessPreviewMode = sessPreviewRefs
	app.toggleDailyView()
	if app.sessPreviewMode != sessPreviewConversation {
		t.Fatalf("expected the browser to restore its own preview, got %d", app.sessPreviewMode)
	}
	app.toggleDailyView()
	if app.sessPreviewMode != sessPreviewRefs {
		t.Fatalf("expected the daily view to remember refs, got %d", app.sessPreviewMode)
	}
}

func TestToggleKeepsCursorOnSameRowKind(t *testing.T) {
	// Landing on the same session is not enough: if the cursor was on a project
	// head and the swap drops it onto one of that project's children, the
	// toggle reads as "it lost my place".
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0)},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0).Add(-time.Hour)},
		{ID: "b1", ShortID: "b1", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(0).Add(-2 * time.Hour)},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.preDailyGroupMode = groupProjectCentric
	app.rebuildSessionList()

	// Park on a project head row in the browser.
	var wantPath string
	for i, it := range app.sessionList.VisibleItems() {
		if p, ok := it.(projectItem); ok {
			app.sessionList.Select(i)
			wantPath = p.basePath
			break
		}
	}
	if wantPath == "" {
		t.Fatal("expected a project row in the project browser")
	}

	app.toggleDailyView()
	p, ok := app.sessionList.SelectedItem().(projectItem)
	if !ok {
		t.Fatalf("expected to land on a project row in the daily view, got %T", app.sessionList.SelectedItem())
	}
	if p.basePath != wantPath {
		t.Fatalf("expected the same project (%s), got %s", wantPath, p.basePath)
	}

	app.toggleDailyView()
	back, ok := app.sessionList.SelectedItem().(projectItem)
	if !ok {
		t.Fatalf("expected to land back on a project row, got %T", app.sessionList.SelectedItem())
	}
	if back.basePath != wantPath {
		t.Fatalf("expected the same project on the way back (%s), got %s", wantPath, back.basePath)
	}
}

func TestToggleKeepsCursorOnSession(t *testing.T) {
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0)},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0).Add(-time.Hour)},
		{ID: "b1", ShortID: "b1", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(0).Add(-2 * time.Hour)},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.preDailyGroupMode = groupProjectCentric
	app.rebuildSessionList()

	var want string
	for i, it := range app.sessionList.VisibleItems() {
		if si, ok := it.(sessionItem); ok && si.sess.ID == "b1" {
			app.sessionList.Select(i)
			want = si.sess.ID
			break
		}
	}
	if want == "" {
		t.Fatal("expected session b1 to be visible")
	}

	app.toggleDailyView()
	si, ok := app.sessionList.SelectedItem().(sessionItem)
	if !ok {
		t.Fatalf("expected to land on a session row, got %T", app.sessionList.SelectedItem())
	}
	if si.sess.ID != want {
		t.Fatalf("expected session %s, got %s", want, si.sess.ID)
	}
}

func TestRebuildKeepsDayCursorWhenNewerSessionArrives(t *testing.T) {
	// The live tick rebuilds the list every few seconds. A day row used to be
	// re-found via its newest session, so a fresh session landing on that day
	// moved the anchor and dropped the cursor onto a child row.
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0)},
		{ID: "y1", ShortID: "y1", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(1)},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()

	wantDay := ""
	for i, it := range app.sessionList.VisibleItems() {
		if di, ok := it.(dayItem); ok && di.dayKey == dayOf(1).Format("2006-01-02") {
			app.sessionList.Select(i)
			wantDay = di.dayKey
			break
		}
	}
	if wantDay == "" {
		t.Fatal("expected yesterday's day row to be visible")
	}

	// A newer session appears on that same day — the day's representative
	// session changes, its identity does not.
	app.sessions = append(app.sessions, session.Session{
		ID: "y2", ShortID: "y2", ProjectPath: "/tmp/repo-c", ProjectName: "repo-c",
		ModTime: dayOf(1).Add(2 * time.Hour),
	})
	app.rebuildSessionList()

	di, ok := app.sessionList.SelectedItem().(dayItem)
	if !ok {
		t.Fatalf("expected to stay on a day row, got %T", app.sessionList.SelectedItem())
	}
	if di.dayKey != wantDay {
		t.Fatalf("expected to stay on day %s, got %s", wantDay, di.dayKey)
	}
}

func TestRebuildKeepsProjectCursorOnItsOwnDay(t *testing.T) {
	// The same repo appears under every day it was touched. Matching on the
	// path alone walked the newest-day-first list and snapped the cursor to
	// today's copy of the row.
	sessions := []session.Session{
		{ID: "t1", ShortID: "t1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0)},
		{ID: "y1", ShortID: "y1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(1)},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.rebuildSessionList()

	wantDay := dayOf(1).Format("2006-01-02")
	found := false
	for i, it := range app.sessionList.VisibleItems() {
		if p, ok := it.(projectItem); ok && p.dayKey == wantDay {
			app.sessionList.Select(i)
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected repo-a to appear under %s", wantDay)
	}

	app.rebuildSessionList()

	p, ok := app.sessionList.SelectedItem().(projectItem)
	if !ok {
		t.Fatalf("expected to stay on a project row, got %T", app.sessionList.SelectedItem())
	}
	if p.dayKey != wantDay {
		t.Fatalf("expected to stay under day %s, got %s", wantDay, p.dayKey)
	}
	if p.basePath != "/tmp/repo-a" {
		t.Fatalf("expected repo-a, got %s", p.basePath)
	}
}

// foldedKeys returns the currently collapsed fold keys, sorted, for assertions
// that the auto-expand touched only the cursor's own ancestors.
func foldedKeys(app *App) []string {
	var keys []string
	for k, v := range app.sessFolded {
		if v {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// selectedIsOnScreen reports whether the cursor's row is on the list's current
// page — Select() sets the paginator page, so a restore that only sets the
// index without scrolling would leave the user staring at an unrelated screen.
func selectedIsOnScreen(app *App) bool {
	idx := app.sessionList.Index()
	per := app.sessionList.Paginator.PerPage
	if per <= 0 {
		return false
	}
	return app.sessionList.Paginator.Page == idx/per
}

func TestToggleIntoDailyExpandsAncestorsOfSession(t *testing.T) {
	// The defect: with the destination day (and the project under it) folded,
	// the target session is not in VisibleItems() at all, so the restore fell
	// through to its last-resort branch and the cursor landed somewhere else.
	sessions := []session.Session{
		{ID: "t1", ShortID: "t1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0)},
		{ID: "y1", ShortID: "y1", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(2)},
		{ID: "y2", ShortID: "y2", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(2).Add(-time.Hour)},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.preDailyGroupMode = groupProjectCentric
	yesterKey := dayOf(2).Format("2006-01-02")
	todayKey := dayOf(0).Format("2006-01-02")
	// Everything on the destination side is collapsed, including one fold the
	// cursor has no business in (today's date row).
	app.sessFolded = map[string]bool{
		dayFoldKey(yesterKey):                       true,
		dayProjectFoldKey(yesterKey, "/tmp/repo-b"): true,
		dayFoldKey(todayKey):                        true,
		dayProjectFoldKey(todayKey, "/tmp/repo-a"):  true,
	}
	app.rebuildSessionList()

	found := false
	for i, it := range app.sessionList.VisibleItems() {
		if si, ok := it.(sessionItem); ok && si.sess.ID == "y2" {
			app.sessionList.Select(i)
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected session y2 in the project browser")
	}

	app.toggleDailyView()

	si, ok := app.sessionList.SelectedItem().(sessionItem)
	if !ok {
		t.Fatalf("expected to land on a session row, got %T", app.sessionList.SelectedItem())
	}
	if si.sess.ID != "y2" {
		t.Fatalf("expected session y2, got %s", si.sess.ID)
	}
	if !selectedIsOnScreen(app) {
		t.Fatal("expected the restored cursor to be scrolled onto the visible page")
	}
	// Only the cursor's own ancestors were opened; today's folds are untouched.
	want := []string{dayProjectFoldKey(todayKey, "/tmp/repo-a"), dayFoldKey(todayKey)}
	sort.Strings(want)
	if got := foldedKeys(app); !slices.Equal(got, want) {
		t.Fatalf("expected only the target's ancestors to be expanded\n got: %v\nwant: %v", got, want)
	}
}

func TestToggleOutOfDailyExpandsFoldedProject(t *testing.T) {
	// Same defect in the other direction: the project browser folds by
	// "repo:<basePath>", so a session under a collapsed project row is invisible
	// on the way back out of the daily view.
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0)},
		{ID: "b1", ShortID: "b1", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(0).Add(-time.Hour)},
		{ID: "b2", ShortID: "b2", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(0).Add(-2 * time.Hour)},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.preDailyGroupMode = groupProjectCentric
	app.sessFolded = map[string]bool{
		"repo:/tmp/repo-b": true,
		"repo:/tmp/repo-a": true, // unrelated: must survive the swap
	}
	app.rebuildSessionList()

	found := false
	for i, it := range app.sessionList.VisibleItems() {
		if si, ok := it.(sessionItem); ok && si.sess.ID == "b2" {
			app.sessionList.Select(i)
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected session b2 under its day in the daily view")
	}

	app.toggleDailyView()

	si, ok := app.sessionList.SelectedItem().(sessionItem)
	if !ok {
		t.Fatalf("expected to land on a session row, got %T", app.sessionList.SelectedItem())
	}
	if si.sess.ID != "b2" {
		t.Fatalf("expected session b2, got %s", si.sess.ID)
	}
	if !selectedIsOnScreen(app) {
		t.Fatal("expected the restored cursor to be scrolled onto the visible page")
	}
	if app.sessFolded["repo:/tmp/repo-b"] {
		t.Fatal("expected the cursor's own project to be expanded")
	}
	if !app.sessFolded["repo:/tmp/repo-a"] {
		t.Fatal("expected an unrelated project to stay folded")
	}
}

func TestToggleIntoDailyExpandsDayForProjectRow(t *testing.T) {
	// A project row's only ancestor in the daily view is its date row — the
	// day-scoped project fold belongs to the row itself and must NOT be cleared,
	// or the swap silently unfolds that project's whole session list.
	sessions := []session.Session{
		{ID: "y1", ShortID: "y1", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(1)},
		{ID: "y2", ShortID: "y2", ProjectPath: "/tmp/repo-b", ProjectName: "repo-b", ModTime: dayOf(1).Add(-time.Hour)},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.preDailyGroupMode = groupProjectCentric
	dayKey := dayOf(1).Format("2006-01-02")
	app.sessFolded = map[string]bool{
		dayFoldKey(dayKey):                       true,
		dayProjectFoldKey(dayKey, "/tmp/repo-b"): true,
	}
	app.rebuildSessionList()

	found := false
	for i, it := range app.sessionList.VisibleItems() {
		if p, ok := it.(projectItem); ok && p.basePath == "/tmp/repo-b" {
			app.sessionList.Select(i)
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a repo-b project row in the project browser")
	}

	app.toggleDailyView()

	p, ok := app.sessionList.SelectedItem().(projectItem)
	if !ok {
		t.Fatalf("expected to land on a project row, got %T", app.sessionList.SelectedItem())
	}
	if p.basePath != "/tmp/repo-b" || p.dayKey != dayKey {
		t.Fatalf("expected repo-b under %s, got %s under %s", dayKey, p.basePath, p.dayKey)
	}
	if app.sessFolded[dayFoldKey(dayKey)] {
		t.Fatal("expected the date row to be expanded so its project row is visible")
	}
	if !app.sessFolded[dayProjectFoldKey(dayKey, "/tmp/repo-b")] {
		t.Fatal("expected the project row's own fold to be left alone — it is the target, not an ancestor")
	}
}

func TestToggleOutOfDailyExpandsGroupedSessionHeader(t *testing.T) {
	// Non-daily groupings fold via sessionItem.groupKey ("repo:" here) rather
	// than a projectItem, so the ancestor walk has to handle a session row
	// heading a folded group too.
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0)},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0).Add(-time.Hour)},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupDaily
	app.preDailyGroupMode = groupBaseProject
	app.sessFolded = map[string]bool{"repo:/tmp/repo-a": true}
	app.rebuildSessionList()

	found := false
	for i, it := range app.sessionList.VisibleItems() {
		if si, ok := it.(sessionItem); ok && si.sess.ID == "a2" {
			app.sessionList.Select(i)
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected session a2 in the daily view")
	}

	app.toggleDailyView()

	if app.sessGroupMode != groupBaseProject {
		t.Fatalf("expected to return to base-project grouping, got %d", app.sessGroupMode)
	}
	si, ok := app.sessionList.SelectedItem().(sessionItem)
	if !ok {
		t.Fatalf("expected to land on a session row, got %T", app.sessionList.SelectedItem())
	}
	if si.sess.ID != "a2" {
		t.Fatalf("expected session a2, got %s", si.sess.ID)
	}
	if app.sessFolded["repo:/tmp/repo-a"] {
		t.Fatal("expected the folded base-repo group to be expanded for the cursor")
	}
}

func TestToggleScrollsRevealedRowIntoView(t *testing.T) {
	// Revealing the row is only half the fix: on a day with enough projects the
	// target sits pages below the top, and a restore that set the index without
	// paging would leave the user looking at an unrelated screen.
	var sessions []session.Session
	for i := 0; i < 40; i++ {
		sessions = append(sessions, session.Session{
			ID:      fmt.Sprintf("s%02d", i),
			ShortID: fmt.Sprintf("s%02d", i),
			// One project per session so the daily view emits a project row for
			// each, pushing the last one well past the first page.
			ProjectPath: fmt.Sprintf("/tmp/repo-%02d", i),
			ProjectName: fmt.Sprintf("repo-%02d", i),
			ModTime:     dayOf(0).Add(-time.Duration(i) * time.Minute),
		})
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.preDailyGroupMode = groupProjectCentric
	dayKey := dayOf(0).Format("2006-01-02")
	app.sessFolded = map[string]bool{
		dayFoldKey(dayKey):                        true,
		dayProjectFoldKey(dayKey, "/tmp/repo-39"): true,
	}
	app.rebuildSessionList()

	for i, it := range app.sessionList.VisibleItems() {
		if si, ok := it.(sessionItem); ok && si.sess.ID == "s39" {
			app.sessionList.Select(i)
			break
		}
	}

	app.toggleDailyView()

	si, ok := app.sessionList.SelectedItem().(sessionItem)
	if !ok || si.sess.ID != "s39" {
		t.Fatalf("expected to land on session s39, got %T", app.sessionList.SelectedItem())
	}
	idx := app.sessionList.Index()
	per := app.sessionList.Paginator.PerPage
	if per <= 0 || idx < per {
		t.Fatalf("test setup no longer places the target off the first page: idx=%d perPage=%d", idx, per)
	}
	if app.sessionList.Paginator.Page != idx/per {
		t.Fatalf("expected the list scrolled to page %d, got %d", idx/per, app.sessionList.Paginator.Page)
	}
}
