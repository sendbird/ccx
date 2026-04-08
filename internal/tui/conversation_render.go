package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
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

	if ci.label != "" {
		style := dimStyle
		if selected {
			style = selectedStyle
		}
		if ci.groupTag != "" {
			fold := "▾"
			if ci.folded {
				fold = "▸"
			}
			line := fmt.Sprintf("%s%s %s", indent, cursor, style.Render(fmt.Sprintf("%s %s [%d]", fold, ci.label, ci.count)))
			fmt.Fprint(w, clamp.Render(line))
			return
		}

		status := "○"
		switch ci.kind {
		case convAgent:
			status = agentBadgeStyle.Render("⊕")
			switch ci.agentStatus {
			case "completed":
				status = taskDoneStyle.Render("✓")
			case "stopped":
				status = dimStyle.Render("⏹")
			case "running":
				status = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Render("◉")
			}
		case convTask:
			switch ci.task.Status {
			case "completed":
				status = taskDoneStyle.Render("✓")
			case "in_progress":
				status = taskInProgressStyle.Render("◉")
			case "stopped":
				status = dimStyle.Render("⏹")
			}
		}

		maxW := width - len(indent) - 6
		label := ci.label
		if filterTerm != "" && maxW > 0 {
			label = highlightSnippet(label, filterTerm, maxW, style)
		} else {
			if maxW > 3 && len(label) > maxW {
				label = label[:maxW-3] + "..."
			}
			label = style.Render(label)
		}
		line := fmt.Sprintf("%s%s %s %s", indent, cursor, status, label)
		fmt.Fprint(w, clamp.Render(line))
		return
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
		switch ci.agentStatus {
		case "completed":
			badge = taskDoneStyle.Render("✓")
		case "stopped":
			badge = dimStyle.Render("⏹")
		case "running":
			badge = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Render("◉")
		}
		typeStr := ""
		if a.AgentType == "aside_question" {
			if ci.agentStatus == "" {
				badge = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Render("?")
			}
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

// buildBgTaskMap scans merged messages for background bash tasks.
// A background task is identified by a tool_result containing
// "Command running in background with ID: <taskID>".
// Returns taskID → short command description.
func buildBgTaskMap(merged []mergedMsg) map[string]string {
	// First pass: collect ALL Bash tool_use commands by block ID across all entries.
	bashCmds := make(map[string]string) // tool_use block ID → command description
	for _, m := range merged {
		for _, b := range m.entry.Content {
			if b.Type == "tool_use" && b.ToolName == "Bash" {
				var input struct {
					Command     string `json:"command"`
					Description string `json:"description"`
				}
				json.Unmarshal([]byte(b.ToolInput), &input)
				label := input.Description
				if label == "" {
					// Use first line of command, truncated
					label = input.Command
					if nl := strings.IndexByte(label, '\n'); nl > 0 {
						label = label[:nl]
					}
				}
				bashCmds[b.ID] = label
			}
		}
	}

	// Second pass: find tool_results that mention background task IDs.
	bgTasks := make(map[string]string)
	for _, m := range merged {
		for _, b := range m.entry.Content {
			if b.Type != "tool_result" || b.Text == "" {
				continue
			}
			const prefix = "Command running in background with ID: "
			idx := strings.Index(b.Text, prefix)
			if idx < 0 {
				continue
			}
			rest := b.Text[idx+len(prefix):]
			taskID := rest
			for i, c := range rest {
				if c == '.' || c == ' ' || c == '\n' {
					taskID = rest[:i]
					break
				}
			}
			if taskID == "" {
				continue
			}
			// b.ID is the tool_use_id from the tool_result, matching the Bash tool_use
			if cmd, ok := bashCmds[b.ID]; ok {
				bgTasks[taskID] = cmd
			} else {
				bgTasks[taskID] = "bash"
			}
		}
	}
	return bgTasks
}

// inferAgentStatuses scans conversation entries to determine the last known status
// of each agent: "running" (TaskOutput sent, no result yet), "completed" (result received),
// or "stopped" (TaskStop sent).
func inferAgentStatuses(merged []mergedMsg) map[string]string {
	statuses := make(map[string]string)
	pendingOutput := make(map[string]string) // tool_use block ID → taskID

	for _, m := range merged {
		for _, b := range m.entry.Content {
			switch {
			case b.Type == "tool_use" && b.ToolName == "TaskOutput":
				var input struct {
					TaskID string `json:"task_id"`
				}
				json.Unmarshal([]byte(b.ToolInput), &input)
				if input.TaskID != "" {
					statuses[input.TaskID] = "running"
					pendingOutput[b.ID] = input.TaskID
				}
			case b.Type == "tool_use" && b.ToolName == "TaskStop":
				var input struct {
					TaskID string `json:"task_id"`
				}
				json.Unmarshal([]byte(b.ToolInput), &input)
				if input.TaskID != "" {
					statuses[input.TaskID] = "stopped"
				}
			case b.Type == "tool_result":
				if taskID, ok := pendingOutput[b.ID]; ok {
					// Result received — mark completed unless already stopped
					if statuses[taskID] == "running" {
						statuses[taskID] = "completed"
					}
					delete(pendingOutput, b.ID)
				}
			}
		}
	}

	return statuses
}

// buildConvItems builds a flattened conversation item list from merged messages,
// with inline task and agent sub-items under assistant messages.
// A collapsible task group header appears at every task-touching message.
// Individual task rows (expandable) are attached only under the LAST one.
func buildConvItems(merged []mergedMsg, agents []session.Subagent, tasks []session.TaskItem) []convItem {
	// First pass: find all task-touching message indices and the last one.
	// Always scan for task operations (TaskCreate, TaskOutput, etc.) regardless
	// of whether a resolved task list exists — operations should be visible as
	// sub-items even without a task board.
	var taskMsgIndices []int
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

	// Build agent lookup by both full ID and ShortID for resolving task references.
	agentsByID := make(map[string]session.Subagent, len(agents)*2)
	for _, a := range agents {
		agentsByID[a.ID] = a
		if a.ShortID != "" {
			agentsByID[a.ShortID] = a
		}
	}

	// Build background task lookup: taskID → command description.
	// Background bash tasks produce tool_result with "Command running in background with ID: <taskID>".
	bgTasks := buildBgTaskMap(merged)

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

	// Infer agent status by scanning all entries for TaskOutput/TaskStop/TaskResult.
	// Last operation per agent wins: TaskStop→"stopped", tool_result after TaskOutput→"completed",
	// TaskOutput without result→"running".
	agentStatuses := inferAgentStatuses(merged)

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
			status := agentStatuses[a.ID]
			if status == "" {
				status = agentStatuses[a.ShortID]
			}
			items = append(items, convItem{
				kind:        convAgent,
				agent:       a,
				agentStatus: status,
				indent:      1,
				parentIdx:   parentIdx,
			})
		}

		// Attach task operations and task list items under assistant messages.
		if taskMsgSet[mi] {
			expandable := mi == lastTaskMsgIdx

			// Add individual task operation lines as separate items.
			for _, b := range m.entry.Content {
				if b.Type != "tool_use" {
					continue
				}
				var taskID, icon, verb, subject string
				switch b.ToolName {
				case "TaskCreate":
					var input struct {
						Subject string `json:"subject"`
					}
					json.Unmarshal([]byte(b.ToolInput), &input)
					if input.Subject != "" {
						icon, verb, subject = "📋", "Create", input.Subject
					}
				case "TaskOutput":
					var input struct {
						TaskID string `json:"task_id"`
					}
					json.Unmarshal([]byte(b.ToolInput), &input)
					taskID, icon, verb = input.TaskID, "⏳", "Waiting"
				case "TaskStop":
					var input struct {
						TaskID string `json:"task_id"`
					}
					json.Unmarshal([]byte(b.ToolInput), &input)
					taskID, icon, verb = input.TaskID, "⏹", "Stop"
				}
				if taskID == "" && subject == "" {
					continue
				}
				if subject != "" {
					// TaskCreate: show subject directly
					label := icon + " " + subject
					if len(label) > 50 {
						label = label[:47] + "..."
					}
					items = append(items, convItem{
						kind:      convTask,
						task:      session.TaskItem{Subject: label},
						indent:    1,
						parentIdx: parentIdx,
					})
				} else {
					label, detail := resolveTaskLabel(icon, verb, taskID, agentsByID, bgTasks, 40)
					items = append(items, convItem{
						kind:      convTask,
						task:      session.TaskItem{Subject: label, Description: detail, ID: taskID},
						bgTaskID:  taskID,
						indent:    1,
						parentIdx: parentIdx,
					})
				}
			}

			// Expandable task list header (only on last task-touching message, and only if tasks exist)
			if expandable && len(tasks) > 0 {
				items = append(items, convItem{
					kind:      convTask,
					groupTag:  "tasks",
					count:     len(tasks),
					folded:    true,
					indent:    1,
					parentIdx: parentIdx,
					task:      session.TaskItem{Status: fmt.Sprintf("%d/%d", completed, len(tasks))},
				})
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

// buildEntityTree builds an entity-centric tree view: agents, background jobs,
// and task board items grouped under collapsible section headers.
func buildEntityTree(
	merged []mergedMsg,
	agents []session.Subagent,
	tasks []session.TaskItem,
	agentStatuses map[string]string,
) []convItem {
	var items []convItem

	// --- Agents section ---
	if len(agents) > 0 {
		visibleAgents := make([]session.Subagent, 0, len(agents))
		for _, a := range agents {
			if !isSystemAgent(a) {
				visibleAgents = append(visibleAgents, a)
			}
		}
		if len(visibleAgents) > 0 {
			items = append(items, convItem{
				kind:     convTask,
				groupTag: "agents",
				count:    len(visibleAgents),
				folded:   false,
				indent:   0,
				label:    "Agents",
				task:     session.TaskItem{Subject: fmt.Sprintf("Agents (%d)", len(visibleAgents))},
			})
			for _, a := range visibleAgents {
				status := agentStatuses[a.ID]
				if status == "" {
					status = agentStatuses[a.ShortID]
				}
				items = append(items, convItem{
					kind:        convAgent,
					agent:       a,
					agentStatus: status,
					indent:      1,
					label:       compactTreeLabel("Agent", agentTreeName(a), 44),
				})
			}
		}
	}

	// --- Background jobs section ---
	bgTasks := buildBgTaskMap(merged)
	if len(bgTasks) > 0 {
		// Build job items with status from TaskOutput results
		type bgJob struct {
			id     string
			desc   string
			status string // "pending", "completed", "stopped"
		}
		var jobs []bgJob
		for id, desc := range bgTasks {
			status := "pending"
			// Scan for TaskOutput results to determine status
			for _, m := range merged {
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
			jobs = append(jobs, bgJob{id: id, desc: desc, status: status})
		}
		// Sort: pending first, then by ID
		sort.Slice(jobs, func(i, j int) bool {
			if jobs[i].status != jobs[j].status {
				if jobs[i].status == "pending" {
					return true
				}
				if jobs[j].status == "pending" {
					return false
				}
			}
			return jobs[i].id < jobs[j].id
		})

		items = append(items, convItem{
			kind:     convTask,
			groupTag: "bgjobs",
			count:    len(jobs),
			folded:   false,
			indent:   0,
			label:    "BG Jobs",
			task:     session.TaskItem{Subject: fmt.Sprintf("Background Jobs (%d)", len(jobs))},
		})
		for _, j := range jobs {
			// Show just the first line of the command description
			desc := j.desc
			if nl := strings.IndexByte(desc, '\n'); nl > 0 {
				desc = desc[:nl]
			}
			items = append(items, convItem{
				kind:     convTask,
				task:     session.TaskItem{Subject: desc, ID: j.id, Status: j.status},
				bgTaskID: j.id,
				indent:   1,
				label:    compactTreeLabel("BG", j.id+" "+desc, 44),
			})
		}
	}

	// --- Task board section ---
	if len(tasks) > 0 {
		completed := 0
		for _, t := range tasks {
			if t.Status == "completed" {
				completed++
			}
		}
		items = append(items, convItem{
			kind:     convTask,
			groupTag: "tasks",
			count:    len(tasks),
			folded:   false,
			indent:   0,
			label:    "Tasks",
			task:     session.TaskItem{Subject: fmt.Sprintf("Task Board (%d/%d)", completed, len(tasks))},
		})
		for _, t := range tasks {
			idTag := ""
			if t.ID != "" {
				idTag = "#" + t.ID + " "
			}
			items = append(items, convItem{
				kind:   convTask,
				task:   t,
				indent: 1,
				label:  compactTreeLabel("Task", idTag+t.Subject, 44),
			})
		}
	}

	return items
}

func truncate(s string, maxW int) string {
	if len(s) <= maxW || maxW <= 3 {
		return s
	}
	return s[:maxW-3] + "..."
}

func compactTreeLabel(kind, text string, maxW int) string {
	label := kind + ": " + strings.TrimSpace(text)
	if len(label) <= maxW || maxW <= 3 {
		return label
	}
	return label[:maxW-3] + "..."
}

func agentTreeName(a session.Subagent) string {
	name := a.ShortID
	if name == "" {
		name = "agent"
	}
	if a.AgentType != "" {
		name += " [" + a.AgentType + "]"
	}
	return name
}

// extractInlineTasks builds a task list from TaskCreate/TaskUpdate tool calls
// in the conversation entries. Used as fallback when no file-based tasks exist.
func extractInlineTasks(entries []session.Entry) []session.TaskItem {
	tasks := make(map[string]*session.TaskItem) // keyed by task ID
	var order []string                          // preserve creation order
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

// agentLabel returns a human-readable label for an agent, e.g. ":Explore (search for files)"
func agentLabel(a session.Subagent, maxW int) string {
	var parts []string
	if a.AgentType != "" {
		parts = append(parts, ":"+a.AgentType)
	}
	if a.FirstPrompt != "" {
		prompt := a.FirstPrompt
		remaining := maxW
		for _, p := range parts {
			remaining -= len(p) + 1
		}
		if remaining > 6 && len(prompt) > remaining {
			prompt = prompt[:remaining-3] + "..."
		}
		parts = append(parts, prompt)
	}
	if len(parts) == 0 {
		return "#" + a.ShortID
	}
	return strings.Join(parts, " ")
}

// taskOpItem holds extracted info for a single task operation (TaskOutput/TaskStop).
type taskOpItem struct {
	taskID  string
	compact string
	detail  string
}

// resolveTaskLabel returns compact and detail labels for a task/agent/background-task reference.
func resolveTaskLabel(icon, verb, taskID string, agentsByID map[string]session.Subagent, bgTasks map[string]string, maxW int) (string, string) {
	if ag, ok := agentsByID[taskID]; ok {
		return icon + " " + agentLabel(ag, maxW), icon + " " + verb + ": " + agentLabel(ag, 80)
	}
	if cmd, ok := bgTasks[taskID]; ok {
		short := cmd
		if len(short) > maxW {
			short = short[:maxW-3] + "..."
		}
		return icon + " " + short, icon + " " + verb + ": " + cmd
	}
	shortID := taskID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	return icon + " #" + shortID, icon + " " + verb + " #" + taskID
}

func taskOpSummaryResult(entry session.Entry, tasksByID map[string]session.TaskItem, agentsByID map[string]session.Subagent, bgTasks map[string]string) taskOpResult {
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
			label, detail := resolveTaskLabel("⏳", "Waiting", input.TaskID, agentsByID, bgTasks, 25)
			compactParts = append(compactParts, label)
			detailLines = append(detailLines, detail)
		case "TaskGet":
			var input struct {
				TaskID string `json:"taskId"`
			}
			json.Unmarshal([]byte(b.ToolInput), &input)
			label, detail := resolveTaskLabel("📋", "Read", input.TaskID, agentsByID, bgTasks, 25)
			compactParts = append(compactParts, label)
			detailLines = append(detailLines, detail)
		case "TaskStop":
			var input struct {
				TaskID string `json:"task_id"`
			}
			json.Unmarshal([]byte(b.ToolInput), &input)
			label, detail := resolveTaskLabel("⏹", "Stop", input.TaskID, agentsByID, bgTasks, 25)
			compactParts = append(compactParts, label)
			detailLines = append(detailLines, detail)
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
