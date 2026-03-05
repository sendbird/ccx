package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/ccx/internal/session"
)

// convItemKind classifies conversation list items.
type convItemKind int

const (
	convMsg   convItemKind = iota // user/assistant message turn
	convTask                      // task item (under assistant message)
	convAgent                     // agent reference (under assistant message)
)

// convItem represents a single row in the conversation list.
type convItem struct {
	kind      convItemKind
	merged    mergedMsg          // for convMsg
	task      session.TaskItem   // for convTask
	agent     session.Subagent   // for convAgent
	indent    int                // 0=message, 1=sub-item
	folded    bool               // for expandable group headers (tasks/agents)
	parentIdx int                // index of parent message in items slice
	groupTag  string             // "tasks" or "agents" — for group header rows
	count     int                // number of items in group (for header display)
}

func (c convItem) FilterValue() string {
	switch c.kind {
	case convMsg:
		return entryFullText(c.merged.entry) + " " + c.merged.entry.Role
	case convTask:
		return c.task.Subject + " " + c.task.Status
	case convAgent:
		return c.agent.FirstPrompt + " " + c.agent.ShortID + " " + c.agent.AgentType
	}
	return ""
}

// convDelegate renders conversation list items.
type convDelegate struct{}

func (d convDelegate) Height() int                             { return 1 }
func (d convDelegate) Spacing() int                            { return 0 }
func (d convDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d convDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	ci, ok := item.(convItem)
	if !ok {
		return
	}
	selected := index == m.Index()
	width := m.Width()
	clamp := lipgloss.NewStyle().MaxWidth(width)
	filterTerm := listFilterTerm(m)

	switch ci.kind {
	case convMsg:
		renderConvMsg(w, ci, selected, width, clamp, filterTerm)
	case convTask:
		renderConvTaskOrAgent(w, ci, selected, width, clamp, filterTerm)
	case convAgent:
		renderConvTaskOrAgent(w, ci, selected, width, clamp, filterTerm)
	}
}

func renderConvMsg(w io.Writer, ci convItem, selected bool, width int, clamp lipgloss.Style, filterTerm string) {
	e := ci.merged.entry
	cursor := "  "
	if selected {
		cursor = convCursorStyle.Render("> ")
	}

	isCompacted := isAutoCompacted(e)

	role := userLabelStyle.Render("USER")
	if isCompacted {
		role = compactBadgeStyle.Render("CMPX")
	} else if e.Role == "assistant" {
		role = assistantLabelStyle.Render("ASST")
	}

	ts := "     "
	if !e.Timestamp.IsZero() {
		ts = dimStyle.Render(e.Timestamp.Format("15:04"))
	}

	// Index range
	idxStr := dimStyle.Render(fmt.Sprintf("#%d", ci.merged.startIdx+1))
	if ci.merged.endIdx > ci.merged.startIdx {
		idxStr = dimStyle.Render(fmt.Sprintf("#%d-%d", ci.merged.startIdx+1, ci.merged.endIdx+1))
	}

	// Text preview
	preview := convMsgPreview(e, width-20)
	pStyle := dimStyle
	if selected {
		pStyle = selectedStyle
	} else if isCompacted {
		pStyle = acDimStyle
	}
	if preview != "" {
		availW := width - 20
		if filterTerm != "" && availW > 0 {
			preview = "  " + highlightSnippet(preview, filterTerm, availW, pStyle)
		} else {
			preview = "  " + pStyle.Render(preview)
		}
	}

	line := fmt.Sprintf("%s%s  %s  %s%s", cursor, role, ts, idxStr, preview)
	fmt.Fprint(w, clamp.Render(line))
}

