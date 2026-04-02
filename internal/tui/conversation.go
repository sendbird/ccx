package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

	// Load agents
	agents, _ := session.FindSubagents(sess.FilePath)
	a.conv.agents = agents

	// Build conversation items — use file-based tasks, or extract from JSONL
	tasks := sess.Tasks
	if len(tasks) == 0 {
		tasks = extractInlineTasks(entries)
		sess.Tasks = tasks
		a.conv.sess = sess
		a.currentSess = sess
	}
	a.conv.items = buildConvItems(a.conv.merged, agents, tasks)

	if info, err := os.Stat(sess.FilePath); err == nil {
		a.lastMsgLoadTime = info.ModTime()
	}

	// Create list with preview auto-open (text-only mode by default)
	contentH := ContentHeight(a.height)
	a.conv.split.Show = true
	a.conv.split.Focus = false
	a.conv.split.CacheKey = ""
	// Keep the persisted detail level (don't reset to compact on every open)
	a.convList = newConvList(a.conv.items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH)
	a.conv.split.List = &a.convList

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
			if a.conv.task.ID != "" || a.conv.agent.ShortID != "" {
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
		// Toggle fold on expandable group headers; marker headers are no-op
		if item.groupTag != "" {
			if item.count > 0 {
				a.toggleConvGroupFold(item)
			}
			return a, nil
		}
		switch item.kind {
		case convTask:
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
					// Jump to agent for Task blocks
					if block.Type == "tool_use" && block.ToolName == "Task" {
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
	case "L":
		return a.toggleConvLiveTail()
	case "R":
		cmd := a.refreshConversation()
		a.copiedMsg = "Refreshed"
		return a, cmd
	case "e":
		return a.openEditMenu(a.currentSess)
	case "i":
		return a.openMessageImage()
	case "I":
		if !a.config.TmuxEnabled {
			return a, nil
		}
		return a.openLiveInput(a.currentSess.ProjectPath, a.currentSess.ID)
	case "J":
		if !a.config.TmuxEnabled {
			return a, nil
		}
		return a.jumpToTmuxPane(a.currentSess.ProjectPath, a.currentSess.ID)
	case "x":
		a.convActionsMenu = true
		return a, nil
	}

	// Tab/shift+tab cycles detail level when preview is open: text → tool → hook
	if (key == "tab" || key == "shift+tab") && sp.Show {

		if key == "shift+tab" {
			a.conv.previewMode = (a.conv.previewMode + 2) % 3
		} else {
			a.conv.previewMode = (a.conv.previewMode + 1) % 3
		}
		if sp.Folds != nil {
			sp.Folds.HideHooks = a.conv.previewMode == previewTool
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
			if a.conv.task.ID != "" || a.conv.agent.ShortID != "" {
				return a.popNavFrame()
			}
			a.state = viewSessions
			return a, nil
		}
	}

	// Focused preview keys
	if sp.Focus && sp.Show {
		if key == "up" || key == "down" {
			if a.conv.previewMode == previewText {
				// Text mode: scroll viewport directly
				scrollPreview(&sp.Preview, key)
				return a, nil
			}
			if sp.Folds != nil {
				fr := sp.Folds.HandleKey(key)
				if fr == foldCursorMoved {
					sp.RefreshFoldCursor(a.width, a.splitRatio)
					sp.ScrollToBlock()
					return a, nil
				}
				if fr == foldHandled {
					sp.RefreshFoldCursor(a.width, a.splitRatio)
					return a, nil
				}
				return a, nil
			}
		}
		result = sp.HandleFocusedKeys(key)
		switch result {
		case splitKeySearchFromPreview:
			if a.conv.previewMode != previewText {
				a.startBlockFilter()
				return a, nil
			}
			return a, startListSearch(&a.convList)
		case splitKeyCursorMoved:
			sp.RefreshFoldCursor(a.width, a.splitRatio)
			sp.ScrollToBlock()
			return a, nil
		case splitKeyHandled:
			sp.RefreshFoldPreview(a.width, a.splitRatio)
			return a, nil
		case splitKeyScrolled:
			return a, nil
		case splitKeyUnfocused:
			return a, nil
		}
	}

	// List boundary
	if !sp.Focus && sp.HandleListBoundary(key) {
		if a.liveTail {
			a.liveTail = false
			a.conv.split.BottomAlign = false
		}
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
		a.liveTail = false
		a.conv.split.BottomAlign = false
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
		if item.groupTag != "" && item.count > 0 {
			// Expandable group header: show full task board
			a.setConvPreviewText(a.renderConvTaskBoard(pw))
		} else if item.groupTag != "" {
			// Marker header: show per-message operation summary
			a.setConvPreviewText(renderTaskMarkerPreview(item, pw))
		} else {
			// Individual task: show task details
			a.setConvPreviewText(renderTaskSummary(item.task, pw))
		}
		return
	}

	// Text-only preview mode: show clean conversation text (no tool calls)
	if a.conv.previewMode == previewText {
		a.renderTextOnlyPreview(item, entry)
		return
	}

	var cacheKey string
	if item.kind == convAgent {
		cacheKey = fmt.Sprintf("agent:%s:%d", item.agent.ShortID, len(entry.Content))
	} else {
		cacheKey = fmt.Sprintf("%d:%d", item.merged.startIdx, len(entry.Content))
	}
	if cacheKey == sp.CacheKey {
		debugLog.Printf("updateConvPreview: CACHE HIT key=%q", cacheKey)
		return
	}

	oldCacheKey := sp.CacheKey
	isNewEntry := true
	if oldCacheKey != "" {
		if item.kind == convAgent {
			isNewEntry = !strings.HasPrefix(oldCacheKey, "agent:"+item.agent.ShortID+":")
		} else {
			var oldIdx int
			fmt.Sscanf(oldCacheKey, "%d:", &oldIdx)
			isNewEntry = oldIdx != item.merged.startIdx
		}
	}

	if isNewEntry {
		debugLog.Printf("updateConvPreview: NEW ENTRY old=%q new=%q blocks=%d TypeFoldPrefs=%v TypeFmtPrefs=%v",
			oldCacheKey, cacheKey, len(entry.Content), sp.TypeFoldPrefs, sp.TypeFmtPrefs)
		sp.CacheKey = cacheKey
		if sp.Folds != nil {
			sp.Folds.ResetWithPrefs(entry, sp.TypeFoldPrefs, sp.TypeFmtPrefs)
			sp.Folds.HideHooks = a.conv.previewMode == previewTool
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

// renderTextOnlyPreview renders a clean text-only view of the entry (no tool calls).
func (a *App) renderTextOnlyPreview(item convItem, entry session.Entry) {
	sp := &a.conv.split
	pw := sp.PreviewWidth(a.width, a.splitRatio)
	textW := max(pw-2, 10)

	var cacheKey string
	if item.kind == convAgent {
		cacheKey = fmt.Sprintf("text:agent:%s:%d", item.agent.ShortID, len(entry.Content))
	} else {
		cacheKey = fmt.Sprintf("text:%d:%d", item.merged.startIdx, len(entry.Content))
	}
	if cacheKey == sp.CacheKey {
		return
	}

	// Header
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

	// Extract text blocks only
	text := entryFullText(entry)
	if text == "" {
		sb.WriteString(dimStyle.Render("(no text content)"))
	} else {
		sb.WriteString(wrapText(text, textW))
	}

	sp.CacheKey = cacheKey
	sp.Preview.SetContent(sb.String())
	sp.Preview.YOffset = 0
	// Clear fold state to prevent fold keys from acting on stale data
	if sp.Folds != nil {
		sp.Folds.Entry = session.Entry{}
		sp.Folds.BlockStarts = nil
	}
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
	if err != nil || len(entries) == 0 {
		// Fallback: just show prompt as text
		return session.Entry{
			Role:      "assistant",
			Timestamp: agent.Timestamp,
			Content: []session.ContentBlock{
				{Type: "text", Text: fmt.Sprintf("Agent: %s  Type: %s  Messages: %d\n\n%s",
					agent.ShortID, agent.AgentType, agent.MsgCount, agent.FirstPrompt)},
			},
		}
	}

	entries = filterAgentContextEntries(entries)
	if agent.AgentType == "aside_question" {
		entries = filterSideQuestionContext(entries)
	}

	// Header block
	header := fmt.Sprintf("Agent: %s", agent.ShortID)
	if agent.AgentType != "" {
		header += "  Type: " + agent.AgentType
	}
	header += fmt.Sprintf("  Messages: %d", agent.MsgCount)

	var blocks []session.ContentBlock
	blocks = append(blocks, session.ContentBlock{Type: "text", Text: header})

	// Collect content blocks from all messages (skip system text)
	for _, e := range entries {
		for _, b := range e.Content {
			if b.Type == "text" {
				text := strings.TrimSpace(session.StripXMLTags(b.Text))
				if text == "" || isSystemText(text) {
					continue
				}
				blocks = append(blocks, b)
			} else {
				blocks = append(blocks, b)
			}
		}
	}

	return session.Entry{
		Role:      "assistant",
		Timestamp: agent.Timestamp,
		Content:   blocks,
	}
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

// findAgentForConv finds the agent matching a message entry in the conversation.
// findTaskAgents returns all subagents referenced by TaskOutput tool_use blocks
// in the conversation. TaskOutput.task_id is the agent ID.
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
	for _, e := range a.conv.messages {
		for _, b := range e.Content {
			if b.Type != "tool_use" || b.ToolName != "TaskOutput" {
				continue
			}
			var input struct {
				TaskID string `json:"task_id"`
			}
			json.Unmarshal([]byte(b.ToolInput), &input)
			if input.TaskID == "" || seen[input.TaskID] {
				continue
			}
			seen[input.TaskID] = true
			if ag, ok := agentByID[input.TaskID]; ok {
				result = append(result, ag)
			}
		}
	}
	return result
}

func (a *App) findAgentForConv(entry session.Entry) (session.Subagent, bool) {
	agents := a.conv.agents
	if len(agents) == 0 {
		return session.Subagent{}, false
	}

	hasTask := false
	for _, block := range entry.Content {
		if block.Type == "tool_use" && block.ToolName == "Task" {
			hasTask = true
			break
		}
	}
	if !hasTask || entry.Timestamp.IsZero() {
		return session.Subagent{}, false
	}

	var best session.Subagent
	bestDiff := float64(1e18)
	for _, ag := range agents {
		if ag.Timestamp.IsZero() {
			continue
		}
		diff := ag.Timestamp.Sub(entry.Timestamp).Seconds()
		if diff >= -5 && diff < 60 {
			absDiff := diff
			if absDiff < 0 {
				absDiff = -absDiff
			}
			if absDiff < bestDiff {
				bestDiff = absDiff
				best = ag
			}
		}
	}
	if bestDiff < 1e18 {
		return best, true
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
	a.conv.items = buildConvItems(a.conv.merged, agents, tasks)

	// Preserve cursor position
	oldIdx := a.convList.Index()
	contentH := ContentHeight(a.height)
	a.convList = newConvList(a.conv.items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH)
	a.conv.split.List = &a.convList

	visCount := len(a.convList.Items())
	if oldIdx < visCount {
		a.convList.Select(oldIdx)
	}
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

// toggleConvGroupFold toggles the fold state of a group header in the conversation
// items list and rebuilds the visible list, preserving cursor on the header.
func (a *App) toggleConvGroupFold(header convItem) {
	// Find the group header in the full items slice and toggle its fold state.
	for i := range a.conv.items {
		if a.conv.items[i].groupTag == header.groupTag && a.conv.items[i].parentIdx == header.parentIdx {
			a.conv.items[i].folded = !a.conv.items[i].folded
			break
		}
	}

	// Rebuild visible list; find the header's new index.
	vis := visibleConvItems(a.conv.items)
	contentH := ContentHeight(a.height)
	a.convList = newConvList(a.conv.items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH)
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

// renderConvSplit renders the conversation split view.
func (a *App) renderConvSplit() string {
	sp := &a.conv.split
	rendered := sp.Render(a.width, a.height, a.splitRatio)

	// Show tooltip for selected item when list is focused
	if !sp.Focus && sp.Show && len(a.convList.Items()) > 0 {
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
		// Fallback: collect all entries that mention this task ID
		for _, e := range entries {
			for _, b := range e.Content {
				if b.Type == "tool_use" && (b.ToolName == "TaskUpdate" || b.ToolName == "TaskCreate") {
					var input struct {
						TaskID string `json:"taskId"`
					}
					json.Unmarshal([]byte(b.ToolInput), &input)
					if input.TaskID == taskID {
						return []session.Entry{e}
					}
				}
			}
		}
		return nil
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

// openTaskConversation opens a conversation view filtered to entries related to a task.
func (a *App) openTaskConversation(task session.TaskItem) (tea.Model, tea.Cmd) {
	taskEntries := extractTaskEntries(a.conv.messages, task.ID)
	if len(taskEntries) == 0 {
		a.copiedMsg = "No entries for task " + task.ID
		return a, nil
	}

	merged := filterConversation(mergeConversationTurns(taskEntries))
	agents, _ := session.FindSubagents(a.conv.sess.FilePath)
	items := buildConvItems(merged, agents, nil)

	a.conv.sess = a.currentSess
	a.conv.messages = taskEntries
	a.conv.merged = merged
	a.conv.agents = agents
	a.conv.items = items
	a.conv.agent = session.Subagent{}
	a.conv.task = task

	contentH := ContentHeight(a.height)
	a.conv.split.Focus = false
	a.conv.split.CacheKey = ""
	a.convList = newConvList(items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH)
	a.conv.split.List = &a.convList

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
	items := buildConvItems(merged, agents, nil)

	a.conv.sess = a.currentSess
	a.conv.messages = entries
	a.conv.merged = merged
	a.conv.agents = agents
	a.conv.items = items
	a.conv.agent = agent
	a.conv.task = session.TaskItem{}

	contentH := ContentHeight(a.height)
	a.conv.split.Focus = false
	a.conv.split.CacheKey = ""
	a.convList = newConvList(items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH)
	a.conv.split.List = &a.convList

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
