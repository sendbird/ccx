package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/kitty"
	"github.com/sendbird/ccx/internal/session"
)

var debugLog *log.Logger

func init() {
	if os.Getenv("CCX_DEBUG") != "" {
		f, err := os.OpenFile("/tmp/ccx-debug.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			debugLog = log.New(os.Stderr, "ccx: ", log.Ltime|log.Lmicroseconds)
		} else {
			debugLog = log.New(f, "", log.Ltime|log.Lmicroseconds)
		}
	} else {
		debugLog = log.New(io.Discard, "", 0)
	}
}

// openConversation loads a session's messages and builds the conversation view.
func (a *App) openConversation(sess session.Session) tea.Cmd {
	entries, err := session.LoadMessages(sess.FilePath)
	if err != nil {
		return nil
	}

	a.currentSess = sess
	a.conv.sess = sess
	a.conv.execution = executionRailState{}
	a.conv.messages = entries
	a.conv.merged = filterConversation(mergeConversationTurns(entries))
	a.conv.agent = session.Subagent{}
	a.conv.task = session.TaskItem{}
	a.conv.cron = session.CronItem{}
	a.conv.toolUseToAgent = buildToolUseToAgentMap(entries)
	a.conv.inspector = conversationInspector{Scope: session.ScopeNode}
	a.conv.split.PreviewOnly = false

	// File-backed tasks provide durable metadata; transcript events provide the
	// latest state and IDs for current TaskCreate/TaskUpdate calls.
	tasks := mergeConversationTasks(sess.Tasks, session.LoadTasksFromEntries(entries))
	sess.Tasks = tasks
	crons := sess.Crons
	if len(crons) == 0 && sess.HasCrons {
		crons = session.LoadCronsFromEntries(entries)
		sess.Crons = crons
	}
	a.conv.sess = sess
	a.currentSess = sess
	flow, _ := session.BuildSessionFlow(&sess)
	agents, _ := session.FindSubagents(sess.FilePath)
	if flow != nil {
		agents = flow.Agents()
	}
	a.conv.flow = flow
	a.conv.agents = agents
	a.initExecutionRail(agents)
	a.conv.execution.ActiveKey = executionContextKey(sess.FilePath)
	a.conv.execution.CursorKey = a.conv.execution.ActiveKey
	a.conv.contextItems = buildConvContextItems(sess, a.conv.merged, flow)
	a.conv.contextIndex = 0
	a.conv.contextActive = len(a.conv.contextItems) > 0
	a.conv.items = buildConvItems(sess, a.conv.merged, agents, tasks, crons, flow)

	a.inspectorMenu = false

	if info, err := os.Stat(sess.FilePath); err == nil {
		a.lastMsgLoadTime = info.ModTime()
	}

	// Create the unified flow list with preview auto-open.
	a.conv.split.Show = true
	a.conv.split.Focus = false
	a.conv.split.CacheKey = ""
	a.rebuildConversationList(0)

	a.state = viewConversation

	// Auto-enable live tail for live sessions
	a.liveTail = false
	if sess.IsLive {
		a.liveTail = true
		a.conv.split.BottomAlign = true
		// Select the latest chronological message, never a fixed context row.
		a.selectLastConvMessage()
		a.updateConvPreview()
		a.scrollConvPreviewToTail()
		return liveTickCmd()
	}

	// Non-live sessions start on Session Flow (or the first available context).
	a.updateConvPreview()
	return nil
}

func (a *App) pauseLiveTail() {
	if a.liveTail {
		a.liveTail = false
		a.conv.split.BottomAlign = false
	}
}

// openConversationInspectorForEntry selects the exact unified-flow message row
// and opens its Node-scoped Conversation facet in full-width zoom.
func (a *App) openConversationInspectorForEntry(m mergedMsg, blockIdx int) (tea.Model, tea.Cmd) {
	return a.openConversationInspectorForEntryWithHistory(m, blockIdx, true)
}

func (a *App) openConversationInspectorForEntryWithHistory(m mergedMsg, blockIdx int, pushHistory bool) (tea.Model, tea.Cmd) {
	for i, raw := range a.convList.VisibleItems() {
		item, ok := raw.(convItem)
		if !ok || item.kind != convMsg || item.merged.startIdx != m.startIdx {
			continue
		}
		if pushHistory {
			a.pushInspectorHistory()
		}
		a.pauseLiveTail()
		a.selectConvBody(i)
		// Exact block jumps use the verbose representation because compact and
		// standard previews intentionally collapse or omit tool_result blocks.
		var targetBlock session.ContentBlock
		hasTargetBlock := blockIdx >= 0 && blockIdx < len(m.entry.Content)
		if hasTargetBlock {
			targetBlock = m.entry.Content[blockIdx]
			a.conv.rightPaneMode = previewHook
		}
		a.openInspector(inspectorConversation, session.ScopeNode, true)
		if hasTargetBlock && a.conv.split.Folds != nil {
			// Verbose previews preserve merged block order and prepend one inspector
			// header. Prefer the exact coordinate so identical repeated blocks do not
			// jump to the first occurrence.
			headerBlocks := max(len(a.conv.split.Folds.Entry.Content)-len(m.entry.Content), 0)
			renderedBlockIdx := blockIdx + headerBlocks
			if renderedBlockIdx < 0 || renderedBlockIdx >= len(a.conv.split.Folds.Entry.Content) ||
				!sameConversationBlock(a.conv.split.Folds.Entry.Content[renderedBlockIdx], targetBlock) {
				renderedBlockIdx = -1
				for i, block := range a.conv.split.Folds.Entry.Content {
					if sameConversationBlock(block, targetBlock) {
						renderedBlockIdx = i
						break
					}
				}
			}
			if renderedBlockIdx >= 0 {
				a.conv.split.Folds.BlockCursor = renderedBlockIdx
				a.conv.split.RefreshFoldCursor(a.width, a.splitRatio)
				a.conv.split.ScrollToBlock()
			}
		}
		return a, nil
	}
	return a, nil
}

func sameConversationBlock(a, b session.ContentBlock) bool {
	if a.Type != b.Type {
		return false
	}
	if a.ID != "" || b.ID != "" {
		return a.ID == b.ID
	}
	return a.ToolName == b.ToolName &&
		a.ToolInput == b.ToolInput &&
		a.Text == b.Text &&
		a.ImagePasteID == b.ImagePasteID
}

// openParentConversationInspector opens the exact parent message for a
// lifecycle/task marker without introducing a separate navigation frame.
func (a *App) openParentConversationInspector(item convItem) (tea.Model, tea.Cmd) {
	if item.parentIdx < 0 || item.parentIdx >= len(a.conv.items) {
		return a, nil
	}
	parent := a.conv.items[item.parentIdx]
	if parent.kind != convMsg {
		return a, nil
	}
	return a.openConversationInspectorForEntry(parent.merged, -1)
}

// handleConversationEnter dispatches Enter from the region that owns focus.
// The inspector never falls back to a list action: a focused block either has
// an explicit target or Enter is a no-op.
func (a *App) handleConversationEnter() (tea.Model, tea.Cmd) {
	sp := &a.conv.split
	item, ok := a.selectedConversationItem()
	if !ok {
		return a, nil
	}

	if sp.Show && sp.Focus {
		if item.kind == convSessionMeta {
			target, hasTarget := a.currentMetaTarget()
			if !hasTarget || target.kind == metaTargetNone || target.kind == metaTargetCron {
				a.copiedMsg = "No action for this inspector row"
				return a, nil
			}
			if (target.kind == metaTargetMemoryFile && a.conv.inspector.MetaDrill == "" && target.fileName != "") ||
				(target.kind == metaTargetPlan && a.conv.inspector.MetaPlanDrill == "" && target.planKey != "") {
				a.pushInspectorHistory()
			}
			if handled, m, cmd := a.handleMetaEntryEnter(); handled {
				return m, cmd
			}
			a.copiedMsg = "No action for this inspector row"
			return a, nil
		}

		if sp.Folds != nil {
			bc := sp.Folds.BlockCursor
			entry := sp.Folds.Entry
			if bc >= 0 && bc < len(entry.Content) {
				block := entry.Content[bc]
				if block.Type == "image" && block.ImagePasteID > 0 {
					return a.openCachedImage(block.ImagePasteID)
				}
				if block.Type == "tool_use" && (block.ToolName == "Agent" || block.ToolName == "Task") {
					if agent, found := a.findAgentForToolUse(block.ID); found {
						return a.drillIntoAgentConversation(agent)
					}
				}
			}
		}
		a.copiedMsg = "No action for this inspector block"
		return a, nil
	}

	if item.kind == convSessionMeta {
		a.pushInspectorHistory()
		a.openInspector(inspectorOverview, session.ScopeSession, true)
		return a, nil
	}
	if item.groupTag != "" {
		if item.count > 0 {
			a.toggleConvGroupFold(item)
			return a, nil
		}
		if agent, found := a.findAgentInParentMsg(item); found {
			return a.drillIntoAgentConversation(agent)
		}
		return a.openParentConversationInspector(item)
	}

	switch item.kind {
	case convTask:
		if item.bgTaskID != "" {
			if m, blockIdx, found := a.findBgTaskResultMsg(item.bgTaskID); found {
				return a.openConversationInspectorForEntry(m, blockIdx)
			}
			return a.openParentConversationInspector(item)
		}
		if agents := a.findTaskAgents(); item.groupTag == "" && len(agents) == 1 {
			return a.drillIntoAgentConversation(agents[0])
		}
		if item.task.ID == "" {
			return a.openParentConversationInspector(item)
		}
		return a.drillIntoTaskConversation(item.task)
	case convAgent:
		if item.summaryOnly || item.agent.FilePath == "" {
			a.pushInspectorHistory()
			a.openInspector(inspectorOverview, session.ScopeNode, true)
			return a, nil
		}
		return a.drillIntoAgentConversation(item.agent)
	case convPhase, convShell:
		a.pushInspectorHistory()
		a.openInspector(inspectorOverview, session.ScopeNode, true)
		return a, nil
	case convDecision:
		if task, found := a.decisionTask(item.decision); found {
			if _, _, ok := a.taskConversationData(task); ok {
				return a.drillIntoTaskConversation(task)
			}
		}
		a.pushInspectorHistory()
		a.openInspector(inspectorOverview, session.ScopeNode, true)
		return a, nil
	case convMsg:
		a.pushInspectorHistory()
		a.openInspector(inspectorConversation, session.ScopeNode, true)
	}
	return a, nil
}

