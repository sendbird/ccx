package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

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
