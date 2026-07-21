package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

func TestExecutionRailRendersVerticalWindowFollowingCursor(t *testing.T) {
	app, _, _ := setupConversationStateFixture(t)
	contexts := make([]executionContext, 10)
	for i := range contexts {
		contexts[i] = executionContext{
			Key:    fmt.Sprintf("context-%d", i),
			Label:  fmt.Sprintf("context-%d", i),
			Type:   "agent",
			Status: "unknown",
		}
	}
	app.conv.execution.Contexts = contexts
	app.conv.execution.ActiveKey = contexts[0].Key
	app.conv.execution.CursorKey = contexts[0].Key
	app.conv.execution.Focused = true

	lines := strings.Split(stripANSI(app.renderExecutionRail()), "\n")
	if len(lines) != 1+executionRailMaxItems {
		t.Fatalf("vertical rail lines = %d, want header + %d rows", len(lines), executionRailMaxItems)
	}
	if !strings.Contains(lines[1], "context-0") || !strings.Contains(lines[len(lines)-1], "context-7") {
		t.Fatalf("initial vertical window = %#v", lines)
	}
	if strings.Contains(strings.Join(lines, "\n"), "context-8") {
		t.Fatalf("initial window rendered offscreen context: %#v", lines)
	}

	app.conv.execution.CursorKey = contexts[9].Key
	lines = strings.Split(stripANSI(app.renderExecutionRail()), "\n")
	window := strings.Join(lines, "\n")
	if strings.Contains(window, "context-0") || strings.Contains(window, "context-1") {
		t.Fatalf("cursor-follow window retained leading rows: %q", window)
	}
	if !strings.Contains(window, "context-2") || !strings.Contains(window, "context-9") {
		t.Fatalf("cursor-follow window = %q", window)
	}
}

func TestExecutionRowShowsLifecycleStatusAndTimes(t *testing.T) {
	app, _, _ := setupConversationStateFixture(t)
	plain := stripANSI(app.renderConvSplit())
	// The agent transcripts carry no terminal signal, so they read as live with a
	// start → now window; main reflects the (non-live) session as ended.
	if !strings.Contains(plain, "live · 07-04 10:01 → now · agent · parent11") {
		t.Fatalf("parent lifecycle row missing: %q", plain)
	}
	if !strings.Contains(plain, "ended ·") {
		t.Fatalf("main lifecycle status missing: %q", plain)
	}
}

func TestExecutionContextMenuJumpsToSpawnOrigin(t *testing.T) {
	app, sess, _ := setupConversationStateFixture(t)
	app = pressKey(app, "A")
	app = pressKey(app, "down") // cursor → parent agent context
	if got := app.cursorExecutionContext().Agent.ID; got != "parent1111111111" {
		t.Fatalf("rail cursor = %q, want parent agent", got)
	}
	app = pressKey(app, "x")
	if !app.executionContextMenu {
		t.Fatal("x did not open the execution context menu")
	}
	menu := stripANSI(app.renderExecutionContextMenu())
	if !strings.Contains(menu, "jump to origin turn") {
		t.Fatalf("context menu missing jump action: %q", menu)
	}

	app = pressKey(app, "enter")
	if app.executionContextMenu || app.conv.execution.Focused {
		t.Fatalf("jump left menu/rail focused: menu=%t focus=%t", app.executionContextMenu, app.conv.execution.Focused)
	}
	// parent agent was spawned from the root transcript's spawn-parent turn.
	if app.conv.execution.ActiveKey != executionContextKey(sess.FilePath) {
		t.Fatalf("jump active context = %q, want root", app.conv.execution.ActiveKey)
	}
	if !app.conv.inspector.Zoom {
		t.Fatal("origin jump did not open the conversation inspector")
	}
	// mergeConversationTurns folds the root tool turns into one turn; the origin
	// (entry index of spawn-parent) must land inside it and focus the parent-call
	// tool_use block.
	item, ok := app.selectedConversationItem()
	if !ok || item.kind != convMsg || item.merged.startIdx > 2 || item.merged.endIdx < 2 {
		t.Fatalf("origin jump landed on %#v, want the turn containing spawn-parent (entry 2)", item)
	}
	if app.conv.split.Folds != nil {
		bc := app.conv.split.Folds.BlockCursor
		if bc >= 0 && bc < len(app.conv.split.Folds.Entry.Content) {
			if got := app.conv.split.Folds.Entry.Content[bc].ID; got != "parent-call" {
				t.Fatalf("origin jump focused block %q, want parent-call", got)
			}
		}
	}
}

