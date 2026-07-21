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

// setupChangeDiffApp builds a session that edits the same file twice, so change
// decisions and the Changes facet have multiple occurrences to render.
func setupChangeDiffApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	sessID := "change-diff"
	sessPath := filepath.Join(root, sessID+".jsonl")
	transcript := `{"type":"user","uuid":"u1","timestamp":"2026-07-01T10:00:00Z","message":{"role":"user","content":"edit the file"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-07-01T10:00:10Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"edit-1","name":"Edit","input":{"file_path":"/repo/svc.go","old_string":"alpha","new_string":"beta"}}]}}
{"type":"user","uuid":"u2","timestamp":"2026-07-01T10:00:20Z","message":{"role":"user","content":"one more change"}}
{"type":"assistant","uuid":"a2","timestamp":"2026-07-01T10:00:30Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"edit-2","name":"Edit","input":{"file_path":"/repo/svc.go","old_string":"gamma","new_string":"delta"}}]}}
`
	if err := os.WriteFile(sessPath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := session.Session{ID: sessID, ShortID: "chg", FilePath: sessPath, ProjectPath: root, ProjectName: "chg"}
	app := NewApp([]session.Session{sess}, Config{})
	model, _ := app.Update(tea.WindowSizeMsg{Width: 150, Height: 45})
	app = model.(*App)
	app.openConversation(sess)
	return app
}

func TestChangeDecisionInspectorShowsAllChangesWithDiff(t *testing.T) {
	app := setupChangeDiffApp(t)
	var decision session.Artifact
	found := false
	for _, d := range app.conv.flow.Decisions(session.ScopeSession) {
		data, _ := d.Data.(session.DecisionData)
		if data.Kind == session.DecisionFirstChange && d.Key == "first-change:/repo/svc.go" {
			decision = d
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no first-change decision for /repo/svc.go")
	}
	out := stripANSI(app.renderDecisionInspector(decision))
	// Both edits to the same file must appear, each with its diff hunk.
	for _, want := range []string{"1. Edit /repo/svc.go", "2. Edit /repo/svc.go", "alpha", "beta", "gamma", "delta"} {
		if !strings.Contains(out, want) {
			t.Fatalf("change decision inspector missing %q:\n%s", want, out)
		}
	}
}

func TestChangesFacetRendersInlineDiff(t *testing.T) {
	app := setupChangeDiffApp(t)
	app.conv.inspector.Scope = session.ScopeSession
	out := stripANSI(app.renderInspectorChanges(app.conv.flow.RootID))
	for _, want := range []string{"Edit /repo/svc.go", "beta", "delta"} {
		if !strings.Contains(out, want) {
			t.Fatalf("changes facet missing %q:\n%s", want, out)
		}
	}
}

func selectInspectorItem(t *testing.T, app *App, match func(convItem) bool) convItem {
	t.Helper()
	for i, raw := range app.convList.Items() {
		item, ok := raw.(convItem)
		if ok && match(item) {
			app.selectConvBody(i)
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
	// The Explore agent has no descendants, so scope is [Node] only — session-wide
	// aggregation lives in the pinned Session Flow row, not on an individual node.
	// Pressing s must be a no-op rather than cycling to Subtree or Session.
	if scopes := app.inspectorScopesFor(app.conv.inspector.NodeID); len(scopes) != 1 || scopes[0] != session.ScopeNode {
		t.Fatalf("childless agent scopes = %v, want [Node]", scopes)
	}
	app = pressKey(app, "s")
	if app.conv.inspector.Scope != session.ScopeNode {
		t.Fatalf("scope = %v, want node (childless node has no other scope)", app.conv.inspector.Scope)
	}
}

// TestInspectorScopeCycleIncludesSubtreeForParent verifies that a node with
// descendants (a turn that spawned an agent) offers Node → Subtree — but never
// Session, which is a whole-session concern owned by the pinned Session Flow row.
func TestInspectorScopeCycleIncludesSubtreeForParent(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	// Turn a2 spawns the Explore agent, so its flow node has a child.
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a2"
	})
	app.conv.split.Focus = true
	app.conv.inspector.Scope = session.ScopeNode
	app.updateConvPreview()

	scopes := app.inspectorScopesFor(app.conv.inspector.NodeID)
	if len(scopes) != 2 || scopes[0] != session.ScopeNode || scopes[1] != session.ScopeSubtree {
		t.Fatalf("parent node scopes = %v, want [Node Subtree]", scopes)
	}
	app = pressKey(app, "s")
	if app.conv.inspector.Scope != session.ScopeSubtree {
		t.Fatalf("scope = %v, want subtree (parent node)", app.conv.inspector.Scope)
	}
	app = pressKey(app, "s")
	if app.conv.inspector.Scope != session.ScopeNode {
		t.Fatalf("scope = %v, want node (wraps past subtree, no session)", app.conv.inspector.Scope)
	}
}

// TestInspectorScopeNormalizesOnChildlessNode verifies that selecting a childless
// node while a Subtree scope is active collapses the scope back to Node, so the
// selector never highlights an entry it does not display.
func TestInspectorScopeNormalizesOnChildlessNode(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a2"
	})
	app.conv.inspector.Scope = session.ScopeSubtree
	app.updateConvPreview()

	// u1 is a plain user turn with no descendants — Subtree must not survive.
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "u1"
	})
	if app.conv.inspector.Scope == session.ScopeSubtree {
		t.Fatal("Subtree scope survived selection of a childless node")
	}
}