// handleConversationKeys handles keyboard input for the conversation split view.
func (a *App) handleConversationKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sp := &a.conv.split
	key := msg.String()

	if a.copyModeActive {
		return a.handleCopyModeKeys(msg)
	}

	if a.inspectorMenu {
		a.inspectorMenu = false
		return a.handleInspectorMenu(key)
	}

	// Block filter input owns all keystrokes until it is applied or cancelled.
	if a.conv.blockFiltering {
		return a.handleBlockFilterInput(msg)
	}

	if a.executionContextMenu {
		return a.handleExecutionContextMenuKey(key)
	}

	if a.conv.execution.Focused {
		if nav, navMsg := a.keymap.TranslateNav(key, msg); nav != "" {
			key = nav
			msg = navMsg
		}
		if key == "q" {
			return a.quit()
		}
		a.handleExecutionRailKey(key)
		return a, nil
	}

	// Inspector controls are handled before generic split-pane keys so [ and ]
	// cycle facets rather than resize while the inspector is focused.
	if key == "z" {
		a.setInspectorZoom(!a.conv.inspector.Zoom)
		return a, nil
	}
	if sp.Show && sp.Focus {
		switch key {
		case "[":
			a.cycleInspectorTabBy(-1)
			return a, nil
		case "]":
			a.cycleInspectorTabBy(1)
			return a, nil
		case "s":
			a.cycleInspectorScope()
			return a, nil
		}
	}

	// Translate navigation aliases (vim hjkl, etc.)
	if nav, navMsg := a.keymap.TranslateNav(key, msg); nav != "" {
		key = nav
		msg = navMsg
	}

	// Edit menu
	if a.editMenu {
		return a.handleEditMenu(key)
	}

	// Actions menu
	if a.convActionsMenu {
		return a.handleConvActionsMenu(key)
	}

	switch key {
	case "q":
		return a.quit()
	case "esc":
		if sp.Folds != nil && sp.Folds.BlockFilter != "" {
			a.clearBlockFilter()
			return a, nil
		}
		if len(a.conv.inspector.History) == 0 {
			if a.exitMemoryDrill() || a.exitPlanDrill() {
				return a, nil
			}
		}
		if a.popInspectorHistory() {
			return a, nil
		}
		if a.conv.inspector.Zoom {
			a.setInspectorZoom(false)
			return a, nil
		}
		if sp.Show && sp.Focus {
			sp.Focus = false
			a.updateConvHeader()
			return a, nil
		}
		if len(a.navStack) > 0 || a.conv.task.ID != "" || a.conv.cron.ID != "" {
			return a.popNavFrame()
		}
		if sp.Show {
			a.clearInspectorHistory()
			sp.HandleSplitKey("esc", a.width, a.height, a.splitRatio, a.adjustSplitRatio)
			return a, nil
		}
		a.liveTail = false
		a.conv.split.BottomAlign = false
		a.state = viewSessions
		return a, nil
	case "enter":
		return a.handleConversationEnter()
	case a.keymap.Conversation.SwitchRegion:
		if a.switchConversationRegion() {
			a.pauseLiveTail()
		}
		return a, nil
	case a.keymap.Conversation.ExecutionContexts:
		a.focusExecutionRail()
		return a, nil
	case a.keymap.Conversation.LiveToggle:
		return a.toggleConvLiveTail()
	case a.keymap.Session.Refresh:
		cmd := a.refreshConversation()
		a.copiedMsg = "Refreshed"
		return a, cmd
	case a.keymap.Conversation.Edit:
		return a.openEditMenu(a.currentSess)
	case "t":
		a.convTooltipOn = !a.convTooltipOn
		a.convTooltipScroll = 0
		return a, nil
	case "i":
		return a.openMessageImage()
	case a.keymap.Conversation.Input:
		if !a.config.TmuxEnabled {
			return a, nil
		}
		return a.openLiveInput(a.currentSess.ProjectPath, a.currentSess.ID)
	case a.keymap.Conversation.JumpToTree:
		item, ok := a.selectedConversationItem()
		if ok && item.kind == convSessionMeta {
			if target, has := a.currentMetaTarget(); has {
				if m, cmd, jumped := a.jumpToMetaTarget(target); jumped {
					return m, cmd
				}
			}
			a.copiedMsg = "no origin turn for this entry"
			return a, nil
		}
		if ok && item.kind != convMsg {
			return a.jumpToOriginMessage()
		}
		if a.config.TmuxEnabled {
			return a.jumpToTmuxPane(a.currentSess.ProjectPath, a.currentSess.ID)
		}
		return a, nil
	case a.keymap.Conversation.Actions:
		a.convActionsMenu = true
		return a, nil
	case a.keymap.Preview.CopyMode:
		if sp.Focus {
			a.enterCopyMode()
			return a, nil
		}
	case "p":
		a.inspectorMenu = true
		return a, nil
	}

	// Tab moves between the unified flow and inspector. Detail level remains
	// available through the explicit detail commands and structured preview keys.
	if key == "tab" || key == "shift+tab" {
		// Zoom hides the flow list; Tab leaves zoom and lands on the list
		// instead of focusing an invisible pane.
		if a.conv.inspector.Zoom {
			a.popInspectorHistory()
			if a.conv.inspector.Zoom {
				a.setInspectorZoom(false)
			}
			sp.Focus = false
			return a, nil
		}
		if !sp.Show {
			sp.Show = true
			sp.Focus = false
			a.updateConvPreview()
		} else {
			sp.Focus = !sp.Focus
			if sp.Focus {
				a.updateConvPreview()
			}
		}
		return a, nil
	}

	// Common split pane keys
	result := sp.HandleSplitKey(key, a.width, a.conversationLayoutHeight(), a.splitRatio, a.adjustSplitRatio)
	switch result {
	case splitKeyClosed:
		a.clearInspectorHistory()
		return a, nil
	case splitKeyFocused, splitKeyOpened:
		a.updateConvPreview()
		return a, nil
	case splitKeyUnfocused:
		return a, nil
	case splitKeyHandled:
		if sp.Focus {
			sp.RefreshFoldPreview(a.width, a.splitRatio)
		}
		return a, nil
	case splitKeyUnhandled:
		if key == "left" {
			a.liveTail = false
			a.conv.split.BottomAlign = false
			a.clearInspectorHistory()
			if a.conv.task.ID != "" || a.conv.cron.ID != "" || len(a.navStack) > 0 {
				return a.popNavFrame()
			}
			a.state = viewSessions
			return a, nil
		}
	}

	// Focused preview keys
	if sp.Focus && sp.Show {
		if key == "up" || key == "pgup" || key == "home" {
			a.pauseLiveTail()
		}
		if key == "up" || key == "down" {
			if sp.Folds != nil {
				switch HandleFoldNav(sp.Folds, &sp.Preview, key) {
				case NavCursorMoved:
					sp.RefreshFoldCursor(a.width, a.splitRatio)
					sp.ScrollToBlock()
				case NavFoldChanged:
					sp.RefreshFoldCursor(a.width, a.splitRatio)
				case NavBoundaryDown:
					return a.convPreviewBoundaryCross("down")
				case NavBoundaryUp:
					return a.convPreviewBoundaryCross("up")
				}
				return a, nil
			}
		}
		result = sp.HandleFocusedKeys(key)
		switch result {
		case splitKeySearchFromPreview:
			// Always run the in-pane block filter when the preview pane is
			// focused — it filters the blocks of the current message, which
			// is the user's actual mental model of "search inside the
			// preview". Previously text mode dropped back into the convList
			// filter, which felt like `/` was broken inside the preview.
			if sp.Folds != nil {
				a.startBlockFilter()
				sp.Focus = true
				return a, nil
			}
			a.conv.contextActive = false
			a.updateConvHeader()
			return a, startListSearch(&a.convList)
		case splitKeyCursorMoved:
			if key == "up" {
				a.pauseLiveTail()
			}
			sp.RefreshFoldCursor(a.width, a.splitRatio)
			sp.ScrollToBlock()
			return a, nil
		case splitKeyHandled:
			sp.RefreshFoldPreview(a.width, a.splitRatio)
			return a, nil
		case splitKeyScrolled:
			if key == "pgup" || key == "home" {
				a.pauseLiveTail()
			}
			return a, nil
		case splitKeyUnfocused:
			return a, nil
		case splitKeyBoundaryDown:
			return a.convPreviewBoundaryCross("down")
		case splitKeyBoundaryUp:
			a.pauseLiveTail()
			return a.convPreviewBoundaryCross("up")
		}
	}

	if !sp.Focus && a.handleConvListNavigation(key) {
		a.pauseLiveTail()
		a.updateConvPreview()
		return a, nil
	}

	// List boundary
	if !sp.Focus && sp.HandleListBoundary(key) {
		a.pauseLiveTail()
		if sp.Show {
			a.updateConvPreview()
		}
		return a, nil
	}

	// Search filters only the chronological body; fixed context stays visible.
	if !sp.Focus && key == "/" {
		a.conv.contextActive = false
		a.updateConvHeader()
	}

	// Default list update
	oldIdx := a.convList.Index()
	m, cmd := a.convList.Update(msg)
	a.convList = m
	if oldIdx != a.convList.Index() {
		a.conv.contextActive = false
		a.updateConvHeader()
	}
	newIdx := a.convList.Index()
	if oldIdx != newIdx && a.liveTail {
		a.pauseLiveTail()
	}
	if sp.Show {
		if oldIdx == newIdx {
			switch key {
			case "down", "up", "pgdown", "pgup":
				scrollPreview(&sp.Preview, key)
				return a, nil
			}
		}
		debounceCmd := a.schedulePreviewUpdate()
		return a, tea.Batch(cmd, debounceCmd)
	}
	return a, cmd
}

// convPreviewBoundaryCross advances to the adjacent item only within the
// active region when the block cursor reaches the current preview boundary.
// Pinned and timeline selections never cross implicitly.
func (a *App) convPreviewBoundaryCross(key string) (tea.Model, tea.Cmd) {
	sp := &a.conv.split
	items := a.convList.VisibleItems()

	finish := func(first bool) (tea.Model, tea.Cmd) {
		sp.CacheKey = ""
		a.updateConvPreview()
		if sp.Folds != nil {
			block := sp.Folds.lastVisibleBlock()
			if first {
				block = sp.Folds.firstVisibleBlock()
			}
			if block >= 0 {
				sp.Folds.BlockCursor = block
			}
			sp.RefreshFoldCursor(a.width, a.splitRatio)
			sp.ScrollToBlock()
		}
		return a, nil
	}

	switch key {
	case "down":
		if a.conv.contextActive {
			if a.conv.contextIndex+1 < len(a.conv.contextItems) {
				a.selectConvContext(a.conv.contextIndex + 1)
				return finish(true)
			}
			return a, nil
		}
		for i := a.convList.Index() + 1; i < len(items); i++ {
			if item, ok := items[i].(convItem); ok && item.kind == convMsg {
				a.selectConvBody(i)
				return finish(true)
			}
		}
	case "up":
		if a.conv.contextActive {
			if a.conv.contextIndex > 0 {
				a.selectConvContext(a.conv.contextIndex - 1)
				return finish(false)
			}
			return a, nil
		}
		for i := a.convList.Index() - 1; i >= 0; i-- {
			if item, ok := items[i].(convItem); ok && item.kind == convMsg {
				a.selectConvBody(i)
				return finish(false)
			}
		}
	}
	return a, nil
}

// updateConvPreview refreshes the right-pane preview for the selected conversation item.
func (a *App) updateConvPreview() {
	a.convTooltipScroll = 0 // reset tooltip scroll on selection change
	sp := &a.conv.split
	if !sp.Show {
		return
	}

	item, ok := a.selectedConversationItem()
	if !ok {
		return
	}

	// Drill state belongs to its owning pinned row. Leaving that row returns it to
	// list mode so re-entry is deterministic.
	if a.conv.inspector.MetaDrill != "" && !(item.kind == convSessionMeta && item.sessionMeta == "memory") {
		a.conv.inspector.MetaDrill = ""
	}
	if a.conv.inspector.MetaPlanDrill != "" && !(item.kind == convSessionMeta && item.sessionMeta == "tasksplan") {
		a.conv.inspector.MetaPlanDrill = ""
	}

	baseKey := convPreviewBaseKey(item)
	// Drill-down is a distinct view of the same meta row; include it in the cache
	// identity so list↔detail transitions reset fold state and scroll.
	if item.kind == convSessionMeta && a.conv.inspector.MetaDrill != "" {
		baseKey += ":memory-drill:" + a.conv.inspector.MetaDrill
	}
	if item.kind == convSessionMeta && a.conv.inspector.MetaPlanDrill != "" {
		baseKey += ":plan-drill:" + a.conv.inspector.MetaPlanDrill
	}
	oldCacheKey := sp.CacheKey
	anchor := captureConvPreviewAnchor(sp, baseKey)
	node, hasNode := a.syncInspectorSelection(item)
	a.conv.inspector.Rendered = ""
	// Session-meta rows (memory/tasks-plan/summary) render as a selectable
	// synthetic entry so each item can be cursor-selected and jumped from, even
	// though they map to the root flow node. Handled below via the fold path.
	// Exception: the "summary" (Session Flow) row renders session-wide facet tabs
	// (Changes/Files/Refs/Images/Stats) through the facet path so the `p` picker
	// can survey the whole session; its Overview stays on the synthetic path.
	summaryFacet := item.kind == convSessionMeta && item.sessionMeta == "summary" &&
		a.conv.inspector.Tab != inspectorOverview && a.conv.inspector.Tab != inspectorConversation
	if hasNode && (item.kind != convSessionMeta || summaryFacet) && a.conv.inspector.Tab != inspectorConversation {
		content := a.renderInspector(item, node, a.renderInspectorTab(item, node))
		a.conv.inspector.Rendered = content
		// baseKey keeps rows that share a flow node distinct (all session
		// context rows map to the root node; decisions map to their turn).
		cacheKey := fmt.Sprintf("inspector:%s:%s:%d:%d:%t", baseKey, node.ID, a.conv.inspector.Tab, a.conv.inspector.Scope, a.conv.inspector.Zoom)
		a.setConvPreviewTextKey(content, cacheKey)
		return
	}

	var build previewBuild
	// metaEntry, when set, is a pre-built selectable synthetic entry (session
	// meta rows) that bypasses the previewBuild → transformer pipeline. Its
	// MetaTargets are stored on the inspector so Enter/J can act per block.
	var metaEntry *session.Entry
	var metaTargets []metaEntryTarget
	switch item.kind {
	case convMsg:
		build.Fallback = item.merged.entry
		if item.merged.startIdx >= 0 && item.merged.endIdx < len(a.conv.messages) && item.merged.startIdx <= item.merged.endIdx {
			build.Sources = append([]session.Entry(nil), a.conv.messages[item.merged.startIdx:item.merged.endIdx+1]...)
		}
	case convAgent:
		build = buildAgentPreview(item.agent)
	case convWorkflow:
		a.setConvPreviewText(renderWorkflowInspector(item.workflow, item.facets))
		return
	case convPhase:
		nodeID := session.FlowPhaseNodeID(item.workflow.RunID, item.phase.Index)
		a.setConvPreviewText(renderPhaseInspector(item.workflow, item.phase, a.conv.flow, nodeID, session.ScopeNode))
		return
	case convShell:
		a.setConvPreviewText(renderShellInspector(item.shell))
		return
	case convDecision:
		a.setConvPreviewText(a.renderDecisionInspector(item.decision))
		return
	case convSessionMeta:
		e, targets := a.buildSessionMetaEntry(item)
		metaEntry = &e
		metaTargets = targets
	case convTask:
		pw := sp.PreviewWidth(a.width, a.splitRatio)
		if item.groupTag == "agents" && item.count > 0 {
			a.setConvPreviewText(a.renderAgentsSummary(pw))
			return
		}
		if item.groupTag == "bgjobs" && item.count > 0 {
			a.setConvPreviewText(a.renderBgJobsSummary(pw))
			return
		}
		if item.groupTag != "" && item.count > 0 {
			a.setConvPreviewText(a.renderConvTaskBoard(pw))
			return
		}
		if item.groupTag != "" {
			a.setConvPreviewText(renderTaskMarkerPreview(item, pw))
			return
		}
		if item.bgTaskID != "" {
			build = a.buildBgJobPreview(item.bgTaskID)
		} else {
			build = a.buildTaskPreview(item.task)
		}
	}

	// Mode transformation is uniform: every preview kind expresses itself as
	// a previewBuild, and the three transformers consume it the same way.
	// Session-meta rows skip the transformer — they arrive as a pre-built
	// selectable synthetic entry with parallel jump targets.
	var entry session.Entry
	var blockSrcIdx []int
	if metaEntry != nil {
		entry = *metaEntry
		blockSrcIdx = nil
		a.conv.inspector.MetaTargets = metaTargets
	} else {
		a.conv.inspector.MetaTargets = nil
		switch a.conv.rightPaneMode {
		case previewText:
			entry, blockSrcIdx = compactPreview(build)
		case previewTool:
			entry, blockSrcIdx = standardPreview(build)
		default:
			entry, blockSrcIdx = verbosePreview(build)
		}
	}
	if hasNode {
		header := a.inspectorHeader(item, node)
		entry.Content = append([]session.ContentBlock{{Type: "text", Text: header}}, entry.Content...)
		blockSrcIdx = append([]int{-1}, blockSrcIdx...)
		// Keep MetaTargets aligned with the (now header-prefixed) blocks so
		// block cursor i indexes the same target slot.
		if a.conv.inspector.MetaTargets != nil {
			a.conv.inspector.MetaTargets = append([]metaEntryTarget{{blockIdx: -1}}, a.conv.inspector.MetaTargets...)
		}
	}

	cacheKey := fmt.Sprintf("%s:%d:%d:%t:%d:%x", baseKey, a.conv.inspector.Tab, a.conv.inspector.Scope, a.conv.inspector.Zoom, len(entry.Content), entryContentHash(entry.Content))
	if cacheKey == sp.CacheKey {
		debugLog.Printf("updateConvPreview: CACHE HIT key=%q", cacheKey)
		return
	}

	isNewEntry := oldCacheKey == "" || !strings.HasPrefix(oldCacheKey, baseKey+":")
	// Session-meta rows can be the very first preview rendered (a context row
	// selected before any Render pass), where the viewport still has zero
	// dimensions and the fold renderer would produce a blank pane. Fill them in
	// for the meta path only, so convMsg/agent scroll math is untouched.
	if metaEntry != nil {
		if sp.Preview.Height <= 0 {
			sp.Preview.Height = a.conversationContentHeight()
		}
		if sp.Preview.Width <= 0 {
			sp.Preview.Width = sp.PreviewWidth(a.width, a.splitRatio)
		}
	}
	if isNewEntry {
		sp.CacheKey = cacheKey
		if sp.Folds != nil {
			sp.Folds.ResetWithPrefs(entry, sp.TypeFoldPrefs, sp.TypeFmtPrefs)
			sp.Folds.BlockSourceIdx = blockSrcIdx
			sp.Folds.HideHooks = a.conv.rightPaneMode == previewTool
			if sp.Folds.BlockFilter != "" {
				sp.Folds.BlockVisible = applyBlockFilter(sp.Folds.BlockFilter, entry)
				if first := sp.Folds.firstVisibleBlock(); first >= 0 {
					sp.Folds.BlockCursor = first
				}
			}
		}
		sp.RefreshFoldPreview(a.width, a.splitRatio)
		sp.Preview.YOffset = 0
	} else {
		oldBC := 0
		if sp.Folds != nil {
			oldBC = len(sp.Folds.Entry.Content)
		}
		sp.CacheKey = cacheKey
		if sp.Folds != nil {
			sp.Folds.GrowBlocks(entry, oldBC, sp.TypeFoldPrefs, sp.TypeFmtPrefs)
			sp.Folds.BlockSourceIdx = blockSrcIdx
			sp.Folds.HideHooks = a.conv.rightPaneMode == previewTool
		}
		sp.RefreshFoldPreview(a.width, a.splitRatio)
	}

	if !isNewEntry && anchor.baseKey != "" {
		restoreConvPreviewAnchor(sp, anchor)
	}
}

