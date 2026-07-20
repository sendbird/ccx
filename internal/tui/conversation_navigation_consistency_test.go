package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

func TestPinnedAndTimelineRenderAsDistinctRegions(t *testing.T) {
	app := setupFixedContextConvApp(t, 120, 24)
	view := stripANSI(app.renderConvSplit())
	if !strings.Contains(view, "PINNED") || !strings.Contains(view, "CONVERSATION") {
		t.Fatalf("conversation regions are not labeled: %q", view)
	}
	if !app.conv.contextActive {
		t.Fatal("initial pinned selection should make the pinned region active")
	}

	app.selectConvBody(0)
	view = stripANSI(app.renderConvSplit())
	if app.conv.contextActive {
		t.Fatal("timeline selection left the pinned region active")
	}
	if !strings.Contains(view, "CONVERSATION") {
		t.Fatalf("timeline label disappeared after region switch: %q", view)
	}
}

func TestInspectorEnterOnNonActionableMetaRowIsNoop(t *testing.T) {
	app := setupDecisionFlowApp(t)
	selectMetaContextRow(t, app, "summary")
	app.conv.split.Focus = true
	app.updateConvPreview()
	if app.conv.split.Folds == nil || len(app.conv.inspector.MetaTargets) == 0 {
		t.Fatal("summary inspector did not expose selectable blocks")
	}

	app.conv.split.Folds.BlockCursor = 0 // summary header: metaTargetNone
	beforeID := app.selectedConversationItemID()
	beforeHistory := len(app.conv.inspector.History)
	beforeZoom := app.conv.inspector.Zoom
	app = pressKey(app, "enter")

	if got := app.selectedConversationItemID(); got != beforeID {
		t.Fatalf("non-actionable Enter moved selection to %q, want %q", got, beforeID)
	}
	if got := len(app.conv.inspector.History); got != beforeHistory {
		t.Fatalf("non-actionable Enter pushed history: got %d want %d", got, beforeHistory)
	}
	if app.conv.inspector.Zoom != beforeZoom {
		t.Fatal("non-actionable Enter changed zoom")
	}
	if !strings.Contains(app.copiedMsg, "No action") {
		t.Fatalf("non-actionable Enter gave no feedback: %q", app.copiedMsg)
	}
}

func TestNestedInspectorHistoryRestoresExactPinnedState(t *testing.T) {
	app := setupDecisionFlowApp(t)
	selectMetaContextRow(t, app, "summary")
	originID := app.selectedConversationItemID()
	app.conv.inspector.Scope = 0
	app.conv.split.Focus = false

	// First Enter: pinned row -> zoomed inspector.
	app = pressKey(app, "enter")
	if !app.conv.inspector.Zoom || len(app.conv.inspector.History) != 1 {
		t.Fatalf("first Enter state zoom=%t history=%d", app.conv.inspector.Zoom, len(app.conv.inspector.History))
	}

	decisionCursor := -1
	for i, target := range app.conv.inspector.MetaTargets {
		if target.kind == metaTargetDecision && (target.messageUUID != "" || target.entryIndex >= 0) {
			decisionCursor = i
			break
		}
	}
	if decisionCursor < 0 || app.conv.split.Folds == nil {
		t.Skip("fixture has no jumpable decision block")
	}
	app.conv.split.Folds.BlockCursor = decisionCursor
	app.conv.split.Preview.Height = 2
	app.conv.split.Preview.YOffset = 1

	// Second Enter: selected decision block -> exact origin turn.
	app = pressKey(app, "enter")
	if app.selectedConversationItemID() == originID || len(app.conv.inspector.History) != 2 {
		t.Fatalf("nested jump selection=%q history=%d", app.selectedConversationItemID(), len(app.conv.inspector.History))
	}

	// First Esc restores the zoomed pinned inspector, including its block cursor.
	app = pressKey(app, "esc")
	if got := app.selectedConversationItemID(); got != originID {
		t.Fatalf("first Esc restored %q, want %q", got, originID)
	}
	if !app.conv.inspector.Zoom || !app.conv.split.Focus {
		t.Fatalf("first Esc did not restore zoomed inspector: zoom=%t focus=%t", app.conv.inspector.Zoom, app.conv.split.Focus)
	}
	if got := app.conv.split.Folds.BlockCursor; got != decisionCursor {
		t.Fatalf("first Esc restored block cursor %d, want %d", got, decisionCursor)
	}
	if len(app.conv.inspector.History) != 1 {
		t.Fatalf("first Esc history=%d, want 1", len(app.conv.inspector.History))
	}

	// Second Esc returns to the original pinned-list location.
	app = pressKey(app, "esc")
	if got := app.selectedConversationItemID(); got != originID {
		t.Fatalf("second Esc restored %q, want %q", got, originID)
	}
	if app.conv.inspector.Zoom || app.conv.split.Focus || !app.conv.contextActive {
		t.Fatalf("second Esc state zoom=%t focus=%t pinned=%t", app.conv.inspector.Zoom, app.conv.split.Focus, app.conv.contextActive)
	}
	if len(app.conv.inspector.History) != 0 {
		t.Fatalf("second Esc left %d history frames", len(app.conv.inspector.History))
	}
}