func renderConvTaskOrAgent(w io.Writer, ci convItem, selected bool, width int, clamp lipgloss.Style, filterTerm string) {
	indent := strings.Repeat("  ", ci.indent+1)
	cursor := " "
	if selected {
		cursor = convCursorStyle.Render(">")
	}

	var line string
	switch ci.kind {
	case convTask:
		// Group header row
		if ci.groupTag != "" {
			// ci.task.Status carries "completed/total" as a formatted string
			counter := ci.task.Status
			counterStyle := dimStyle
			// Parse completed/total to color green when all done
			var comp, total int
			if _, err := fmt.Sscanf(counter, "%d/%d", &comp, &total); err == nil && comp == total && total > 0 {
				counterStyle = lipgloss.NewStyle().Foreground(colorAccent)
			}

			var label string
			if ci.count > 0 {
				// Expandable header (last task-touching message)
				fold := "▸"
				if !ci.folded {
					fold = "▾"
				}
				if selected {
					label = fmt.Sprintf("%s Tasks [%s]", fold, counter+" ✓")
				} else {
					label = fmt.Sprintf("%s Tasks [%s]", fold, counterStyle.Render(counter+" ✓"))
				}
			} else {
				// Marker header — show per-message operation summary
				opDesc := ci.task.Subject
				style := dimStyle
				if selected {
					style = selectedStyle
				}
				maxW := width - len(indent) - 12
				if opDesc != "" {
					if maxW > 3 && len(opDesc) > maxW {
						opDesc = opDesc[:maxW-3] + "..."
					}
					label = "· " + style.Render(opDesc)
				} else {
					if selected {
						label = fmt.Sprintf("· Tasks [%s]", counter+" ✓")
					} else {
						label = fmt.Sprintf("· Tasks [%s]", counterStyle.Render(counter+" ✓"))
					}
				}
			}
			line = fmt.Sprintf("%s%s %s", indent, cursor, label)
			fmt.Fprint(w, clamp.Render(line))
			return
		}

		status := "○"
		switch ci.task.Status {
		case "completed":
			status = lipgloss.NewStyle().Foreground(colorAccent).Render("✓")
		case "in_progress":
			status = lipgloss.NewStyle().Foreground(colorAssistant).Render("◉")
		}
		idLabel := ""
		if ci.task.ID != "" {
			idLabel = dimStyle.Render("#"+ci.task.ID) + " "
		}
		subj := ci.task.Subject
		idW := lipgloss.Width(idLabel)
		maxW := width - len(indent) - 6 - idW
		style := dimStyle
		if selected {
			style = selectedStyle
		}
		if filterTerm != "" && maxW > 0 {
			line = fmt.Sprintf("%s%s %s %s%s", indent, cursor, status, idLabel, highlightSnippet(subj, filterTerm, maxW, style))
		} else {
			if maxW > 3 && len(subj) > maxW {
				subj = subj[:maxW-3] + "..."
			}
			line = fmt.Sprintf("%s%s %s %s%s", indent, cursor, status, idLabel, style.Render(subj))
		}
	case convAgent:
		a := ci.agent
		badge := agentBadgeStyle.Render("⊕")
		typeStr := ""
		if a.AgentType != "" {
			typeStr = dimStyle.Render(":" + a.AgentType)
		}
		msgs := dimStyle.Render(fmt.Sprintf("(%dm)", a.MsgCount))
		prompt := a.FirstPrompt
		maxW := width - len(indent) - 20
		style := dimStyle
		if selected {
			style = selectedStyle
		}
		if filterTerm != "" && maxW > 0 {
			line = fmt.Sprintf("%s%s %s%s %s %s", indent, cursor, badge, typeStr, msgs, highlightSnippet(prompt, filterTerm, maxW, style))
		} else {
			if maxW > 3 && len(prompt) > maxW {
				prompt = prompt[:maxW-3] + "..."
			}
			line = fmt.Sprintf("%s%s %s%s %s %s", indent, cursor, badge, typeStr, msgs, style.Render(prompt))
		}
	}
	fmt.Fprint(w, clamp.Render(line))
}

// convMsgPreview returns a short text preview for a conversation message.
func convMsgPreview(e session.Entry, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	for _, block := range e.Content {
		if block.Type == "text" {
			text := strings.TrimSpace(session.StripXMLTags(stripANSI(block.Text)))
			if text == "" || isSystemText(text) {
				continue
			}
			// Single line, collapse whitespace
			text = strings.ReplaceAll(text, "\n", " ")
			for strings.Contains(text, "  ") {
				text = strings.ReplaceAll(text, "  ", " ")
			}
			if len(text) > maxW {
				text = text[:maxW-3] + "..."
			}
			return text
		}
	}
	// No text — summarize tools
	summary := mergedToolSummary(e)
	if summary != "" {
		if len(summary) > maxW {
			summary = summary[:maxW-3] + "..."
		}
		return toolStyle.Render(summary)
	}
	return ""
}

