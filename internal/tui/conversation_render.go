package tui

import (
	"encoding/json"
	"io"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/ccx/internal/session"
)

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

	// Image badge
	imgBadge := ""
	for _, block := range e.Content {
		if block.Type == "image" {
			imgBadge = " " + lipgloss.NewStyle().Foreground(lipgloss.Color("#87CEEB")).Render("🖼")
			break
		}
	}

	line := fmt.Sprintf("%s%s  %s  %s%s%s", cursor, role, ts, idxStr, imgBadge, preview)
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
				counterStyle = taskDoneStyle
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
			status = taskDoneStyle.Render("✓")
		case "in_progress":
			status = taskInProgressStyle.Render("◉")
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
		// Group header for unattached agents
		if ci.groupTag != "" {
			fold := "▸"
			if !ci.folded {
				fold = "▾"
			}
			label := fmt.Sprintf("%s Agents [%d]", fold, ci.count)
			style := dimStyle
			if selected {
				style = selectedStyle
			}
			line = fmt.Sprintf("%s%s %s", indent, cursor, style.Render(label))
			break
		}
		a := ci.agent
		badge := agentBadgeStyle.Render("⊕")
		typeStr := ""
		if a.AgentType == "aside_question" {
			badge = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Render("?")
			typeStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Render(":btw")
		} else if a.AgentType != "" {
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
	// No text — check for images
	var images int
	for _, block := range e.Content {
		if block.Type == "image" {
			images++
		}
	}
	if images > 0 {
		s := fmt.Sprintf("[%d image(s)]", images)
		if len(s) > maxW {
			s = s[:maxW-3] + "..."
		}
		return dimStyle.Render(s)
	}
	// Summarize tools
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
				if block.Type == "tool_use" && isTaskTool(block.ToolName) {
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

	// Pre-assign each agent to the last assistant message that precedes its timestamp.
	// This places agents chronologically at the right position in the conversation.
	agentsByMsg := make(map[int][]session.Subagent) // message index → agents
	for _, a := range agents {
		if a.Timestamp.IsZero() || isSystemAgent(a) {
			continue
		}
		bestIdx := -1
		for mi, m := range merged {
			if m.entry.Role != "assistant" || m.entry.Timestamp.IsZero() {
				continue
			}
			if !a.Timestamp.Before(m.entry.Timestamp) {
				bestIdx = mi
			}
		}
		if bestIdx >= 0 {
			agentsByMsg[bestIdx] = append(agentsByMsg[bestIdx], a)
		} else {
			// Agent predates all messages — attach to first assistant message
			for mi, m := range merged {
				if m.entry.Role == "assistant" {
					agentsByMsg[mi] = append(agentsByMsg[mi], a)
					break
				}
			}
		}
	}

	var items []convItem

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

		// Add agent sub-items assigned to this message
		for _, a := range agentsByMsg[mi] {
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

// extractInlineTasks builds a task list from TaskCreate/TaskUpdate tool calls
// in the conversation entries. Used as fallback when no file-based tasks exist.
func extractInlineTasks(entries []session.Entry) []session.TaskItem {
	tasks := make(map[string]*session.TaskItem) // keyed by task ID
	var order []string                           // preserve creation order
	nextID := 1

	for _, e := range entries {
		if e.Role != "assistant" {
			continue
		}
		for _, b := range e.Content {
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
				if input.Subject == "" {
					continue
				}
				id := fmt.Sprintf("%d", nextID)
				nextID++
				t := &session.TaskItem{
					ID:          id,
					Subject:     input.Subject,
					Description: input.Description,
					Status:      "pending",
				}
				tasks[id] = t
				order = append(order, id)
			case "TaskUpdate":
				var input struct {
					TaskID  string `json:"taskId"`
					Status  string `json:"status"`
					Subject string `json:"subject"`
				}
				json.Unmarshal([]byte(b.ToolInput), &input)
				if input.TaskID == "" {
					continue
				}
				t, ok := tasks[input.TaskID]
				if !ok {
					// Task created before our scan window; create a stub
					t = &session.TaskItem{ID: input.TaskID, Status: "pending"}
					tasks[input.TaskID] = t
					order = append(order, input.TaskID)
				}
				if input.Status != "" {
					t.Status = input.Status
				}
				if input.Subject != "" {
					t.Subject = input.Subject
				}
			}
		}
	}

	result := make([]session.TaskItem, 0, len(order))
	for _, id := range order {
		result = append(result, *tasks[id])
	}
	return result
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
		case "TaskOutput":
			var input struct {
				TaskID string `json:"task_id"`
			}
			json.Unmarshal([]byte(b.ToolInput), &input)
			label := "⏳ waiting #" + input.TaskID
			if len(input.TaskID) > 8 {
				label = "⏳ waiting #" + input.TaskID[:8]
			}
			compactParts = append(compactParts, label)
			detailLines = append(detailLines, "⏳ Waiting for agent output: "+input.TaskID)
		case "TaskGet":
			var input struct {
				TaskID string `json:"taskId"`
			}
			json.Unmarshal([]byte(b.ToolInput), &input)
			compactParts = append(compactParts, "get #"+input.TaskID)
			detailLines = append(detailLines, "📋 Read task #"+input.TaskID)
		case "TaskStop":
			var input struct {
				TaskID string `json:"task_id"`
			}
			json.Unmarshal([]byte(b.ToolInput), &input)
			label := "⏹ stop #" + input.TaskID
			if len(input.TaskID) > 8 {
				label = "⏹ stop #" + input.TaskID[:8]
			}
			compactParts = append(compactParts, label)
			detailLines = append(detailLines, "⏹ Stopped agent: "+input.TaskID)
		case "TaskList":
			compactParts = append(compactParts, "list")
			detailLines = append(detailLines, "📋 Listed tasks")
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

func isTaskTool(name string) bool {
	switch name {
	case "TaskCreate", "TaskUpdate", "TaskGet", "TaskOutput", "TaskStop", "TaskList", "TodoWrite":
		return true
	}
	return false
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