func TestConversationEscUsesFocusThenPaneThenParent(t *testing.T) {
	app := setupConvApp(t, testEntries(), 120, 30)
	app.state = viewConversation
	app.conv.split.Show = true
	app.conv.split.Focus = true

	app = pressKey(app, "esc")
	if !app.conv.split.Show || app.conv.split.Focus || app.state != viewConversation {
		t.Fatalf("first Esc should focus list: show=%t focus=%t state=%v", app.conv.split.Show, app.conv.split.Focus, app.state)
	}

	app = pressKey(app, "esc")
	if app.conv.split.Show || app.state != viewConversation {
		t.Fatalf("second Esc should close preview: show=%t state=%v", app.conv.split.Show, app.state)
	}

	app = pressKey(app, "esc")
	if app.state != viewSessions {
		t.Fatalf("third Esc should return to sessions, got %v", app.state)
	}
}

func TestSessionPreviewEscReturnsFocusBeforeClosing(t *testing.T) {
	app := newSessionKeybindingApp()
	app.sessSplit.Show = true
	app.sessSplit.Focus = true
	app.sessPreviewMode = sessPreviewConversation

	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyEscape})
	app = m.(*App)
	if !app.sessSplit.Show || app.sessSplit.Focus {
		t.Fatalf("first Esc should return focus to sessions list: show=%t focus=%t", app.sessSplit.Show, app.sessSplit.Focus)
	}

	m, _ = app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyEscape})
	app = m.(*App)
	if app.sessSplit.Show {
		t.Fatal("second Esc should close the session preview")
	}
}

func TestAgentDrilldownUsesIndependentInspectorHistory(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app = pressKey(app, "enter")
	if len(app.conv.inspector.History) != 1 || !app.conv.inspector.Zoom {
		t.Fatalf("parent inspector history=%d zoom=%t", len(app.conv.inspector.History), app.conv.inspector.Zoom)
	}
	parentID := app.selectedConversationItemID()
	parentFrameID := app.conv.inspector.History[0].location.ItemID
	if len(app.conv.agents) == 0 {
		t.Fatal("fixture has no agent")
	}

	app.pushNavFrame()
	model, _ := app.openAgentConversation(app.conv.agents[0])
	app = model.(*App)
	if len(app.conv.inspector.History) != 0 || app.conv.inspector.ReturnToID != "" {
		t.Fatalf("parent history leaked into child: history=%d return=%q", len(app.conv.inspector.History), app.conv.inspector.ReturnToID)
	}
	if len(app.navStack) != 1 || app.conv.agent.ID == "" {
		t.Fatalf("agent drilldown state stack=%d agent=%q", len(app.navStack), app.conv.agent.ID)
	}

	app = pressKey(app, "esc")
	if app.conv.agent.ID != "" || len(app.navStack) != 0 {
		t.Fatalf("Esc did not restore parent: agent=%q stack=%d", app.conv.agent.ID, len(app.navStack))
	}
	if got := app.selectedConversationItemID(); got != parentID {
		t.Fatalf("parent selection=%q, want %q", got, parentID)
	}
	if len(app.conv.inspector.History) != 1 || app.conv.inspector.History[0].location.ItemID != parentFrameID {
		t.Fatalf("parent history was mutated: %+v", app.conv.inspector.History)
	}
	if !app.conv.inspector.Zoom {
		t.Fatal("parent zoom state was not restored")
	}
}