func TestZoomedInspectorRetainsFilterAndCopyModes(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app = pressKey(app, "enter")

	app = pressKey(app, "/")
	if !app.conv.blockFiltering || !app.conv.inspector.Zoom {
		t.Fatalf("zoomed filter state = filtering:%t zoom:%t", app.conv.blockFiltering, app.conv.inspector.Zoom)
	}
	app = pressKey(app, "esc")
	if app.conv.blockFiltering || !app.conv.inspector.Zoom {
		t.Fatalf("filter cancel changed zoom state: filtering:%t zoom:%t", app.conv.blockFiltering, app.conv.inspector.Zoom)
	}

	copyKey := app.keymap.Preview.CopyMode
	app = pressKey(app, copyKey)
	if !app.copyModeActive || !app.conv.inspector.Zoom {
		blockCount := 0
		if app.conv.split.Folds != nil {
			blockCount = len(app.conv.split.Folds.Entry.Content)
		}
		t.Fatalf("zoomed copy state = copy:%t zoom:%t key:%q focus:%t mode:%d blocks:%d", app.copyModeActive, app.conv.inspector.Zoom, copyKey, app.conv.split.Focus, app.conv.rightPaneMode, blockCount)
	}
	app = pressKey(app, "esc")
	if app.copyModeActive || !app.conv.inspector.Zoom {
		t.Fatalf("copy cancel changed zoom state: copy:%t zoom:%t", app.copyModeActive, app.conv.inspector.Zoom)
	}
}

func TestInspectorFacetCopyUsesRenderedProvenance(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	// Turn a1 owns the reference, so its node-scope Refs facet is non-empty.
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app.openInspector(inspectorRefs, session.ScopeNode, true)

	app = pressKey(app, app.keymap.Preview.CopyMode)
	if !app.copyModeActive {
		t.Fatal("copy mode did not start for references facet")
	}
	copiedSource := strings.Join(app.copyLines, "\n")
	if !strings.Contains(copiedSource, "acme/repo#42") || !strings.Contains(copiedSource, "origin:") {
		t.Fatalf("copy source is not the rendered references facet: %q", copiedSource)
	}
	if strings.Contains(copiedSource, "delegate image inspection") {
		t.Fatalf("copy source leaked selected conversation text: %q", copiedSource)
	}
	app = pressKey(app, "esc")
	if app.copyModeActive {
		t.Fatal("copy mode remained active after escape")
	}
	if restored := stripANSI(app.conv.split.Preview.View()); !strings.Contains(restored, "# References & URLs") || !strings.Contains(restored, "acme/repo#42") {
		t.Fatalf("copy exit did not restore references facet: %q", restored)
	}
}

func TestExplicitEmptyFacetClearsWhenSelectionChanges(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a2"
	})
	app.openInspector(inspectorFiles, session.ScopeNode, false)
	if app.conv.inspector.Tab != inspectorFiles {
		t.Fatalf("explicit empty tab = %v, want Files", app.conv.inspector.Tab)
	}

	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "u1"
	})
	if app.conv.inspector.Explicit {
		t.Fatal("explicit facet pin survived node selection change")
	}
	if app.conv.inspector.Tab == inspectorFiles {
		t.Fatal("empty Files facet remained pinned on a different node")
	}
}

