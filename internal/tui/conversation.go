package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
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
	a.conv.messages = entries
	a.conv.merged = filterConversation(mergeConversationTurns(entries))
	a.conv.agent = session.Subagent{}
	a.conv.task = session.TaskItem{}
	a.conv.cron = session.CronItem{}
	a.conv.toolUseToAgent = buildToolUseToAgentMap(entries)

	// Load agents
	agents, _ := session.FindSubagents(sess.FilePath)
	a.conv.agents = agents

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
	a.conv.items = buildConvItems(a.conv.merged, agents, tasks, crons)

	if info, err := os.Stat(sess.FilePath); err == nil {
		a.lastMsgLoadTime = info.ModTime()
	}

	// Create list with preview auto-open while keeping the current flat/tree mode.
	a.conv.split.Show = true
	a.conv.split.Focus = false
	a.conv.split.CacheKey = ""
	a.rebuildConversationList(0)

	a.state = viewConversation

	// Auto-enable live tail for live sessions
	a.liveTail = false
	if sess.IsLive && a.conv.leftPaneMode != convPaneTree {
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
func (a *App) handleConversationKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sp := &a.conv.split
	key := msg.String()

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
				return a.popNavFrame()
			}
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
		// In tree mode: jump to origin message in flat view
		if a.conv.leftPaneMode == convPaneTree {
			return a.jumpToOriginMessage()
		}
		// In flat mode with tmux: jump to tmux pane
		if a.config.TmuxEnabled {
			return a.jumpToTmuxPane(a.currentSess.ProjectPath, a.currentSess.ID)
		}
		// In flat mode without tmux: jump to tree
		return a.jumpToEntityTree()
	case a.keymap.Conversation.Actions:
		a.convActionsMenu = true
		return a, nil
	}

	// Tab/shift+tab act on the focused pane: left toggles flat/tree, right cycles compact/standard/verbose.
	if (key == "tab" || key == "shift+tab") && sp.Show {
		if sp.Focus {
			if key == "shift+tab" {
				a.conv.rightPaneMode = (a.conv.rightPaneMode + len(previewModeLabels) - 1) % len(previewModeLabels)
			} else {
				a.conv.rightPaneMode = (a.conv.rightPaneMode + 1) % len(previewModeLabels)
			}
			if sp.Folds != nil {
				sp.Folds.HideHooks = a.conv.rightPaneMode == previewTool
			}
		} else {
			if a.conv.leftPaneMode == convPaneFlat {
				a.conv.leftPaneMode = convPaneTree
			} else {
				a.conv.leftPaneMode = convPaneFlat
			}
			if a.conv.leftPaneMode == convPaneTree && a.liveTail {
				a.liveTail = false
				a.conv.split.BottomAlign = false
			}
			a.rebuildConversationList(0)
		}
		sp.CacheKey = "" // force re-render
		a.updateConvPreview()
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
			if a.conv.rightPaneMode == previewText {
				// Text mode: scroll viewport directly
				scrollPreview(&sp.Preview, key)
				return a, nil
			}
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
			if a.conv.rightPaneMode != previewText {
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
		a.updateConvPreview()
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

	var entry session.Entry
	switch item.kind {
	case convMsg:
		entry = item.merged.entry
	case convAgent:
		entry = buildAgentPreviewEntry(item.agent)
	case convTask:
		pw := sp.PreviewWidth(a.width, a.splitRatio)
		if item.groupTag == "agents" && item.count > 0 {
			// Agents group header: show agent summary list
			a.setConvPreviewText(a.renderAgentsSummary(pw))
			return
		}
		if item.groupTag == "bgjobs" && item.count > 0 {
			// Background jobs group header: show job summary
			a.setConvPreviewText(a.renderBgJobsSummary(pw))
			return
		}
		if item.groupTag != "" && item.count > 0 {
			// Expandable group header (tasks): show full task board
			a.setConvPreviewText(a.renderConvTaskBoard(pw))
			return
		}
		if item.groupTag != "" {
			// Marker header: show per-message operation summary
			a.setConvPreviewText(renderTaskMarkerPreview(item, pw))
			return
		}
		if a.conv.leftPaneMode == convPaneTree && item.bgTaskID != "" {
			entry = a.buildBgJobPreviewEntry(item.bgTaskID)
		} else if a.conv.leftPaneMode == convPaneTree {
			entry = a.buildTaskPreviewEntry(item.task)
		} else {
			// Individual task: show task details
			a.setConvPreviewText(renderTaskSummary(item.task, pw))
			return
		}
	}

	// Compact preview: text only.
	// Standard preview: conversation text + artifacts only.
	// Verbose preview: full structured blocks + hooks.
	if a.conv.leftPaneMode != convPaneTree {
		if a.conv.rightPaneMode == previewText {
			a.renderTextOnlyPreview(item, entry)
			return
		}
		if a.conv.rightPaneMode == previewTool {
			sp.CacheKey = fmt.Sprintf("standard:%s:%d", convPreviewBaseKey(item), len(entry.Content))
			sp.SetPreviewContent(renderStandardPreviewContent(entry, max(sp.PreviewWidth(a.width, a.splitRatio)-2, 10)), a.width, a.height, a.splitRatio)
			sp.Preview.YOffset = 0
			if sp.Folds != nil {
				sp.Folds.Entry = session.Entry{}
				sp.Folds.BlockStarts = nil
			}
			return
		}
	}

	baseKey := convPreviewBaseKey(item)
	cacheKey := fmt.Sprintf("%s:%d", baseKey, len(entry.Content))
	if cacheKey == sp.CacheKey {
		debugLog.Printf("updateConvPreview: CACHE HIT key=%q", cacheKey)
		return
	}

	oldCacheKey := sp.CacheKey
	isNewEntry := oldCacheKey == "" || !strings.HasPrefix(oldCacheKey, baseKey+":")

	if isNewEntry {
		debugLog.Printf("updateConvPreview: NEW ENTRY old=%q new=%q blocks=%d TypeFoldPrefs=%v TypeFmtPrefs=%v",
			oldCacheKey, cacheKey, len(entry.Content), sp.TypeFoldPrefs, sp.TypeFmtPrefs)
		sp.CacheKey = cacheKey
		if sp.Folds != nil {
			sp.Folds.ResetWithPrefs(entry, sp.TypeFoldPrefs, sp.TypeFmtPrefs)
			sp.Folds.HideHooks = a.conv.rightPaneMode == previewTool
			// Re-apply block filter to new entry
			if sp.Folds.BlockFilter != "" {
				sp.Folds.BlockVisible = applyBlockFilter(sp.Folds.BlockFilter, entry)
				if first := sp.Folds.firstVisibleBlock(); first >= 0 {
					sp.Folds.BlockCursor = first
				}
			}
			debugLog.Printf("  after ResetWithPrefs: collapsed=%v formatted=%v blockCursor=%d",
				sp.Folds.Collapsed, sp.Folds.Formatted, sp.Folds.BlockCursor)
		}
		sp.RefreshFoldPreview(a.width, a.splitRatio)
		sp.Preview.YOffset = 0
	} else {
		oldBC := 0
		if sp.Folds != nil {
			oldBC = len(sp.Folds.Entry.Content)
		}
		debugLog.Printf("updateConvPreview: GROW old=%q new=%q oldBlocks=%d newBlocks=%d TypeFmtPrefs=%v",
			oldCacheKey, cacheKey, oldBC, len(entry.Content), sp.TypeFmtPrefs)
		sp.CacheKey = cacheKey
		if sp.Folds != nil {
			sp.Folds.GrowBlocks(entry, oldBC, sp.TypeFoldPrefs, sp.TypeFmtPrefs)
			debugLog.Printf("  after GrowBlocks: collapsed=%v formatted=%v blockCursor=%d",
				sp.Folds.Collapsed, sp.Folds.Formatted, sp.Folds.BlockCursor)
		}
		sp.RefreshFoldPreview(a.width, a.splitRatio)
	}
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

func renderPreviewHeader(entry session.Entry, textW int) string {
	roleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)

	var sb strings.Builder
	role := strings.ToUpper(entry.Role)
	if role == "" {
		role = "UNKNOWN"
	}
	sb.WriteString(roleStyle.Render(role))
	if !entry.Timestamp.IsZero() {
		sb.WriteString(dimStyle.Render("  " + entry.Timestamp.Format("15:04:05")))
	}
	if entry.Model != "" {
		sb.WriteString(dimStyle.Render("  " + entry.Model))
	}
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render(strings.Repeat("─", min(textW, 60))) + "\n\n")
	return sb.String()
}

