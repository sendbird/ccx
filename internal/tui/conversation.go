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
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"
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
	a.conv.messages = entries
	a.conv.merged = filterConversation(mergeConversationTurns(entries))
	a.conv.agent = session.Subagent{}
	a.conv.task = session.TaskItem{}
	a.conv.cron = session.CronItem{}
	a.conv.toolUseToAgent = buildToolUseToAgentMap(entries)

	// Build conversation items — use file-based tasks/crons, or extract from JSONL
	tasks := sess.Tasks
	if len(tasks) == 0 {
		tasks = extractInlineTasks(entries)
		sess.Tasks = tasks
	}
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
	a.conv.items = buildConvItems(sess, a.conv.merged, agents, tasks, crons, flow)

	// Reset artifact page browser state on fresh conversation open.
	a.convPageActive = false
	a.convPageMenu = false
	a.convPageActionsMenu = false
	a.convPage = convPageURLs
	a.convPageItems = nil
	a.convPageChangeMap = nil
	a.convPageCursor = 0

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
		// Select last item
		items := a.convList.Items()
		if len(items) > 0 {
			a.convList.Select(len(items) - 1)
		}
		a.updateConvPreview()
		a.scrollConvPreviewToTail()
		return liveTickCmd()
	}

	// Select first message
	a.updateConvPreview()
	return nil
}

func (a *App) pauseLiveTail() {
	if a.liveTail {
		a.liveTail = false
		a.conv.split.BottomAlign = false
	}
}

// handleConversationKeys handles keyboard input for the conversation split view.
func (a *App) convPageSelectedItem() *convPageItem {
	if a.convPageCursor < 0 || a.convPageCursor >= len(a.convPageItems) {
		return nil
	}
	return &a.convPageItems[a.convPageCursor]
}

func (a *App) convPageItemResolvedTarget(item convPageItem) string {
	if a.convPage == convPageImages {
		if item.URL != "" && !strings.HasPrefix(item.URL, "paste:") {
			return item.URL
		}
		if item.imagePasteID > 0 {
			return a.resolveImagePath(item.imagePasteID)
		}
	}
	return item.URL
}

func (a *App) convPageOpenSelected() (tea.Model, tea.Cmd) {
	item := a.convPageSelectedItem()
	if item == nil {
		return a, nil
	}
	if a.convPage == convPageContexts {
		node := session.ContextNode{
			RelatedView:                item.relatedView,
			RelatedPath:                item.relatedPath,
			RelatedPluginID:            item.relatedPluginID,
			RelatedPluginComponentPath: item.relatedPluginComponentPath,
			RelatedPluginComponentType: item.relatedPluginComponentType,
		}
		return a.openRelatedContextNode(node)
	}
	switch a.convPage {
	case convPageImages:
		if item.imagePasteID > 0 {
			return a.openCachedImage(item.imagePasteID)
		}
	case convPageFiles, convPageChanges:
		target := a.convPageItemResolvedTarget(*item)
		if target != "" {
			return a.openInEditor(target)
		}
	case convPageURLs:
		if item.URL != "" {
			if err := a.openInBrowser(item.URL); err == nil {
				a.copiedMsg = "Opened URL"
			}
		}
	}
	return a, nil
}

func (a *App) convPageEditSelected() (tea.Model, tea.Cmd) {
	item := a.convPageSelectedItem()
	if item == nil {
		return a, nil
	}
	if a.convPage == convPageFiles || a.convPage == convPageChanges || a.convPage == convPageImages {
		target := a.convPageItemResolvedTarget(*item)
		if target != "" {
			return a.openInEditor(target)
		}
	}
	return a, nil
}

func (a *App) convPageCopySelected() (tea.Model, tea.Cmd) {
	item := a.convPageSelectedItem()
	if item == nil {
		return a, nil
	}
	target := item.URL
	if a.convPage == convPageContexts && item.relatedPath != "" {
		target = item.relatedPath
	}
	if a.convPage == convPageFiles || a.convPage == convPageChanges || a.convPage == convPageImages {
		target = a.convPageItemResolvedTarget(*item)
	}
	if target != "" {
		copyToClipboard(target)
		a.copiedMsg = "Copied path"
	}
	return a, nil
}