func renderWorkflowInspector(run session.WorkflowRun, facets session.FacetSummary) string {
	var b strings.Builder
	name := run.Name
	if name == "" {
		name = run.RunID
	}
	fmt.Fprintf(&b, "# Workflow: %s\n\nStatus: %s · %d agents · %s · %d tool calls\n", name, run.Status, max(run.AgentCount, len(run.Agents)), compactTokenCount(run.TotalTokens), run.TotalToolCalls)
	if run.Summary != "" {
		fmt.Fprintf(&b, "\n%s\n", run.Summary)
	}
	if badges := renderFacetBadges(facets, true); badges != "" {
		fmt.Fprintf(&b, "\nArtifacts: %s\n", stripANSI(badges))
	}
	for _, phase := range workflowPhases(run) {
		fmt.Fprintf(&b, "\n## %s\n", phase.Title)
		if phase.Detail != "" {
			fmt.Fprintln(&b, phase.Detail)
		}
		for _, agent := range run.Agents {
			if agent.PhaseIndex == phase.Index {
				fmt.Fprintf(&b, "  %s %s · %s · %s\n", workflowStatusGlyph(agent.State), agent.Label, compactTokenCount(agent.Tokens), agent.ResultPreview)
			}
		}
	}
	if run.Result != "" {
		fmt.Fprintf(&b, "\n## Result\n%s\n", run.Result)
	}
	return b.String()
}

func renderPhaseInspector(run session.WorkflowRun, phase session.WorkflowPhase, flow *session.FlowIndex, nodeID string, scope session.Scope) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Phase: %s\n", phase.Title)
	if phase.Detail != "" {
		fmt.Fprintf(&b, "\n%s\n", phase.Detail)
	}

	agentCount := 0
	for _, agent := range run.Agents {
		if agent.PhaseIndex == phase.Index {
			agentCount++
		}
	}
	if flow != nil && nodeID != "" {
		facets := flow.Facets(nodeID, scope)
		stats := flow.Stats(nodeID, scope)
		children := flow.Children(nodeID)
		if len(children) > agentCount {
			agentCount = len(children)
		}
		fmt.Fprintf(&b, "\nAgents: %d\nArtifacts: %s\n", agentCount, inspectorFacetSummary(facets))
		exactTokens := stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheReadTokens + stats.TotalCacheCreationTokens
		toolCalls := 0
		for _, count := range stats.ToolCounts {
			toolCalls += count
		}
		fmt.Fprintf(&b, "Tokens: %d exact", exactTokens)
		if stats.EstimatedTokens > 0 {
			fmt.Fprintf(&b, " + ~%d estimated", stats.EstimatedTokens)
		}
		fmt.Fprintf(&b, "\nTool calls: %d exact", toolCalls)
		if stats.EstimatedToolCalls > 0 {
			fmt.Fprintf(&b, " + ~%d estimated", stats.EstimatedToolCalls)
		}
		b.WriteByte('\n')
		if !stats.FirstTimestamp.IsZero() || !stats.LastTimestamp.IsZero() {
			fmt.Fprintf(&b, "Range: %s → %s\n", stats.FirstTimestamp.Format(time.RFC3339), stats.LastTimestamp.Format(time.RFC3339))
		}
	}

	for _, agent := range run.Agents {
		if agent.PhaseIndex == phase.Index {
			fmt.Fprintf(&b, "\n%s %s\n  model %s · %s · %d tools\n  %s\n", workflowStatusGlyph(agent.State), agent.Label, agent.Model, compactTokenCount(agent.Tokens), agent.ToolCalls, agent.ResultPreview)
		}
	}
	return b.String()
}

func renderShellInspector(job session.ShellJob) string {
	var b strings.Builder
	name := job.Description
	if name == "" {
		name = job.Command
	}
	fmt.Fprintf(&b, "# %s: %s\n\nStatus: %s", job.ToolName, name, job.Status)
	if job.Persistent {
		b.WriteString(" · persistent")
	}
	fmt.Fprintf(&b, " · %d polls\n", job.PollCount)
	if !job.StartedAt.IsZero() {
		fmt.Fprintf(&b, "Started: %s\n", job.StartedAt.Format(time.RFC3339))
	}
	if !job.LastEventAt.IsZero() {
		fmt.Fprintf(&b, "Last event: %s\n", job.LastEventAt.Format(time.RFC3339))
	}
	if job.Command != "" {
		fmt.Fprintf(&b, "\n$ %s\n", job.Command)
	}
	return b.String()
}

func (a *App) renderDecisionInspector(artifact session.Artifact) string {
	data, _ := artifact.Data.(session.DecisionData)
	var b strings.Builder
	fmt.Fprintf(&b, "# Decision: %s\n\nKind: %s\n", data.Label, data.Kind)
	fmt.Fprintf(&b, "Origin: %s · entry %d · block %d\n", artifact.Origin.Transcript, artifact.Origin.EntryIndex+1, artifact.Origin.BlockIndex+1)
	if data.Kind == session.DecisionFirstChange && a.conv.flow != nil {
		a.writeChangeHistory(&b, artifact)
	} else if a.conv.flow != nil && data.Related != "" {
		if related, ok := a.conv.flow.ArtifactByID(data.Related); ok {
			a.writeDecisionRelated(&b, related)
		}
	}
	if _, ok := a.decisionTask(artifact); ok {
		b.WriteString("\nEnter opens the task view · J jumps to the originating turn.\n")
	} else {
		b.WriteString("\nPress J to jump to the originating turn.\n")
	}
	return b.String()
}

// writeChangeHistory lists every Edit/Write occurrence for the file a
// first-change decision points at, each with its inline diff — so selecting the
// decision surfaces the file's full change history, not just the first edit.
func (a *App) writeChangeHistory(b *strings.Builder, artifact session.Artifact) {
	path := strings.TrimPrefix(artifact.Key, "first-change:")
	diffWidth := max(a.conv.split.PreviewWidth(a.width, a.splitRatio)-4, 20)
	var occs []session.Artifact
	for _, art := range a.conv.flow.Artifacts(a.conv.flow.RootID, session.ArtifactChange, session.ScopeSession) {
		if art.Key == path {
			occs = append(occs, art)
		}
	}
	// Sort chronologically so cross-transcript edits read as a real timeline,
	// matching the Changes facet ordering.
	sort.SliceStable(occs, func(i, j int) bool {
		return occs[i].Origin.Timestamp.Before(occs[j].Origin.Timestamp)
	})
	n := 0
	for _, art := range occs {
		data, _ := art.Data.(session.ChangeData)
		summary := data.Summary
		if summary == "" {
			summary = changeInputSummary(data.ToolName, data.ToolInput)
		}
		n++
		fmt.Fprintf(b, "\n## %d. %s %s", n, data.ToolName, art.Key)
		if summary != "" {
			fmt.Fprintf(b, " · %s", summary)
		}
		b.WriteByte('\n')
		fmt.Fprintf(b, "origin: %s\n", inspectorArtifactOrigin(art.Origin))
		if diff := changeDiff(data, diffWidth); diff != "" {
			b.WriteString("\n" + diff + "\n")
		}
	}
	if n == 0 {
		b.WriteString("\nNo change occurrences found.\n")
	}
}

// writeDecisionRelated inlines the artifact a decision derives from, so each
// decision marker inspects to its own plan/task/memory content instead of a
// generic stub.
func (a *App) writeDecisionRelated(b *strings.Builder, related session.Artifact) {
	switch payload := related.Data.(type) {
	case session.PlanData:
		if payload.PlanFilePath != "" {
			fmt.Fprintf(b, "Plan file: %s\n", payload.PlanFilePath)
		}
		if payload.Plan != "" {
			fmt.Fprintf(b, "\n## Plan\n%s\n", payload.Plan)
		}
	case session.TaskEventData:
		b.WriteString("\n## Task\n")
		if payload.TaskID != "" {
			fmt.Fprintf(b, "ID: %s\n", payload.TaskID)
		}
		if payload.Subject != "" {
			fmt.Fprintf(b, "Subject: %s\n", payload.Subject)
		}
		if payload.Status != "" {
			fmt.Fprintf(b, "Status: %s\n", payload.Status)
		}
	case session.ChangeData:
		summary := payload.Summary
		if summary == "" {
			summary = changeInputSummary(payload.ToolName, payload.ToolInput)
		}
		fmt.Fprintf(b, "\n## Change\n%s %s", payload.ToolName, related.Key)
		if summary != "" {
			fmt.Fprintf(b, " · %s", summary)
		}
		b.WriteByte('\n')
		diffWidth := max(a.conv.split.PreviewWidth(a.width, a.splitRatio)-4, 20)
		if diff := changeDiff(payload, diffWidth); diff != "" {
			b.WriteString("\n" + diff + "\n")
		}
	}
}

// decisionTask resolves a task decision marker to the task it points at, so
// Enter can open the task's own view instead of the originating turn.
func (a *App) decisionTask(artifact session.Artifact) (session.TaskItem, bool) {
	data, ok := artifact.Data.(session.DecisionData)
	if !ok || data.Kind != session.DecisionTask {
		return session.TaskItem{}, false
	}
	taskID := strings.TrimPrefix(artifact.Key, "task:")
	if a.conv.flow != nil {
		if related, ok := a.conv.flow.ArtifactByID(data.Related); ok {
			if event, ok := related.Data.(session.TaskEventData); ok && event.TaskID != "" {
				taskID = event.TaskID
			}
		}
	}
	if taskID == "" {
		return session.TaskItem{}, false
	}
	for _, task := range a.conv.sess.Tasks {
		if task.ID == taskID {
			return task, true
		}
	}
	return session.TaskItem{ID: taskID}, true
}

func (a *App) renderFlowSummary() string {
	if a.conv.flow == nil {
		return "# Session Flow\n\nNo flow index available.\n"
	}
	flow := a.conv.flow
	facets := flow.Facets(flow.RootID, session.ScopeSession)
	stats := flow.Stats(flow.RootID, session.ScopeSession)
	var b strings.Builder
	fmt.Fprintf(&b, "# Session Flow\n\n%d turns · %d agents · %d workflows · %d decisions\n", len(a.conv.merged), len(flow.Agents()), len(flow.Workflows()), len(flow.Decisions(session.ScopeSession)))
	fmt.Fprintf(&b, "Tokens: %s", compactTokenCount(stats.TotalInputTokens+stats.TotalOutputTokens+stats.TotalCacheReadTokens+stats.TotalCacheCreationTokens))
	if stats.EstimatedTokens > 0 {
		fmt.Fprintf(&b, " + ~%s", compactTokenCount(stats.EstimatedTokens))
	}
	fmt.Fprintf(&b, " · Errors: %d\n", facets.Errors)
	if badges := renderFacetBadges(facets, true); badges != "" {
		fmt.Fprintf(&b, "Artifacts: %s\n", stripANSI(badges))
	}
	b.WriteString("\n## Decisions\n")
	for _, d := range flow.Decisions(session.ScopeSession) {
		if dd, ok := d.Data.(session.DecisionData); ok {
			fmt.Fprintf(&b, "  ▣ %s\n", dd.Label)
		}
	}
	return b.String()
}