// buildConvItems builds a flattened conversation item list from merged messages,
// with inline task and agent sub-items under assistant messages.
// A collapsible task group header appears at every task-touching message.
// Individual task rows (expandable) are attached only under the LAST one.
func buildConvItems(merged []mergedMsg, agents []session.Subagent, tasks []session.TaskItem) []convItem {
	// First pass: find all task-touching message indices and the last one.
	var taskMsgIndices []int
	if len(tasks) > 0 {
		for i, m := range merged {
			if m.entry.Role != "assistant" {
				continue
			}
			for _, block := range m.entry.Content {
				if block.Type == "tool_use" && (block.ToolName == "TaskCreate" || block.ToolName == "TaskUpdate" || block.ToolName == "TodoWrite") {
					taskMsgIndices = append(taskMsgIndices, i)
					break
				}
			}
		}
	}
	lastTaskMsgIdx := -1
	if len(taskMsgIndices) > 0 {
		lastTaskMsgIdx = taskMsgIndices[len(taskMsgIndices)-1]
	}
	taskMsgSet := make(map[int]bool, len(taskMsgIndices))
	for _, idx := range taskMsgIndices {
		taskMsgSet[idx] = true
	}

	// Pre-compute task completion stats and ID lookup
	completed := 0
	tasksByID := make(map[string]session.TaskItem, len(tasks))
	for _, t := range tasks {
		if t.Status == "completed" {
			completed++
		}
		tasksByID[t.ID] = t
	}

	var items []convItem
	assignedAgents := make(map[string]bool) // track agents already placed

	for mi, m := range merged {
		parentIdx := len(items)
		items = append(items, convItem{
			kind:   convMsg,
			merged: m,
		})

		// Only add sub-items under assistant messages
		if m.entry.Role != "assistant" {
			continue
		}

		// Find agents spawned during this message range (skip already-assigned and system agents)
		var msgAgents []session.Subagent
		for _, a := range agents {
			if a.Timestamp.IsZero() || assignedAgents[a.ID] || isSystemAgent(a) {
				continue
			}
			// Agent timestamp should fall within the message time range
			if !m.entry.Timestamp.IsZero() {
				diff := a.Timestamp.Sub(m.entry.Timestamp).Seconds()
				if diff >= -5 && diff < 120 {
					msgAgents = append(msgAgents, a)
				}
			}
		}

		// Add agent sub-items
		for _, a := range msgAgents {
			assignedAgents[a.ID] = true
			items = append(items, convItem{
				kind:      convAgent,
				agent:     a,
				indent:    1,
				parentIdx: parentIdx,
			})
		}

		// Attach task group header at every task-touching message.
		// The last one is expandable (count > 0, has children); earlier ones are markers (count = 0).
		if taskMsgSet[mi] {
			expandable := mi == lastTaskMsgIdx
			headerCount := 0
			if expandable {
				headerCount = len(tasks)
			}
			// Build per-message operation summary
			ops := taskOpSummaryResult(m.entry, tasksByID)
			items = append(items, convItem{
				kind:      convTask,
				groupTag:  "tasks",
				count:     headerCount,
				folded:    true,
				indent:    1,
				parentIdx: parentIdx,
				task:      session.TaskItem{Status: fmt.Sprintf("%d/%d", completed, len(tasks)), Subject: ops.compact, Description: ops.detailed},
			})
			if expandable {
				for _, t := range tasks {
					items = append(items, convItem{
						kind:      convTask,
						task:      t,
						indent:    2,
						parentIdx: parentIdx,
					})
				}
			}
		}
	}

	return items
}

// taskOpResult holds both compact (for list label) and detailed (for preview) summaries.
type taskOpResult struct {
	compact  string // one-line summary for conv list
	detailed string // multi-line detail for preview
}