func TestClosedPaneDoesNotReopenStaleInspectorHistory(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app = pressKey(app, "enter")
	app = pressKey(app, "z") // leave zoom while preserving the Enter history
	app.conv.split.Focus = false
	if len(app.conv.inspector.History) == 0 {
		t.Fatal("test requires inspector history")
	}

	app = pressKey(app, "left")
	if app.conv.split.Show || len(app.conv.inspector.History) != 0 {
		t.Fatalf("pane close left stale state: show=%t history=%d", app.conv.split.Show, len(app.conv.inspector.History))
	}
	app = pressKey(app, "esc")
	if app.state != viewSessions || app.conv.split.Show {
		t.Fatalf("Esc reopened stale pane: state=%v show=%t", app.state, app.conv.split.Show)
	}
}

func TestZoomToggleDoesNotConsumeInspectorHistory(t *testing.T) {
	app := setupDecisionFlowApp(t)
	selectMetaContextRow(t, app, "summary")
	app = pressKey(app, "enter")
	depth := len(app.conv.inspector.History)
	if depth == 0 || !app.conv.inspector.Zoom {
		t.Fatalf("zoom entry history=%d zoom=%t", depth, app.conv.inspector.Zoom)
	}

	app = pressKey(app, "z")
	if app.conv.inspector.Zoom || len(app.conv.inspector.History) != depth {
		t.Fatalf("zoom-off consumed history: zoom=%t history=%d want=%d", app.conv.inspector.Zoom, len(app.conv.inspector.History), depth)
	}
	app = pressKey(app, "z")
	if !app.conv.inspector.Zoom || len(app.conv.inspector.History) != depth {
		t.Fatalf("zoom-on changed history: zoom=%t history=%d want=%d", app.conv.inspector.Zoom, len(app.conv.inspector.History), depth)
	}
}

func TestFailedAgentDrilldownRollsBackNavigationFrame(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app = pressKey(app, "enter")
	beforeID := app.selectedConversationItemID()
	beforeHistory := len(app.conv.inspector.History)
	beforeStack := len(app.navStack)

	agent := app.conv.agents[0]
	agent.FilePath = t.TempDir() + "/missing-agent.jsonl"
	model, _ := app.drillIntoAgentConversation(agent)
	app = model.(*App)

	if got := len(app.navStack); got != beforeStack {
		t.Fatalf("failed drilldown left nav frame: got %d want %d", got, beforeStack)
	}
	if got := app.selectedConversationItemID(); got != beforeID {
		t.Fatalf("failed drilldown moved selection to %q, want %q", got, beforeID)
	}
	if got := len(app.conv.inspector.History); got != beforeHistory {
		t.Fatalf("failed drilldown changed inspector history: got %d want %d", got, beforeHistory)
	}
	if !strings.Contains(app.copiedMsg, "No agent messages") {
		t.Fatalf("failed drilldown gave no feedback: %q", app.copiedMsg)
	}
}

func TestTabFromNestedZoomLandsOnVisibleList(t *testing.T) {
	app := setupDecisionFlowApp(t)
	selectMetaContextRow(t, app, "summary")
	app = pressKey(app, "enter")

	cursor := -1
	for i, target := range app.conv.inspector.MetaTargets {
		if target.kind == metaTargetDecision && (target.messageUUID != "" || target.entryIndex >= 0) {
			cursor = i
			break
		}
	}
	if cursor < 0 || app.conv.split.Folds == nil {
		t.Skip("fixture has no jumpable decision")
	}
	app.conv.split.Folds.BlockCursor = cursor
	app = pressKey(app, "enter")
	if len(app.conv.inspector.History) < 2 || !app.conv.inspector.Zoom {
		t.Fatalf("nested zoom precondition history=%d zoom=%t", len(app.conv.inspector.History), app.conv.inspector.Zoom)
	}

	app = pressKey(app, "tab")
	if app.conv.inspector.Zoom || app.conv.split.PreviewOnly || app.conv.split.Focus {
		t.Fatalf("Tab left hidden-list focus: zoom=%t previewOnly=%t focus=%t", app.conv.inspector.Zoom, app.conv.split.PreviewOnly, app.conv.split.Focus)
	}
}