func previewTextChunks(e session.Entry) []string {
	var chunks []string
	for _, b := range e.Content {
		if b.Type != "text" {
			continue
		}
		text := strings.TrimSpace(session.StripXMLTags(b.Text))
		if text == "" {
			continue
		}
		chunks = append(chunks, text)
	}
	return chunks
}

func entryContentHash(blocks []session.ContentBlock) uint64 {
	var h uint64
	for i, b := range blocks {
		h ^= uint64(i+1) * 1469598103934665603
		h ^= uint64(len(b.Type) + len(b.Text) + len(b.ToolName) + len(b.ToolInput) + b.ImagePasteID)
	}
	return h
}

// previewBuild captures everything a preview transformer needs. Every preview
// kind — flat conversation turn, task, agent, background job — produces one
// of these, and the compact / standard / verbose transformers consume them
// uniformly so there's no per-kind special casing downstream.
//
//   - Header   : optional descriptor (task subject, agent id, bg command).
//     Prepended as a single text block in every mode so the user
//     always sees the context.
//   - Sources  : per-turn raw entries; compact and standard summarise each.
//   - Fallback : pre-flattened entry used for verbose mode (and as the
//     cache-key carrier even when Sources is empty).
type previewBuild struct {
	Header   string
	Sources  []session.Entry
	Fallback session.Entry
}

// compactPreview emits one text block per turn (plus an optional header block
// at the top). Returns the entry and a parallel slice mapping each output
// block to its source-entry index (-1 for the synthetic header).
func compactPreview(b previewBuild) (session.Entry, []int) {
	blocks := make([]session.ContentBlock, 0, len(b.Sources)+1)
	var srcIdx []int
	if b.Header != "" {
		blocks = append(blocks, session.ContentBlock{Type: "text", Text: strings.TrimSpace(b.Header)})
		srcIdx = append(srcIdx, -1)
	}
	first := len(blocks) == 0
	for i, raw := range b.Sources {
		text := compactPreviewMessageText(raw)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if !first {
			text = "[separator]\n\n" + text
		}
		blocks = append(blocks, session.ContentBlock{Type: "text", Text: text})
		srcIdx = append(srcIdx, i)
		first = false
	}
	if len(blocks) == 0 {
		blocks = append(blocks, session.ContentBlock{Type: "text", Text: "(no text content)"})
		srcIdx = append(srcIdx, -1)
	}
	out := b.Fallback
	out.Content = blocks
	return out, srcIdx
}

// standardPreview emits a text summary + artifact rows (file / change / url)
// per turn, with the optional header as the first block.
func standardPreview(b previewBuild) (session.Entry, []int) {
	sources := b.Sources
	if len(sources) == 0 {
		// No per-turn breakdown — treat the fallback entry as a single turn so
		// we still get text-summary + artifact extraction.
		sources = []session.Entry{{
			Role:      b.Fallback.Role,
			Timestamp: b.Fallback.Timestamp,
			Content:   append([]session.ContentBlock(nil), b.Fallback.Content...),
		}}
	}

	blocks := make([]session.ContentBlock, 0, len(sources)*4+1)
	var srcIdx []int
	if b.Header != "" {
		blocks = append(blocks, session.ContentBlock{Type: "text", Text: strings.TrimSpace(b.Header)})
		srcIdx = append(srcIdx, -1)
	}
	firstSection := len(blocks) == 0
	for i, raw := range sources {
		sectionBlocks := make([]session.ContentBlock, 0, len(raw.Content)+4)
		if msg := previewMessageText(raw); msg != "" {
			sectionBlocks = append(sectionBlocks, session.ContentBlock{Type: "text", Text: msg})
		}
		for _, blk := range raw.Content {
			if blk.Type == "image" {
				sectionBlocks = append(sectionBlocks, blk)
			}
		}
		for _, item := range extract.BlockFilePaths(raw.Content) {
			sectionBlocks = append(sectionBlocks, session.ContentBlock{Type: "text", Text: "[file] " + item.URL})
		}
		for _, ch := range extract.BlockChanges(raw.Content) {
			if len(ch.ToolInputs) > 0 {
				sectionBlocks = append(sectionBlocks, session.ContentBlock{Type: "tool_use", ToolName: ch.ToolNames[0], ToolInput: ch.ToolInputs[0]})
			} else {
				sectionBlocks = append(sectionBlocks, session.ContentBlock{Type: "text", Text: "[change] " + ch.Item.URL})
			}
		}
		for _, item := range extract.BlockURLs(raw.Content) {
			sectionBlocks = append(sectionBlocks, session.ContentBlock{Type: "text", Text: "[url] " + item.URL})
		}
		if len(sectionBlocks) == 0 {
			continue
		}
		for j := range sectionBlocks {
			if !firstSection && j == 0 && sectionBlocks[j].Type == "text" {
				sectionBlocks[j].Text = "[separator]\n\n" + sectionBlocks[j].Text
			}
		}
		blocks = append(blocks, sectionBlocks...)
		for range sectionBlocks {
			srcIdx = append(srcIdx, i)
		}
		firstSection = false
	}
	if len(blocks) == 0 {
		blocks = append(blocks, session.ContentBlock{Type: "text", Text: "(no content)"})
		srcIdx = append(srcIdx, -1)
	}
	out := b.Fallback
	out.Content = blocks
	return out, srcIdx
}

// verbosePreview returns the pre-flattened fallback entry unchanged and a
// parallel source-index map (when the fallback's block count matches the
// concatenated Sources content shape). The mapping is nil for synthetic
// fallbacks that insert their own header/divider blocks; anchor matching
// then falls back to numeric block index, which is fine for verbose.
func verbosePreview(b previewBuild) (session.Entry, []int) {
	srcIdx := computeVerboseBlockSources(b.Fallback, b.Sources)
	return b.Fallback, srcIdx
}

// computeVerboseBlockSources maps each block of the entry back to its
// source-entry index by walking sources in order. Returns nil when the merged
// content shape doesn't match the source content totals.
func computeVerboseBlockSources(entry session.Entry, sources []session.Entry) []int {
	if len(sources) == 0 {
		return nil
	}
	total := 0
	for _, src := range sources {
		total += len(src.Content)
	}
	if total != len(entry.Content) {
		return nil
	}
	out := make([]int, 0, len(entry.Content))
	for i, src := range sources {
		for range src.Content {
			out = append(out, i)
		}
	}
	return out
}

func renderPreviewHeader(entry session.Entry, textW int) string {
	roleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	ds := lipgloss.NewStyle().Foreground(colorDim)

	var sb strings.Builder
	role := strings.ToUpper(entry.Role)
	if role == "" {
		role = "UNKNOWN"
	}
	sb.WriteString(roleStyle.Render(role))
	if !entry.Timestamp.IsZero() {
		sb.WriteString(ds.Render("  " + entry.Timestamp.Format("15:04:05")))
	}
	if entry.Model != "" {
		sb.WriteString(ds.Render("  " + entry.Model))
	}
	sb.WriteString("\n")
	sb.WriteString(ds.Render(strings.Repeat("─", min(textW, 60))) + "\n\n")
	return sb.String()
}

func (a *App) setConvPreviewText(content string) {
	a.setConvPreviewTextKey(content, "text")
}

func (a *App) setConvPreviewTextKey(content, cacheKey string) {
	sp := &a.conv.split
	oldOffset := sp.Preview.YOffset
	sameKey := sp.CacheKey == cacheKey
	sp.CacheKey = cacheKey
	sp.Preview.Width = sp.PreviewWidth(a.width, a.splitRatio)
	sp.Preview.Height = a.conversationContentHeight()
	sp.Preview.SetContent(content)
	if sameKey {
		maxOffset := max(sp.Preview.TotalLineCount()-sp.Preview.Height, 0)
		sp.Preview.YOffset = min(oldOffset, maxOffset)
	} else {
		sp.Preview.YOffset = 0
	}
	// Clear stale fold state so fold keys don't re-render a previous message.
	if sp.Folds != nil {
		sp.Folds.Entry = session.Entry{}
		sp.Folds.BlockStarts = nil
	}
}

type convPreviewAnchor struct {
	baseKey     string
	blockText   string
	blockCore   string
	blockType   string
	toolName    string // for tool_use blocks or text summaries like "[ToolName]"
	toolOrdinal int    // 0-based occurrence index among same-name tool blocks before the cursor
	sourceIdx   int    // source-entry index for this block (-1 = unknown)
	viewportY   int
	blockIndex  int
}

// previewBlockToolRef returns the tool name carried by a block — directly from
// tool_use blocks, or extracted from "[ToolName]" summaries embedded in text
// blocks. Returns "" if the block does not reference a single tool.
func previewBlockToolRef(block session.ContentBlock) string {
	if block.Type == "tool_use" {
		return block.ToolName
	}
	if block.Type != "text" {
		return ""
	}
	// Strip "[separator]\n\nROLE  HH:MM:SS\n" decorations first so the
	// "[ToolName]" summary isn't shadowed by a leading "[separator]" match.
	core := previewBlockCore(strings.TrimSpace(session.StripXMLTags(block.Text)))
	if core == "" {
		return ""
	}
	names := previewBlockToolNames(core)
	if len(names) != 1 {
		return ""
	}
	name := names[0]
	// Skip pseudo-tags ("[file] /path", "[url] http://...", "[change] /path")
	// emitted by standardPreview — they are not real tool names.
	switch name {
	case "file", "url", "change", "separator":
		return ""
	}
	return name
}

func captureConvPreviewAnchor(sp *SplitPane, baseKey string) convPreviewAnchor {
	anchor := convPreviewAnchor{baseKey: baseKey, blockIndex: -1, sourceIdx: -1}
	if sp == nil {
		return anchor
	}
	anchor.viewportY = sp.Preview.YOffset
	if sp.Folds == nil || len(sp.Folds.Entry.Content) == 0 {
		return anchor
	}
	if sp.Folds.BlockCursor < 0 || sp.Folds.BlockCursor >= len(sp.Folds.Entry.Content) {
		return anchor
	}
	block := sp.Folds.Entry.Content[sp.Folds.BlockCursor]
	anchor.blockType = block.Type
	anchor.blockText = strings.TrimSpace(session.StripXMLTags(block.Text))
	anchor.blockCore = previewBlockCore(anchor.blockText)
	anchor.toolName = previewBlockToolRef(block)
	if anchor.toolName != "" {
		// Count preceding blocks that reference the same tool so we can pick
		// the same occurrence in the new mode.
		for i := 0; i < sp.Folds.BlockCursor; i++ {
			if previewBlockToolRef(sp.Folds.Entry.Content[i]) == anchor.toolName {
				anchor.toolOrdinal++
			}
		}
	}
	if len(sp.Folds.BlockSourceIdx) == len(sp.Folds.Entry.Content) {
		anchor.sourceIdx = sp.Folds.BlockSourceIdx[sp.Folds.BlockCursor]
	}
	anchor.blockIndex = sp.Folds.BlockCursor
	return anchor
}

// previewHeaderRE matches a role-header line like "USER" or "ASSISTANT  12:00:03"
// produced by compactPreviewMessageText / previewMessageText.
var previewHeaderRE = regexp.MustCompile(`^[^\s]+(\s+[^\s]+)?(\s+\d{2}:\d{2}:\d{2})?$`)

// previewToolSummaryRE extracts tool names from a previewMessageText summary
// such as "[TaskUpdate]", "[[TaskUpdate]]", or "[Read×3, Edit]".
var previewToolSummaryRE = regexp.MustCompile(`\[+([^\[\]]+)\]+`)