func TestExecutionRailAndMenuSuppressGlobalShortcuts(t *testing.T) {
	app, _, _ := setupConversationStateFixture(t)
	if app.isInOverlay() {
		t.Fatal("precondition: overlay active before focusing rail")
	}
	app = pressKey(app, "A")
	if !app.conv.execution.Focused || !app.isInOverlay() {
		t.Fatalf("rail focus must count as overlay: focus=%t overlay=%t", app.conv.execution.Focused, app.isInOverlay())
	}
	// A detail shortcut (1/2/3) must not leak through to the conversation while the
	// rail owns input; rightPaneMode stays put.
	before := app.conv.rightPaneMode
	app = pressKey(app, "3")
	if app.conv.rightPaneMode != before {
		t.Fatalf("detail shortcut leaked while rail focused: mode %d → %d", before, app.conv.rightPaneMode)
	}
	if !app.conv.execution.Focused {
		t.Fatal("digit key should be absorbed by rail, not exit focus")
	}

	app = pressKey(app, "x")
	if !app.executionContextMenu || !app.isInOverlay() {
		t.Fatalf("context menu must count as overlay: menu=%t overlay=%t", app.executionContextMenu, app.isInOverlay())
	}
	app = pressKey(app, "2")
	if app.conv.rightPaneMode != before {
		t.Fatalf("detail shortcut leaked while menu open: mode %d → %d", before, app.conv.rightPaneMode)
	}
}

func TestExecutionContextMenuMainHasNoOrigin(t *testing.T) {
	app, _, _ := setupConversationStateFixture(t)
	app = pressKey(app, "A") // cursor starts on main
	if app.cursorExecutionContext().Agent.ID != "" {
		t.Fatal("expected main context at rail cursor")
	}
	app = pressKey(app, "x")
	app = pressKey(app, "enter")
	if app.copiedMsg != "No spawn origin for this context" {
		t.Fatalf("main origin jump feedback = %q", app.copiedMsg)
	}
}

func TestExecutionRailSwitchesContextsAndRestoresSelection(t *testing.T) {
	app, sess, parentPath := setupConversationStateFixture(t)
	if got := len(app.conv.execution.Contexts); got != 4 {
		t.Fatalf("execution contexts = %d, want main + 3 transcript agents", got)
	}
	plain := stripANSI(app.renderConvSplit())
	for _, want := range []string{"EXECUTION CONTEXTS", "main", "agent · parent11", "coordinate"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("execution rail missing %q: %q", want, plain)
		}
	}

	app.selectConvBody(2)
	mainSelection := app.selectedConversationItemID()
	app = pressKey(app, "A")
	if !app.conv.execution.Focused {
		t.Fatal("A did not focus execution rail")
	}
	app = pressKey(app, "down")
	if got := app.conv.execution.CursorKey; got != executionContextKey(parentPath) {
		t.Fatalf("rail cursor = %q, want parent transcript", got)
	}
	app = pressKey(app, "enter")
	if app.conv.execution.Focused || app.conv.execution.ActiveKey != executionContextKey(parentPath) {
		t.Fatalf("agent activation focus=%t active=%q", app.conv.execution.Focused, app.conv.execution.ActiveKey)
	}
	if app.conv.agent.ID != "parent1111111111" || len(app.navStack) != 0 {
		t.Fatalf("rail switch behaved like drilldown: agent=%q stack=%d", app.conv.agent.ID, len(app.navStack))
	}

	app.selectConvBody(len(app.convList.VisibleItems()) - 1)
	agentSelection := app.selectedConversationItemID()
	app = pressKey(app, "A")
	app = pressKey(app, "home")
	app = pressKey(app, "enter")
	if app.conv.execution.ActiveKey != executionContextKey(sess.FilePath) {
		t.Fatalf("main activation = %q", app.conv.execution.ActiveKey)
	}
	if got := app.selectedConversationItemID(); got != mainSelection {
		t.Fatalf("main selection = %q, want %q", got, mainSelection)
	}

	app = pressKey(app, "A")
	app = pressKey(app, "down")
	app = pressKey(app, "enter")
	if got := app.selectedConversationItemID(); got != agentSelection {
		t.Fatalf("agent selection = %q, want %q", got, agentSelection)
	}
}