func taskOpSummaryResult(entry session.Entry, tasksByID map[string]session.TaskItem) taskOpResult {
	var compactParts []string
	var detailLines []string
	for _, b := range entry.Content {
		if b.Type != "tool_use" {
			continue
		}
		switch b.ToolName {
		case "TaskCreate":
			var input struct {
				Subject     string `json:"subject"`
				Description string `json:"description"`
			}
			json.Unmarshal([]byte(b.ToolInput), &input)
			subj := input.Subject
			compactSubj := subj
			if len(compactSubj) > 30 {
				compactSubj = compactSubj[:27] + "..."
			}
			if compactSubj != "" {
				compactParts = append(compactParts, "+"+compactSubj)
			}
			detail := "+ Created: " + subj
			if input.Description != "" {
				desc := input.Description
				if len(desc) > 120 {
					desc = desc[:117] + "..."
				}
				detail += "\n    " + desc
			}
			detailLines = append(detailLines, detail)
		case "TaskUpdate":
			var input struct {
				TaskID string `json:"taskId"`
				Status string `json:"status"`
			}
			json.Unmarshal([]byte(b.ToolInput), &input)
			if input.Status == "" {
				continue
			}
			icon := "○"
			switch input.Status {
			case "completed":
				icon = "✓"
			case "in_progress":
				icon = "◉"
			}
			compactLabel := icon + " #" + input.TaskID
			detailLabel := icon + " #" + input.TaskID
			if t, ok := tasksByID[input.TaskID]; ok {
				compactSubj := t.Subject
				if len(compactSubj) > 25 {
					compactSubj = compactSubj[:22] + "..."
				}
				compactLabel = icon + " " + compactSubj
				detailLabel += " " + t.Subject
			}
			compactParts = append(compactParts, compactLabel)
			detailLines = append(detailLines, detailLabel)
		case "TodoWrite":
			compactParts = append(compactParts, "todo updated")
			detailLines = append(detailLines, "Todo list updated")
		}
	}
	return taskOpResult{
		compact:  strings.Join(compactParts, ", "),
		detailed: strings.Join(detailLines, "\n"),
	}
}

// visibleConvItems returns only the items that should be displayed,
// hiding children of folded group headers.
func visibleConvItems(items []convItem) []convItem {
	var visible []convItem
	skipIndent := -1 // when >= 0, skip items with indent > skipIndent
	for _, it := range items {
		if skipIndent >= 0 {
			if it.indent > skipIndent {
				continue
			}
			skipIndent = -1
		}
		visible = append(visible, it)
		if it.groupTag != "" && it.folded {
			skipIndent = it.indent
		}
	}
	return visible
}