// renderTextOnlyPreview renders a clean text-only view of the entry (no tool calls).
func (a *App) renderTextOnlyPreview(item convItem, entry session.Entry) {
	sp := &a.conv.split
	pw := sp.PreviewWidth(a.width, a.splitRatio)
	textW := max(pw-2, 10)

	cacheKey := fmt.Sprintf("text:%s:%d", convPreviewBaseKey(item), len(entry.Content))
	if cacheKey == sp.CacheKey {
		return
	}

	var sb strings.Builder
	sb.WriteString(renderPreviewHeader(entry, textW))

	chunks := previewTextChunks(entry)
	if len(chunks) == 0 {
		sb.WriteString(dimStyle.Render("(no text content)"))
	} else {
		for i, chunk := range chunks {
			if i > 0 {
				sb.WriteString("\n\n" + dimStyle.Render(strings.Repeat("─", min(textW, 24))) + "\n\n")
			}
			sb.WriteString(wrapText(chunk, textW))
		}
	}

	sp.CacheKey = cacheKey
	sp.SetPreviewContent(sb.String(), a.width, a.height, a.splitRatio)
	sp.Preview.YOffset = 0
	// Clear fold state to prevent fold keys from acting on stale data
	if sp.Folds != nil {
		sp.Folds.Entry = session.Entry{}
		sp.Folds.BlockStarts = nil
	}
}