func TestExecutionRailPreservesInlineDrillStack(t *testing.T) {
	app, _, _ := setupConversationStateFixture(t)
	app.selectConvBody(0)
	applyListFilter(&app.convList, "parent")
	mainSelection := app.selectedConversationItemID()
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
	if len(app.navStack) != 1 {
		t.Fatalf("inline drill stack = %d, want 1", len(app.navStack))
	}

	// Re-selecting the current context through the rail must not destroy the
	// parent route, even though activateExecutionContext is a no-op.
	app = pressKey(app, "A")
	app = pressKey(app, "enter")
	if len(app.navStack) != 1 {
		t.Fatalf("rail re-selection cleared inline drill stack: %d", len(app.navStack))
	}

	app = pressKey(app, "A")
	app = pressKey(app, "home")
	app = pressKey(app, "enter")
	if !app.hasFilterApplied() || app.convList.FilterInput.Value() != "parent" {
		t.Fatalf("rail main restore lost filter: state=%v value=%q", app.convList.FilterState(), app.convList.FilterInput.Value())
	}
	if got := app.selectedConversationItemID(); got != mainSelection {
		t.Fatalf("rail main restore selection=%q, want %q", got, mainSelection)
	}
	app = pressKey(app, "esc")
	if app.conv.execution.ActiveKey != executionContextKey(app.currentSess.FilePath) {
		t.Fatalf("Esc did not restore inline parent context: %q", app.conv.execution.ActiveKey)
	}
}

func TestExecutionContextIsolatesBlockFilterAndBottomAlignment(t *testing.T) {
	app, sess, parentPath := setupConversationStateFixture(t)
	app.selectConvBody(0)
	app.updateConvPreview()
	if app.conv.split.Folds == nil {
		t.Fatal("main preview has no fold state")
	}
	app.conv.split.Folds.BlockFilter = "is:tool"
	app.conv.split.Folds.BlockVisible = applyBlockFilter("is:tool", app.conv.split.Folds.Entry)
	app.liveTail = true
	app.conv.split.BottomAlign = true

	if !app.activateExecutionContext(executionContextKey(parentPath), true) {
		t.Fatal("failed to activate agent context")
	}
	if app.liveTail || app.conv.split.BottomAlign {
		t.Fatalf("main live state leaked: live=%t bottom=%t", app.liveTail, app.conv.split.BottomAlign)
	}
	if app.conv.split.Folds != nil && app.conv.split.Folds.BlockFilter != "" {
		t.Fatalf("main block filter leaked: %q", app.conv.split.Folds.BlockFilter)
	}

	app.selectConvBody(0)
	app.updateConvPreview()
	app.conv.split.Folds.BlockFilter = "is:text"
	app.conv.split.Folds.BlockVisible = applyBlockFilter("is:text", app.conv.split.Folds.Entry)
	if !app.activateExecutionContext(executionContextKey(sess.FilePath), true) {
		t.Fatal("failed to restore main context")
	}
	if !app.liveTail || !app.conv.split.BottomAlign {
		t.Fatalf("main live state not restored: live=%t bottom=%t", app.liveTail, app.conv.split.BottomAlign)
	}
	if got := app.conv.split.Folds.BlockFilter; got != "is:tool" {
		t.Fatalf("main block filter = %q, want is:tool", got)
	}
}

