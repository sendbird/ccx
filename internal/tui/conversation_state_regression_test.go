package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

func setupConversationStateFixture(t *testing.T) (*App, session.Session, string) {
	t.Helper()
	root := t.TempDir()
	sessID := "state-regression"
	sessPath := filepath.Join(root, sessID+".jsonl")
	rootTranscript := `{"type":"assistant","uuid":"task-create","timestamp":"2026-07-04T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"create-task","name":"TaskCreate","input":{"subject":"Track refresh","status":"pending"}}]}}
{"type":"user","uuid":"task-result","timestamp":"2026-07-04T10:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"create-task","content":"Task #1 created successfully"}]}}
{"type":"assistant","uuid":"spawn-parent","timestamp":"2026-07-04T10:00:02Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"parent-call","name":"Agent","input":{"prompt":"parent"}}]}}
{"type":"user","uuid":"parent-result","timestamp":"2026-07-04T10:00:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"parent-call","content":"done"}]},"toolUseResult":{"agentId":"parent1111111111","agentType":"general-purpose"}}
{"type":"assistant","uuid":"workflow-call","timestamp":"2026-07-04T10:00:04Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"workflow-tool","name":"Workflow","input":{"script":"test"}}]}}
{"type":"user","uuid":"workflow-result","timestamp":"2026-07-04T10:00:05Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"workflow-tool","content":"launched"}]},"toolUseResult":{"runId":"run-summary"}}
`
	if err := os.WriteFile(sessPath, []byte(rootTranscript), 0o644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(root, sessID, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	parentPath := filepath.Join(subDir, "agent-parent1111111111.jsonl")
	parentTranscript := `{"type":"user","uuid":"parent-u","agentId":"parent1111111111","timestamp":"2026-07-04T10:01:00Z","message":{"role":"user","content":"coordinate"}}
{"type":"assistant","uuid":"parent-spawns","agentId":"parent1111111111","timestamp":"2026-07-04T10:01:01Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"child-call-a","name":"Agent","input":{"prompt":"first"}},{"type":"tool_use","id":"child-call-b","name":"Agent","input":{"prompt":"second"}}]}}
{"type":"user","uuid":"child-result-a","agentId":"parent1111111111","timestamp":"2026-07-04T10:01:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"child-call-a","content":"done"}]},"toolUseResult":{"agentId":"child2222222222","agentType":"general-purpose"}}
{"type":"user","uuid":"child-result-b","agentId":"parent1111111111","timestamp":"2026-07-04T10:01:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"child-call-b","content":"done"}]},"toolUseResult":{"agentId":"child3333333333","agentType":"general-purpose"}}
`
	if err := os.WriteFile(parentPath, []byte(parentTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, child := range []struct{ id, prompt string }{{"child2222222222", "first child"}, {"child3333333333", "second child"}} {
		content := `{"type":"user","uuid":"` + child.id + `-u","agentId":"` + child.id + `","timestamp":"2026-07-04T10:02:00Z","message":{"role":"user","content":"` + child.prompt + `"}}
`
		if err := os.WriteFile(filepath.Join(subDir, "agent-"+child.id+".jsonl"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	workflowDir := filepath.Join(root, sessID, "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := `{"runId":"run-summary","workflowName":"summary","status":"completed","phases":[{"title":"Review"}],"workflowProgress":[{"type":"workflow_agent","label":"missing transcript","phaseIndex":1,"phaseTitle":"Review","agentId":"ghost4444444444","state":"done","tokens":44,"toolCalls":2}]}`
	if err := os.WriteFile(filepath.Join(workflowDir, "run-summary.json"), []byte(summary), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := session.Session{ID: sessID, ShortID: "state", FilePath: sessPath, ProjectPath: root, ProjectName: "state"}
	app := NewApp([]session.Session{sess}, Config{})
	model, _ := app.Update(tea.WindowSizeMsg{Width: 150, Height: 45})
	app = model.(*App)
	app.openConversation(sess)
	return app, sess, parentPath
}

func TestAgentRefreshPreservesSessionGraphAndUpdatesTasks(t *testing.T) {
	app, sess, _ := setupConversationStateFixture(t)
	var parent session.Subagent
	for _, agent := range app.conv.agents {
		if agent.ID == "parent1111111111" {
			parent = agent
			break
		}
	}
	if parent.ID == "" {
		t.Fatal("parent agent missing")
	}
	app.pushNavFrame()
	model, _ := app.openAgentConversation(parent)
	app = model.(*App)
	if got := app.conv.toolUseToAgent["child-call-b"]; got != "child3333333333" {
		t.Fatalf("agent transcript spawn map = %q", got)
	}

	update := `{"type":"assistant","uuid":"task-update","timestamp":"2026-07-04T10:03:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"update-task","name":"TaskUpdate","input":{"taskId":"1","status":"completed"}}]}}
`
	f, err := os.OpenFile(sess.FilePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(update); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	app.refreshConversation()
	if _, ok := app.conv.flow.Node(session.FlowAgentNodeID("child3333333333")); !ok {
		t.Fatal("agent refresh discarded session-wide child graph")
	}
	if _, ok := app.conv.flow.Node(session.FlowWorkflowNodeID("run-summary")); !ok {
		t.Fatal("agent refresh discarded root workflow")
	}
	if len(app.conv.sess.Tasks) != 1 || app.conv.sess.Tasks[0].Status != "completed" {
		t.Fatalf("refreshed tasks = %#v", app.conv.sess.Tasks)
	}
	if got := app.conv.toolUseToAgent["child-call-a"]; got != "child2222222222" {
		t.Fatalf("refresh replaced current transcript spawn map: %q", got)
	}
}

func TestFocusedAgentBlockUsesExactToolUseID(t *testing.T) {
	app, _, _ := setupConversationStateFixture(t)
	var parent session.Subagent
	for _, agent := range app.conv.agents {
		if agent.ID == "parent1111111111" {
			parent = agent
		}
	}
	model, _ := app.openAgentConversation(parent)
	app = model.(*App)
	first, ok := app.findAgentForToolUse("child-call-a")
	if !ok || first.ID != "child2222222222" {
		t.Fatalf("first child = %#v, %t", first, ok)
	}
	second, ok := app.findAgentForToolUse("child-call-b")
	if !ok || second.ID != "child3333333333" {
		t.Fatalf("second child = %#v, %t", second, ok)
	}
}

func TestSummaryOnlyWorkflowAgentEnterOpensInspector(t *testing.T) {
	app, _, _ := setupConversationStateFixture(t)
	for i := range app.conv.items {
		if app.conv.items[i].kind == convWorkflow && app.conv.items[i].workflow.RunID == "run-summary" {
			app.conv.items[i].folded = false
		}
	}
	app.rebuildConversationList(0)
	selectConvItemBy(t, app, func(item convItem) bool {
		return item.kind == convAgent && item.summaryOnly && item.agent.ID == "ghost4444444444"
	})
	app = pressKey(app, "enter")
	if !app.conv.inspector.Zoom || app.conv.inspector.Tab != inspectorOverview {
		t.Fatalf("summary-only enter did not open overview: %+v", app.conv.inspector)
	}
	if app.copiedMsg == "No agent messages" {
		t.Fatal("summary-only enter attempted transcript drill-down")
	}
}
