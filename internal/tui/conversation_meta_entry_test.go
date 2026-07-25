package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// writeTestMemoryNotes writes memory notes (name → body) into the encoded
// memory directory for projectPath under the real home, returning the memory
// dir path. All created dirs/files are cleaned up via t.Cleanup.
func writeTestMemoryNotes(t *testing.T, projectPath string, notes map[string]string) string {
	t.Helper()
	enc := session.EncodeProjectPath(projectPath)
	memDir := filepath.Join(homeDir(), ".claude", "projects", enc, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("mkdir memory dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(homeDir(), ".claude", "projects", enc)) })
	for name, body := range notes {
		if err := os.WriteFile(filepath.Join(memDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return memDir
}

func TestOriginVisibilityCachesTranscriptUntilRailRefresh(t *testing.T) {
	app, sess, _ := setupConversationStateFixture(t)
	origin := session.ArtifactOrigin{
		Transcript:  sess.FilePath,
		MessageUUID: "task-create",
		EntryIndex:  0,
		BlockIndex:  0,
	}
	if !app.originVisibleInExecutionContext(origin) {
		t.Fatal("visible root origin was not found")
	}
	if len(app.conv.execution.OriginVisibility) != 1 {
		t.Fatalf("visibility cache size = %d, want 1", len(app.conv.execution.OriginVisibility))
	}
	if err := os.Remove(sess.FilePath); err != nil {
		t.Fatal(err)
	}
	if !app.originVisibleInExecutionContext(origin) {
		t.Fatal("cached visibility reloaded the removed transcript")
	}

	app.initExecutionRail(app.conv.agents)
	if app.originVisibleInExecutionContext(origin) {
		t.Fatal("rail refresh retained stale provenance visibility")
	}
}

func TestTodoRowsAreIndividuallySelectableWithLatestOrigin(t *testing.T) {
	app, sess, parentPath := setupConversationStateFixture(t)
	app.conv.sess.Todos = []session.TodoItem{
		{Content: "first todo", Status: "in_progress"},
		{Content: "second todo", Status: "completed"},
	}
	app.currentSess.Todos = app.conv.sess.Todos

	// Add TodoWrite occurrences directly to the agent transcript and rebuild the
	// session flow so each current todo can resolve its latest exact origin.
	f, err := os.OpenFile(parentPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"assistant","uuid":"todo-write","agentId":"parent1111111111","timestamp":"2026-07-04T10:04:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"todo-call","name":"TodoWrite","input":{"todos":[{"content":"first todo","status":"in_progress"},{"content":"second todo","status":"completed"}]}}]}}
`)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	flow, err := session.BuildSessionFlow(&sess)
	if err != nil {
		t.Fatal(err)
	}
	app.conv.flow = flow
	app.initExecutionRail(flow.Agents())

	rows := app.metaTodoEntries(app.conv.sess.Todos)
	if len(rows) != 3 { // header + two independent rows
		t.Fatalf("todo rows = %d, want header + 2", len(rows))
	}
	for i, want := range []string{"first todo", "second todo"} {
		row := rows[i+1]
		if !strings.Contains(stripANSI(row.block.Text), want) {
			t.Fatalf("todo row %d missing %q: %q", i, want, row.block.Text)
		}
		if row.target.kind != metaTargetTodo || row.target.transcript != parentPath || row.target.messageUUID != "todo-write" {
			t.Fatalf("todo target %d = %+v", i, row.target)
		}
	}
}

func TestTodoOriginSkipsHiddenAsideHistory(t *testing.T) {
	app, sess, _ := setupConversationStateFixture(t)
	rootTodo := `{"type":"assistant","uuid":"root-todo","timestamp":"2026-07-04T10:05:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"root-todo-call","name":"TodoWrite","input":{"todos":[{"content":"shared todo","status":"pending"}]}}]}}
`
	f, err := os.OpenFile(sess.FilePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(rootTodo)
	f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}

	asidePath := filepath.Join(filepath.Dir(sess.FilePath), sess.ID, "subagents", "agent-aside_question-abcdef1234567890.jsonl")
	asideTranscript := `{"type":"assistant","uuid":"hidden-todo","timestamp":"2026-07-04T11:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"hidden-todo-call","name":"TodoWrite","input":{"todos":[{"content":"shared todo","status":"in_progress"}]}}]}}
{"type":"assistant","uuid":"hidden-context","timestamp":"2026-07-04T11:00:01Z","message":{"role":"assistant","content":"inherited parent history"}}
{"type":"user","uuid":"aside-question","timestamp":"2026-07-04T11:00:02Z","message":{"role":"user","content":"actual side question"}}
{"type":"assistant","uuid":"aside-answer","timestamp":"2026-07-04T11:00:03Z","message":{"role":"assistant","content":"actual side answer"}}
`
	if err := os.WriteFile(asidePath, []byte(asideTranscript), 0o644); err != nil {
		t.Fatal(err)
	}

	flow, err := session.BuildSessionFlow(&sess)
	if err != nil {
		t.Fatal(err)
	}
	app.conv.flow = flow
	app.conv.agents = flow.Agents()
	app.initExecutionRail(app.conv.agents)
	origins := app.latestTodoOrigins()
	origin, ok := origins["shared todo"]
	if !ok {
		t.Fatal("visible todo origin was not selected")
	}
	if origin.Transcript != sess.FilePath || origin.MessageUUID != "root-todo" {
		t.Fatalf("todo origin = transcript %q uuid %q, want visible root origin", origin.Transcript, origin.MessageUUID)
	}
}

func TestHiddenAsideOriginIsDisabledForAllResourceKinds(t *testing.T) {
	app, sess, _ := setupConversationStateFixture(t)
	asidePath := filepath.Join(filepath.Dir(sess.FilePath), sess.ID, "subagents", "agent-aside_question-fedcba0987654321.jsonl")
	asideTranscript := `{"type":"assistant","uuid":"hidden-plan","timestamp":"2026-07-04T11:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"hidden-plan-call","name":"ExitPlanMode","input":{"plan":"hidden inherited plan","planFilePath":"/repo/hidden.md"}}]}}
{"type":"assistant","uuid":"hidden-task","timestamp":"2026-07-04T11:00:01Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"hidden-task-call","name":"TaskUpdate","input":{"taskId":"1","status":"completed"}}]}}
{"type":"user","uuid":"aside-question","timestamp":"2026-07-04T11:00:02Z","message":{"role":"user","content":"actual question"}}
{"type":"assistant","uuid":"aside-answer","timestamp":"2026-07-04T11:00:03Z","message":{"role":"assistant","content":"actual answer"}}
`
	if err := os.WriteFile(asidePath, []byte(asideTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	flow, err := session.BuildSessionFlow(&sess)
	if err != nil {
		t.Fatal(err)
	}
	app.conv.flow = flow
	app.conv.agents = flow.Agents()
	app.initExecutionRail(app.conv.agents)

	for _, artifact := range flow.Artifacts(flow.RootID, session.ArtifactPlan, session.ScopeSession) {
		if artifact.Origin.Transcript != asidePath {
			continue
		}
		target := app.originTarget(metaTargetPlan, artifact.Origin)
		if target.messageUUID != "" || target.entryIndex >= 0 {
			t.Fatalf("hidden plan remained jumpable: %+v", target)
		}
	}
	if _, ok := app.latestPlanArtifact("/repo/hidden.md"); ok {
		t.Fatal("hidden aside plan remained available for drill")
	}
	if _, ok := app.taskOriginByID()["1"]; ok {
		t.Fatal("hidden aside task replaced visible task origin")
	}
	for _, entry := range app.metaSummaryEntries() {
		if strings.Contains(stripANSI(entry.block.Text), "hidden") && entry.target.kind.jumpable() {
			t.Fatalf("hidden decision remained jumpable: %+v", entry.target)
		}
	}
}

func TestMemoryHistoryLine(t *testing.T) {
	first := time.Date(2026, 4, 18, 14, 22, 0, 0, time.UTC)
	last := time.Date(2026, 7, 12, 9, 3, 0, 0, time.UTC)

	tests := []struct {
		name string
		hist session.TouchHistory
		want string
	}{
		{"none", session.TouchHistory{}, ""},
		{"single", session.TouchHistory{First: first, Last: first, Count: 1}, "04-18 14:22"},
		{"window", session.TouchHistory{First: first, Last: last, Count: 3}, "04-18 14:22 → 07-12 09:03 (×3)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := memoryHistoryLine(tt.hist); got != tt.want {
				t.Errorf("memoryHistoryLine = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMemoryListRowShowsNameTypeAndHistory(t *testing.T) {
	note := session.MemoryNote{
		Name:        "kiro-tool-name-and-token-pitfalls",
		FileName:    "kiro-tool-name-and-token-pitfalls.md",
		Type:        "feedback",
		Description: "64-char tool-name limit",
	}
	hist := session.TouchHistory{
		First: time.Date(2026, 4, 18, 14, 22, 0, 0, time.UTC),
		Last:  time.Date(2026, 7, 12, 9, 3, 0, 0, time.UTC),
		Count: 2,
	}
	row := stripANSI(memoryListRow(note, hist))
	for _, want := range []string{"kiro-tool-name-and-token-pitfalls", "[feedback]", "64-char tool-name limit", "04-18 14:22 → 07-12 09:03"} {
		if !strings.Contains(row, want) {
			t.Errorf("row missing %q: %q", want, row)
		}
	}
}

func TestMemoryListRowIndexHasNoTypeTag(t *testing.T) {
	note := session.MemoryNote{Name: "MEMORY", FileName: "MEMORY.md", IsIndex: true}
	row := stripANSI(memoryListRow(note, session.TouchHistory{}))
	if !strings.Contains(row, "MEMORY.md") {
		t.Errorf("index row should show MEMORY.md: %q", row)
	}
	if strings.Contains(row, "[") {
		t.Errorf("index row should not carry a type tag: %q", row)
	}
}

func TestMemoryDrillFallbackWhenFileMissing(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.currentSess.HasMemory = true
	app.conv.sess = app.currentSess
	app.conv.contextItems = buildConvContextItems(app.conv.sess, app.conv.merged, nil)
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, nil, nil, nil)
	app.rebuildConversationList(0)
	for i, item := range app.conv.contextItems {
		if item.sessionMeta == "memory" {
			app.selectConvContext(i)
			break
		}
	}
	// Drilling into a note that does not exist on disk must fall back to the
	// file list rather than leaving a dangling drill state.
	app.enterMemoryDrill("does-not-exist.md")
	if app.conv.inspector.MetaDrill != "" {
		t.Fatalf("drill into missing file should reset, got %q", app.conv.inspector.MetaDrill)
	}
}

func TestExitMemoryDrillNoop(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	if app.exitMemoryDrill() {
		t.Fatal("exitMemoryDrill should be a no-op when not drilled")
	}
	// Set drill directly and confirm exit clears it and reports handled.
	app.conv.inspector.MetaDrill = "kiro.md"
	if !app.exitMemoryDrill() {
		t.Fatal("exitMemoryDrill should report handled while drilled")
	}
	if app.conv.inspector.MetaDrill != "" {
		t.Fatalf("exitMemoryDrill did not clear drill state: %q", app.conv.inspector.MetaDrill)
	}
}

func TestMergedByUUID(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	// testEntries includes a user turn; find any real UUID to look up.
	if len(app.conv.merged) == 0 {
		t.Skip("no merged turns")
	}
	want := app.conv.merged[0].entry.UUID
	if want == "" {
		t.Skip("first turn has no UUID")
	}
	m, ok := app.mergedByUUID(want)
	if !ok || m.entry.UUID != want {
		t.Fatalf("mergedByUUID(%q) = %q,%v", want, m.entry.UUID, ok)
	}
	if _, ok := app.mergedByUUID("no-such-uuid"); ok {
		t.Fatal("mergedByUUID should miss on unknown UUID")
	}
}

func TestCurrentMetaTargetRespectsCursor(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.inspector.MetaTargets = []metaEntryTarget{
		{blockIdx: -1},
		{kind: metaTargetMemoryFile, fileName: "a.md"},
		{kind: metaTargetDecision, messageUUID: "u9", blockIdx: 2},
	}
	app.conv.split.Folds.Entry = session.Entry{Content: []session.ContentBlock{
		{Type: "text", Text: "h"}, {Type: "text", Text: "a"}, {Type: "text", Text: "b"},
	}}

	app.conv.split.Folds.BlockCursor = 1
	if tgt, ok := app.currentMetaTarget(); !ok || tgt.fileName != "a.md" {
		t.Fatalf("cursor 1 target = %+v ok=%v", tgt, ok)
	}
	app.conv.split.Folds.BlockCursor = 2
	if tgt, ok := app.currentMetaTarget(); !ok || tgt.messageUUID != "u9" {
		t.Fatalf("cursor 2 target = %+v ok=%v", tgt, ok)
	}
	// Out of range → no target.
	app.conv.split.Folds.BlockCursor = 9
	if _, ok := app.currentMetaTarget(); ok {
		t.Fatal("out-of-range cursor should yield no target")
	}
}

func TestMemorySearchMatchesByLine(t *testing.T) {
	note := session.MemoryNote{FileName: "x.md", Body: "alpha beta\nGAMMA the query here\nunrelated\nquery again"}
	matches := memorySearchMatches(note, "query")
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2: %+v", len(matches), matches)
	}
	if matches[0].lineNo != 2 || matches[1].lineNo != 4 {
		t.Fatalf("line numbers = %d, %d, want 2, 4", matches[0].lineNo, matches[1].lineNo)
	}
	if memorySearchMatches(note, "QUERY") == nil {
		t.Fatal("search should be case-insensitive")
	}
	if memorySearchMatches(note, "") != nil {
		t.Fatal("empty query should yield no matches")
	}
}

func TestHighlightMemorySnippetHighlightsAndWindows(t *testing.T) {
	// Short line fits within budget: full content shown, no ellipsis.
	got := highlightMemorySnippet("the quick brown fox jumps", "BROWN", 40)
	bare := stripANSI(got)
	if !strings.Contains(bare, "brown") {
		t.Fatalf("snippet missing match: %q", bare)
	}
	if strings.Contains(bare, "…") {
		t.Fatalf("unexpected ellipsis for short line: %q", bare)
	}

	// Long line with match in the middle: context windowed with ellipses on
	// both sides, match preserved.
	long := "prefix " + strings.Repeat("x", 60) + " NEEDLE " + strings.Repeat("y", 60) + " suffix"
	got = highlightMemorySnippet(long, "needle", 30)
	bare = stripANSI(got)
	if !strings.Contains(strings.ToLower(bare), "needle") {
		t.Fatalf("windowed snippet missing match: %q", bare)
	}
	if !strings.Contains(bare, "…") {
		t.Fatalf("expected ellipsis for windowed context, got %q", bare)
	}

	// Match near the start should not prepend an ellipsis.
	got = highlightMemorySnippet("query at start and a long tail of text", "query", 20)
	bare = stripANSI(got)
	if strings.HasPrefix(bare, "…") {
		t.Fatalf("leading ellipsis unexpected for start match: %q", bare)
	}

	// No match falls back to a plain truncated render.
	got = highlightMemorySnippet("nothing here", "zzz", 40)
	if strings.TrimSpace(stripANSI(got)) != "nothing here" {
		t.Fatalf("no-match fallback = %q", stripANSI(got))
	}
}

func TestHighlightMemorySnippetBoundsBudget(t *testing.T) {
	long := strings.Repeat("abcdefghij", 20) // 200 chars, no match
	got := highlightMemorySnippet(long, "zzz", 30)
	bare := stripANSI(got)
	if w := lipgloss.Width(bare); w > 30 {
		t.Fatalf("truncated snippet width = %d, want <= 30: %q", w, bare)
	}
}

func TestHighlightMemorySnippetWideRuneBoundsBudget(t *testing.T) {
	// CJK runes are 2 display cells each; windowing must measure display width,
	// not rune count, so the styled snippet never exceeds the budget (which
	// would word-wrap mid-ANSI and break the highlight).
	wide := strings.Repeat("키", 40) // 40 runes = 80 cells, match in the middle
	got := highlightMemorySnippet(wide, "키", 20)
	bare := stripANSI(got)
	if w := lipgloss.Width(bare); w > 20 {
		t.Fatalf("wide-rune snippet width = %d, want <= 20: %q", w, bare)
	}
	if !strings.Contains(bare, "…") {
		t.Fatalf("expected ellipsis for windowed wide rune: %q", bare)
	}
}

func TestMemorySearchGroupHeaderCount(t *testing.T) {
	note := session.MemoryNote{Name: "k7s", FileName: "k7s.md", Type: "feedback"}
	if h := stripANSI(memorySearchGroupHeader(note, 3, 3)); !strings.Contains(h, "3 match(es)") {
		t.Fatalf("uncapped header = %q", h)
	}
	if h := stripANSI(memorySearchGroupHeader(note, 10, 25)); !strings.Contains(h, "10 of 25+ matches") {
		t.Fatalf("capped header = %q", h)
	}
}

func TestMetaMemorySearchEntriesBuildsRowsAndTargets(t *testing.T) {
	root := t.TempDir()
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.sess.ProjectPath = root
	writeTestMemoryNotes(t, root, map[string]string{
		"alpha.md": "---\nname: alpha\ndescription: alpha note\nmetadata:\n  type: project\n---\nalpha first occurrence\nanother line\nalpha again here\n",
		"beta.md":  "---\nname: beta\nmetadata:\n  type: feedback\n---\nno match in this one\n",
	})

	app.conv.inspector.MemorySearch = "alpha"
	entries := app.metaMemoryEntries()
	if len(entries) < 2 {
		t.Fatalf("entries = %d, want at least header + rows", len(entries))
	}
	// Leading summary header.
	if h := stripANSI(entries[0].block.Text); !strings.Contains(h, "Memory search") || !strings.Contains(h, "alpha") {
		t.Fatalf("header = %q", h)
	}
	// Group header for alpha.md + at least two match rows.
	if h := stripANSI(entries[1].block.Text); !strings.Contains(h, "alpha") {
		t.Fatalf("group header = %q", h)
	}
	matchTargets := 0
	for _, e := range entries[2:] {
		if e.target.kind == metaTargetMemoryFile && e.target.fileName == "alpha.md" {
			matchTargets++
		}
	}
	if matchTargets < 2 {
		t.Fatalf("match targets = %d, want >= 2", matchTargets)
	}
}

func TestCommitMemorySearchShowsMatchesAndClearReturns(t *testing.T) {
	root := t.TempDir()
	app := setupConvApp(t, testEntries(), 160, 50)
	app.currentSess.HasMemory = true
	app.currentSess.ProjectPath = root
	app.conv.sess = app.currentSess
	app.conv.contextItems = buildConvContextItems(app.conv.sess, app.conv.merged, nil)
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, nil, nil, nil)
	app.rebuildConversationList(0)
	for i, item := range app.conv.contextItems {
		if item.sessionMeta == "memory" {
			app.selectConvContext(i)
			break
		}
	}
	writeTestMemoryNotes(t, root, map[string]string{
		"gamma.md": "---\nname: gamma\nmetadata:\n  type: project\n---\nthe needle is here\n",
	})

	if !app.memoryPaneActive() {
		t.Fatal("memory pane should be the active inspector row")
	}
	app.startMemorySearch()
	if !app.conv.memorySearching || app.conv.inspector.MetaDrill != "" {
		t.Fatalf("startMemorySearch did not activate input / clear drill: searching=%v drill=%q", app.conv.memorySearching, app.conv.inspector.MetaDrill)
	}
	app.conv.memorySearchTI.SetValue("needle")
	app.commitMemorySearch()
	if app.conv.memorySearching {
		t.Fatal("commit should clear the typing flag")
	}
	if app.conv.inspector.MemorySearch != "needle" {
		t.Fatalf("MemorySearch = %q, want needle", app.conv.inspector.MemorySearch)
	}
	if !strings.Contains(stripANSI(app.conv.inspector.Rendered), "needle") {
		// Rendered may be empty if the fold preview wasn't refreshed in the test
		// harness; fall back to checking the built entries directly.
		entries := app.metaMemoryEntries()
		if !strings.Contains(stripANSI(entries[0].block.Text), "Memory search") {
			t.Fatalf("commit did not produce a search render; entries[0]=%q", entries[0].block.Text)
		}
	}

	if !app.clearMemorySearch() {
		t.Fatal("clearMemorySearch should report handled when a query is set")
	}
	if app.conv.inspector.MemorySearch != "" {
		t.Fatalf("MemorySearch = %q after clear, want empty", app.conv.inspector.MemorySearch)
	}
	if app.clearMemorySearch() {
		t.Fatal("clearMemorySearch should be a no-op when already clear")
	}
}

func TestMemorySearchEnterDrillsIntoMatchFile(t *testing.T) {
	root := t.TempDir()
	app := setupConvApp(t, testEntries(), 160, 50)
	app.currentSess.HasMemory = true
	app.currentSess.ProjectPath = root
	app.conv.sess = app.currentSess
	app.conv.contextItems = buildConvContextItems(app.conv.sess, app.conv.merged, nil)
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, nil, nil, nil)
	app.rebuildConversationList(0)
	for i, item := range app.conv.contextItems {
		if item.sessionMeta == "memory" {
			app.selectConvContext(i)
			break
		}
	}
	writeTestMemoryNotes(t, root, map[string]string{
		"delta.md": "---\nname: delta\nmetadata:\n  type: project\n---\nthe needle is here\nmore text\n",
	})

	app.startMemorySearch()
	app.conv.memorySearchTI.SetValue("needle")
	app.commitMemorySearch()
	if app.conv.inspector.MemorySearch != "needle" {
		t.Fatalf("MemorySearch = %q, want needle", app.conv.inspector.MemorySearch)
	}

	// Find the match row target and press Enter on it.
	targets := app.conv.inspector.MetaTargets
	matchIdx := -1
	for i, tg := range targets {
		if tg.kind == metaTargetMemoryFile && tg.fileName == "delta.md" {
			matchIdx = i
		}
	}
	if matchIdx < 0 {
		t.Fatal("no delta.md match target found after search")
	}
	if app.conv.split.Folds == nil {
		t.Fatal("Folds not set after commit")
	}
	app.conv.split.Folds.BlockCursor = matchIdx
	handled, _, _ := app.handleMetaEntryEnter()
	if !handled {
		t.Fatal("handleMetaEntryEnter should handle the match row")
	}
	if app.conv.inspector.MetaDrill != "delta.md" {
		t.Fatalf("MetaDrill = %q, want delta.md (Enter should drill into the match file)", app.conv.inspector.MetaDrill)
	}
	// MemorySearch must remain set so Esc returns to the search results.
	if app.conv.inspector.MemorySearch != "needle" {
		t.Fatalf("MemorySearch = %q, want needle preserved while drilled", app.conv.inspector.MemorySearch)
	}
	// The rendered entries must now be the drill (note body), not the search list.
	entries := app.metaMemoryEntries()
	var body strings.Builder
	for _, e := range entries {
		body.WriteString(stripANSI(e.block.Text))
		body.WriteByte('\n')
	}
	if !strings.Contains(body.String(), "the needle is here") {
		t.Fatalf("drill view should render the note body; got:\n%s", body.String())
	}
	if strings.Contains(body.String(), "Memory search") {
		t.Fatalf("drill should take precedence over search; got search header:\n%s", body.String())
	}

	// Esc unwinds drill → search results (MemorySearch still set).
	if !app.exitMemoryDrill() {
		t.Fatal("exitMemoryDrill should report handled while drilled")
	}
	if app.conv.inspector.MemorySearch != "needle" {
		t.Fatalf("Esc from drill should preserve search, got MemorySearch=%q", app.conv.inspector.MemorySearch)
	}
	entries = app.metaMemoryEntries()
	header := stripANSI(entries[0].block.Text)
	if !strings.Contains(header, "Memory search") {
		t.Fatalf("after Esc, search results should render; got:\n%s", header)
	}
}