// TestInspectorFacetPickerUsesSessionScope verifies the `p` facet picker
// surveys the whole session: it targets the pinned Session Flow (root) row at
// ScopeSession, not the individual turn that was selected when it opened.
func TestInspectorFacetPickerUsesSessionScope(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a2"
	})
	app.conv.inspector.Scope = session.ScopeNode

	app = pressKey(app, "p")
	if !app.inspectorMenu {
		t.Fatal("p did not open the inspector facet picker")
	}
	app = pressKey(app, "u")
	if app.inspectorMenu {
		t.Fatal("facet picker remained open")
	}
	// Picker jumps to the pinned Session Flow row and renders session-wide.
	if !app.conv.contextActive {
		t.Fatal("facet picker did not activate the pinned Session Flow row")
	}
	if app.conv.inspector.NodeID != app.conv.flow.RootID {
		t.Fatalf("facet picker node = %q, want root %q", app.conv.inspector.NodeID, app.conv.flow.RootID)
	}
	if app.conv.inspector.Tab != inspectorRefs || app.conv.inspector.Scope != session.ScopeSession {
		t.Fatalf("facet picker state = tab:%v scope:%v", app.conv.inspector.Tab, app.conv.inspector.Scope)
	}
	if app.urlMenu {
		t.Fatal("facet picker opened the legacy URL menu")
	}

	app = pressKey(app, "p")
	app = pressKey(app, "f")
	if app.conv.inspector.Tab != inspectorFiles || app.conv.inspector.Scope != session.ScopeSession {
		t.Fatalf("files picker state = tab:%v scope:%v", app.conv.inspector.Tab, app.conv.inspector.Scope)
	}
	// Session-wide Files must include both the parent turn's and the agent's
	// files, proving the facet aggregates across the whole session, not one node.
	rendered := app.conv.inspector.Rendered
	if !strings.Contains(rendered, "/repo/parent.go") || !strings.Contains(rendered, "/repo/agent.go") {
		t.Fatalf("files picker did not render session-wide files: %q", rendered)
	}
}

func setupDecisionFlowApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	sessID := "decision-flow"
	sessPath := filepath.Join(root, sessID+".jsonl")
	body := `{"type":"user","uuid":"u1","timestamp":"2026-07-01T10:00:00Z","message":{"role":"user","content":"plan the work"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-07-01T10:00:10Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"task-create","name":"TaskCreate","input":{"id":"T1","subject":"build the feature"}}]}}
{"type":"user","uuid":"u2","timestamp":"2026-07-01T10:00:15Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"task-create","content":"created"}]}}
{"type":"assistant","uuid":"a2","timestamp":"2026-07-01T10:00:20Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-1","name":"ExitPlanMode","input":{"plan":"do the thing","planFilePath":"/repo/plan.md"}}]}}
`
	if err := os.WriteFile(sessPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := session.Session{ID: sessID, ShortID: "decision", FilePath: sessPath, ProjectPath: root, ProjectName: "decision"}
	app := NewApp([]session.Session{sess}, Config{})
	model, _ := app.Update(tea.WindowSizeMsg{Width: 150, Height: 45})
	app = model.(*App)
	app.openConversation(sess)
	return app
}

func TestDecisionTaskEnterOpensTaskViewAndBackRestores(t *testing.T) {
	app := setupDecisionFlowApp(t)
	item := selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convDecision && strings.HasPrefix(item.decision.Key, "task:")
	})
	decisionID := convItemID(item)
	app.conv.split.Focus = false

	app = pressKey(app, "enter")
	if app.conv.task.ID != "T1" {
		t.Fatalf("enter on task decision opened task %q, want T1", app.conv.task.ID)
	}
	if len(app.navStack) != 1 {
		t.Fatalf("navStack depth = %d, want 1", len(app.navStack))
	}

	model, _ := app.popNavFrame()
	app = model.(*App)
	if got := app.selectedConversationItemID(); got != decisionID {
		t.Fatalf("back selection = %q, want the decision row %q", got, decisionID)
	}
}