func newConvList(items []convItem, width, height int) list.Model {
	vis := visibleConvItems(items)
	listItems := make([]list.Item, len(vis))
	for i, ci := range vis {
		listItems[i] = ci
	}

	l := list.New(listItems, convDelegate{}, width, height)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowFilter(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.Filter = substringFilter
	l.DisableQuitKeybindings()
	configureListSearch(&l)
	l.SetSize(width, height)
	return l
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

	// Build conversation items
	a.conv.items = buildConvItems(a.conv.merged, agents, sess.Tasks)

	if info, err := os.Stat(sess.FilePath); err == nil {
		a.lastMsgLoadTime = info.ModTime()
	}

	// Create list
	contentH := ContentHeight(a.height)
	a.conv.split.Focus = false
	a.conv.split.CacheKey = ""
	a.convList = newConvList(a.conv.items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH)

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

	// Edit menu
	if a.editMenu {
		return a.handleEditMenu(key)
	}

	switch key {
	case "q":
		return a, tea.Quit
	case "esc":
		if !sp.Show {
			a.liveTail = false
			a.conv.split.BottomAlign = false
			if a.conv.task.ID != "" || a.conv.agent.ShortID != "" {
				return a.popNavFrame()
			}
			a.state = viewSessions
			return a, nil
		}
	case "c":
		a.pushNavFrame()
		return a.openFullConversation()
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
			// Drill into task — show conversation entries related to this task
			a.pushNavFrame()
			return a.openTaskConversation(item.task)
		case convAgent:
			// Push nav stack and open agent as conversation split view
			a.pushNavFrame()
			return a.openAgentConversation(item.agent)
		case convMsg:
			// If preview focused on a Task block, jump to the agent
			if sp.Focus && sp.Folds != nil {
				bc := sp.Folds.BlockCursor
				entry := sp.Folds.Entry
				if bc >= 0 && bc < len(entry.Content) {
					block := entry.Content[bc]
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
	case "I":
		if !a.config.TmuxEnabled {
			return a, nil
		}
		return a.sendInputToLive(a.currentSess.ProjectPath, a.currentSess.ID)
	case "J":
		if !a.config.TmuxEnabled {
			return a, nil
		}
		return a.jumpToTmuxPane(a.currentSess.ProjectPath, a.currentSess.ID)
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
			if sp.Folds != nil {
				debugLog("conv: %s pressed, cursor=%d nBlocks=%d vpH=%d vpOffset=%d",
					key, sp.Folds.BlockCursor, len(sp.Folds.Entry.Content), sp.Preview.Height, sp.Preview.YOffset)
				fr := sp.Folds.HandleKey(key)
				debugLog("conv: HandleKey result=%d newCursor=%d", fr, sp.Folds.BlockCursor)
				if fr == foldCursorMoved {
					sp.RefreshFoldCursor(a.width, a.splitRatio)
					sp.ScrollToBlock()
					return a, nil
				}
				return a, nil
			}
		}
		result = sp.HandleFocusedKeys(key)
		switch result {
		case splitKeySearchFromPreview:
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

	var cacheKey string
	if item.kind == convAgent {
		cacheKey = fmt.Sprintf("agent:%s:%d", item.agent.ShortID, len(entry.Content))
	} else {
		cacheKey = fmt.Sprintf("%d:%d", item.merged.startIdx, len(entry.Content))
	}
	if cacheKey == sp.CacheKey {
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
		sp.CacheKey = cacheKey
		if sp.Folds != nil {
			sp.Folds.Reset(entry)
		}
		sp.RefreshFoldPreview(a.width, a.splitRatio)
		sp.Preview.YOffset = 0
	} else {
		sp.CacheKey = cacheKey
		if sp.Folds != nil {
			oldBlockCount := len(sp.Folds.Entry.Content)
			sp.Folds.GrowBlocks(entry, oldBlockCount)
		}
		sp.RefreshFoldPreview(a.width, a.splitRatio)
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
			a.convList.Select(len(items) - 1)
		}
		a.updateConvPreview()
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
	a.conv.items = buildConvItems(a.conv.merged, agents, a.conv.sess.Tasks)

	// Preserve cursor position
	oldIdx := a.convList.Index()
	contentH := ContentHeight(a.height)
	a.convList = newConvList(a.conv.items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH)
	visCount := len(a.convList.Items())
	if oldIdx < visCount {
		a.convList.Select(oldIdx)
	}
	a.conv.split.CacheKey = ""
	a.updateConvPreview()
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
	for i, v := range vis {
		if v.groupTag == header.groupTag && v.parentIdx == header.parentIdx {
			a.convList.Select(i)
			break
		}
	}
	a.conv.split.CacheKey = ""
	a.updateConvPreview()
}

// renderConvSplit renders the conversation split view.
func (a *App) renderConvSplit() string {
	sp := &a.conv.split
	return sp.Render(a.width, a.height, a.splitRatio)
}

// extractTaskEntries returns entries related to a specific task.
// It finds ranges where the task was in_progress and collects all entries
// in those ranges, plus the TaskCreate and final TaskUpdate entries.
func extractTaskEntries(entries []session.Entry, taskID string) []session.Entry {
	type taskRange struct{ start, end int }
	var ranges []taskRange
	curStart := -1

	for i, e := range entries {
		for _, b := range e.Content {
			if b.Type != "tool_use" || (b.ToolName != "TaskUpdate" && b.ToolName != "TaskCreate") {
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

	a.state = viewConversation
	a.updateConvPreview()
	return a, nil
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