func TestHiddenConversationHelpShowsEscDestination(t *testing.T) {
	app := setupConvApp(t, testEntries(), 120, 30)
	app.conv.split.Show = false
	app.state = viewConversation
	if help := stripANSI(app.convHelpLine("")); !strings.Contains(help, "esc:sessions") {
		t.Fatalf("root hidden-pane help missing sessions destination: %q", help)
	}

	app.pushNavFrame()
	if help := stripANSI(app.convHelpLine("")); !strings.Contains(help, "esc:parent") {
		t.Fatalf("nested hidden-pane help missing parent destination: %q", help)
	}
}

func TestFilteredEmptyAgentDrilldownRollsBackNavigationFrame(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	beforeID := app.selectedConversationItemID()
	beforeStack := len(app.navStack)

	continuation := makeTextEntry("user", time.Now(), "This session is being continued from a previous conversation that ran out of context.")
	agent := app.conv.agents[0]
	agent.FilePath = writeSessionJSONL(t, []session.Entry{continuation})
	model, _ := app.drillIntoAgentConversation(agent)
	app = model.(*App)

	if got := len(app.navStack); got != beforeStack {
		t.Fatalf("filtered-empty drilldown left nav frame: got %d want %d", got, beforeStack)
	}
	if app.conv.agent.ID != "" {
		t.Fatalf("filtered-empty drilldown entered child agent %q", app.conv.agent.ID)
	}
	if got := app.selectedConversationItemID(); got != beforeID {
		t.Fatalf("filtered-empty drilldown moved selection to %q, want %q", got, beforeID)
	}
	if !strings.Contains(app.copiedMsg, "No agent messages") {
		t.Fatalf("filtered-empty drilldown gave no feedback: %q", app.copiedMsg)
	}
}

func TestMemoryDetailWithoutOriginDisablesEnterAndShowsFeedback(t *testing.T) {
	app := setupFixedContextConvApp(t, 120, 24)
	selectMetaContextRow(t, app, "memory")
	app.conv.split.Focus = true
	app.conv.inspector.MetaDrill = "untracked.md"
	app.conv.inspector.MetaTargets = []metaEntryTarget{{
		kind:       metaTargetMemoryFile,
		fileName:   "untracked.md",
		entryIndex: -1,
		blockIdx:   -1,
	}}
	app.conv.split.Folds = &FoldState{
		Entry:       session.Entry{Content: []session.ContentBlock{{Type: "text", Text: "untracked"}}},
		BlockCursor: 0,
	}

	if action := app.conversationEnterHelpAction(); action.Enabled {
		t.Fatalf("originless memory detail advertised Enter: %+v", action)
	}
	app = pressKey(app, "enter")
	if !strings.Contains(app.copiedMsg, "No origin turn") {
		t.Fatalf("originless memory Enter gave no feedback: %q", app.copiedMsg)
	}
}

func TestTaskWithoutOriginDoesNotJumpToFirstTurn(t *testing.T) {
	app := setupFixedContextConvApp(t, 120, 24)
	app.conv.sess.Tasks = []session.TaskItem{{ID: "orphan-task", Subject: "orphan task", Status: "pending"}}
	selectMetaContextRow(t, app, "tasksplan")
	app.conv.split.Focus = true
	app.updateConvPreview()

	cursor := -1
	for i, target := range app.conv.inspector.MetaTargets {
		if target.kind == metaTargetTask && target.taskID == "orphan-task" {
			cursor = i
			if target.entryIndex != -1 || target.messageUUID != "" {
				t.Fatalf("originless task received a jump origin: %+v", target)
			}
			break
		}
	}
	if cursor < 0 || app.conv.split.Folds == nil {
		t.Fatal("originless task row was not rendered")
	}
	app.conv.split.Folds.BlockCursor = cursor
	before := app.selectedConversationItemID()
	if action := app.conversationEnterHelpAction(); action.Enabled {
		t.Fatalf("originless task advertised Enter: %+v", action)
	}

	app = pressKey(app, "enter")
	if got := app.selectedConversationItemID(); got != before {
		t.Fatalf("originless task jumped to %q, want to remain at %q", got, before)
	}
	if !strings.Contains(app.copiedMsg, "No conversation or origin turn") {
		t.Fatalf("originless task Enter gave no feedback: %q", app.copiedMsg)
	}
}