func (a *App) handleConversationKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sp := &a.conv.split
	key := msg.String()

	// Artifact page actions menu
	if a.convPageActionsMenu {
		a.convPageActionsMenu = false
		switch key {
		case "enter", "o":
			return a.convPageOpenSelected()
		case "e":
			return a.convPageEditSelected()
		case "y":
			return a.convPageCopySelected()
		case "x":
			a.convPageActionsMenu = true
			return a, nil
		}
		return a, nil
	}

	// Page jump menu: second key picks the page
	if a.convPageMenu {
		a.convPageMenu = false
		return a.handleConvPageMenu(key)
	}

	// Dedicated conversation artifact page browser
	if a.convPageActive {
		// Search input active: intercept all keys
		if a.convPageSearching {
			switch key {
			case "enter":
				a.convPageSearching = false
				a.convPageSearchTerm = a.convPageSearchTI.Value()
				a.applyConvPageFilter()
				return a, nil
			case "esc":
				a.convPageSearching = false
				a.convPageSearchTI.SetValue("")
				// Restore unfiltered items
				if a.convPageAllItems != nil {
					a.convPageItems = a.convPageAllItems
					a.convPageAllItems = nil
					a.convPageSearchTerm = ""
					a.convPageCursor = 0
				}
				return a, nil
			default:
				var cmd tea.Cmd
				a.convPageSearchTI, cmd = a.convPageSearchTI.Update(msg)
				// Live filter as user types
				a.convPageSearchTerm = a.convPageSearchTI.Value()
				a.applyConvPageFilter()
				return a, cmd
			}
		}

		// Right pane focused: scroll keys move viewport
		if a.convPageFocus {
			switch key {
			case "esc", "left", "h":
				a.convPageFocus = false
				return a, nil
			case "up", "k":
				a.convPageVP.LineUp(1)
				return a, nil
			case "down", "j":
				a.convPageVP.LineDown(1)
				return a, nil
			case "pgup":
				a.convPageVP.ViewUp()
				return a, nil
			case "pgdown":
				a.convPageVP.ViewDown()
				return a, nil
			case "g", "home":
				a.convPageVP.GotoTop()
				return a, nil
			case "G", "end":
				a.convPageVP.GotoBottom()
				return a, nil
			case "p":
				a.convPageMenu = true
				return a, nil
			case "x":
				a.convPageActionsMenu = true
				return a, nil
			case "i":
				a.convPageKitty = !a.convPageKitty
				return a, nil
			case "/":
				a.convPageFocus = false
				a.startConvPageSearch()
				return a, nil
			case "[":
				a.adjustSplitRatio(-5)
				return a, nil
			case "]":
				a.adjustSplitRatio(5)
				return a, nil
			default:
				return a, nil
			}
		}

		// Left pane (list) focused
		switch key {
		case "p":
			a.convPageMenu = true
			return a, nil
		case "esc":
			// First esc clears filter, second esc closes browser
			if a.convPageSearchTerm != "" {
				a.convPageSearchTerm = ""
				a.convPageSearchTI.SetValue("")
				if a.convPageAllItems != nil {
					a.convPageItems = a.convPageAllItems
					a.convPageAllItems = nil
					a.convPageCursor = 0
					a.convPageLastCursor = -1
				}
				return a, nil
			}
			a.convPageActive = false
			a.convPageFocus = false
			a.convPageActionsMenu = false
			a.convPageItems = nil
			a.convPageAllItems = nil
			a.convPageChangeMap = nil
			a.convPageSearchTerm = ""
			a.conv.split.CacheKey = ""
			a.updateConvPreview()
			return a, nil
		case "right", "l", "tab":
			a.convPageFocus = true
			return a, nil
		case "up", "k":
			if a.convPageCursor > 0 {
				a.convPageCursor--
			}
			return a, nil
		case "down", "j":
			if a.convPageCursor < len(a.convPageItems)-1 {
				a.convPageCursor++
			}
			return a, nil
		case "g", "home":
			if len(a.convPageItems) > 0 {
				a.convPageCursor = 0
			}
			return a, nil
		case "G", "end":
			if len(a.convPageItems) > 0 {
				a.convPageCursor = len(a.convPageItems) - 1
			}
			return a, nil
		case "pgdown":
			if len(a.convPageItems) > 0 {
				page := max(ContentHeight(a.height)-3, 1)
				a.convPageCursor = min(a.convPageCursor+page, len(a.convPageItems)-1)
			}
			return a, nil
		case "pgup":
			if len(a.convPageItems) > 0 {
				page := max(ContentHeight(a.height)-3, 1)
				a.convPageCursor = max(a.convPageCursor-page, 0)
			}
			return a, nil
		case "x":
			a.convPageActionsMenu = true
			return a, nil
		case "i":
			a.convPageKitty = !a.convPageKitty
			return a, nil
		case "/":
			a.startConvPageSearch()
			return a, nil
		case "[":
			a.adjustSplitRatio(-5)
			return a, nil
		case "]":
			a.adjustSplitRatio(5)
			return a, nil
		default:
			return a, nil
		}
	}

	// Block filter input intercepts all keys
	if a.conv.blockFiltering {
		return a.handleBlockFilterInput(msg)
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
		// Clear block filter first
		if sp.Folds != nil && sp.Folds.BlockFilter != "" {
			a.clearBlockFilter()
			return a, nil
		}
		if !sp.Show {
			a.liveTail = false
			a.conv.split.BottomAlign = false
			if a.conv.task.ID != "" || a.conv.agent.ShortID != "" || a.conv.cron.ID != "" {
				// Pop one level back into the originating conv view and
				// re-open the preview pane. The next ESC will close that
				// preview before considering further navigation, so the
				// user lands cleanly in the parent conv view instead of
				// skipping past it to the session list.
				m, cmd := a.popNavFrame()
				if app, ok := m.(*App); ok {
					app.conv.split.Show = true
					app.conv.split.CacheKey = ""
					app.updateConvPreview()
				}
				return m, cmd
			}
			// Plain conv view (no drilldown, preview already closed): ESC
			// exits back to the session list, matching `left`.
			a.state = viewSessions
			return a, nil
		}
	case "enter":
		item, ok := a.convList.SelectedItem().(convItem)
		if !ok {
			return a, nil
		}
		// Toggle fold on expandable group headers; marker headers jump to agent
		if item.groupTag != "" {
			if item.count > 0 {
				a.toggleConvGroupFold(item)
				return a, nil
			}
			// Marker header (count==0): try to jump to an agent referenced in parent message
			if agent, ok := a.findAgentInParentMsg(item); ok {
				a.pushNavFrame()
				return a.openAgentConversation(agent)
			}
			// No agent found (background task) — open parent message detail view
			items := a.convList.Items()
			if item.parentIdx >= 0 && item.parentIdx < len(items) {
				if parent, ok := items[item.parentIdx].(convItem); ok && parent.kind == convMsg {
					a.pushNavFrame()
					return a.openMsgFullForEntry(parent.merged)
				}
			}
			return a, nil
		}
		switch item.kind {
		case convTask:
			// Background task sub-item: find the message with TaskOutput result and open it
			if item.bgTaskID != "" {
				if m, blockIdx, ok := a.findBgTaskResultMsg(item.bgTaskID); ok {
					a.pushNavFrame()
					return a.openMsgFullForEntryAt(m, blockIdx)
				}
				// Fallback: open parent message
				items := a.convList.Items()
				if item.parentIdx >= 0 && item.parentIdx < len(items) {
					if parent, ok := items[item.parentIdx].(convItem); ok && parent.kind == convMsg {
						a.pushNavFrame()
						return a.openMsgFullForEntry(parent.merged)
					}
				}
				return a, nil
			}
			// If this task has a corresponding agent (via TaskOutput), jump to it
			if item.groupTag == "" {
				if agents := a.findTaskAgents(); len(agents) == 1 {
					a.pushNavFrame()
					return a.openAgentConversation(agents[0])
				}
			}
			// Tasks without a real ID (e.g. TaskCreate-only items built from
			// the tool input alone) can't be filtered by ID. Falling through
			// to openTaskConversation would mismatch and surface an unrelated
			// task's content. Open the parent message in msgFull instead so
			// the user lands on the turn that defined the task.
			if item.task.ID == "" {
				items := a.convList.Items()
				if item.parentIdx >= 0 && item.parentIdx < len(items) {
					if parent, ok := items[item.parentIdx].(convItem); ok && parent.kind == convMsg {
						a.pushNavFrame()
						return a.openMsgFullForEntry(parent.merged)
					}
				}
				return a, nil
			}
			// Otherwise drill into task — show conversation entries related to this task
			a.pushNavFrame()
			return a.openTaskConversation(item.task)
		case convAgent:
			// Push nav stack and open agent as conversation split view
			a.pushNavFrame()
			return a.openAgentConversation(item.agent)
		case convMsg:
			// If preview focused on a block, check for actionable types
			if sp.Focus && sp.Folds != nil {
				bc := sp.Folds.BlockCursor
				entry := sp.Folds.Entry
				if bc >= 0 && bc < len(entry.Content) {
					block := entry.Content[bc]
					// Open cached image
					if block.Type == "image" && block.ImagePasteID > 0 {
						return a.openCachedImage(block.ImagePasteID)
					}
					// Jump to agent for Agent/Task tool_use blocks
					if block.Type == "tool_use" && (block.ToolName == "Agent" || block.ToolName == "Task") {
						if agent, found := a.findAgentForConv(entry); found {
							a.pushNavFrame()
							return a.openAgentConversation(agent)
						}
					}
				}
			}
			// Open full-screen detail for this message
			a.pushNavFrame()
			return a.openMsgFullForEntry(item.merged)
		}
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
		if item, ok := a.convList.SelectedItem().(convItem); ok && item.kind != convMsg {
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
		if sp.Focus && a.conv.rightPaneMode == previewText {
			a.enterCopyMode()
			return a, nil
		}
	case "p":
		a.convPageMenu = true
		return a, nil
	}

	// Tab moves between the unified flow and inspector. Detail level remains
	// available through the explicit detail commands and structured preview keys.
	if key == "tab" || key == "shift+tab" {
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
	result := sp.HandleSplitKey(key, a.width, a.height, a.splitRatio, a.adjustSplitRatio)
	switch result {
	case splitKeyClosed:
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
			if a.conv.task.ID != "" || a.conv.agent.ShortID != "" || a.conv.cron.ID != "" {
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
				return a, nil
			}
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

	// List boundary
	if !sp.Focus && sp.HandleListBoundary(key) {
		a.pauseLiveTail()
		if sp.Show {
			a.updateConvPreview()
		}
		return a, nil
	}

	// Default list update
	oldIdx := a.convList.Index()
	m, cmd := a.convList.Update(msg)
	a.convList = m
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

// convPreviewBoundaryCross advances to the next/prev list item when the block
// cursor hits the top or bottom boundary of the current preview.
func (a *App) convPreviewBoundaryCross(key string) (tea.Model, tea.Cmd) {
	sp := &a.conv.split
	idx := a.convList.Index()
	items := a.convList.Items()
	n := len(items)

	switch key {
	case "down":
		// Find next convMsg item after current index
		for i := idx + 1; i < n; i++ {
			if ci, ok := items[i].(convItem); ok && ci.kind == convMsg {
				a.convList.Select(i)
				sp.CacheKey = ""
				a.updateConvPreview()
				// Position cursor at first block
				if sp.Folds != nil {
					if first := sp.Folds.firstVisibleBlock(); first >= 0 {
						sp.Folds.BlockCursor = first
					}
				}
				sp.RefreshFoldCursor(a.width, a.splitRatio)
				sp.ScrollToBlock()
				return a, nil
			}
		}
	case "up":
		// Find prev convMsg item before current index
		for i := idx - 1; i >= 0; i-- {
			if ci, ok := items[i].(convItem); ok && ci.kind == convMsg {
				a.convList.Select(i)
				sp.CacheKey = ""
				a.updateConvPreview()
				// Position cursor at last block
				if sp.Folds != nil {
					if last := sp.Folds.lastVisibleBlock(); last >= 0 {
						sp.Folds.BlockCursor = last
					}
				}
				sp.RefreshFoldCursor(a.width, a.splitRatio)
				sp.ScrollToBlock()
				return a, nil
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

	item, ok := a.convList.SelectedItem().(convItem)
	if !ok {
		return
	}

	baseKey := convPreviewBaseKey(item)
	oldCacheKey := sp.CacheKey
	anchor := captureConvPreviewAnchor(sp, baseKey)
	if item.kind == convAgent && oldCacheKey != "" && strings.HasPrefix(oldCacheKey, baseKey+":") {
		debugLog.Printf("updateConvPreview: CACHE HIT key=%q (agent)", oldCacheKey)
		return
	}

	var build previewBuild
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
		a.setConvPreviewText(renderPhaseInspector(item.workflow, item.phase))
		return
	case convShell:
		a.setConvPreviewText(renderShellInspector(item.shell))
		return
	case convDecision:
		a.setConvPreviewText(renderDecisionInspector(item.decision))
		return
	case convSessionMeta:
		switch item.sessionMeta {
		case "summary":
			a.setConvPreviewText(a.renderFlowSummary())
		case "memory":
			a.setConvPreviewText(a.buildMemoryContent(a.conv.sess))
		default:
			a.setConvPreviewText(a.buildTasksPlanContent(a.conv.sess))
		}
		return
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
	var entry session.Entry
	var blockSrcIdx []int
	switch a.conv.rightPaneMode {
	case previewText:
		entry, blockSrcIdx = compactPreview(build)
	case previewTool:
		entry, blockSrcIdx = standardPreview(build)
	default:
		entry, blockSrcIdx = verbosePreview(build)
	}

	cacheKey := fmt.Sprintf("%s:%d:%x", baseKey, len(entry.Content), entryContentHash(entry.Content))
	if cacheKey == sp.CacheKey {
		debugLog.Printf("updateConvPreview: CACHE HIT key=%q", cacheKey)
		return
	}

	isNewEntry := oldCacheKey == "" || !strings.HasPrefix(oldCacheKey, baseKey+":")
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

func renderPhaseInspector(run session.WorkflowRun, phase session.WorkflowPhase) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Phase: %s\n\n%s\n", phase.Title, phase.Detail)
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

func renderDecisionInspector(artifact session.Artifact) string {
	data, _ := artifact.Data.(session.DecisionData)
	var b strings.Builder
	fmt.Fprintf(&b, "# Decision: %s\n\nKind: %s\n", data.Label, data.Kind)
	fmt.Fprintf(&b, "Origin: %s · entry %d · block %d\n", artifact.Origin.Transcript, artifact.Origin.EntryIndex+1, artifact.Origin.BlockIndex+1)
	if data.Related != "" {
		fmt.Fprintf(&b, "Related: %s\n", data.Related)
	}
	b.WriteString("\nPress J to jump to the originating turn.\n")
	return b.String()
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

func makeConvPageItem(item extract.Item, ts time.Time, turnPreview, userPrompt string, imagePasteID int) convPageItem {
	return makeConvPageItemWithTarget(item, ts, turnPreview, userPrompt, imagePasteID, "", "", "", "")
}

func makeConvPageItemWithTarget(item extract.Item, ts time.Time, turnPreview, userPrompt string, imagePasteID int, relatedView, relatedPath, relatedPluginID, relatedPluginComponentPath string) convPageItem {
	return convPageItem{
		Item:                       item,
		timestamp:                  ts,
		turnPreview:                turnPreview,
		userPrompt:                 userPrompt,
		imagePasteID:               imagePasteID,
		relatedView:                relatedView,
		relatedPath:                relatedPath,
		relatedPluginID:            relatedPluginID,
		relatedPluginComponentPath: relatedPluginComponentPath,
	}
}

func sortConvPageItemsByTime(items []convPageItem) {
	sort.SliceStable(items, func(i, j int) bool {
		ti, tj := items[i].timestamp, items[j].timestamp
		if ti.Equal(tj) {
			return i < j
		}
		if ti.IsZero() {
			return false
		}
		if tj.IsZero() {
			return true
		}
		return ti.After(tj)
	})
}

func convPageItemContext(item convPageItem, width int) string {
	var sections []string
	if !item.timestamp.IsZero() {
		sections = append(sections, dimStyle.Render("Timestamp")+"\n"+item.timestamp.Format("2006-01-02 15:04:05"))
	}
	if item.turnPreview != "" {
		sections = append(sections, dimStyle.Render("Turn")+"\n"+wrapText(item.turnPreview, width))
	}
	if item.userPrompt != "" {
		sections = append(sections, dimStyle.Render("Related user prompt")+"\n"+wrapText(item.userPrompt, width))
	}
	if item.relatedView != "" {
		sections = append(sections, dimStyle.Render("Related view")+"\n"+wrapText(item.relatedView, width))
	}
	return strings.Join(sections, "\n\n")
}

func renderFilePreview(path string, width int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return dimStyle.Render("(file preview unavailable)")
	}
	const maxBytes = 4000
	text := string(data)
	truncated := false
	if len(text) > maxBytes {
		text = text[:maxBytes]
		truncated = true
	}
	out := wrapText(text, width)
	if truncated {
		out += "\n\n" + dimStyle.Render("(truncated)")
	}
	return out
}

// windowRange returns start/end indices to display around cursor within visibleRows.
func windowRange(total, cursor, visibleRows int) (int, int) {
	if total <= visibleRows {
		return 0, total
	}
	half := visibleRows / 2
	start := cursor - half
	if start < 0 {
		start = 0
	}
	end := start + visibleRows
	if end > total {
		end = total
		start = max(end-visibleRows, 0)
	}
	return start, end
}

func (a *App) startConvPageSearch() {
	a.convPageSearching = true
	a.convPageSearchTI = textinput.New()
	a.convPageSearchTI.Prompt = "Search: "
	a.convPageSearchTI.Focus()
	// Save unfiltered items on first search
	if a.convPageAllItems == nil {
		a.convPageAllItems = a.convPageItems
	}
}

func (a *App) applyConvPageFilter() {
	if a.convPageAllItems == nil {
		return
	}
	term := strings.ToLower(a.convPageSearchTerm)
	if term == "" {
		a.convPageItems = a.convPageAllItems
	} else {
		var filtered []convPageItem
		for _, item := range a.convPageAllItems {
			text := strings.ToLower(item.Label + " " + item.URL + " " + item.userPrompt)
			if strings.Contains(text, term) {
				filtered = append(filtered, item)
			}
		}
		a.convPageItems = filtered
	}
	a.convPageCursor = 0
	a.convPageLastCursor = -1
}

func convPageTitle(kind convPageKind) string {
	switch kind {
	case convPageURLs:
		return "URLs"
	case convPageImages:
		return "Images"
	case convPageChanges:
		return "Changes"
	case convPageFiles:
		return "Files"
	case convPageContexts:
		return "Contexts"
	default:
		return "Conversation"
	}
}

func (a *App) renderConvPageBrowser() string {
	contentH := ContentHeight(a.height)
	browserRatio := a.splitRatio
	// Calculate widths directly — don't use sp.ListWidth/PreviewWidth which
	// return full width when sp.Show is false. The browser always needs a split.
	listW := max(a.width*browserRatio/100, 30)
	previewW := max(a.width-listW-1, 1)

	var left strings.Builder
	title := convPageTitle(a.convPage)
	n := len(a.convPageItems)
	left.WriteString(dimStyle.Render(fmt.Sprintf("── %s (%d) ──", title, n)) + "\n\n")
	if n == 0 {
		left.WriteString(dimStyle.Render("(no items)"))
	} else {
		// Window the list so the cursor is always visible.
		// Header takes 2 lines, leaving visibleRows for items.
		// Reserve rows for ↑/↓ indicators before computing the window
		// so the cursor never falls outside the visible range.
		visibleRows := max(contentH-2, 1)
		start, end := windowRange(n, a.convPageCursor, visibleRows)
		needTop := start > 0
		needBottom := end < n
		if needTop || needBottom {
			// Recompute with reduced rows to leave room for indicators
			reserved := 0
			if needTop {
				reserved++
			}
			if needBottom {
				reserved++
			}
			itemRows := max(visibleRows-reserved, 1)
			start, end = windowRange(n, a.convPageCursor, itemRows)
			needTop = start > 0
			needBottom = end < n
		}
		if needTop {
			left.WriteString(dimStyle.Render(fmt.Sprintf("  ↑ %d more", start)) + "\n")
		}
		prevPrompt := ""
		if start > 0 {
			prevPrompt = a.convPageItems[start-1].userPrompt
		}
		for i := start; i < end; i++ {
			item := a.convPageItems[i]
			// Show separator when user prompt changes between items
			if i > start && item.userPrompt != prevPrompt {
				left.WriteString(dimStyle.Render("  ─") + "\n")
			}
			prevPrompt = item.userPrompt
			cursor := " "
			style := dimStyle
			if i == a.convPageCursor {
				cursor = ">"
				style = selectedStyle
			}
			label := item.Label
			if label == "" {
				label = item.URL
			}
			if lipgloss.Width(label) > listW-4 {
				label = truncate(label, max(listW-7, 1))
			}
			left.WriteString(cursor + " " + style.Render(label) + "\n")
		}
		if needBottom {
			left.WriteString(dimStyle.Render(fmt.Sprintf("  ↓ %d more", n-end)) + "\n")
		}
	}

	rightContent := dimStyle.Render("(no selection)")
	if a.convPageCursor >= 0 && a.convPageCursor < len(a.convPageItems) {
		item := a.convPageItems[a.convPageCursor]
		pw := max(previewW, 10)
		// Metadata first, then page-specific content below
		context := convPageItemContext(item, pw)
		var detail string
		switch a.convPage {
		case convPageChanges:
			if a.convPageChangeMap != nil {
				if ch, ok := a.convPageChangeMap[item.URL]; ok && len(ch.ToolInputs) > 0 {
					block := session.ContentBlock{Type: "tool_use", ToolName: ch.ToolNames[0], ToolInput: ch.ToolInputs[0]}
					if diff := toolDiffOutput(block, pw); diff != "" {
						detail = diff
						goto done
					}
				}
			}
			detail = wrapText(item.URL, pw)
		case convPageContexts:
			detail = wrapText(item.URL, pw)
		case convPageImages:
			id := strings.TrimPrefix(item.URL, "paste:")
			var pasteID int
			fmt.Sscanf(id, "%d", &pasteID)
			parts := []string{wrapText(item.Label, pw)}
			if pasteID > 0 {
				parts = append(parts, fmt.Sprintf("paste #%d", pasteID))
			}
			if cachePath := session.ImageCachePath(homeDir(), a.currentSess.ID, pasteID); cachePath != "" {
				parts = append(parts, wrapText(cachePath, pw))
			}
			detail = strings.Join(parts, "\n")
		case convPageFiles:
			detail = dimStyle.Render("["+item.Category+"]") + " " + wrapText(item.URL, pw)
		case convPageURLs:
			detail = wrapText(item.URL, pw)
		default:
			detail = wrapText(item.URL, pw)
		}
	done:
		rightContent = context + "\n\n" + dimStyle.Render("────") + "\n\n" + detail
	}

	rightContent = clampLines(rightContent, max(previewW, 1))

	// Update viewport when cursor changes or viewport size differs
	if a.convPageCursor != a.convPageLastCursor ||
		a.convPageVP.Width != previewW || a.convPageVP.Height != contentH {
		a.convPageVP = viewport.New(previewW, contentH)
		a.convPageVP.SetContent(rightContent)
		a.convPageLastCursor = a.convPageCursor
	}

	borderColor := colorBorderDim
	if a.convPageFocus {
		borderColor = colorBorderFocused
	}
	return renderFixedSplit(left.String(), a.convPageVP.View(), listW, previewW, contentH, borderColor)
}

// clampLines truncates each line to maxW display width, preserving ANSI escapes.
func clampLines(s string, maxW int) string {
	if maxW <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if ansi.StringWidth(line) > maxW {
			lines[i] = ansi.Truncate(line, maxW, "")
		}
	}
	return strings.Join(lines, "\n")
}

func relatedUserPrompt(messages []session.Entry, idx int) string {
	for i := idx - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			text := entryFullText(messages[i])
			if text != "" {
				return text
			}
		}
	}
	return ""
}

func (a *App) openConvImagesPage() (tea.Model, tea.Cmd) {
	a.convPageActive = true
	a.convPageFocus = false
	a.convPageLastCursor = -1
	a.convPageKitty = true
	a.convPage = convPageImages
	a.convPageItems = nil
	for idx, e := range a.conv.messages {
		for _, b := range e.Content {
			if b.Type == "image" && b.ImagePasteID > 0 {
				label := b.Text
				if label == "" {
					label = "[Image]"
				}
				a.convPageItems = append(a.convPageItems, makeConvPageItem(
					extract.Item{URL: fmt.Sprintf("paste:%d", b.ImagePasteID), Label: fmt.Sprintf("[%s] paste #%d", label, b.ImagePasteID), Category: "image"},
					e.Timestamp,
					label,
					relatedUserPrompt(a.conv.messages, idx),
					b.ImagePasteID,
				))
			}
		}
	}
	a.convPageCursor = 0
	sortConvPageItemsByTime(a.convPageItems)
	return a, nil
}

func (a *App) openConvURLsPage() (tea.Model, tea.Cmd) {
	a.convPageActive = true
	a.convPageFocus = false
	a.convPageLastCursor = -1
	a.convPage = convPageURLs
	a.convPageItems = nil
	seen := make(map[string]int)
	for idx, m := range a.conv.merged {
		for _, item := range extract.BlockURLs(m.entry.Content) {
			if existing, ok := seen[item.URL]; ok {
				a.convPageItems[existing].timestamp = m.entry.Timestamp
				a.convPageItems[existing].turnPreview = convMsgPreview(m.entry, 80)
				a.convPageItems[existing].userPrompt = relatedUserPrompt(a.conv.messages, idx)
				continue
			}
			seen[item.URL] = len(a.convPageItems)
			a.convPageItems = append(a.convPageItems, makeConvPageItem(item, m.entry.Timestamp, convMsgPreview(m.entry, 80), relatedUserPrompt(a.conv.messages, idx), 0))
		}
	}
	sortConvPageItemsByTime(a.convPageItems)
	a.convPageCursor = 0
	return a, nil
}

func (a *App) openConvFilesPage() (tea.Model, tea.Cmd) {
	a.convPageActive = true
	a.convPageFocus = false
	a.convPageLastCursor = -1
	a.convPage = convPageFiles
	a.convPageItems = nil
	seen := make(map[string]int)
	for idx, m := range a.conv.merged {
		for _, item := range extract.BlockModifiedFiles(m.entry.Content) {
			if existing, ok := seen[item.URL]; ok {
				a.convPageItems[existing].timestamp = m.entry.Timestamp
				a.convPageItems[existing].turnPreview = convMsgPreview(m.entry, 80)
				a.convPageItems[existing].userPrompt = relatedUserPrompt(a.conv.messages, idx)
				continue
			}
			seen[item.URL] = len(a.convPageItems)
			a.convPageItems = append(a.convPageItems, makeConvPageItem(item, m.entry.Timestamp, convMsgPreview(m.entry, 80), relatedUserPrompt(a.conv.messages, idx), 0))
		}
	}
	sortConvPageItemsByTime(a.convPageItems)
	a.convPageCursor = 0
	return a, nil
}

func flattenContextNodes(nodes []session.ContextNode, prefix string, items *[]convPageItem) {
	for _, node := range nodes {
		label := prefix + node.Label
		if node.Count > 0 {
			label += fmt.Sprintf(" [%d]", node.Count)
		}
		meta := []string{}
		if node.Status != "" {
			meta = append(meta, node.Status)
		}
		if node.Detail != "" {
			meta = append(meta, node.Detail)
		}
		url := node.Path
		if url == "" {
			url = strings.Join(meta, "\n")
		}
		item := extract.Item{URL: url, Label: label, Category: "context"}
		pageItem := makeConvPageItemWithTarget(item, time.Time{}, strings.Join(meta, "\n"), "", 0, node.RelatedView, node.RelatedPath, node.RelatedPluginID, node.RelatedPluginComponentPath)
		pageItem.relatedPluginComponentType = node.RelatedPluginComponentType
		*items = append(*items, pageItem)
		if len(node.Children) > 0 {
			flattenContextNodes(node.Children, prefix+"  ", items)
		}
	}
}

func (a *App) openConvContextsPage() (tea.Model, tea.Cmd) {
	a.convPageActive = true
	a.convPageFocus = false
	a.convPageLastCursor = -1
	a.convPage = convPageContexts
	a.convPageItems = nil
	if tree, err := session.BuildSessionContextTree(a.config.ClaudeDir, a.currentSess); err == nil {
		flattenContextNodes(tree.Roots, "", &a.convPageItems)
	}
	a.convPageCursor = 0
	return a, nil
}

func (a *App) openConvChangesPage() (tea.Model, tea.Cmd) {
	a.convPageActive = true
	a.convPageFocus = false
	a.convPageLastCursor = -1
	a.convPage = convPageChanges
	a.convPageItems = nil
	a.convPageChangeMap = make(map[string]extract.ChangeItem)
	seen := make(map[string]int)
	for idx, m := range a.conv.merged {
		for _, ch := range extract.BlockChanges(m.entry.Content) {
			if existing, ok := seen[ch.Item.URL]; ok {
				a.convPageItems[existing].timestamp = m.entry.Timestamp
				a.convPageItems[existing].turnPreview = convMsgPreview(m.entry, 80)
				a.convPageItems[existing].userPrompt = relatedUserPrompt(a.conv.messages, idx)
				a.convPageChangeMap[ch.Item.URL] = ch
				continue
			}
			seen[ch.Item.URL] = len(a.convPageItems)
			item := extract.Item{URL: ch.Item.URL, Label: ch.Item.URL, Category: "change"}
			a.convPageItems = append(a.convPageItems, makeConvPageItem(item, m.entry.Timestamp, convMsgPreview(m.entry, 80), relatedUserPrompt(a.conv.messages, idx), 0))
			a.convPageChangeMap[ch.Item.URL] = ch
		}
	}
	sortConvPageItemsByTime(a.convPageItems)
	a.convPageCursor = 0
	return a, nil
}

func (a *App) setConvPreviewText(content string) {
	sp := &a.conv.split
	sp.CacheKey = "text"
	sp.SetPreviewContent(content, a.width, a.height, a.splitRatio)
	sp.Preview.YOffset = 0
	// Clear stale fold state so fold keys don't re-render a previous message
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
	items := a.convList.Items()
	if item.parentIdx < 0 || item.parentIdx >= len(items) {
		return session.Subagent{}, false
	}
	parent, ok := items[item.parentIdx].(convItem)
	if !ok || parent.kind != convMsg {
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

// findAgentForConv finds the subagent matching an entry that contains an Agent tool_use.
// Uses the toolUseToAgent map (tool_use_id → agentId) built from tool_result entries.
func (a *App) findAgentForConv(entry session.Entry) (session.Subagent, bool) {
	agents := a.conv.agents
	if len(agents) == 0 {
		return session.Subagent{}, false
	}

	agentByID := make(map[string]session.Subagent, len(agents))
	for _, ag := range agents {
		agentByID[ag.ID] = ag
	}

	// Look for Agent tool_use blocks and resolve via the toolUseToAgent map
	for _, block := range entry.Content {
		if block.Type == "tool_use" && block.ToolName == "Agent" && block.ID != "" {
			if agID, ok := a.conv.toolUseToAgent[block.ID]; ok {
				if ag, ok := agentByID[agID]; ok {
					return ag, true
				}
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
		items := a.convList.Items()
		if len(items) > 0 {
			// Select the last convMsg item (skip trailing agent/task sub-items)
			lastMsg := len(items) - 1
			for i := len(items) - 1; i >= 0; i-- {
				if ci, ok := items[i].(convItem); ok && ci.kind == convMsg {
					lastMsg = i
					break
				}
			}
			a.convList.Select(lastMsg)
		}
		a.updateConvPreview()
		a.scrollConvPreviewToTail()
		return a, liveTickCmd()
	}
	a.conv.split.BottomAlign = false
	return a, nil
}

// jumpToOriginMessage selects the exact spawning turn for the current flow node.
func (a *App) jumpToOriginMessage() (tea.Model, tea.Cmd) {
	item, ok := a.convList.SelectedItem().(convItem)
	if !ok || item.parentIdx < 0 || item.parentIdx >= len(a.conv.items) {
		a.copiedMsg = "no origin turn found"
		return a, nil
	}
	target := a.conv.items[item.parentIdx]
	if target.kind != convMsg {
		a.copiedMsg = "no origin turn found"
		return a, nil
	}
	for i, li := range a.convList.Items() {
		ci, ok := li.(convItem)
		if ok && ci.kind == convMsg && ci.merged.entry.UUID == target.merged.entry.UUID {
			a.convList.Select(i)
			a.updateConvPreview()
			return a, nil
		}
	}
	a.copiedMsg = "origin turn is hidden by filter"
	return a, nil
}

// rebuildConversationList rebuilds the single unified flow list.
func (a *App) rebuildConversationList(selectIdx int) {
	contentH := ContentHeight(a.height)
	a.convList = newConvList(a.conv.items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH)
	a.conv.split.List = &a.convList
	if selectIdx >= 0 && selectIdx < len(a.convList.Items()) {
		a.convList.Select(selectIdx)
	}
	a.conv.split.CacheKey = ""
}

func (a *App) activeConvItems() []convItem { return a.conv.items }

// refreshConversation reloads messages for the current conversation.
func (a *App) refreshConversation() tea.Cmd {
	entries, err := session.LoadMessages(a.conv.sess.FilePath)
	if err != nil {
		return nil
	}
	a.conv.messages = entries
	a.conv.merged = filterConversation(mergeConversationTurns(entries))
	flow, _ := session.BuildSessionFlow(&a.conv.sess)
	agents, _ := session.FindSubagents(a.conv.sess.FilePath)
	if flow != nil {
		agents = flow.Agents()
	}
	a.conv.flow = flow
	a.conv.agents = agents
	tasks := a.conv.sess.Tasks
	if len(tasks) == 0 {
		tasks = extractInlineTasks(entries)
		a.conv.sess.Tasks = tasks
	}
	crons := a.conv.sess.Crons
	if len(crons) == 0 && a.conv.sess.HasCrons {
		crons = session.LoadCronsFromEntries(entries)
		a.conv.sess.Crons = crons
	}
	a.conv.items = buildConvItems(a.conv.sess, a.conv.merged, agents, tasks, crons, flow)
	a.conv.sess.Tasks = tasks

	// Preserve list cursor and preview selection across the rebuild
	oldIdx := a.convList.Index()
	prevCacheKey := a.conv.split.CacheKey
	prevYOffset := a.conv.split.Preview.YOffset
	a.rebuildConversationList(oldIdx)
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
	contentH := ContentHeight(a.height)
	a.convList = newConvList(items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH)
	a.conv.split.List = &a.convList

	for i, v := range vis {
		if v.groupTag == header.groupTag && v.parentIdx == header.parentIdx {
			a.convList.Select(i)
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
	contentH := ContentHeight(a.height)
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

	// Images page: render into the right detail pane of the artifact browser
	// (only when kitty preview is enabled via `i` toggle)
	if a.convPageActive && a.convPage == convPageImages && a.convPageKitty && a.convPageCursor >= 0 && a.convPageCursor < len(a.convPageItems) {
		item := a.convPageItems[a.convPageCursor]
		id := strings.TrimPrefix(item.URL, "paste:")
		var pasteID int
		fmt.Sscanf(id, "%d", &pasteID)
		if pasteID > 0 {
			cachePath := session.ImageCachePath(homeDir(), a.currentSess.ID, pasteID)
			if cachePath == "" {
				cachePath = a.resolveImagePath(pasteID)
			}
			if cachePath != "" {
				browserRatio := a.splitRatio
				listW := max(a.width*browserRatio/100, 30)
				previewW := max(a.width-listW-1, 1)
				contentH := ContentHeight(a.height)
				maxCols := max(previewW-2, 10)
				maxRows := max(contentH-2, 4)
				imgW, imgH := kitty.ImageSize(cachePath)
				cols, rows := kitty.FitSize(imgW, imgH, maxCols, maxRows)
				imageY := 2 + max((maxRows-rows)/2, 0)
				imageX := listW + 2
				return kitty.ClearImages() + kitty.PlaceImage(cachePath, imageY, imageX, cols, rows)
			}
		}
		return kitty.ClearImages()
	}

	// Default: focused image artifact in normal conversation view → left pane
	cachePath := a.kittyImagePath()
	if cachePath == "" {
		return kitty.ClearImages()
	}
	sp := &a.conv.split

	listW := sp.ListWidth(a.width, a.splitRatio)
	contentH := ContentHeight(a.height)
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
	rendered := sp.Render(a.width, a.height, a.splitRatio)

	// Show tooltip for selected item when list is focused and tooltip is on.
	// When preview is focused, prefer a tooltip for the focused artifact/block.
	// Skip text tooltip for image blocks when Kitty rendering is active.
	if sp.Focus && sp.Show && !a.kittyImageActive() {
		if tooltip := a.focusedArtifactTooltip(sp, a.width); tooltip != "" {
			contentH := ContentHeight(a.height)
			rendered = overlayTooltip(rendered, tooltip, a.width, contentH, a.convList.Index(), a.convList.Paginator.PerPage, a.convTooltipScroll, a.activeDividerCol())
		}
	} else if a.convTooltipOn && sp.Show && len(a.convList.Items()) > 0 {
		if tooltip := a.convTooltip(); tooltip != "" {
			contentH := ContentHeight(a.height)
			rendered = overlayTooltip(rendered, tooltip, a.width, contentH, a.convList.Index(), a.convList.Paginator.PerPage, a.convTooltipScroll, a.activeDividerCol())
		}
	}

	return rendered
}

// convTooltip returns the full text of the selected conversation item, or empty if it fits.
func (a *App) convTooltip() string {
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
func overlayTooltip(bg, text string, screenW, screenH, cursorIdx, perPage, scroll, dividerCol int) string {
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
	y := visibleIdx + 1 // +1 for title bar
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
	a.conv.items = items
	a.conv.agent = session.Subagent{}
	a.conv.task = session.TaskItem{}
	a.conv.cron = cron

	a.conv.split.Focus = false
	a.conv.split.CacheKey = ""
	a.rebuildConversationList(0)

	a.state = viewConversation
	a.updateConvPreview()
	return a, nil
}

// openTaskConversation opens a conversation view filtered to entries related to a task.
func (a *App) openTaskConversation(task session.TaskItem) (tea.Model, tea.Cmd) {
	taskEntries := extractTaskEntries(a.conv.messages, task.ID)
	if len(taskEntries) == 0 {
		a.copiedMsg = "No entries for task " + task.ID
		return a, nil
	}

	merged := filterConversation(mergeConversationTurns(taskEntries))
	agents, _ := session.FindSubagents(a.conv.sess.FilePath)
	items := buildConvItems(a.currentSess, merged, agents, nil, nil, a.conv.flow)

	a.conv.sess = a.currentSess
	a.conv.messages = taskEntries
	a.conv.merged = merged
	a.conv.agents = agents
	a.conv.items = items
	a.conv.agent = session.Subagent{}
	a.conv.task = task
	a.conv.cron = session.CronItem{}

	a.conv.split.Focus = false
	a.conv.split.CacheKey = ""
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

	merged := filterConversation(mergeConversationTurns(entries))
	agentSess := a.currentSess
	agentSess.ID = agent.ID
	agentSess.FilePath = agent.FilePath
	flow, _ := session.BuildSessionFlow(&agentSess)
	agents, _ := session.FindSubagents(agent.FilePath)
	if flow != nil {
		agents = flow.Agents()
	}
	items := buildConvItems(agentSess, merged, agents, nil, nil, flow)

	a.conv.sess = agentSess
	a.conv.flow = flow
	a.conv.messages = entries
	a.conv.merged = merged
	a.conv.agents = agents
	a.conv.items = items
	a.conv.agent = agent
	a.conv.task = session.TaskItem{}
	a.conv.cron = session.CronItem{}

	a.conv.split.Focus = false
	a.conv.split.CacheKey = ""
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

// openFullConversation renders all merged messages into a single scrollable view.
func (a *App) openFullConversation() (tea.Model, tea.Cmd) {
	if len(a.conv.merged) == 0 {
		a.copiedMsg = "No messages"
		return a, nil
	}

	content := renderAllMessages(a.conv.merged, a.width)
	contentH := ContentHeight(a.height)

	a.msgFull.sess = a.currentSess
	a.msgFull.agent = a.conv.agent
	a.msgFull.messages = a.conv.messages
	a.msgFull.merged = a.conv.merged
	a.msgFull.agents = a.conv.agents
	a.msgFull.idx = 0
	a.msgFull.content = content
	a.msgFull.allMessages = true
	a.msgFull.folds = FoldState{}

	a.msgFull.vp = viewport.New(a.width, contentH)
	a.msgFull.vp.SetContent(content)

	a.state = viewMessageFull
	return a, nil
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