func TestAgentRefreshRetainsFilteredTranscript(t *testing.T) {
	app, _, parentPath := setupConversationStateFixture(t)
	transcript := `{"type":"user","uuid":"context","timestamp":"2026-07-04T11:00:00Z","message":{"role":"user","content":"This session is being continued from a previous conversation context"}}
{"type":"assistant","uuid":"visible","timestamp":"2026-07-04T11:00:01Z","message":{"role":"assistant","content":"visible agent answer"}}
`
	if err := os.WriteFile(parentPath, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	if !app.activateExecutionContext(executionContextKey(parentPath), false) {
		t.Fatal("failed to activate parent context")
	}
	if len(app.conv.messages) != 1 || app.conv.messages[0].UUID != "visible" {
		t.Fatalf("activated messages = %#v", app.conv.messages)
	}
	app.refreshConversation()
	if len(app.conv.messages) != 1 || app.conv.messages[0].UUID != "visible" {
		t.Fatalf("refresh exposed filtered context: %#v", app.conv.messages)
	}
}

func TestRefreshPreservesFilterAndUpdatesTodos(t *testing.T) {
	app, sess, _ := setupConversationStateFixture(t)
	app.selectConvBody(0)
	applyListFilter(&app.convList, "parent")
	if !app.hasFilterApplied() {
		t.Fatal("precondition: conversation filter was not applied")
	}

	addition := `{"type":"assistant","uuid":"todo-refresh","timestamp":"2026-07-04T10:06:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"todo-refresh-call","name":"TodoWrite","input":{"todos":[{"content":"refreshed todo","status":"in_progress"}]}}]}}
`
	f, err := os.OpenFile(sess.FilePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(addition)
	f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}

	app.refreshConversation()
	if !app.hasFilterApplied() || app.convList.FilterInput.Value() != "parent" {
		t.Fatalf("refresh lost list filter: state=%v value=%q", app.convList.FilterState(), app.convList.FilterInput.Value())
	}
	if len(app.currentSess.Todos) != 1 || app.currentSess.Todos[0].Content != "refreshed todo" {
		t.Fatalf("refresh todos = %#v", app.currentSess.Todos)
	}
	if len(app.conv.sess.Todos) != 1 || app.conv.sess.Todos[0].Status != "in_progress" {
		t.Fatalf("visible todos = %#v", app.conv.sess.Todos)
	}
}

func TestRefreshAppliesEmptyTodoSnapshot(t *testing.T) {
	app, sess, _ := setupConversationStateFixture(t)
	app.currentSess.Todos = []session.TodoItem{{Content: "stale todo", Status: "pending"}}
	app.conv.sess.Todos = append([]session.TodoItem(nil), app.currentSess.Todos...)

	addition := `{"type":"assistant","uuid":"todo-clear","timestamp":"2026-07-04T10:06:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"todo-clear-call","name":"TodoWrite","input":{"todos":[]}}]}}
`
	f, err := os.OpenFile(sess.FilePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(addition)
	f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}

	app.refreshConversation()
	if len(app.currentSess.Todos) != 0 || len(app.conv.sess.Todos) != 0 {
		t.Fatalf("empty TodoWrite did not clear todos: current=%#v visible=%#v", app.currentSess.Todos, app.conv.sess.Todos)
	}
}

func TestExecutionRailAgentEscDoesNotPretendParent(t *testing.T) {
	app, _, parentPath := setupConversationStateFixture(t)
	if !app.activateExecutionContext(executionContextKey(parentPath), false) {
		t.Fatal("failed to activate parent execution context")
	}
	if strings.Contains(stripANSI(app.convHelpLine("")), "esc:parent") {
		t.Fatal("sibling execution context advertised a nonexistent parent route")
	}

	app.conv.split.Show = false
	app = pressKey(app, "esc")
	if app.state != viewSessions {
		t.Fatalf("Esc from closed sibling context stayed in state %v", app.state)
	}
}

func TestResourceJumpCrossesExecutionContextAndReturns(t *testing.T) {
	app, sess, parentPath := setupConversationStateFixture(t)
	selectMetaContextRow(t, app, "summary")
	sourceID := app.selectedConversationItemID()
	sourceKey := app.conv.execution.ActiveKey

	target := metaEntryTarget{
		kind:        metaTargetDecision,
		transcript:  parentPath,
		messageUUID: "parent-spawns",
		entryIndex:  1,
		blockIdx:    1,
	}
	model, _, jumped := app.jumpToMetaTarget(target)
	if !jumped {
		t.Fatal("cross-context resource jump was not handled")
	}
	app = model.(*App)
	if app.conv.execution.ActiveKey != executionContextKey(parentPath) || app.conv.agent.ID != "parent1111111111" {
		t.Fatalf("jump destination active=%q agent=%q", app.conv.execution.ActiveKey, app.conv.agent.ID)
	}
	if !app.conv.inspector.Zoom || app.conv.split.Folds == nil {
		t.Fatal("jump did not open the destination inspector")
	}
	// Agent-owned turns have no synthetic turn-node header; the exact second
	// source block remains block 1 in the verbose inspector.
	if got := app.conv.split.Folds.BlockCursor; got != 1 {
		t.Fatalf("exact destination block cursor = %d, want 1", got)
	}

	app = pressKey(app, "esc")
	if app.conv.execution.ActiveKey != sourceKey || app.conv.execution.ActiveKey != executionContextKey(sess.FilePath) {
		t.Fatalf("Esc restored execution context %q, want %q", app.conv.execution.ActiveKey, sourceKey)
	}
	if got := app.selectedConversationItemID(); got != sourceID || !app.conv.contextActive {
		t.Fatalf("Esc restored selection=%q resources=%t, want %q", got, app.conv.contextActive, sourceID)
	}
}

func TestResourceJumpMapsRawAgentIndexAfterContextFiltering(t *testing.T) {
	app, _, parentPath := setupConversationStateFixture(t)
	filteredTranscript := `{"type":"user","timestamp":"2026-07-04T11:00:00Z","message":{"role":"user","content":"This session is being continued from a previous conversation context"}}
{"type":"assistant","timestamp":"2026-07-04T11:00:01Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"legacy-write","name":"Write","input":{"file_path":"/repo/result.txt","content":"done"}}]}}
`
	if err := os.WriteFile(parentPath, []byte(filteredTranscript), 0o644); err != nil {
		t.Fatal(err)
	}

	target := metaEntryTarget{
		kind:       metaTargetDecision,
		transcript: parentPath,
		entryIndex: 1, // raw index before filterAgentContextEntries removes index 0
		blockIdx:   0,
	}
	model, _, jumped := app.jumpToMetaTarget(target)
	if !jumped {
		t.Fatal("legacy raw-index jump was not handled")
	}
	app = model.(*App)
	if app.conv.execution.ActiveKey != executionContextKey(parentPath) {
		t.Fatalf("legacy jump active context = %q", app.conv.execution.ActiveKey)
	}
	if !app.conv.inspector.Zoom || app.conv.split.Folds == nil {
		t.Fatal("legacy jump did not open the destination inspector")
	}
	if got := app.conv.split.Folds.BlockCursor; got != 0 {
		t.Fatalf("legacy exact block cursor = %d, want 0", got)
	}
	block := app.conv.split.Folds.Entry.Content[app.conv.split.Folds.BlockCursor]
	if block.ToolName != "Write" || block.ID != "legacy-write" {
		t.Fatalf("legacy jump selected block %#v", block)
	}
}

func TestFailedCrossContextJumpRestoresResourceFrame(t *testing.T) {
	app, sess, parentPath := setupConversationStateFixture(t)
	selectMetaContextRow(t, app, "summary")
	sourceID := app.selectedConversationItemID()
	transcript := `{"type":"user","timestamp":"2026-07-04T12:00:00Z","message":{"role":"user","content":"This session is being continued from a previous conversation context"}}
{"type":"assistant","timestamp":"2026-07-04T12:00:01Z","message":{"role":"assistant","content":"visible answer"}}
`
	if err := os.WriteFile(parentPath, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	model, _, jumped := app.jumpToMetaTarget(metaEntryTarget{
		kind:       metaTargetDecision,
		transcript: parentPath,
		entryIndex: 0, // hidden continuation entry
		blockIdx:   0,
	})
	if !jumped {
		t.Fatal("hidden-origin jump was not handled")
	}
	app = model.(*App)
	if app.conv.execution.ActiveKey != executionContextKey(sess.FilePath) {
		t.Fatalf("failed jump left active context %q", app.conv.execution.ActiveKey)
	}
	if got := app.selectedConversationItemID(); got != sourceID || !app.conv.contextActive {
		t.Fatalf("failed jump restored selection=%q resources=%t, want %q", got, app.conv.contextActive, sourceID)
	}
	if len(app.conv.inspector.History) != 0 {
		t.Fatalf("failed jump left history depth %d", len(app.conv.inspector.History))
	}
	if app.copiedMsg != "origin turn not found" {
		t.Fatalf("failed jump feedback = %q", app.copiedMsg)
	}
}

func TestHiddenRawOriginDoesNotFallBackToVisibleIndex(t *testing.T) {
	app, _, parentPath := setupConversationStateFixture(t)
	transcript := `{"type":"user","timestamp":"2026-07-04T12:00:00Z","message":{"role":"user","content":"This session is being continued from a previous conversation context"}}
{"type":"user","timestamp":"2026-07-04T12:00:01Z","message":{"role":"user","content":"visible one"}}
{"type":"assistant","timestamp":"2026-07-04T12:00:02Z","message":{"role":"assistant","content":"visible two"}}
`
	if err := os.WriteFile(parentPath, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	if !app.activateExecutionContext(executionContextKey(parentPath), false) {
		t.Fatal("failed to activate filtered context")
	}
	idx, _ := app.resolveVisibleOrigin(parentPath, metaEntryTarget{entryIndex: 0, blockIdx: 0})
	if idx != -1 {
		t.Fatalf("hidden raw origin mapped to visible index %d", idx)
	}
}

func TestExecutionRailMouseDoesNotChangeConversationSelection(t *testing.T) {
	app, _, _ := setupConversationStateFixture(t)
	app.selectConvBody(1)
	before := app.selectedConversationItemID()
	railY := app.executionRailTop() + 1

	model, _ := app.handleMouseClick(tea.MouseMsg{
		X:      2,
		Y:      railY,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	app = model.(*App)
	if !app.conv.execution.Focused {
		t.Fatal("rail click did not focus execution contexts")
	}
	if got := app.selectedConversationItemID(); got != before {
		t.Fatalf("rail click changed conversation selection to %q, want %q", got, before)
	}
	if got := app.conv.execution.CursorKey; got != app.conv.execution.Contexts[0].Key {
		t.Fatalf("first vertical row selected %q", got)
	}

	model, _ = app.handleMouseClick(tea.MouseMsg{
		X:      2,
		Y:      railY + 1,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	app = model.(*App)
	if got := app.conv.execution.CursorKey; got != app.conv.execution.Contexts[1].Key {
		t.Fatalf("second vertical row selected %q, want %q", got, app.conv.execution.Contexts[1].Key)
	}

	model, _ = app.handleMouseScroll(tea.MouseMsg{X: 2, Y: railY, Button: tea.MouseButtonWheelDown})
	app = model.(*App)
	if got := app.selectedConversationItemID(); got != before {
		t.Fatalf("rail wheel changed conversation selection to %q, want %q", got, before)
	}
}

// TestRegionNavigationCyclesWithJK verifies uppercase K/J move focus up/down
// through the RESOURCES → CONVERSATION → EXECUTION CONTEXTS stack, stopping at
// the ends, and that the fixture exposes all three regions.
func TestRegionNavigationCyclesWithJK(t *testing.T) {
	app, _, _ := setupConversationStateFixture(t)
	if len(app.conv.contextItems) == 0 {
		t.Fatal("fixture has no RESOURCES region")
	}
	if app.executionRailItemCount() == 0 {
		t.Fatal("fixture has no EXECUTION CONTEXTS region")
	}

	// Start focused on the top region (RESOURCES).
	app.focusConversationRegion(conversationRegionPinned)
	if got := app.currentConversationRegion(); got != conversationRegionPinned {
		t.Fatalf("initial region = %v, want pinned", got)
	}

	// J descends: pinned → timeline → execution, then stops.
	app = pressKey(app, "J")
	if got := app.currentConversationRegion(); got != conversationRegionTimeline {
		t.Fatalf("after J region = %v, want timeline", got)
	}
	app = pressKey(app, "J")
	if got := app.currentConversationRegion(); got != conversationRegionExecution {
		t.Fatalf("after JJ region = %v, want execution", got)
	}
	app = pressKey(app, "J")
	if got := app.currentConversationRegion(); got != conversationRegionExecution {
		t.Fatalf("J past bottom region = %v, want execution (clamped)", got)
	}

	// K ascends back to the top and stops.
	app = pressKey(app, "K")
	if got := app.currentConversationRegion(); got != conversationRegionTimeline {
		t.Fatalf("after K region = %v, want timeline", got)
	}
	app = pressKey(app, "K")
	if got := app.currentConversationRegion(); got != conversationRegionPinned {
		t.Fatalf("after KK region = %v, want pinned", got)
	}
	app = pressKey(app, "K")
	if got := app.currentConversationRegion(); got != conversationRegionPinned {
		t.Fatalf("K past top region = %v, want pinned (clamped)", got)
	}
}

// TestRegionNavAndJumpDeferToOpenModals verifies that K/J (region nav) and o
// (origin jump) do not fire while a dismiss-on-any-key hint modal (actions menu)
// is open — the modal must own the key, not the region-nav shortcut behind it.
func TestRegionNavAndJumpDeferToOpenModals(t *testing.T) {
	app, _, _ := setupConversationStateFixture(t)
	// Focus the top region so a region change would be observable.
	app.focusConversationRegion(conversationRegionPinned)
	if app.currentConversationRegion() != conversationRegionPinned {
		t.Fatal("precondition: not on pinned region")
	}

	// Open the actions menu (x), then press J — the menu must absorb it (any key
	// dismisses it), and the region must NOT move underneath the overlay.
	app = pressKey(app, "x")
	if !app.convActionsMenu {
		t.Fatal("x did not open the actions menu")
	}
	app = pressKey(app, "J")
	if app.currentConversationRegion() != conversationRegionPinned {
		t.Fatal("J moved the region while the actions menu was open")
	}
	if app.convActionsMenu {
		// The menu dismissed on the keypress — that's fine; the key must NOT have
		// also driven region navigation, which the assertion above guarantees.
	}
}

// TestJumpTreeCollisionBreaksThenResolvesRegionNav reproduces the reported
// regression: a stale config that binds jump_to_tree to the region-down key (J)
// makes J stop navigating regions (jump wins the earlier dispatch). Applying
// resolveConversationConflicts frees J and region-down works again.
func TestJumpTreeCollisionBreaksThenResolvesRegionNav(t *testing.T) {
	app, _, _ := setupConversationStateFixture(t)
	if app.executionRailItemCount() == 0 {
		t.Fatal("fixture has no EXECUTION CONTEXTS region")
	}

	// Simulate the stale collision: jump_to_tree bound to the region-down key.
	app.keymap.Conversation.JumpToTree = app.keymap.Conversation.RegionDown // "J"
	app.focusConversationRegion(conversationRegionTimeline)
	before := app.currentConversationRegion()
	app = pressKey(app, "J")
	if app.currentConversationRegion() != before {
		t.Fatalf("precondition: with the collision, J should be swallowed by jump, region moved to %v", app.currentConversationRegion())
	}

	// Resolve the collision (what LoadCCXConfig now does at load) and retry.
	app.keymap.resolveConversationConflicts()
	if app.keymap.Conversation.JumpToTree == app.keymap.Conversation.RegionDown {
		t.Fatal("resolveConversationConflicts did not clear the collision")
	}
	app.focusConversationRegion(conversationRegionTimeline)
	app = pressKey(app, "J")
	if got := app.currentConversationRegion(); got != conversationRegionExecution {
		t.Fatalf("after resolving, J region = %v, want execution", got)
	}
}