func TestAppliedBlockFilterHelpMatchesFirstEsc(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app.conv.split.Show = true
	app.conv.split.Focus = true
	app.updateConvPreview()
	if app.conv.split.Folds == nil {
		t.Fatal("message preview has no fold state")
	}
	app.conv.split.Folds.BlockFilter = "tool"
	app.conv.split.Folds.BlockVisible = applyBlockFilter("tool", app.conv.split.Folds.Entry)

	help := stripANSI(app.convHelpLine(""))
	if !strings.Contains(help, "esc:clear filter") {
		t.Fatalf("applied filter help does not describe first Esc: %q", help)
	}
	app = pressKey(app, "esc")
	if app.conv.split.Folds.BlockFilter != "" {
		t.Fatalf("first Esc did not clear block filter: %q", app.conv.split.Folds.BlockFilter)
	}
	if !app.conv.split.Show || !app.conv.split.Focus {
		t.Fatalf("clearing filter changed pane state: show=%t focus=%t", app.conv.split.Show, app.conv.split.Focus)
	}
}

func TestTaskWithoutVisibleTurnsRollsBackNavigationFrame(t *testing.T) {
	entry := session.Entry{
		Role: "user",
		Content: []session.ContentBlock{{
			Type: "tool_result",
			Text: `{"taskId":"invisible-task"}`,
		}},
	}
	app := setupConvApp(t, []session.Entry{entry}, 120, 30)
	beforeStack := len(app.navStack)
	beforeID := app.selectedConversationItemID()
	task := session.TaskItem{ID: "invisible-task", Subject: "invisible task"}

	if _, _, visible := app.taskConversationData(task); visible {
		t.Fatal("tool-result-only task unexpectedly has a visible turn")
	}
	model, _ := app.drillIntoTaskConversation(task)
	app = model.(*App)

	if got := len(app.navStack); got != beforeStack {
		t.Fatalf("invisible task left nav frame: got %d want %d", got, beforeStack)
	}
	if app.conv.task.ID != "" {
		t.Fatalf("invisible task entered child view %q", app.conv.task.ID)
	}
	if got := app.selectedConversationItemID(); got != beforeID {
		t.Fatalf("invisible task moved selection to %q, want %q", got, beforeID)
	}
	if !strings.Contains(app.copiedMsg, "No visible entries") {
		t.Fatalf("invisible task gave no feedback: %q", app.copiedMsg)
	}
}

func TestListSearchAndBlockFilterUnwindInsideOut(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app.conv.split.Focus = false

	// Apply a chronological-list search through the same key path as the UI.
	app = pressKey(app, "/")
	app = pressKey(app, "edit")
	app = pressKey(app, "enter")
	if !app.hasFilterApplied() {
		t.Fatal("conversation list search was not applied")
	}

	// Focus the preview, then apply its independent block filter through `/`.
	app = pressKey(app, "right")
	app = pressKey(app, "/")
	if !app.conv.blockFiltering {
		t.Fatal("preview slash did not start block filtering")
	}
	app = pressKey(app, "tool")
	app = pressKey(app, "enter")
	if app.conv.split.Folds == nil || app.conv.split.Folds.BlockFilter != "tool" {
		t.Fatalf("block filter was not applied: folds=%v", app.conv.split.Folds)
	}
	if !app.hasFilterApplied() {
		t.Fatal("applying block filter cleared the list search")
	}

	// Esc unwinds the focused preview filter first, preserving list search.
	app = pressKey(app, "esc")
	if app.conv.split.Folds.BlockFilter != "" {
		t.Fatalf("first Esc left block filter %q", app.conv.split.Folds.BlockFilter)
	}
	if !app.hasFilterApplied() {
		t.Fatal("first Esc cleared outer list search")
	}
	if !app.conv.split.Show || !app.conv.split.Focus {
		t.Fatalf("first Esc changed pane state: show=%t focus=%t", app.conv.split.Show, app.conv.split.Focus)
	}

	// The next Esc clears the outer chronological-list search.
	app = pressKey(app, "esc")
	if app.hasFilterApplied() {
		t.Fatal("second Esc did not clear list search")
	}
	if !app.conv.split.Show || !app.conv.split.Focus {
		t.Fatalf("second Esc changed pane state: show=%t focus=%t", app.conv.split.Show, app.conv.split.Focus)
	}
}