func renderStandardPreviewContent(entry session.Entry, textW int) string {
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	var sb strings.Builder
	sb.WriteString(renderPreviewHeader(entry, textW))

	chunks := previewTextChunks(entry)
	if len(chunks) == 0 {
		sb.WriteString(dimStyle.Render("(no text content)"))
	} else {
		for i, chunk := range chunks {
			if i > 0 {
				sb.WriteString("\n\n" + dimStyle.Render(strings.Repeat("╌", max(textW, 24))) + "\n\n")
			}
			sb.WriteString(wrapText(chunk, textW))
		}
	}

	var artifactLines []string
	for _, b := range entry.Content {
		if b.Type == "image" {
			label := b.Text
			if label == "" {
				label = "[image]"
			}
			artifactLines = append(artifactLines, "[image] "+label)
		}
	}
	for _, item := range extract.BlockFilePaths(entry.Content) {
		artifactLines = append(artifactLines, "[file] "+item.URL)
	}
	for _, item := range extract.BlockURLs(entry.Content) {
		artifactLines = append(artifactLines, "[url] "+item.URL)
	}
	for _, item := range extract.BlockChanges(entry.Content) {
		artifactLines = append(artifactLines, "[change] "+item.Item.URL)
	}
	if len(artifactLines) > 0 {
		sb.WriteString("\n\n" + sectionStyle.Render("Artifacts") + "\n")
		for _, line := range artifactLines {
			sb.WriteString(dimStyle.Render("  "+line) + "\n")
		}
	}
	return sb.String()
}

func (a *App) setConvPreviewText(content string) {
	sp := &a.conv.split
	sp.CacheKey = "text"
	sp.Preview.SetContent(content)
	sp.Preview.YOffset = 0
	// Clear stale fold state so fold keys don't re-render a previous message
	if sp.Folds != nil {
		sp.Folds.Entry = session.Entry{}
		sp.Folds.BlockStarts = nil
	}
}

// buildAgentPreviewEntry builds a synthetic Entry from an agent's messages
// so the preview can use fold/unfold block cursor like regular messages.
func buildAgentPreviewEntry(agent session.Subagent) session.Entry {
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
	return buildConversationPreviewEntry(header, agent.Timestamp, entries)
}