func TestDecisionPlanEnterZoomsOwnContent(t *testing.T) {
	app := setupDecisionFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convDecision && strings.HasPrefix(item.decision.Key, "plan:")
	})
	app.conv.split.Focus = false

	app = pressKey(app, "enter")
	if !app.conv.inspector.Zoom || app.conv.inspector.Tab != inspectorOverview {
		t.Fatalf("plan decision enter state = zoom:%t tab:%v", app.conv.inspector.Zoom, app.conv.inspector.Tab)
	}
	if !strings.Contains(app.conv.inspector.Rendered, "do the thing") {
		t.Fatalf("plan decision inspector missing plan content: %q", app.conv.inspector.Rendered)
	}
}

func TestSessionMetaEnterShowsRowSpecificContent(t *testing.T) {
	app := setupDecisionFlowApp(t)
	if len(app.conv.contextItems) < 2 {
		t.Fatalf("expected summary + tasksplan context rows, got %d", len(app.conv.contextItems))
	}
	// Sticky facet tab from a previous node must not leak into the zoomed
	// context rows — they all share the flow root node.
	app.conv.inspector.Tab = inspectorStats
	app.conv.split.Focus = false

	app.selectConvContext(0)
	app = pressKey(app, "enter")
	summary := app.conv.split.Preview.View()
	if app.conv.inspector.Tab != inspectorOverview || !strings.Contains(summary, "Session Flow") {
		t.Fatalf("summary row enter tab=%v rendered=%q", app.conv.inspector.Tab, summary)
	}
	app = pressKey(app, "esc")

	app.conv.inspector.Tab = inspectorStats
	app.selectConvContext(1)
	app = pressKey(app, "enter")
	tasks := app.conv.split.Preview.View()
	if !strings.Contains(tasks, "build the feature") {
		t.Fatalf("tasksplan row enter did not render the task board: %q", tasks)
	}
	if tasks == summary {
		t.Fatal("context rows rendered identical inspector content")
	}
}

func TestZoomExitRestoresJumpEntrySelection(t *testing.T) {
	app := setupDecisionFlowApp(t)
	item := selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convDecision && strings.HasPrefix(item.decision.Key, "plan:")
	})
	originID := convItemID(item)

	var parent mergedMsg
	found := false
	for _, m := range app.conv.merged {
		if m.entry.UUID == "a1" {
			parent = m
			found = true
		}
	}
	if !found {
		t.Fatal("merged turn a1 not found")
	}
	model, _ := app.openConversationInspectorForEntry(parent, -1)
	app = model.(*App)
	if app.selectedConversationItemID() == originID {
		t.Fatal("inspector jump did not move the selection")
	}
	if app.conv.inspector.ReturnToID != originID {
		t.Fatalf("ReturnToID = %q, want %q", app.conv.inspector.ReturnToID, originID)
	}

	app = pressKey(app, "esc")
	if app.conv.inspector.Zoom {
		t.Fatal("esc did not exit zoom")
	}
	if got := app.selectedConversationItemID(); got != originID {
		t.Fatalf("zoom exit selection = %q, want entry row %q", got, originID)
	}
}

func TestTabInZoomReturnsToFlowList(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app.conv.split.Focus = false
	app = pressKey(app, "enter")
	if !app.conv.inspector.Zoom {
		t.Fatal("enter did not zoom")
	}

	app = pressKey(app, "tab")
	if app.conv.inspector.Zoom || app.conv.split.PreviewOnly {
		t.Fatalf("tab in zoom left zoom flags set: zoom:%t previewOnly:%t", app.conv.inspector.Zoom, app.conv.split.PreviewOnly)
	}
	if app.conv.split.Focus {
		t.Fatal("tab in zoom did not focus the flow list")
	}
}

func TestPhaseAndShellEnterZoomOwnInspector(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	item := selectInspectorItem(t, app, func(item convItem) bool { return item.kind == convAgent })
	if item.agent.FilePath == "" {
		t.Fatal("fixture agent has no transcript")
	}
	// Simulate the summary-only lifecycle path shared by phase/shell rows:
	// enter must zoom the node's own overview without moving the selection.
	before := app.selectedConversationItemID()
	app.conv.split.Focus = false
	app = pressKey(app, "enter")
	// Full agents drill down instead; back must restore the same row.
	model, _ := app.popNavFrame()
	app = model.(*App)
	if got := app.selectedConversationItemID(); got != before {
		t.Fatalf("agent drill-down back selection = %q, want %q", got, before)
	}
}
