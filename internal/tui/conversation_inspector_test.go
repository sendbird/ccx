package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

func setupInspectorFlowApp(t *testing.T) (*App, string, string) {
	t.Helper()
	root := t.TempDir()
	sessID := "inspector-flow"
	sessPath := filepath.Join(root, sessID+".jsonl")
	parent := `{"type":"user","uuid":"u1","timestamp":"2026-07-01T10:00:00Z","message":{"role":"user","content":"inspect this flow"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-07-01T10:00:10Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"edit-parent","name":"Edit","input":{"file_path":"/repo/parent.go","old_string":"old","new_string":"new"}},{"type":"text","text":"https://github.com/acme/repo/pull/42"}]}}
{"type":"user","uuid":"u2","timestamp":"2026-07-01T10:00:15Z","message":{"role":"user","content":"delegate image inspection"}}
{"type":"assistant","uuid":"a2","timestamp":"2026-07-01T10:00:20Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"spawn-agent","name":"Agent","input":{"subagent_type":"Explore","prompt":"inspect image"}}]}}
{"type":"user","uuid":"r1","timestamp":"2026-07-01T10:00:21Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"spawn-agent","content":"done"}]},"toolUseResult":{"agentId":"aaaaaaaaaaaa1111","agentType":"Explore"}}
`
	if err := os.WriteFile(sessPath, []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}

	agentDir := filepath.Join(root, sessID, "subagents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(agentDir, "agent-aaaaaaaaaaaa1111.jsonl")
	agent := `{"type":"user","uuid":"ag-u1","timestamp":"2026-07-01T10:00:30Z","imagePasteIds":[7],"message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"eA=="}}]}}
{"type":"assistant","uuid":"ag-a1","timestamp":"2026-07-01T10:00:40Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"edit-agent","name":"Edit","input":{"file_path":"/repo/agent.go","old_string":"a","new_string":"b"}}]}}
`
	if err := os.WriteFile(agentPath, []byte(agent), 0o600); err != nil {
		t.Fatal(err)
	}

	sess := session.Session{ID: sessID, ShortID: "inspect", FilePath: sessPath, ProjectPath: root, ProjectName: "inspect"}
	app := NewApp([]session.Session{sess}, Config{})
	model, _ := app.Update(tea.WindowSizeMsg{Width: 150, Height: 45})
	app = model.(*App)
	app.openConversation(sess)
	return app, sessPath, agentPath
}

func selectInspectorItem(t *testing.T, app *App, match func(convItem) bool) convItem {
	t.Helper()
	for i, raw := range app.convList.Items() {
		item, ok := raw.(convItem)
		if ok && match(item) {
			app.convList.Select(i)
			app.conv.split.CacheKey = ""
			app.updateConvPreview()
			return item
		}
	}
	t.Fatal("matching inspector item not found")
	return convItem{}
}

func TestInspectorTabsHideEmptyFacets(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	item := selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a2"
	})
	nodeID := convItemFlowNodeID(item, app.conv.flow)
	tabs := availableInspectorTabs(item, app.conv.flow, nodeID, session.ScopeNode)
	if !containsInspectorTab(tabs, inspectorConversation) || !containsInspectorTab(tabs, inspectorStats) {
		t.Fatalf("spawn turn tabs = %v, want Conversation and child Stats", tabs)
	}
	if containsInspectorTab(tabs, inspectorChanges) || containsInspectorTab(tabs, inspectorRefs) || containsInspectorTab(tabs, inspectorImages) {
		t.Fatalf("empty artifact facets should be hidden at node scope: %v", tabs)
	}

	tabs = availableInspectorTabs(item, app.conv.flow, nodeID, session.ScopeSession)
	if !containsInspectorTab(tabs, inspectorChanges) || !containsInspectorTab(tabs, inspectorRefs) || !containsInspectorTab(tabs, inspectorImages) {
		t.Fatalf("session scope tabs = %v, want changes/refs/images", tabs)
	}
}