func TestFilteredConversationEndAndEnterUseVisibleSelection(t *testing.T) {
	app := setupFixedContextConvApp(t, 120, 24)
	applyListFilter(&app.convList, "fixed-context-message-12")
	visible := app.convList.VisibleItems()
	if len(visible) != 1 {
		t.Fatalf("filtered body rows = %d, want 1", len(visible))
	}
	want, ok := visible[0].(convItem)
	if !ok || want.kind != convMsg {
		t.Fatalf("filtered row = %#v, want conversation message", visible[0])
	}

	app = pressKey(app, "end")
	if app.conv.contextActive {
		t.Fatal("end left the pinned region active")
	}
	if got := app.selectedConversationItemID(); got != convItemID(want) {
		t.Fatalf("end selected %q, want visible row %q", got, convItemID(want))
	}

	app = pressKey(app, "enter")
	if !app.conv.inspector.Zoom || !app.conv.split.PreviewOnly {
		t.Fatalf("Enter did not open the visible row: zoom=%t previewOnly=%t", app.conv.inspector.Zoom, app.conv.split.PreviewOnly)
	}
	if got := app.selectedConversationItemID(); got != convItemID(want) {
		t.Fatalf("Enter changed selection to %q, want %q", got, convItemID(want))
	}

	// Applied list search unwinds before inspector history. Both steps must keep
	// the same logical row selected as the visible coordinate space expands.
	app = pressKey(app, "esc")
	if app.hasFilterApplied() || !app.conv.inspector.Zoom {
		t.Fatalf("first Esc state filter=%t zoom=%t", app.hasFilterApplied(), app.conv.inspector.Zoom)
	}
	if got := app.selectedConversationItemID(); got != convItemID(want) {
		t.Fatalf("filter unwind selected %q, want %q", got, convItemID(want))
	}
	app = pressKey(app, "esc")
	if app.conv.inspector.Zoom {
		t.Fatal("second Esc did not restore the list")
	}
	if got := app.selectedConversationItemID(); got != convItemID(want) {
		t.Fatalf("history restore selected %q, want %q", got, convItemID(want))
	}
}

func TestPreviewBoundaryCrossesPinnedAndFilteredTimeline(t *testing.T) {
	app := setupFixedContextConvApp(t, 120, 24)
	applyListFilter(&app.convList, "fixed-context-message-12")
	visible := app.convList.VisibleItems()
	if len(visible) != 1 {
		t.Fatalf("filtered body rows = %d, want 1", len(visible))
	}
	message, ok := visible[0].(convItem)
	if !ok || message.kind != convMsg {
		t.Fatalf("filtered row = %#v, want conversation message", visible[0])
	}

	lastPinned := len(app.conv.contextItems) - 1
	if lastPinned < 0 || !app.selectConvContext(lastPinned) {
		t.Fatal("fixture has no pinned row")
	}
	app.updateConvPreview()
	model, _ := app.convPreviewBoundaryCross("down")
	app = model.(*App)
	if app.conv.contextActive || app.convList.Index() != 0 {
		t.Fatalf("down did not cross into filtered timeline: pinned=%t index=%d", app.conv.contextActive, app.convList.Index())
	}
	if got := app.selectedConversationItemID(); got != convItemID(message) {
		t.Fatalf("down selected %q, want %q", got, convItemID(message))
	}

	model, _ = app.convPreviewBoundaryCross("up")
	app = model.(*App)
	if !app.conv.contextActive || app.conv.contextIndex != lastPinned {
		t.Fatalf("up did not return to last pinned row: pinned=%t index=%d want=%d", app.conv.contextActive, app.conv.contextIndex, lastPinned)
	}

	model, _ = app.convPreviewBoundaryCross("up")
	app = model.(*App)
	if !app.conv.contextActive || app.conv.contextIndex != lastPinned-1 {
		t.Fatalf("up did not move within pinned rows: pinned=%t index=%d want=%d", app.conv.contextActive, app.conv.contextIndex, lastPinned-1)
	}
	model, _ = app.convPreviewBoundaryCross("down")
	app = model.(*App)
	if !app.conv.contextActive || app.conv.contextIndex != lastPinned {
		t.Fatalf("down did not move within pinned rows: pinned=%t index=%d want=%d", app.conv.contextActive, app.conv.contextIndex, lastPinned)
	}
}