func convPreviewBaseKey(item convItem) string {
	switch {
	case item.kind == convMsg:
		return fmt.Sprintf("msg:%d", item.merged.startIdx)
	case item.kind == convAgent:
		return "agent:" + item.agent.ShortID
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

	for idx, e := range entries {
		if idx > 0 {
			blocks = append(blocks, session.ContentBlock{Type: "text", Text: strings.Repeat("─", 24)})
		}
		if ts.IsZero() && !e.Timestamp.IsZero() {
			ts = e.Timestamp
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

func previewMessageText(e session.Entry) string {
	role := strings.ToUpper(e.Role)
	if role == "" {
		role = "ENTRY"
	}
	header := role
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

func (a *App) buildBgJobPreviewEntry(taskID string) session.Entry {
	header := fmt.Sprintf("Background Job: %s", taskID)
	if cmd := buildBgTaskMap(a.conv.merged)[taskID]; cmd != "" {
		header += "\nCommand: " + cmd
	}
	return buildConversationPreviewEntry(header, time.Time{}, extractBgTaskEntries(a.conv.merged, taskID))
}

func (a *App) buildTaskPreviewEntry(task session.TaskItem) session.Entry {
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
	return buildConversationPreviewEntry(header, time.Time{}, extractTaskEntries(a.conv.messages, task.ID))
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
	status := "◉ active"
	if cron.Status == "deleted" {
		status = "⏹ deleted"
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

// renderTaskSummary renders a summary for a task in the preview pane.
func renderTaskSummary(task session.TaskItem, width int) string {
	var sb strings.Builder
	status := "○ pending"
	switch task.Status {
	case "completed":
		status = "✓ completed"
	case "in_progress":
		status = "◉ in progress"
	}
	sb.WriteString(taskBadgeStyle.Render("Task: "+task.ID) + "  " + status + "\n")
	sb.WriteString("\n" + task.Subject + "\n")
	if task.Description != "" {
		sb.WriteString("\n" + dimStyle.Render("Description:") + "\n")
		sb.WriteString(wrapText(task.Description, width-2) + "\n")
	}
	if len(task.BlockedBy) > 0 {
		sb.WriteString("\n" + dimStyle.Render("Blocked by: ") + strings.Join(task.BlockedBy, ", ") + "\n")
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
func buildToolUseToAgentMap(entries []session.Entry) map[string]string {
	m := make(map[string]string)
	for _, e := range entries {
		if e.AgentID == "" {
			continue
		}
		for _, b := range e.Content {
			if b.Type == "tool_result" && b.ID != "" {
				m[b.ID] = e.AgentID
			}
		}
	}
	return m
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

// jumpToEntityTree switches to tree mode, optionally selecting the entity matching
// the currently selected conv sub-item (agent or bg task).
func (a *App) jumpToEntityTree() (tea.Model, tea.Cmd) {
	// Capture the entity ID to select in tree
	var targetAgentID, targetBgTaskID string
	if item, ok := a.convList.SelectedItem().(convItem); ok {
		switch item.kind {
		case convAgent:
			targetAgentID = item.agent.ID
		case convTask:
			if item.bgTaskID != "" {
				targetBgTaskID = item.bgTaskID
			}
		}
	}

	a.setConvLeftPaneMode(convPaneTree)

	// Find and select the matching entity in the tree
	if targetAgentID != "" || targetBgTaskID != "" {
		for i, item := range a.convList.Items() {
			ci, ok := item.(convItem)
			if !ok {
				continue
			}
			if targetAgentID != "" && ci.kind == convAgent && ci.agent.ID == targetAgentID {
				a.convList.Select(i)
				break
			}
			if targetBgTaskID != "" && ci.kind == convTask && ci.bgTaskID == targetBgTaskID {
				a.convList.Select(i)
				break
			}
		}
	}

	a.updateConvPreview()
	return a, nil
}

// jumpToOriginMessage switches from tree mode to flat mode, jumping to the
// parent message that spawned the currently selected agent or task.
func (a *App) jumpToOriginMessage() (tea.Model, tea.Cmd) {
	item, ok := a.convList.SelectedItem().(convItem)
	if !ok {
		return a, nil
	}

	// Find the parent message's UUID from the tree items
	var targetUUID string
	switch item.kind {
	case convAgent:
		// parentIdx points to the parent convMsg in the current items slice
		if item.parentIdx >= 0 && item.parentIdx < len(a.convList.Items()) {
			if parent, ok := a.convList.Items()[item.parentIdx].(convItem); ok && parent.kind == convMsg {
				targetUUID = parent.merged.entry.UUID
			}
		}
		// Fallback: search by agent timestamp — find assistant message just before agent
		if targetUUID == "" {
			for _, ci := range a.conv.items {
				if ci.kind == convMsg && ci.merged.entry.Role == "assistant" {
					for _, b := range ci.merged.entry.Content {
						if b.ToolName == "Agent" {
							targetUUID = ci.merged.entry.UUID
						}
					}
				}
			}
		}
	case convTask:
		if item.parentIdx >= 0 && item.parentIdx < len(a.convList.Items()) {
			if parent, ok := a.convList.Items()[item.parentIdx].(convItem); ok && parent.kind == convMsg {
				targetUUID = parent.merged.entry.UUID
			}
		}
	case convMsg:
		// Already a message, just switch to flat at this position
		targetUUID = item.merged.entry.UUID
	}

	if targetUUID == "" {
		a.copiedMsg = "no parent message found"
		return a, nil
	}

	// Switch to flat mode
	a.setConvLeftPaneMode(convPaneFlat)

	// Find the matching message in flat items and select it
	for i, li := range a.convList.Items() {
		ci, ok := li.(convItem)
		if !ok {
			continue
		}
		if ci.kind == convMsg && ci.merged.entry.UUID == targetUUID {
			a.convList.Select(i)
			break
		}
	}

	a.updateConvPreview()
	return a, nil
}

// rebuildConversationList rebuilds the left-pane list based on the active flat/tree mode.
func (a *App) rebuildConversationList(selectIdx int) {
	contentH := ContentHeight(a.height)
	items := a.conv.items
	if a.conv.leftPaneMode == convPaneTree {
		a.conv.treeItems = buildEntityTree(a.conv.merged, a.conv.agents, a.conv.sess.Tasks, a.conv.sess.Crons, inferAgentStatuses(a.conv.merged))
		items = a.conv.treeItems
	}
	a.convList = newConvList(items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH)
	a.conv.split.List = &a.convList
	if selectIdx >= 0 && selectIdx < len(a.convList.Items()) {
		a.convList.Select(selectIdx)
	}
	a.conv.split.CacheKey = ""
}

// activeConvItems returns the item slice backing the current list mode (flat or tree).
func (a *App) activeConvItems() []convItem {
	if a.conv.leftPaneMode == convPaneTree {
		return a.conv.treeItems
	}
	return a.conv.items
}

// refreshConversation reloads messages for the current conversation.
func (a *App) refreshConversation() tea.Cmd {
	entries, err := session.LoadMessages(a.conv.sess.FilePath)
	if err != nil {
		return nil
	}
	a.conv.messages = entries
	a.conv.merged = filterConversation(mergeConversationTurns(entries))
	agents, _ := session.FindSubagents(a.conv.sess.FilePath)
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
	a.conv.items = buildConvItems(a.conv.merged, agents, tasks, crons)
	a.conv.sess.Tasks = tasks

	// Preserve cursor position
	oldIdx := a.convList.Index()
	a.rebuildConversationList(oldIdx)
	// During live tail, skip preview update here — handleLiveTail owns the
	// preview lifecycle (select last → update → scroll-to-tail). Updating here
	// would "consume" the CacheKey change, making handleLiveTail's update a
	// no-op cache hit while the scroll position is left at block 0 from
	// RefreshFoldPreview→ScrollToBlock.
	if !a.liveTail {
		a.updateConvPreview()
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
		icon := "○"
		style := dimStyle
		switch t.Status {
		case "completed":
			icon = "✓"
			style = lipgloss.NewStyle().Foreground(colorAccent)
		case "in_progress":
			icon = "◉"
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
		icon := "●"
		status := statuses[ag.ID]
		if status == "" {
			status = statuses[ag.ShortID]
		}
		style := dimStyle
		switch status {
		case "completed":
			icon = "✓"
			style = lipgloss.NewStyle().Foreground(colorAccent)
		case "running":
			icon = "◉"
			style = lipgloss.NewStyle().Foreground(colorAssistant)
		case "stopped":
			icon = "⏹"
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
		icon := "⏳"
		style := dimStyle
		switch status {
		case "completed":
			icon = "✓"
			style = lipgloss.NewStyle().Foreground(colorAccent)
		case "stopped":
			icon = "⏹"
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
			// Re-render so the ▸ cursor marker reflects the new position
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

// kittyImageActive returns true if the focused block is a renderable image.
func (a *App) kittyImageActive() bool {
	if a.state != viewConversation || !kitty.Supported() {
		return false
	}
	sp := &a.conv.split
	if !sp.Focus || !sp.Show || sp.Folds == nil {
		return false
	}
	bc := sp.Folds.BlockCursor
	if bc < 0 || bc >= len(sp.Folds.Entry.Content) {
		return false
	}
	block := sp.Folds.Entry.Content[bc]
	return block.Type == "image" && block.ImagePasteID > 0
}

// kittyImageLayer returns Kitty graphics escape sequences to draw an inline
// image covering the full left pane area when a focused image artifact has
// a cached file. Returns a clear command if no image should be drawn.
func (a *App) kittyImageLayer() string {
	if a.state != viewConversation {
		return kitty.ClearImages()
	}
	sp := &a.conv.split
	if !sp.Focus || !sp.Show || sp.Folds == nil {
		return kitty.ClearImages()
	}
	bc := sp.Folds.BlockCursor
	if bc < 0 || bc >= len(sp.Folds.Entry.Content) {
		return kitty.ClearImages()
	}
	block := sp.Folds.Entry.Content[bc]
	if block.Type != "image" || block.ImagePasteID <= 0 {
		return kitty.ClearImages()
	}
	cachePath := session.ImageCachePath(homeDir(), a.currentSess.ID, block.ImagePasteID)
	if cachePath == "" {
		cachePath = a.resolveImagePath(block.ImagePasteID)
	}
	if cachePath == "" {
		return kitty.ClearImages()
	}

	// Use the full left pane area for the image
	listW := sp.ListWidth(a.width, a.splitRatio)
	contentH := ContentHeight(a.height)
	imageY := 2 // after title bar
	imageX := 1
	cols := listW
	rows := contentH
	if cols < 10 || rows < 4 {
		return kitty.ClearImages()
	}

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
			rendered = overlayTooltip(rendered, tooltip, a.width, contentH, a.convList.Index(), a.convList.Paginator.PerPage, a.convTooltipScroll)
		}
	} else if a.convTooltipOn && sp.Show && len(a.convList.Items()) > 0 {
		if tooltip := a.convTooltip(); tooltip != "" {
			contentH := ContentHeight(a.height)
			rendered = overlayTooltip(rendered, tooltip, a.width, contentH, a.convList.Index(), a.convList.Paginator.PerPage, a.convTooltipScroll)
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
func overlayTooltip(bg, text string, screenW, screenH, cursorIdx, perPage, scroll int) string {
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
			// Place tooltip starting at column 2
			bgLines[row] = overlayLine(bgLine, tl, 2, screenW)
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
		// Fallback: collect ALL entries that mention this task ID
		// (TaskCreate, TaskUpdate, TaskGet, tool_results referencing the task)
		var result []session.Entry
		for _, e := range entries {
			for _, b := range e.Content {
				match := false
				if b.Type == "tool_use" && isTaskTool(b.ToolName) && strings.Contains(b.ToolInput, taskID) {
					match = true
				}
				if b.Type == "tool_result" && strings.Contains(b.Text, taskID) {
					match = true
				}
				if match {
					result = append(result, e)
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
	items := buildConvItems(merged, agents, nil, nil)

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
	items := buildConvItems(merged, agents, nil, nil)

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
	agents, _ := session.FindSubagents(agent.FilePath)
	items := buildConvItems(merged, agents, nil, nil)

	a.conv.sess = a.currentSess
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
	ti.Placeholder = "is:hook is:tool tool:Grep is:error ..."
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