func TestInspectorScopeDoesNotFallbackToParent(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	item := selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a2"
	})
	nodeID := convItemFlowNodeID(item, app.conv.flow)

	app.conv.inspector.Scope = session.ScopeNode
	if got := app.conv.flow.Facets(nodeID, session.ScopeNode).Counts[session.ArtifactChange]; got != 0 {
		t.Fatalf("node changes = %d, want 0", got)
	}
	if text := app.renderInspectorChanges(nodeID); !strings.Contains(text, "No changes in this node scope") {
		t.Fatalf("empty node facet silently fell back: %q", text)
	}

	app.conv.inspector.Scope = session.ScopeSession
	if got := app.conv.flow.Facets(nodeID, session.ScopeSession).Counts[session.ArtifactChange]; got < 2 {
		t.Fatalf("session changes = %d, want parent + agent changes", got)
	}
}

func TestInspectorAgentImageUsesOwningTranscript(t *testing.T) {
	app, parentPath, agentPath := setupInspectorFlowApp(t)
	item := selectInspectorItem(t, app, func(item convItem) bool { return item.kind == convAgent })
	nodeID := convItemFlowNodeID(item, app.conv.flow)
	app.conv.inspector.Scope = session.ScopeNode
	text := app.renderInspectorImages(nodeID)
	if !strings.Contains(text, agentPath) {
		t.Fatalf("image inspector missing agent transcript %q: %q", agentPath, text)
	}
	if strings.Contains(text, "transcript: "+parentPath) {
		t.Fatalf("image inspector incorrectly resolved against parent transcript: %q", text)
	}
}

func TestInspectorEnterMessageZoomsWithoutChangingView(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app.conv.split.Focus = false

	app = pressKey(app, "enter")
	if app.state != viewConversation {
		t.Fatalf("enter changed view to %v, want viewConversation", app.state)
	}
	if !app.conv.inspector.Zoom || !app.conv.split.PreviewOnly {
		t.Fatalf("enter did not zoom inspector: %+v", app.conv.inspector)
	}
	if app.conv.inspector.Tab != inspectorConversation || app.conv.inspector.Scope != session.ScopeNode {
		t.Fatalf("enter inspector state = tab %v scope %v", app.conv.inspector.Tab, app.conv.inspector.Scope)
	}
	plain := stripANSI(app.renderConvSplit())
	if !strings.Contains(plain, "INSPECTOR / ZOOM") || strings.Contains(plain, "Session Flow ·") {
		t.Fatalf("zoom rendering leaked flow list or missed inspector header: %q", plain)
	}
}

func TestInspectorZoomRestoresFocusCursorAndScroll(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app.conv.inspector.Tab = inspectorConversation
	app.conv.inspector.Scope = session.ScopeNode
	app.conv.split.Focus = false
	app.updateConvPreview()
	if app.conv.split.Folds == nil || len(app.conv.split.Folds.Entry.Content) < 2 {
		t.Fatal("test requires structured conversation preview")
	}
	app.conv.split.Folds.BlockCursor = 1
	app.conv.split.Preview.YOffset = 1

	app.setInspectorZoom(true)
	app.setInspectorZoom(false)
	if app.conv.split.Focus {
		t.Fatal("zoom exit did not restore list focus")
	}
	if app.conv.split.PreviewOnly || app.conv.inspector.Zoom {
		t.Fatal("zoom flags remained set")
	}
	if got := app.conv.split.Folds.BlockCursor; got != 1 {
		t.Fatalf("block cursor = %d, want 1", got)
	}
}

func TestInspectorScopeAndTabKeys(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool { return item.kind == convAgent })
	app.conv.split.Focus = true
	app.conv.inspector.Scope = session.ScopeNode
	app.conv.inspector.Tab = inspectorOverview
	app.updateConvPreview()

	app = pressKey(app, "]")
	if app.conv.inspector.Tab == inspectorOverview {
		t.Fatal("] did not cycle to a non-empty facet")
	}
	app = pressKey(app, "s")
	if app.conv.inspector.Scope != session.ScopeSubtree {
		t.Fatalf("scope = %v, want subtree", app.conv.inspector.Scope)
	}
	app = pressKey(app, "s")
	if app.conv.inspector.Scope != session.ScopeSession {
		t.Fatalf("scope = %v, want session", app.conv.inspector.Scope)
	}
}