// previewBlockToolNames returns the tool names referenced inside a "[Name, Name×N]"
// style summary. Returns nil when no summary is present.
func previewBlockToolNames(text string) []string {
	m := previewToolSummaryRE.FindStringSubmatch(text)
	if len(m) < 2 {
		return nil
	}
	parts := strings.Split(m[1], ",")
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if idx := strings.Index(name, "×"); idx > 0 {
			name = strings.TrimSpace(name[:idx])
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// previewBlockCore strips mode-specific decorations from a block's text so the
// same logical block matches across compact / standard / verbose modes.
// Compact and standard wrap each turn with a "[separator]\n\n" prefix and a
// "ROLE  HH:MM:SS\n" header; verbose uses the raw content unchanged.
func previewBlockCore(text string) string {
	s := strings.TrimSpace(text)
	s = strings.TrimPrefix(s, "[separator]")
	s = strings.TrimLeft(s, "\n")
	if idx := strings.IndexByte(s, '\n'); idx > 0 {
		if previewHeaderRE.MatchString(s[:idx]) {
			s = s[idx+1:]
		}
	} else if previewHeaderRE.MatchString(s) {
		// Header-only block (e.g. user tool_result entry with no body).
		return ""
	}
	return strings.TrimSpace(s)
}

func restoreConvPreviewAnchor(sp *SplitPane, anchor convPreviewAnchor) {
	if sp == nil || sp.Folds == nil || len(sp.Folds.Entry.Content) == 0 {
		return
	}

	best := -1
	for i, block := range sp.Folds.Entry.Content {
		text := strings.TrimSpace(session.StripXMLTags(block.Text))
		if anchor.blockText != "" && text == anchor.blockText && block.Type == anchor.blockType {
			best = i
			break
		}
	}
	// Match on stripped-decoration text so a block selected in compact/standard
	// (which carries a "[separator]\n\nROLE  HH:MM:SS\n" prefix) still matches
	// the same logical block in verbose mode (which has only the raw text).
	if best < 0 && anchor.blockCore != "" {
		for i, block := range sp.Folds.Entry.Content {
			if block.Type != anchor.blockType {
				continue
			}
			text := strings.TrimSpace(session.StripXMLTags(block.Text))
			if previewBlockCore(text) == anchor.blockCore {
				best = i
				break
			}
		}
	}
	// Source-idx bridge: blocks built from the same source entry should map
	// to each other across modes. Prefer text-conversation blocks (the user
	// asked tool/result blocks to fall back to the nearest plain-text turn
	// when leaving verbose for compact/standard).
	srcMap := sp.Folds.BlockSourceIdx
	hasSrc := anchor.sourceIdx >= 0 && len(srcMap) == len(sp.Folds.Entry.Content)
	if best < 0 && hasSrc {
		// Same source, same type.
		for i, src := range srcMap {
			if src == anchor.sourceIdx && sp.Folds.Entry.Content[i].Type == anchor.blockType {
				best = i
				break
			}
		}
		// Same source, text block (preferred conversation representation).
		if best < 0 {
			for i, src := range srcMap {
				if src == anchor.sourceIdx && sp.Folds.Entry.Content[i].Type == "text" {
					best = i
					break
				}
			}
		}
		// Same source, any block.
		if best < 0 {
			for i, src := range srcMap {
				if src == anchor.sourceIdx {
					best = i
					break
				}
			}
		}
		// Nearest preceding source that has a text block (handles verbose
		// tool_use -> compact, where the tool's own source produced no text).
		if best < 0 {
			bestSrc := -1
			for i, src := range srcMap {
				if src >= 0 && src <= anchor.sourceIdx && src > bestSrc && sp.Folds.Entry.Content[i].Type == "text" {
					best = i
					bestSrc = src
				}
			}
		}
	}
	// Tool-name bridge: standard mode collapses a tool-only turn into a single
	// "[ToolName]" text summary, while verbose has the underlying tool_use
	// blocks. Pick the same Nth occurrence by source order.
	if best < 0 && anchor.toolName != "" {
		var matches []int
		for i, block := range sp.Folds.Entry.Content {
			if previewBlockToolRef(block) == anchor.toolName {
				matches = append(matches, i)
			}
		}
		if len(matches) > 0 {
			ord := anchor.toolOrdinal
			if ord >= len(matches) {
				ord = len(matches) - 1
			}
			best = matches[ord]
		}
	}
	if best < 0 && anchor.blockIndex >= 0 {
		best = min(anchor.blockIndex, len(sp.Folds.Entry.Content)-1)
	}
	if best < 0 {
		if first := sp.Folds.firstVisibleBlock(); first >= 0 {
			best = first
		}
	}
	if best >= 0 {
		sp.Folds.BlockCursor = best
	}
	sp.RefreshFoldPreview(sp.Preview.Width+sp.List.Width()+1, 50)
	if anchor.viewportY > 0 {
		maxOffset := max(sp.Preview.TotalLineCount()-sp.Preview.Height, 0)
		sp.Preview.YOffset = min(anchor.viewportY, maxOffset)
	}
	sp.ScrollToBlock()
}

// buildAgentPreview assembles the agent preview build (header + raw conv
// entries + flattened fallback). External callers that only need the
// flattened entry can use buildAgentPreviewEntry as a thin wrapper.
func buildAgentPreview(agent session.Subagent) previewBuild {
	entries, err := session.LoadMessages(agent.FilePath)
	if err == nil && len(entries) > 0 {
		entries = filterAgentContextEntries(entries)
		if agent.AgentType == "aside_question" {
			entries = filterSideQuestionContext(entries)
		}
	}

	header := fmt.Sprintf("Agent: %s", agent.ShortID)
	if agent.AgentType != "" {
		header += "\nType: " + agent.AgentType
	}
	if agent.FirstPrompt != "" {
		header += "\nPrompt: " + agent.FirstPrompt
	}
	return previewBuild{
		Header:   header,
		Sources:  entries,
		Fallback: buildConversationPreviewEntry(header, agent.Timestamp, entries),
	}
}

// buildAgentPreviewEntry is a backwards-compatible shim returning just the
// flattened fallback entry. New code should use buildAgentPreview directly.
func buildAgentPreviewEntry(agent session.Subagent) session.Entry {
	return buildAgentPreview(agent).Fallback
}

func convPreviewBaseKey(item convItem) string {
	switch {
	case item.kind == convMsg:
		return fmt.Sprintf("msg:%d", item.merged.startIdx)
	case item.kind == convAgent:
		return "agent:" + item.agent.ShortID
	case item.kind == convWorkflow:
		return "workflow:" + item.workflow.RunID
	case item.kind == convPhase:
		return fmt.Sprintf("phase:%s:%d", item.workflow.RunID, item.phase.Index)
	case item.kind == convShell:
		return "shell:" + item.shell.ID
	case item.kind == convDecision:
		return "decision:" + item.decision.ID
	case item.kind == convSessionMeta:
		return "sessionmeta:" + item.sessionMeta
	case item.bgTaskID != "":
		return "bg:" + item.bgTaskID
	case item.cron.ID != "":
		return "cron:" + item.cron.ID
	case item.kind == convTask && item.task.ID != "":
		return "task:" + item.task.ID
	case item.groupTag != "":
		return fmt.Sprintf("group:%s:%d", item.groupTag, item.parentIdx)
	default:
		return "preview:unknown"
	}
}

func buildConversationPreviewEntry(header string, fallbackTS time.Time, entries []session.Entry) session.Entry {
	ts := fallbackTS
	blocks := make([]session.ContentBlock, 0, len(entries)*2+1)
	if header != "" {
		blocks = append(blocks, session.ContentBlock{Type: "text", Text: header})
	}

	emitted := 0
	for _, e := range entries {
		if ts.IsZero() && !e.Timestamp.IsZero() {
			ts = e.Timestamp
		}
		// Skip entries that have no text and no tool_use blocks (only tool_results).
		// These are typically auto-generated user turns containing only tool results.
		hasText := entryFullText(e) != ""
		hasToolUse := false
		if !hasText {
			for _, b := range e.Content {
				if b.Type == "tool_use" {
					hasToolUse = true
					break
				}
			}
			if !hasToolUse {
				continue
			}
		}
		if emitted > 0 {
			blocks = append(blocks, session.ContentBlock{Type: "text", Text: strings.Repeat("─", 24)})
		}
		if msg := previewMessageText(e); msg != "" {
			blocks = append(blocks, session.ContentBlock{Type: "text", Text: msg})
		}
		for _, b := range e.Content {
			if b.Type == "text" {
				continue
			}
			// Truncate large tool_result content to keep the preview scannable.
			// Show first few lines as a summary instead of the full output.
			if b.Type == "tool_result" {
				b = summarizeToolResult(b)
			}
			blocks = append(blocks, b)
		}
		emitted++
	}

	if len(blocks) == 0 {
		blocks = append(blocks, session.ContentBlock{Type: "text", Text: header})
	}

	return session.Entry{
		Role:      "assistant",
		Timestamp: ts,
		Content:   blocks,
	}
}

// summarizeToolResult truncates long tool_result text to a preview-friendly
// length, keeping the first and last few lines for context.
func summarizeToolResult(b session.ContentBlock) session.ContentBlock {
	const maxLines = 15
	text := b.Text
	if text == "" {
		return b
	}
	// Strip XML wrapper tags for cleaner display
	text = session.StripXMLTags(text)
	text = strings.TrimSpace(text)

	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		b.Text = text
		return b
	}
	// Show first 10 lines + "..." + last 3 lines
	head := strings.Join(lines[:10], "\n")
	tail := strings.Join(lines[len(lines)-3:], "\n")
	b.Text = head + "\n  ... (" + fmt.Sprintf("%d", len(lines)-13) + " more lines) ...\n" + tail
	return b
}

func compactPreviewMessageText(e session.Entry) string {
	header := roleChip(e.Role)
	if header == "" {
		header = "entry"
	}
	if !e.Timestamp.IsZero() {
		header += "  " + e.Timestamp.Format("15:04:05")
	}

	text := entryFullText(e)
	if text == "" {
		return ""
	}

	const maxPreviewLines = 6
	lines := strings.Split(text, "\n")
	if len(lines) > maxPreviewLines {
		text = strings.Join(lines[:maxPreviewLines], "\n") + "\n..."
	}
	return header + "\n" + text
}

func previewMessageText(e session.Entry) string {
	header := roleChip(e.Role)
	if header == "" {
		header = "entry"
	}
	if !e.Timestamp.IsZero() {
		header += "  " + e.Timestamp.Format("15:04:05")
	}

	text := entryFullText(e)
	if text == "" {
		if summary := mergedToolSummary(e); summary != "" {
			text = "[" + summary + "]"
		}
	}
	if text == "" {
		return header
	}
	// Truncate long text to keep preview scannable
	const maxPreviewLines = 6
	lines := strings.Split(text, "\n")
	if len(lines) > maxPreviewLines {
		text = strings.Join(lines[:maxPreviewLines], "\n") + "\n..."
	}
	return header + "\n" + text
}

func extractBgTaskEntries(merged []mergedMsg, taskID string) []session.Entry {
	if taskID == "" {
		return nil
	}

	pendingIDs := make(map[string]bool)
	for _, m := range merged {
		for _, b := range m.entry.Content {
			if b.Type == "tool_use" && (b.ToolName == "TaskOutput" || b.ToolName == "TaskStop") && strings.Contains(b.ToolInput, taskID) {
				if b.ID != "" {
					pendingIDs[b.ID] = true
				}
			}
		}
	}

	// Extract only the relevant blocks from each merged message,
	// not the entire merged entry (which can be huge).
	var entries []session.Entry
	for _, m := range merged {
		var relevant []session.ContentBlock
		for _, b := range m.entry.Content {
			switch {
			case b.Type == "tool_use" && (b.ToolName == "TaskOutput" || b.ToolName == "TaskStop") && strings.Contains(b.ToolInput, taskID):
				relevant = append(relevant, b)
			case b.Type == "tool_result" && strings.Contains(b.Text, taskID):
				relevant = append(relevant, b)
			case b.Type == "tool_result" && b.ID != "" && pendingIDs[b.ID]:
				relevant = append(relevant, b)
			}
		}
		if len(relevant) > 0 {
			entries = append(entries, session.Entry{
				Role:      m.entry.Role,
				Timestamp: m.entry.Timestamp,
				Content:   relevant,
			})
		}
	}
	return entries
}

// buildBgJobPreview assembles the bg-job preview build: header (job id +
// command), per-turn sources, and a flattened fallback entry. Callers that
// only need the fallback entry can use buildBgJobPreviewEntry as a shim.
func (a *App) buildBgJobPreview(taskID string) previewBuild {
	header := fmt.Sprintf("Background Job: %s", taskID)
	if cmd := buildBgTaskMap(a.conv.merged)[taskID]; cmd != "" {
		header += "\nCommand: " + cmd
	}
	raws := extractBgTaskEntries(a.conv.merged, taskID)
	return previewBuild{
		Header:   header,
		Sources:  raws,
		Fallback: buildConversationPreviewEntry(header, time.Time{}, raws),
	}
}

func (a *App) buildBgJobPreviewEntry(taskID string) session.Entry {
	return a.buildBgJobPreview(taskID).Fallback
}

// buildTaskPreview assembles the task preview build: header (id + subject +
// status + description), per-turn sources, and a flattened fallback entry.
func (a *App) buildTaskPreview(task session.TaskItem) previewBuild {
	header := "Task"
	if task.ID != "" {
		header += ": " + task.ID
	}
	if task.Subject != "" {
		header += "\n" + task.Subject
	}
	if task.Status != "" {
		header += "\nStatus: " + task.Status
	}
	if task.Description != "" {
		header += "\n\n" + task.Description
	}
	raws := extractTaskEntries(a.conv.messages, task.ID)
	return previewBuild{
		Header:   header,
		Sources:  raws,
		Fallback: buildConversationPreviewEntry(header, time.Time{}, raws),
	}
}

func (a *App) buildTaskPreviewEntry(task session.TaskItem) session.Entry {
	return a.buildTaskPreview(task).Fallback
}

func extractCronEntries(entries []session.Entry, cron session.CronItem) []session.Entry {
	if cron.ID == "" && cron.Cron == "" {
		return nil
	}
	var result []session.Entry
	for _, e := range entries {
		for _, b := range e.Content {
			match := false
			if b.Type == "tool_use" && isCronTool(b.ToolName) {
				if cron.ID != "" && strings.Contains(b.ToolInput, cron.ID) {
					match = true
				}
				if cron.Cron != "" && strings.Contains(b.ToolInput, cron.Cron) {
					match = true
				}
			}
			if b.Type == "tool_result" {
				if cron.ID != "" && strings.Contains(b.Text, cron.ID) {
					match = true
				}
				if cron.Cron != "" && strings.Contains(b.Text, cron.Cron) {
					match = true
				}
			}
			if match {
				result = append(result, e)
				break
			}
		}
	}
	return result
}

func (a *App) buildCronPreviewEntry(cron session.CronItem) session.Entry {
	header := "Cron"
	if cron.ID != "" {
		header += ": " + cron.ID
	}
	if cron.Cron != "" {
		header += "\nSchedule: " + cron.Cron
	}
	if cron.Status != "" {
		header += "\nStatus: " + cron.Status
	}
	if cron.Recurring {
		header += "\nMode: recurring"
	} else {
		header += "\nMode: once"
	}
	if cron.Prompt != "" {
		header += "\n\n" + cron.Prompt
	}
	return buildConversationPreviewEntry(header, cron.CreatedAt, extractCronEntries(a.conv.messages, cron))
}

func renderCronSummary(cron session.CronItem, width int) string {
	var sb strings.Builder
	status := iconActive + " active"
	if cron.Status == "deleted" {
		status = iconStopped + " deleted"
	}
	name := cron.ID
	if name == "" {
		name = "(unknown)"
	}
	sb.WriteString(taskBadgeStyle.Render("Cron: "+name) + "  " + status + "\n")
	if cron.Cron != "" {
		sb.WriteString("\nSchedule: " + cron.Cron + "\n")
	}
	mode := "once"
	if cron.Recurring {
		mode = "recurring"
	}
	sb.WriteString("Mode: " + mode + "\n")
	if cron.Prompt != "" {
		sb.WriteString("\n" + dimStyle.Render("Prompt:") + "\n")
		sb.WriteString(wrapText(cron.Prompt, width-2) + "\n")
	}
	return sb.String()
}

// renderTaskMarkerPreview renders the preview for a task marker header (non-expandable).
// item.task.Description holds newline-separated operation details from taskOpDetail().
func renderTaskMarkerPreview(item convItem, width int) string {
	var sb strings.Builder
	sb.WriteString(dimStyle.Render("── Task Operations ──") + "\n\n")
	if item.task.Description != "" {
		for _, line := range strings.Split(item.task.Description, "\n") {
			if line == "" {
				continue
			}
			sb.WriteString("  " + wrapText(line, width-4) + "\n\n")
		}
	} else {
		sb.WriteString(dimStyle.Render("  No task operations at this point") + "\n")
	}
	return sb.String()
}

// findTaskAgents returns all subagents referenced by Agent tool_use blocks
// in the conversation, resolved via the toolUseToAgent map.
func (a *App) findTaskAgents() []session.Subagent {
	agents := a.conv.agents
	if len(agents) == 0 {
		return nil
	}

	agentByID := make(map[string]session.Subagent, len(agents))
	for _, ag := range agents {
		agentByID[ag.ID] = ag
	}

	seen := make(map[string]bool)
	var result []session.Subagent
	for _, agID := range a.conv.toolUseToAgent {
		if seen[agID] {
			continue
		}
		seen[agID] = true
		if ag, ok := agentByID[agID]; ok {
			result = append(result, ag)
		}
	}
	return result
}

// findAgentInParentMsg finds a subagent referenced by Agent tool_use blocks
// in the parent message. Used for jumping to agents from marker lines.
func (a *App) findAgentInParentMsg(item convItem) (session.Subagent, bool) {
	if item.parentIdx < 0 || item.parentIdx >= len(a.conv.items) {
		return session.Subagent{}, false
	}
	parent := a.conv.items[item.parentIdx]
	if parent.kind != convMsg {
		return session.Subagent{}, false
	}

	agents := a.conv.agents
	if len(agents) == 0 {
		return session.Subagent{}, false
	}
	agentByID := make(map[string]session.Subagent, len(agents))
	for _, ag := range agents {
		agentByID[ag.ID] = ag
	}

	// Look for Agent tool_use blocks and resolve via toolUseToAgent map
	for _, b := range parent.merged.entry.Content {
		if b.Type == "tool_use" && b.ToolName == "Agent" && b.ID != "" {
			if agID, ok := a.conv.toolUseToAgent[b.ID]; ok {
				if ag, ok := agentByID[agID]; ok {
					return ag, true
				}
			}
		}
	}
	return session.Subagent{}, false
}

// findBgTaskResultMsg finds the merged message and block index containing the
// TaskOutput tool_result for a given background task ID.
// It first looks for a TaskOutput tool_use with matching task_id, then finds
// the corresponding tool_result by tool_use ID. Falls back to the background
// "Command running in background" acknowledgement only if no TaskOutput exists.
func (a *App) findBgTaskResultMsg(taskID string) (mergedMsg, int, bool) {
	// Phase 1: Find TaskOutput tool_use blocks that reference this task_id,
	// collect their tool_use IDs.
	var taskOutputIDs []string
	for _, m := range a.conv.merged {
		for _, b := range m.entry.Content {
			if b.Type == "tool_use" && b.ToolName == "TaskOutput" && b.ToolInput != "" {
				if strings.Contains(b.ToolInput, taskID) {
					taskOutputIDs = append(taskOutputIDs, b.ID)
				}
			}
		}
	}

	// Phase 2: Find the tool_result matching a TaskOutput tool_use ID (prefer last match).
	var bestMsg mergedMsg
	bestBI := -1
	for _, m := range a.conv.merged {
		for bi, b := range m.entry.Content {
			if b.Type != "tool_result" || b.ID == "" {
				continue
			}
			for _, tuID := range taskOutputIDs {
				if b.ID == tuID {
					bestMsg = m
					bestBI = bi
				}
			}
		}
	}
	if bestBI >= 0 {
		return bestMsg, bestBI, true
	}

	// Phase 3: Fallback — find any tool_result mentioning the task ID
	// (e.g. the "Command running in background" acknowledgement).
	for _, m := range a.conv.merged {
		for bi, b := range m.entry.Content {
			if b.Type == "tool_result" && strings.Contains(b.Text, taskID) {
				return m, bi, true
			}
		}
	}
	return mergedMsg{}, 0, false
}

// buildToolUseToAgentMap scans entries for Agent tool_result entries that carry
// AgentID (from toolUseResult.agentId) and builds a map from tool_use_id → agent ID.
// Thin wrapper over session.BuildToolUseToAgentMap so the data layer and the view
// share one implementation.
func buildToolUseToAgentMap(entries []session.Entry) map[string]string {
	return session.BuildToolUseToAgentMap(entries)
}

func (a *App) findAgentForToolUse(toolUseID string) (session.Subagent, bool) {
	agentID := a.conv.toolUseToAgent[toolUseID]
	if agentID == "" {
		return session.Subagent{}, false
	}
	for _, agent := range a.conv.agents {
		if agent.ID == agentID {
			return agent, true
		}
	}
	return session.Subagent{}, false
}

// findAgentForConv finds the first subagent launched by an Agent tool_use in
// the entry. Block-focused actions should call findAgentForToolUse directly.
func (a *App) findAgentForConv(entry session.Entry) (session.Subagent, bool) {
	for _, block := range entry.Content {
		if block.Type == "tool_use" && block.ToolName == "Agent" && block.ID != "" {
			if agent, ok := a.findAgentForToolUse(block.ID); ok {
				return agent, true
			}
		}
	}
	return session.Subagent{}, false
}

// toggleConvLiveTail toggles live tailing in the conversation view.
func (a *App) toggleConvLiveTail() (tea.Model, tea.Cmd) {
	a.liveTail = !a.liveTail
	if a.liveTail {
		a.conv.split.BottomAlign = true
		a.selectLastConvMessage()
		a.updateConvPreview()
		a.scrollConvPreviewToTail()
		return a, liveTickCmd()
	}
	a.conv.split.BottomAlign = false
	return a, nil
}

// jumpToOriginMessage selects the exact spawning turn for the current flow node.
func (a *App) jumpToOriginMessage() (tea.Model, tea.Cmd) {
	item, ok := a.selectedConversationItem()
	if !ok || item.parentIdx < 0 || item.parentIdx >= len(a.conv.items) {
		a.copiedMsg = "no origin turn found"
		return a, nil
	}
	target := a.conv.items[item.parentIdx]
	if target.kind != convMsg {
		a.copiedMsg = "no origin turn found"
		return a, nil
	}
	for i, raw := range a.convList.VisibleItems() {
		candidate, ok := raw.(convItem)
		if ok && candidate.kind == convMsg && candidate.merged.entry.UUID == target.merged.entry.UUID {
			a.selectConvBody(i)
			a.updateConvPreview()
			return a, nil
		}
	}
	a.copiedMsg = "origin turn is hidden by filter"
	return a, nil
}

// rebuildConversationList rebuilds the single unified flow list.
func (a *App) rebuildConversationList(selectIdx int) {
	contentH := a.conversationContentHeight()
	a.updateConvHeader()
	contentH = a.conv.split.listContentHeight(contentH)
	a.convList = newConvList(a.conv.items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH, &a.conv.contextActive)
	a.conv.split.List = &a.convList
	if selectIdx >= 0 && selectIdx < len(a.convList.VisibleItems()) {
		a.convList.Select(selectIdx)
	}
	a.conv.split.CacheKey = ""
}

func (a *App) activeConvItems() []convItem { return a.conv.items }

func mergeConversationTasks(base, updates []session.TaskItem) []session.TaskItem {
	result := append([]session.TaskItem(nil), base...)
	byID := make(map[string]int, len(result))
	for i := range result {
		if result[i].ID != "" {
			byID[result[i].ID] = i
		}
	}
	for _, update := range updates {
		if i, ok := byID[update.ID]; ok && update.ID != "" {
			if update.Subject != "" {
				result[i].Subject = update.Subject
			}
			if update.Status != "" {
				result[i].Status = update.Status
			}
			if update.Description != "" {
				result[i].Description = update.Description
			}
			if update.ActiveForm != "" {
				result[i].ActiveForm = update.ActiveForm
			}
			if update.Blocks != nil {
				result[i].Blocks = update.Blocks
			}
			if update.BlockedBy != nil {
				result[i].BlockedBy = update.BlockedBy
			}
			continue
		}
		byID[update.ID] = len(result)
		result = append(result, update)
	}
	return result
}

// refreshConversation reloads messages for the current conversation.
func (a *App) refreshConversation() tea.Cmd {
	entries, err := session.LoadMessages(a.conv.sess.FilePath)
	if err != nil {
		return nil
	}
	// Match context activation: agent transcripts hide injected parent context,
	// and side-question transcripts collapse their inherited history.
	if a.conv.agent.ID != "" {
		entries = filterAgentContextEntries(entries)
		if a.conv.agent.AgentType == "aside_question" {
			entries = filterSideQuestionContext(entries)
		}
	}
	a.conv.messages = entries
	a.conv.merged = filterConversation(mergeConversationTurns(entries))
	a.conv.toolUseToAgent = buildToolUseToAgentMap(entries)

	// Connection metadata and task state belong to the root session even while
	// an agent transcript is visible. Rebuilding from the agent path discards
	// sibling agents and workflow summaries.
	rootEntries := entries
	if a.currentSess.FilePath != "" && a.currentSess.FilePath != a.conv.sess.FilePath {
		if loaded, loadErr := session.LoadMessages(a.currentSess.FilePath); loadErr == nil {
			rootEntries = loaded
		}
	}
	flow, _ := session.BuildSessionFlow(&a.currentSess)
	var agents []session.Subagent
	if flow != nil {
		agents = flow.Agents()
	} else {
		agents, _ = session.FindSubagents(a.currentSess.FilePath)
	}
	a.conv.flow = flow
	a.conv.agents = agents
	a.refreshExecutionRail()

	tasks := mergeConversationTasks(a.currentSess.Tasks, session.LoadTasksFromEntries(rootEntries))
	todos := a.currentSess.Todos
	if latest, found := session.LoadTodoSnapshotFromEntries(rootEntries); found {
		todos = latest
	}
	crons := a.currentSess.Crons
	if len(crons) == 0 && a.currentSess.HasCrons {
		crons = session.LoadCronsFromEntries(rootEntries)
	}
	a.currentSess.Tasks = tasks
	a.currentSess.Todos = todos
	a.currentSess.Crons = crons
	a.conv.sess.Tasks = tasks
	a.conv.sess.Todos = todos
	a.conv.sess.Crons = crons

	// Root tasks/crons are fixed session context. Only place their chronological
	// rows when the root transcript itself is visible.
	var timelineTasks []session.TaskItem
	var timelineCrons []session.CronItem
	if a.conv.sess.FilePath == a.currentSess.FilePath {
		timelineTasks = tasks
		timelineCrons = crons
	}
	selectedID := a.selectedConversationItemID()
	a.conv.contextItems = buildConvContextItems(a.conv.sess, a.conv.merged, flow)
	a.conv.items = buildConvItems(a.conv.sess, a.conv.merged, agents, timelineTasks, timelineCrons, flow)

	// Preserve logical selection and an applied list search across refresh.
	filterTerm := ""
	if a.hasFilterApplied() {
		filterTerm = a.convList.FilterInput.Value()
	}
	oldIdx := a.convList.Index()
	prevCacheKey := a.conv.split.CacheKey
	prevYOffset := a.conv.split.Preview.YOffset
	a.rebuildConversationList(oldIdx)
	if filterTerm != "" {
		applyListFilter(&a.convList, filterTerm)
	}
	if !a.restoreConvSelection(selectedID) {
		if len(a.conv.contextItems) > 0 {
			a.selectConvContext(0)
		} else if len(a.convList.VisibleItems()) > 0 {
			a.selectConvBody(min(oldIdx, len(a.convList.VisibleItems())-1))
		}
	}
	a.conv.split.CacheKey = prevCacheKey
	// During live tail, skip preview update here — handleLiveTail owns the
	// preview lifecycle (select last → update → scroll-to-tail). Updating here
	// would "consume" the CacheKey change, making handleLiveTail's update a
	// no-op cache hit while the scroll position is left at block 0 from
	// RefreshFoldPreview→ScrollToBlock.
	if !a.liveTail {
		a.updateConvPreview()
		if a.conv.split.Folds != nil {
			a.conv.split.Preview.YOffset = prevYOffset
		}
	}
	return nil
}

// renderConvTaskBoard renders a full task board for the preview pane,
// reusing the same style as buildTasksPlanContent in app.go.
func (a *App) renderConvTaskBoard(width int) string {
	tasks := a.conv.sess.Tasks
	if len(tasks) == 0 {
		return dimStyle.Render("No tasks")
	}

	completed := 0
	for _, t := range tasks {
		if t.Status == "completed" {
			completed++
		}
	}

	var sb strings.Builder
	sb.WriteString(dimStyle.Render(fmt.Sprintf("── Tasks [%d/%d] ──", completed, len(tasks))) + "\n\n")
	for _, t := range tasks {
		icon := iconIdle
		style := dimStyle
		switch t.Status {
		case "completed":
			icon = iconDone
			style = lipgloss.NewStyle().Foreground(colorAccent)
		case "in_progress":
			icon = iconActive
			style = lipgloss.NewStyle().Foreground(colorAssistant)
		}
		idTag := ""
		if t.ID != "" {
			idTag = dimStyle.Render("#"+t.ID) + " "
		}
		sb.WriteString(style.Render(fmt.Sprintf("  %s ", icon)) + idTag + style.Render(t.Subject) + "\n")
		if t.Description != "" {
			descW := width - 6
			if descW < 20 {
				descW = 20
			}
			sb.WriteString(dimStyle.Render(wrapText("    "+t.Description, descW)) + "\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func (a *App) renderConvCronBoard(width int) string {
	crons := a.conv.sess.Crons
	if len(crons) == 0 {
		return dimStyle.Render("No cron jobs")
	}
	active := 0
	for _, c := range crons {
		if c.Status != "deleted" {
			active++
		}
	}
	var sb strings.Builder
	sb.WriteString(dimStyle.Render(fmt.Sprintf("── Cron Jobs [%d/%d active] ──", active, len(crons))) + "\n\n")
	for _, c := range crons {
		sb.WriteString(renderCronSummary(c, width) + "\n")
	}
	return sb.String()
}

// renderAgentsSummary renders a summary of all agents for the tree group header preview.
func (a *App) renderAgentsSummary(width int) string {
	agents := a.conv.agents
	if len(agents) == 0 {
		return dimStyle.Render("No agents")
	}
	statuses := inferAgentStatuses(a.conv.merged)
	var sb strings.Builder
	sb.WriteString(dimStyle.Render(fmt.Sprintf("── Agents (%d) ──", len(agents))) + "\n\n")
	for _, ag := range agents {
		if isSystemAgent(ag) {
			continue
		}
		icon := iconFocused
		status := statuses[ag.ID]
		if status == "" {
			status = statuses[ag.ShortID]
		}
		style := dimStyle
		switch status {
		case "completed":
			icon = iconDone
			style = lipgloss.NewStyle().Foreground(colorAccent)
		case "running":
			icon = iconActive
			style = lipgloss.NewStyle().Foreground(colorAssistant)
		case "stopped":
			icon = iconStopped
		}
		typeBadge := ""
		if ag.AgentType != "" {
			typeBadge = dimStyle.Render("["+ag.AgentType+"]") + " "
		}
		dur := ""
		if !ag.Timestamp.IsZero() {
			dur = dimStyle.Render(fmt.Sprintf(" (%dm)", int(ag.Timestamp.Sub(ag.Timestamp).Minutes())))
		}
		sb.WriteString(fmt.Sprintf("  %s %s%s%s\n", style.Render(icon), typeBadge, style.Render(ag.ShortID), dur))
		if ag.FirstPrompt != "" {
			prompt := ag.FirstPrompt
			if len(prompt) > width-6 {
				prompt = prompt[:width-9] + "..."
			}
			sb.WriteString(dimStyle.Render("    "+prompt) + "\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderBgJobsSummary renders a summary of background jobs for the tree group header preview.
func (a *App) renderBgJobsSummary(width int) string {
	bgTasks := buildBgTaskMap(a.conv.merged)
	if len(bgTasks) == 0 {
		return dimStyle.Render("No background jobs")
	}
	var sb strings.Builder
	sb.WriteString(dimStyle.Render(fmt.Sprintf("── Background Jobs (%d) ──", len(bgTasks))) + "\n\n")
	for id, desc := range bgTasks {
		status := "pending"
		for _, m := range a.conv.merged {
			for _, b := range m.entry.Content {
				if b.Type == "tool_result" && strings.Contains(b.Text, id) {
					if strings.Contains(b.Text, "<status>completed</status>") {
						status = "completed"
					} else if strings.Contains(b.Text, "<status>stopped</status>") {
						status = "stopped"
					}
				}
			}
		}
		icon := iconWaiting
		style := dimStyle
		switch status {
		case "completed":
			icon = iconDone
			style = lipgloss.NewStyle().Foreground(colorAccent)
		case "stopped":
			icon = iconStopped
		}
		label := desc
		if len(label) > width-10 {
			label = label[:width-13] + "..."
		}
		sb.WriteString(fmt.Sprintf("  %s %s %s\n", style.Render(icon), dimStyle.Render(id), style.Render(label)))
	}
	return sb.String()
}

// toggleConvGroupFold toggles the fold state of a group header in the conversation
// items list and rebuilds the visible list, preserving cursor on the header.
func (a *App) toggleConvGroupFold(header convItem) {
	// Find the group header in the active items slice and toggle its fold state.
	items := a.activeConvItems()
	for i := range items {
		if items[i].groupTag == header.groupTag && items[i].parentIdx == header.parentIdx {
			items[i].folded = !items[i].folded
			break
		}
	}

	// Rebuild visible list; find the header's new index.
	vis := visibleConvItems(items)
	contentH := a.conv.split.listContentHeight(a.conversationContentHeight())
	a.convList = newConvList(items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH, &a.conv.contextActive)
	a.conv.split.List = &a.convList

	for i, v := range vis {
		if v.groupTag == header.groupTag && v.parentIdx == header.parentIdx {
			a.selectConvBody(i)
			break
		}
	}
	a.conv.split.CacheKey = ""
	a.updateConvPreview()
}

// scrollConvPreviewToTail scrolls the conversation preview to the bottom
// so the latest content is visible during live tail.
// Always scrolls regardless of focus state — during live tail the user
// expects to see the newest content even when the preview pane is focused.
func (a *App) scrollConvPreviewToTail() {
	sp := &a.conv.split
	if !sp.Show {
		return
	}
	// Ensure preview height is initialized (Render may not have run yet)
	contentH := a.conversationContentHeight()
	if sp.Preview.Height < 1 && contentH > 0 {
		sp.Preview.Height = contentH
	}
	// Move block cursor to the last block so the preview highlights newest content
	if sp.Folds != nil && len(sp.Folds.Entry.Content) > 0 {
		lastBlock := len(sp.Folds.Entry.Content) - 1
		if sp.Folds.BlockCursor != lastBlock {
			sp.Folds.BlockCursor = lastBlock
			// Re-render so the cursor marker reflects the new position
			sp.RefreshFoldCursor(a.width, a.splitRatio)
		}
	}
	// Scroll viewport to show the very bottom of the preview
	total := sp.Preview.TotalLineCount()
	maxOffset := max(total-sp.Preview.Height, 0)
	sp.Preview.YOffset = maxOffset
}

func (a *App) focusedArtifactTooltip(sp *SplitPane, width int) string {
	if sp == nil || sp.Folds == nil {
		return ""
	}
	entry := sp.Folds.Entry
	bc := sp.Folds.BlockCursor
	if bc < 0 || bc >= len(entry.Content) {
		return ""
	}
	block := entry.Content[bc]
	switch {
	case block.Type == "image" && block.ImagePasteID > 0:
		cachePath := session.ImageCachePath(homeDir(), a.currentSess.ID, block.ImagePasteID)
		if cachePath == "" {
			// Extract on focus — this is an intentional user action
			cachePath = a.resolveImagePath(block.ImagePasteID)
		}
		label := block.Text
		if label == "" {
			label = "[Image]"
		}
		if cachePath != "" {
			return fmt.Sprintf("Image\n\n%s\n\npaste #%d\n%s", label, block.ImagePasteID, cachePath)
		}
		return fmt.Sprintf("Image\n\n%s\n\npaste #%d\n(image not available)", label, block.ImagePasteID)
	case len(extract.BlockChanges([]session.ContentBlock{block})) > 0:
		if diff := toolDiffOutput(block, max(width/2, 20)); diff != "" {
			return diff
		}
		return "Change artifact"
	case len(extract.BlockFilePaths([]session.ContentBlock{block})) > 0:
		items := extract.BlockFilePaths([]session.ContentBlock{block})
		if len(items) > 0 {
			return "File\n\n" + items[0].URL
		}
	case len(extract.BlockURLs([]session.ContentBlock{block})) > 0:
		items := extract.BlockURLs([]session.ContentBlock{block})
		if len(items) > 0 {
			return "URL\n\n" + items[0].URL
		}
	}
	return ""
}

// kittyImagePath returns the cached or extracted file path for the currently
// focused preview image block, or empty string if the focused block is not a
// renderable image.
func (a *App) kittyImagePath() string {
	if a.state != viewConversation || !kitty.Supported() || !a.termFocused {
		return ""
	}
	sp := &a.conv.split
	if !sp.Focus || !sp.Show || sp.Folds == nil {
		return ""
	}
	bc := sp.Folds.BlockCursor
	if bc < 0 || bc >= len(sp.Folds.Entry.Content) {
		return ""
	}
	block := sp.Folds.Entry.Content[bc]
	if block.Type != "image" || block.ImagePasteID <= 0 {
		return ""
	}
	cachePath := session.ImageCachePath(homeDir(), a.currentSess.ID, block.ImagePasteID)
	if cachePath == "" {
		cachePath = a.resolveImagePath(block.ImagePasteID)
	}
	return cachePath
}

// kittyImageActive returns true if the focused block is a renderable image.
func (a *App) kittyImageActive() bool {
	return a.kittyImagePath() != ""
}

// kittyImageLayer returns Kitty graphics escape sequences to draw an inline
// image covering the full left pane area when a focused image artifact has
// a cached file. Returns a clear command if no image should be drawn.
func (a *App) kittyImageLayer() string {
	if !kitty.Supported() || !a.termFocused || a.state != viewConversation {
		return kitty.ClearImages()
	}

	// Default: focused image artifact in normal conversation view → left pane
	cachePath := a.kittyImagePath()
	if cachePath == "" {
		return kitty.ClearImages()
	}
	sp := &a.conv.split

	listW := sp.ListWidth(a.width, a.splitRatio)
	contentH := a.conversationContentHeight()
	maxCols := max(listW-1, 10)
	maxRows := max(contentH-1, 4)
	imgW, imgH := kitty.ImageSize(cachePath)
	cols, rows := kitty.FitSize(imgW, imgH, maxCols, maxRows)
	imageY := 2 + (maxRows-rows)/2
	imageX := 1
	return kitty.ClearImages() + kitty.PlaceImage(cachePath, imageY, imageX, cols, rows)
}

// renderConvSplit renders the conversation split view.
func (a *App) renderConvSplit() string {
	sp := &a.conv.split
	rendered := sp.Render(a.width, a.conversationLayoutHeight(), a.splitRatio)

	// Show tooltip for selected item when list is focused and tooltip is on.
	// When preview is focused, prefer a tooltip for the focused artifact/block.
	// Skip text tooltip for image blocks when Kitty rendering is active.
	if !sp.PreviewOnly && sp.Focus && sp.Show && !a.kittyImageActive() {
		if tooltip := a.focusedArtifactTooltip(sp, a.width); tooltip != "" {
			contentH := a.conversationContentHeight()
			rendered = overlayTooltip(rendered, tooltip, a.width, contentH, a.convList.Index(), a.convList.Paginator.PerPage, a.conv.split.headerInset, a.convTooltipScroll, a.activeDividerCol())
		}
	} else if !sp.PreviewOnly && a.convTooltipOn && sp.Show && len(a.convList.Items()) > 0 {
		if tooltip := a.convTooltip(); tooltip != "" {
			contentH := a.conversationContentHeight()
			rendered = overlayTooltip(rendered, tooltip, a.width, contentH, a.convList.Index(), a.convList.Paginator.PerPage, a.conv.split.headerInset, a.convTooltipScroll, a.activeDividerCol())
		}
	}

	if rail := a.renderExecutionRail(); rail != "" {
		return rendered + "\n" + rail
	}
	return rendered
}

// convTooltip returns the full text of the selected conversation item, or empty if it fits.
func (a *App) convTooltip() string {
	if a.conv.contextActive {
		return ""
	}
	idx := a.convList.Index()
	items := a.convList.VisibleItems()
	if idx < 0 || idx >= len(items) {
		return ""
	}
	ci, ok := items[idx].(convItem)
	if !ok {
		return ""
	}

	var text string
	switch ci.kind {
	case convMsg:
		text = entryFullText(ci.merged.entry)
	case convTask:
		text = ci.task.Subject
		if ci.task.Description != "" {
			text += "\n" + ci.task.Description
		}
	case convAgent:
		text = ci.agent.FirstPrompt
	}

	if text == "" {
		return ""
	}

	// Only show tooltip if text is longer than list width (would be truncated)
	listW := a.conv.split.ListWidth(a.width, a.splitRatio)
	if len(text) <= listW-15 && !strings.Contains(text, "\n") {
		return ""
	}

	return text
}

// overlayTooltip places a bordered tooltip near the selected item position.
func overlayTooltip(bg, text string, screenW, screenH, cursorIdx, perPage, headerInset, scroll, dividerCol int) string {
	// Tooltip dimensions
	maxW := screenW / 2
	if maxW > 60 {
		maxW = 60
	}
	if maxW < 20 {
		maxW = screenW - 4
	}

	// Wrap text to fit
	wrapped := wrapText(text, maxW-4)
	allLines := strings.Split(wrapped, "\n")
	maxVisible := screenH / 2
	if maxVisible < 5 {
		maxVisible = 5
	}

	// Apply scroll
	total := len(allLines)
	if scroll > total-maxVisible {
		scroll = max(total-maxVisible, 0)
	}
	end := min(scroll+maxVisible, total)
	lines := allLines[scroll:end]

	// Scroll indicators
	if scroll > 0 {
		lines = append([]string{dimStyle.Render(fmt.Sprintf("↑ %d more above", scroll))}, lines...)
	}
	if end < total {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("↓ %d more below (scroll wheel)", total-end)))
	}

	body := strings.Join(lines, "\n")

	tooltipStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7DD3FC")).
		Width(maxW).
		Padding(0, 1)

	tooltip := tooltipStyle.Render(body)

	// Position: right of the list, near the selected item
	tooltipLines := strings.Split(tooltip, "\n")
	tooltipH := len(tooltipLines)

	// Y position: relative to cursor in the visible page
	visibleIdx := cursorIdx % max(perPage, 1)
	y := visibleIdx + headerInset + 1 // fixed context rows + title bar
	if y+tooltipH > screenH {
		y = max(screenH-tooltipH, 1)
	}

	// Overlay onto bg
	bgLines := strings.Split(bg, "\n")
	for i, tl := range tooltipLines {
		row := y + i
		if row >= 0 && row < len(bgLines) {
			bgLine := bgLines[row]
			limit := screenW
			if dividerCol > 0 {
				limit = dividerCol - 1
			}
			// Place tooltip starting at column 2, but never cross the divider.
			bgLines[row] = overlayLine(bgLine, tl, 2, limit)
		}
	}

	return strings.Join(bgLines, "\n")
}

// overlayLine is defined in sessions.go

// extractTaskEntries returns entries related to a specific task.
// It finds ranges where the task was in_progress and collects all entries
// in those ranges, plus the TaskCreate and final TaskUpdate entries.
func extractTaskEntries(entries []session.Entry, taskID string) []session.Entry {
	type taskRange struct{ start, end int }
	var ranges []taskRange
	curStart := -1

	// Map TaskCreate ordinal → entry index, so we can locate the originating
	// TaskCreate for sequentially-numbered task IDs (e.g. "1","2","3",...).
	taskCreateEntryByOrdinal := make(map[int]int)
	taskCreateOrdinal := 0
	for i, e := range entries {
		for _, b := range e.Content {
			if b.Type == "tool_use" && b.ToolName == "TaskCreate" {
				taskCreateOrdinal++
				if _, exists := taskCreateEntryByOrdinal[taskCreateOrdinal]; !exists {
					taskCreateEntryByOrdinal[taskCreateOrdinal] = i
				}
			}
		}
	}

	for i, e := range entries {
		for _, b := range e.Content {
			if b.Type != "tool_use" || !isTaskTool(b.ToolName) {
				continue
			}
			var input struct {
				TaskID string `json:"taskId"`
				Status string `json:"status"`
			}
			json.Unmarshal([]byte(b.ToolInput), &input)
			if input.TaskID != taskID {
				continue
			}
			if input.Status == "in_progress" && curStart < 0 {
				curStart = i
			} else if input.Status == "completed" && curStart >= 0 {
				ranges = append(ranges, taskRange{curStart, i})
				curStart = -1
			}
		}
	}
	// Unclosed range (still in progress)
	if curStart >= 0 {
		ranges = append(ranges, taskRange{curStart, len(entries) - 1})
	}

	if len(ranges) == 0 {
		// Fallback: collect ALL entries that reference this exact task ID.
		// Use JSON field comparison so taskId "1" doesn't match "10", "11", etc.
		// Also include the originating TaskCreate when the task ID is a
		// 1-based ordinal (the in-memory loader assigns them in creation order).
		var result []session.Entry
		seen := make(map[int]bool)
		if ord, err := strconv.Atoi(taskID); err == nil && ord > 0 {
			if idx, ok := taskCreateEntryByOrdinal[ord]; ok {
				result = append(result, entries[idx])
				seen[idx] = true
			}
		}
		for i, e := range entries {
			if seen[i] {
				continue
			}
			for _, b := range e.Content {
				match := false
				if b.Type == "tool_use" && isTaskTool(b.ToolName) {
					var in struct {
						TaskID string `json:"taskId"`
						ID     string `json:"id"`
					}
					if json.Unmarshal([]byte(b.ToolInput), &in) == nil {
						if in.TaskID == taskID || in.ID == taskID {
							match = true
						}
					}
				}
				if !match && b.Type == "tool_result" && b.Text != "" {
					var out struct {
						TaskID string `json:"taskId"`
						ID     string `json:"id"`
					}
					if json.Unmarshal([]byte(b.Text), &out) == nil {
						if out.TaskID == taskID || out.ID == taskID {
							match = true
						}
					}
				}
				if match {
					result = append(result, e)
					seen[i] = true
					break
				}
			}
		}
		return result
	}

	// Collect unique entries from all ranges
	included := make(map[int]bool)
	var result []session.Entry
	for _, r := range ranges {
		for i := r.start; i <= r.end && i < len(entries); i++ {
			if !included[i] {
				included[i] = true
				result = append(result, entries[i])
			}
		}
	}
	return result
}

func (a *App) openCronConversation(cron session.CronItem) (tea.Model, tea.Cmd) {
	cronEntries := extractCronEntries(a.conv.messages, cron)
	if len(cronEntries) == 0 {
		a.copiedMsg = "No entries for cron " + cron.ID
		return a, nil
	}

	merged := filterConversation(mergeConversationTurns(cronEntries))
	agents, _ := session.FindSubagents(a.conv.sess.FilePath)
	items := buildConvItems(a.currentSess, merged, agents, nil, nil, a.conv.flow)

	a.conv.sess = a.currentSess
	a.conv.messages = cronEntries
	a.conv.merged = merged
	a.conv.agents = agents
	a.conv.contextItems = buildConvContextItems(a.conv.sess, merged, a.conv.flow)
	a.conv.contextIndex = 0
	a.conv.contextActive = len(a.conv.contextItems) > 0
	a.conv.items = items
	a.conv.agent = session.Subagent{}
	a.conv.task = session.TaskItem{}
	a.conv.cron = cron
	a.resetDrilldownInspector()

	a.rebuildConversationList(0)

	a.state = viewConversation
	a.updateConvPreview()
	return a, nil
}

func (a *App) taskConversationData(task session.TaskItem) ([]session.Entry, []mergedMsg, bool) {
	entries := extractTaskEntries(a.conv.messages, task.ID)
	if len(entries) == 0 {
		return nil, nil, false
	}
	merged := filterConversation(mergeConversationTurns(entries))
	return entries, merged, len(merged) > 0
}

// openTaskConversation opens a conversation view filtered to entries related to a task.
func (a *App) openTaskConversation(task session.TaskItem) (tea.Model, tea.Cmd) {
	taskEntries, merged, ok := a.taskConversationData(task)
	if !ok {
		a.copiedMsg = "No visible entries for task " + task.ID
		return a, nil
	}

	agents, _ := session.FindSubagents(a.conv.sess.FilePath)
	items := buildConvItems(a.currentSess, merged, agents, nil, nil, a.conv.flow)

	a.conv.sess = a.currentSess
	a.conv.messages = taskEntries
	a.conv.merged = merged
	a.conv.agents = agents
	a.conv.contextItems = buildConvContextItems(a.conv.sess, merged, a.conv.flow)
	a.conv.contextIndex = 0
	a.conv.contextActive = len(a.conv.contextItems) > 0
	a.conv.items = items
	a.conv.agent = session.Subagent{}
	a.conv.task = task
	a.conv.cron = session.CronItem{}
	a.resetDrilldownInspector()

	a.rebuildConversationList(0)

	a.state = viewConversation
	a.updateConvPreview()
	return a, nil
}

// openAgentConversation loads an agent's messages and opens them in conversation split view.
func (a *App) openAgentConversation(agent session.Subagent) (tea.Model, tea.Cmd) {
	entries, err := session.LoadMessages(agent.FilePath)
	if err != nil || len(entries) == 0 {
		a.copiedMsg = "No agent messages"
		return a, nil
	}

	// For aside/subagents, skip the injected context summary (first user message
	// that starts with "This session is being continued...").
	entries = filterAgentContextEntries(entries)

	// For side-question agents, collapse the parent session context
	if agent.AgentType == "aside_question" {
		entries = filterSideQuestionContext(entries)
	}
	if len(entries) == 0 {
		a.copiedMsg = "No agent messages"
		return a, nil
	}

	// Inline drilldown and execution-rail switching share the same transcript
	// contexts. Save the current context before changing ActiveKey so selecting
	// it again from the rail restores its exact list/filter/inspector state.
	a.saveExecutionViewState()
	merged := filterConversation(mergeConversationTurns(entries))
	agentSess := a.currentSess
	agentSess.ID = agent.ID
	agentSess.FilePath = agent.FilePath
	// Keep the session-wide graph while changing only the visible transcript.
	// Rebuilding from an agent file loses sibling transcripts and workflow
	// summaries, which breaks nested agent/workflow connections.
	flow := a.conv.flow
	if flow == nil {
		flow, _ = session.BuildSessionFlow(&a.currentSess)
	}
	var agents []session.Subagent
	if flow != nil {
		agents = flow.Agents()
	}
	items := buildConvItems(agentSess, merged, agents, nil, nil, flow)

	a.conv.sess = agentSess
	a.conv.flow = flow
	a.conv.messages = entries
	a.conv.merged = merged
	a.conv.agents = agents
	a.conv.toolUseToAgent = buildToolUseToAgentMap(entries)
	a.conv.contextItems = buildConvContextItems(a.conv.sess, merged, a.conv.flow)
	a.conv.contextIndex = 0
	a.conv.contextActive = len(a.conv.contextItems) > 0
	a.conv.items = items
	a.conv.agent = agent
	a.conv.execution.ActiveKey = executionContextKey(agent.FilePath)
	a.conv.execution.CursorKey = a.conv.execution.ActiveKey
	a.conv.task = session.TaskItem{}
	a.conv.cron = session.CronItem{}
	a.resetDrilldownInspector()

	a.rebuildConversationList(0)

	a.state = viewConversation
	a.updateConvPreview()
	return a, nil
}

// openConvAsText exports the conversation as plain text and opens it in $EDITOR.
func (a *App) openConvAsText() (tea.Model, tea.Cmd) {
	if len(a.conv.merged) == 0 {
		a.copiedMsg = "No messages"
		return a, nil
	}
	content := stripANSI(renderAllMessages(a.conv.merged, 80))
	tmpFile, err := os.CreateTemp("", "ccx-conv-*.txt")
	if err != nil {
		a.copiedMsg = "Error: " + err.Error()
		return a, nil
	}
	tmpFile.WriteString(content)
	tmpFile.Close()
	return a.openInEditor(tmpFile.Name())
}

// --- Block filter for conversation preview ---

// startBlockFilter activates the block filter input in the preview pane.
func (a *App) startBlockFilter() {
	ti := textinput.New()
	ti.Prompt = "Filter: "
	ti.Placeholder = "is:hook is:tool is:mcp tool:Grep tool:mcp* is:error ..."
	ti.CharLimit = 200
	ti.Width = a.conv.split.PreviewWidth(a.width, a.splitRatio) - 10
	// Pre-fill with existing filter
	if a.conv.split.Folds != nil && a.conv.split.Folds.BlockFilter != "" {
		ti.SetValue(a.conv.split.Folds.BlockFilter)
	}
	ti.Focus()
	a.conv.blockFilterTI = ti
	a.conv.blockFiltering = true
}

// handleBlockFilterInput handles key events while the block filter input is active.
func (a *App) handleBlockFilterInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "enter":
		a.commitBlockFilter()
		return a, nil
	case "esc":
		a.conv.blockFiltering = false
		return a, nil
	}
	var cmd tea.Cmd
	a.conv.blockFilterTI, cmd = a.conv.blockFilterTI.Update(msg)
	return a, cmd
}

// commitBlockFilter applies the filter and refreshes the preview.
func (a *App) commitBlockFilter() {
	a.conv.blockFiltering = false
	sp := &a.conv.split
	if sp.Folds == nil {
		return
	}
	filter := a.conv.blockFilterTI.Value()
	sp.Folds.BlockFilter = filter
	sp.Folds.BlockVisible = applyBlockFilter(filter, sp.Folds.Entry)

	// Move block cursor to first visible block
	if first := sp.Folds.firstVisibleBlock(); first >= 0 {
		sp.Folds.BlockCursor = first
	}

	sp.CacheKey = "" // force re-render
	sp.RefreshFoldPreview(a.width, a.splitRatio)
	sp.Preview.YOffset = 0
}

// clearBlockFilter removes the block filter and shows all blocks.
func (a *App) clearBlockFilter() {
	sp := &a.conv.split
	if sp.Folds == nil {
		return
	}
	sp.Folds.BlockFilter = ""
	sp.Folds.BlockVisible = nil
	sp.CacheKey = "" // force re-render
	sp.RefreshFoldPreview(a.width, a.splitRatio)
}

// renderBlockFilterHintBox renders a floating hint box for block filter syntax.
func renderBlockFilterHintBox() string {
	h := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8"))
	d := dimStyle

	lines := []string{
		h.Render("is:") + d.Render("tool result error text thinking hook skill image"),
		h.Render("tool:") + d.Render("Bash Read Edit Write Grep Glob Agent Skill"),
		h.Render("!") + d.Render("negate") + "  " + d.Render("space=AND  free text search"),
	}

	body := strings.Join(lines, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}