func TestPreviewBoundaryMovesWithinFilteredTimeline(t *testing.T) {
	app := setupFixedContextConvApp(t, 120, 24)
	applyListFilter(&app.convList, "fixed-context-message-1")
	visible := app.convList.VisibleItems()
	if len(visible) < 3 {
		t.Fatalf("filtered body rows = %d, want at least 3", len(visible))
	}
	first, ok := visible[0].(convItem)
	if !ok || first.kind != convMsg {
		t.Fatalf("first filtered row = %#v, want conversation message", visible[0])
	}
	second, ok := visible[1].(convItem)
	if !ok || second.kind != convMsg {
		t.Fatalf("second filtered row = %#v, want conversation message", visible[1])
	}

	app.selectConvBody(0)
	model, _ := app.convPreviewBoundaryCross("down")
	app = model.(*App)
	if app.convList.Index() != 1 || app.selectedConversationItemID() != convItemID(second) {
		t.Fatalf("down selected index=%d id=%q, want index=1 id=%q", app.convList.Index(), app.selectedConversationItemID(), convItemID(second))
	}
	model, _ = app.convPreviewBoundaryCross("up")
	app = model.(*App)
	if app.convList.Index() != 0 || app.selectedConversationItemID() != convItemID(first) {
		t.Fatalf("up selected index=%d id=%q, want index=0 id=%q", app.convList.Index(), app.selectedConversationItemID(), convItemID(first))
	}
}

func TestFilteredOriginJumpHonorsVisibleItems(t *testing.T) {
	app := setupDecisionFlowApp(t)
	const filterLabel = "origin-jump-probe"
	var decision convItem
	found := false
	for i := range app.conv.items {
		if app.conv.items[i].kind != convDecision {
			continue
		}
		app.conv.items[i].label = filterLabel
		decision = app.conv.items[i]
		found = true
		break
	}
	if !found {
		t.Fatal("fixture has no decision row")
	}
	app.rebuildConversationList(0)
	applyListFilter(&app.convList, filterLabel)
	visible := app.convList.VisibleItems()
	if len(visible) != 1 {
		t.Fatalf("decision filter returned %d rows, want 1", len(visible))
	}
	filtered, ok := visible[0].(convItem)
	if !ok || filtered.kind != convDecision {
		t.Fatalf("filtered row = %#v, want decision", visible[0])
	}
	app.selectConvBody(0)
	before := app.selectedConversationItemID()

	model, _ := app.jumpToOriginMessage()
	app = model.(*App)
	if got := app.selectedConversationItemID(); got != before {
		t.Fatalf("hidden origin jump moved selection to %q, want %q", got, before)
	}
	if !strings.Contains(app.copiedMsg, "hidden by filter") {
		t.Fatalf("hidden origin jump feedback = %q", app.copiedMsg)
	}

	app.resetActiveFilter()
	if !app.restoreConvSelection(convItemID(decision)) {
		t.Fatal("could not restore decision after clearing filter")
	}
	model, _ = app.jumpToOriginMessage()
	app = model.(*App)
	item, ok := app.selectedConversationItem()
	if !ok || item.kind != convMsg || item.merged.entry.UUID != app.conv.items[decision.parentIdx].merged.entry.UUID {
		t.Fatalf("visible origin jump selected %#v", item)
	}
}
