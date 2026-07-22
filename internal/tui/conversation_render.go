package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

func renderConvMsg(w io.Writer, ci convItem, selected bool, width int, clamp lipgloss.Style, filterTerm string) {
	e := ci.merged.entry
	cursor := "  "
	if ci.steering {
		cursor = planBadge.Render("◆ ")
	}
	if selected {
		cursor = convCursorStyle.Render("> ")
	}

	isCompacted := isAutoCompacted(e)

	role := userLabelStyle.Render(roleChip("user"))
	if isCompacted {
		role = compactBadgeStyle.Render(roleChip("compact"))
	} else if e.Role == "assistant" {
		role = assistantLabelStyle.Render(roleChip("assistant"))
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
			imgBadge = " " + lipgloss.NewStyle().Foreground(lipgloss.Color("#87CEEB")).Render(iconImage)
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
			fold := iconFoldOpen
			if ci.folded {
				fold = iconFoldClosed
			}
			line := fmt.Sprintf("%s%s %s", indent, cursor, style.Render(fmt.Sprintf("%s %s [%d]", fold, ci.label, ci.count)))
			fmt.Fprint(w, clamp.Render(line))
			return
		}

		status := iconIdle
		switch ci.kind {
		case convAgent:
			status = agentBadgeStyle.Render(iconAgent)
			switch ci.agentStatus {
			case "completed":
				status = taskDoneStyle.Render(iconDone)
			case "stopped":
				status = dimStyle.Render(iconStopped)
			case "running":
				status = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Render(iconActive)
			}
		case convTask:
			switch ci.task.Status {
			case "completed":
				status = taskDoneStyle.Render(iconDone)
			case "in_progress":
				status = taskInProgressStyle.Render(iconActive)
			case "stopped":
				status = dimStyle.Render(iconStopped)
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
				fold := iconFoldClosed
				if !ci.folded {
					fold = iconFoldOpen
				}
				if selected {
					label = fmt.Sprintf("%s Tasks [%s]", fold, counter+" "+iconDone)
				} else {
					label = fmt.Sprintf("%s Tasks [%s]", fold, counterStyle.Render(counter+" "+iconDone))
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
						label = fmt.Sprintf("· Tasks [%s]", counter+" "+iconDone)
					} else {
						label = fmt.Sprintf("· Tasks [%s]", counterStyle.Render(counter+" "+iconDone))
					}
				}
			}
			line = fmt.Sprintf("%s%s %s", indent, cursor, label)
			fmt.Fprint(w, clamp.Render(line))
			return
		}

		status := iconIdle
		switch ci.task.Status {
		case "completed":
			status = taskDoneStyle.Render(iconDone)
		case "in_progress":
			status = taskInProgressStyle.Render(iconActive)
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
			fold := iconFoldClosed
			if !ci.folded {
				fold = iconFoldOpen
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
		badge := agentBadgeStyle.Render(iconAgent)
		switch ci.agentStatus {
		case "completed":
			badge = taskDoneStyle.Render(iconDone)
		case "stopped":
			badge = dimStyle.Render(iconStopped)
		case "running":
			badge = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Render(iconActive)
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

func renderConvFlowNode(w io.Writer, ci convItem, selected bool, width int, clamp lipgloss.Style, filterTerm string) {
	indent := strings.Repeat("  ", ci.indent+1)
	cursor := " "
	if selected {
		cursor = convCursorStyle.Render(">")
	}
	style := dimStyle
	if selected {
		style = selectedStyle
	}

	glyph := iconIdle
	label := ci.label
	switch ci.kind {
	case convWorkflow:
		glyph = iconFoldOpen
		if ci.folded {
			glyph = iconFoldClosed
		}
		glyph += " " + workflowStatusGlyph(ci.workflow.Status)
		label = ci.workflow.Name
		if label == "" {
			label = ci.workflow.RunID
		}
		label += fmt.Sprintf(" [%d agents", ci.workflow.AgentCount)
		if ci.workflow.TotalTokens > 0 {
			label += " · " + compactTokenCount(ci.workflow.TotalTokens)
		}
		label += "]"
	case convPhase:
		glyph = "─"
		if label == "" {
			label = ci.phase.Title
		}
	case convShell:
		glyph = shellStatusGlyph(ci.shell.Status)
		label = ci.shell.Description
		if label == "" {
			label = ci.shell.Command
		}
		if nl := strings.IndexByte(label, '\n'); nl >= 0 {
			label = label[:nl]
		}
		if ci.shell.PollCount > 0 {
			label += fmt.Sprintf(" · %d poll", ci.shell.PollCount)
			if ci.shell.PollCount != 1 {
				label += "s"
			}
		}
		if ci.shell.Persistent {
			label += " · persistent"
		}
	case convDecision:
		glyph = "▣"
		if data, ok := ci.decision.Data.(session.DecisionData); ok {
			label = data.Label
		}
	}

	if ci.summaryOnly {
		style = acDimStyle
	}
	badges := renderFacetBadges(ci.facets, ci.aggregate)
	maxW := width - lipgloss.Width(indent) - lipgloss.Width(cursor) - lipgloss.Width(glyph) - lipgloss.Width(badges) - 4
	if maxW < 4 {
		maxW = 4
	}
	if filterTerm != "" {
		label = highlightSnippet(label, filterTerm, maxW, style)
	} else {
		label = style.Render(truncate(label, maxW))
	}
	line := fmt.Sprintf("%s%s %s %s%s", indent, cursor, glyph, label, badges)
	fmt.Fprint(w, clamp.Render(line))
}

func workflowStatusGlyph(status string) string {
	switch strings.ToLower(status) {
	case "completed", "done", "success":
		return taskDoneStyle.Render("✓")
	case "failed", "error":
		return errorStyle.Render("✗")
	case "running", "in_progress":
		return taskInProgressStyle.Render("⟳")
	default:
		return dimStyle.Render("○")
	}
}

func shellStatusGlyph(status string) string {
	switch strings.ToLower(status) {
	case "completed":
		return taskDoneStyle.Render("✓")
	case "failed":
		return errorStyle.Render("✗")
	case "killed", "stopped":
		return dimStyle.Render("⊘")
	case "polled":
		return taskInProgressStyle.Render("⟳")
	default:
		return taskInProgressStyle.Render("⟳")
	}
}

func compactTokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM tok", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk tok", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d tok", n)
	}
}

func renderFacetBadges(f session.FacetSummary, aggregate bool) string {
	var parts []string
	if n := f.Counts[session.ArtifactChange]; n > 0 {
		parts = append(parts, fmt.Sprintf("Δ%d", n))
	}
	if n := f.Counts[session.ArtifactRef]; n > 0 {
		parts = append(parts, fmt.Sprintf("R%d", n))
	}
	if n := f.Counts[session.ArtifactImage]; n > 0 {
		parts = append(parts, fmt.Sprintf("I%d", n))
	}
	if f.Errors > 0 {
		parts = append(parts, fmt.Sprintf("!%d", f.Errors))
	}
	if f.Tokens > 0 {
		parts = append(parts, compactTokenCount(f.Tokens))
	}
	if len(parts) == 0 {
		return ""
	}
	prefix := "  "
	if aggregate {
		prefix += "Σ"
	}
	return dimStyle.Render(prefix + strings.Join(parts, " "))
}

func renderConvSessionMeta(w io.Writer, ci convItem, selected bool, width int, clamp lipgloss.Style, filterTerm string) {
	cursor := "  "
	if selected {
		cursor = convCursorStyle.Render("> ")
	}

	style := dimStyle
	if selected {
		style = selectedStyle
	}

	badge := "[?]"
	title := ci.label
	subtitle := ""
	switch ci.sessionMeta {
	case "summary":
		badge = planBadge.Render("[F]")
		if title == "" {
			title = "Session Flow"
		}
		subtitle = renderFacetBadges(ci.facets, true)
	case "memory":
		badge = memoryBadge.Render("[M]")
		if title == "" {
			title = "Session Memory"
		}
		subtitle = "memory files and todos"
	case "tasksplan":
		badge = planBadge.Render("[P]")
		if title == "" {
			title = "Session Tasks/Plan"
		}
		subtitle = "tasks, cron jobs, agents, and plans"
	}

	text := title
	if subtitle != "" {
		text += "  " + subtitle
	}
	availW := max(width-10, 10)
	if filterTerm != "" && availW > 0 {
		text = highlightSnippet(text, filterTerm, availW, style)
	} else {
		text = style.Render(truncate(text, availW))
	}

	line := cursor + badge + " " + text
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
	for _, block := range e.Content {
		if block.Type == "tool_result" && block.IsError {
			text := strings.TrimSpace(session.StripXMLTags(stripANSI(block.Text)))
			text = strings.ReplaceAll(text, "\n", " ")
			for strings.Contains(text, "  ") {
				text = strings.ReplaceAll(text, "  ", " ")
			}
			if text == "" {
				return errorStyle.Render("[error]")
			}
			text = "[error] " + text
			if len(text) > maxW {
				text = text[:maxW-3] + "..."
			}
			return errorStyle.Render(text)
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

func mergedIndexForOrigin(merged []mergedMsg, messageUUID string, entryIndex int) int {
	for i, message := range merged {
		if messageUUID != "" && message.entry.UUID == messageUUID {
			return i
		}
		if entryIndex >= message.startIdx && entryIndex <= message.endIdx {
			return i
		}
	}
	return -1
}

func isTerminalWorkflowStatus(status string) bool {
	switch strings.ToLower(status) {
	case "completed", "done", "success", "failed", "error", "cancelled", "stopped":
		return true
	default:
		return false
	}
}

func workflowAgentStatus(status string) string {
	switch strings.ToLower(status) {
	case "done", "completed", "success":
		return "completed"
	case "error", "failed", "stopped", "cancelled":
		return "stopped"
	case "running", "in_progress":
		return "running"
	default:
		return status
	}
}

func workflowPhases(run session.WorkflowRun) []session.WorkflowPhase {
	if len(run.Phases) > 0 {
		return run.Phases
	}
	seen := make(map[int]bool)
	phases := make([]session.WorkflowPhase, 0)
	for _, agent := range run.Agents {
		if seen[agent.PhaseIndex] {
			continue
		}
		seen[agent.PhaseIndex] = true
		title := agent.PhaseTitle
		if title == "" {
			title = fmt.Sprintf("Phase %d", agent.PhaseIndex)
		}
		phases = append(phases, session.WorkflowPhase{Index: agent.PhaseIndex, Title: title})
	}
	sort.Slice(phases, func(i, j int) bool { return phases[i].Index < phases[j].Index })
	return phases
}

func shortFlowID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func legacyAgentMessageIndex(merged []mergedMsg, timestamp time.Time) int {
	if timestamp.IsZero() {
		return -1
	}
	best := -1
	for i, message := range merged {
		if message.entry.Role != "assistant" || message.entry.Timestamp.IsZero() {
			continue
		}
		if !timestamp.Before(message.entry.Timestamp) {
			best = i
		}
	}
	if best >= 0 {
		return best
	}
	for i, message := range merged {
		if message.entry.Role == "assistant" {
			return i
		}
	}
	return -1
}

// buildConvContextItems builds the fixed, session-wide rows displayed above the
// paginated chronological conversation body.
func buildConvContextItems(sess session.Session, merged []mergedMsg, flow *session.FlowIndex) []convItem {
	items := make([]convItem, 0, 3)
	if flow != nil {
		facets := flow.Facets(flow.RootID, session.ScopeSession)
		decisions := flow.Decisions(session.ScopeSession)
		label := fmt.Sprintf("Session Flow · %d turns · %d agents · %d wf · ▣%d decisions", len(merged), len(flow.Agents()), len(flow.Workflows()), len(decisions))
		items = append(items, convItem{kind: convSessionMeta, sessionMeta: "summary", label: label, facets: facets, aggregate: true})
	}
	if sess.HasMemory || len(sess.Todos) > 0 {
		items = append(items, convItem{kind: convSessionMeta, sessionMeta: "memory", label: "Session Memory & Todos"})
	}
	if sess.HasPlan || len(sess.Tasks) > 0 || len(sess.Crons) > 0 || sess.HasTasks || sess.HasCrons || sess.HasAgents {
		items = append(items, convItem{kind: convSessionMeta, sessionMeta: "tasksplan", label: "Session Tasks/Plans"})
	}
	if sess.HasRefs {
		items = append(items, convItem{kind: convSessionMeta, sessionMeta: "refs", label: "Session Refs & URLs"})
	}
	return items
}

// buildConvItems builds only the chronological conversation spine, with inline
// task and agent sub-items under assistant messages. Keeping session context out
// of this slice preserves parentIdx in the body-only index space.
func buildConvItems(sess session.Session, merged []mergedMsg, agents []session.Subagent, tasks []session.TaskItem, crons []session.CronItem, flows ...*session.FlowIndex) []convItem {
	var flow *session.FlowIndex
	if len(flows) > 0 {
		flow = flows[0]
	}
	if flow != nil {
		agents = flow.Agents()
	}
	// First pass: find all task-touching message indices and the last one.
	// Always scan for task operations (TaskCreate, TaskOutput, etc.) regardless
	// of whether a resolved task list exists — operations should be visible as
	// sub-items even without a task board.
	var taskMsgIndices []int
	var cronMsgIndices []int
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
		for _, block := range m.entry.Content {
			if block.Type == "tool_use" && isCronTool(block.ToolName) {
				cronMsgIndices = append(cronMsgIndices, i)
				break
			}
		}
	}
	lastTaskMsgIdx := -1
	if len(taskMsgIndices) > 0 {
		lastTaskMsgIdx = taskMsgIndices[len(taskMsgIndices)-1]
	}
	lastCronMsgIdx := -1
	if len(cronMsgIndices) > 0 {
		lastCronMsgIdx = cronMsgIndices[len(cronMsgIndices)-1]
	}
	taskMsgSet := make(map[int]bool, len(taskMsgIndices))
	for _, idx := range taskMsgIndices {
		taskMsgSet[idx] = true
	}
	cronMsgSet := make(map[int]bool, len(cronMsgIndices))
	for _, idx := range cronMsgIndices {
		cronMsgSet[idx] = true
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
	cronsByID := make(map[string]session.CronItem, len(crons))
	for _, c := range crons {
		cronsByID[c.ID] = c
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

	// Attach agents to the exact assistant turn that contains their spawning
	// Agent/Task tool_use. Timestamp placement is legacy fallback only. Workflow
	// agents are emitted below their workflow phase and are excluded here.
	agentsByMsg := make(map[int][]session.Subagent)
	workflowAgentIDs := make(map[string]bool)
	if flow != nil {
		for _, run := range flow.Workflows() {
			for _, agent := range run.Agents {
				workflowAgentIDs[agent.AgentID] = true
			}
		}
	}
	for _, agent := range agents {
		if isSystemAgent(agent) || workflowAgentIDs[agent.ID] || agent.WorkflowRunID != "" {
			continue
		}
		// OriginEntryIndex is local to OriginTranscript. Never project an
		// agent-owned spawn onto the root transcript (or another agent) merely
		// because the numeric entry index happens to match.
		if agent.OriginTranscript != "" && agent.OriginTranscript != sess.FilePath {
			continue
		}
		messageIdx := mergedIndexForOrigin(merged, agent.OriginMessageUUID, agent.OriginEntryIndex)
		if messageIdx < 0 && agent.SpawnToolUseID == "" && agent.ParentAgentID == "" {
			messageIdx = legacyAgentMessageIndex(merged, agent.Timestamp)
		}
		if messageIdx >= 0 {
			agentsByMsg[messageIdx] = append(agentsByMsg[messageIdx], agent)
		}
	}

	workflowByMsg := make(map[int][]session.WorkflowRun)
	shellsByMsg := make(map[int][]session.ShellJob)
	decisionsByMsg := make(map[int][]session.Artifact)
	steeringByMsg := make(map[int]bool)
	if flow != nil {
		for _, run := range flow.Workflows() {
			if node, ok := flow.Node(session.FlowWorkflowNodeID(run.RunID)); ok {
				if node.Origin.Transcript != "" && node.Origin.Transcript != sess.FilePath {
					continue
				}
				if messageIdx := mergedIndexForOrigin(merged, node.Origin.MessageUUID, node.Origin.EntryIndex); messageIdx >= 0 {
					workflowByMsg[messageIdx] = append(workflowByMsg[messageIdx], run)
				}
			}
		}
		for _, shell := range flow.ShellJobs() {
			if node, ok := flow.Node(session.FlowShellNodeID(shell.ID)); ok {
				if messageIdx := mergedIndexForOrigin(merged, node.Origin.MessageUUID, node.Origin.EntryIndex); messageIdx >= 0 {
					shellsByMsg[messageIdx] = append(shellsByMsg[messageIdx], shell)
				}
			}
		}
		for _, decision := range flow.Decisions(session.ScopeSession) {
			if decision.Origin.Transcript != sess.FilePath {
				continue
			}
			messageIdx := mergedIndexForOrigin(merged, decision.Origin.MessageUUID, decision.Origin.EntryIndex)
			if messageIdx < 0 {
				continue
			}
			if data, ok := decision.Data.(session.DecisionData); ok && data.Kind == session.DecisionSteering {
				steeringByMsg[messageIdx] = true
				continue
			}
			decisionsByMsg[messageIdx] = append(decisionsByMsg[messageIdx], decision)
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
			kind:     convMsg,
			merged:   m,
			steering: steeringByMsg[mi],
		})

		// Only add sub-items under assistant messages
		if m.entry.Role != "assistant" {
			continue
		}

		// Add agent sub-items assigned to this exact spawning turn.
		for _, a := range agentsByMsg[mi] {
			status := agentStatuses[a.ID]
			if status == "" {
				status = agentStatuses[a.ShortID]
			}
			facets := session.FacetSummary{}
			if flow != nil {
				facets = flow.Facets(session.FlowAgentNodeID(a.ID), session.ScopeNode)
			}
			items = append(items, convItem{
				kind:        convAgent,
				agent:       a,
				agentStatus: status,
				facets:      facets,
				indent:      1,
				parentIdx:   parentIdx,
			})
		}

		// Workflow lifecycle: run → phases → agents. Workflow agents are emitted
		// only here, never as ordinary agent rows.
		for _, run := range workflowByMsg[mi] {
			wfFacets := flow.Facets(session.FlowWorkflowNodeID(run.RunID), session.ScopeSubtree)
			folded := isTerminalWorkflowStatus(run.Status)
			items = append(items, convItem{
				kind:      convWorkflow,
				workflow:  run,
				facets:    wfFacets,
				aggregate: true,
				indent:    1,
				parentIdx: parentIdx,
				groupTag:  "workflow:" + run.RunID,
				count:     max(run.AgentCount, len(run.Agents)),
				folded:    folded,
			})
			transcriptAgents := make(map[string]session.Subagent)
			for _, agent := range agents {
				if agent.WorkflowRunID == run.RunID {
					transcriptAgents[agent.ID] = agent
				}
			}
			for _, phase := range workflowPhases(run) {
				phaseNodeID := session.FlowPhaseNodeID(run.RunID, phase.Index)
				phaseFacets := flow.Facets(phaseNodeID, session.ScopeSubtree)
				items = append(items, convItem{
					kind:      convPhase,
					workflow:  run,
					phase:     phase,
					label:     phase.Title,
					facets:    phaseFacets,
					aggregate: true,
					count:     len(flow.Children(phaseNodeID)),
					indent:    2,
					parentIdx: parentIdx,
				})
				for _, wfAgent := range run.Agents {
					if wfAgent.PhaseIndex != phase.Index {
						continue
					}
					agent, ok := transcriptAgents[wfAgent.AgentID]
					if !ok {
						agent = session.Subagent{
							ID: wfAgent.AgentID, ShortID: shortFlowID(wfAgent.AgentID),
							WorkflowRunID: run.RunID, WorkflowLabel: wfAgent.Label,
							WorkflowPhaseIndex: phase.Index, WorkflowPhaseTitle: phase.Title,
							FirstPrompt: wfAgent.PromptPreview,
						}
					}
					label := wfAgent.Label
					if label == "" {
						label = agentLabel(agent, 42)
					}
					facets := flow.Facets(session.FlowAgentNodeID(wfAgent.AgentID), session.ScopeNode)
					items = append(items, convItem{
						kind: convAgent, agent: agent, agentStatus: workflowAgentStatus(wfAgent.State),
						label: label, facets: facets, summaryOnly: !ok,
						indent: 3, parentIdx: parentIdx,
					})
				}
			}
		}

		for _, shell := range shellsByMsg[mi] {
			items = append(items, convItem{
				kind: convShell, shell: shell, indent: 1, parentIdx: parentIdx,
			})
		}

		for _, decision := range decisionsByMsg[mi] {
			items = append(items, convItem{
				kind: convDecision, decision: decision, indent: 1, parentIdx: parentIdx,
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
						icon, verb, subject = iconTask, "Create", input.Subject
					}
				case "TaskOutput":
					var input struct {
						TaskID string `json:"task_id"`
					}
					json.Unmarshal([]byte(b.ToolInput), &input)
					taskID, icon, verb = input.TaskID, iconWaiting, "Waiting"
				case "TaskStop":
					var input struct {
						TaskID string `json:"task_id"`
					}
					json.Unmarshal([]byte(b.ToolInput), &input)
					taskID, icon, verb = input.TaskID, iconStopped, "Stop"
				}
				if taskID == "" && subject == "" {
					continue
				}
				if subject != "" {
					// TaskCreate: show subject directly. Resolve the task ID
					// from the session's task list by subject so the preview
					// and Enter drilldown can target the right entries —
					// without an ID, downstream filtering (extractTaskEntries
					// / openTaskConversation) can't distinguish this task
					// from any other ID-less TaskCreate match.
					label := icon + " " + subject
					if len(label) > 50 {
						label = label[:47] + "..."
					}
					resolvedID := ""
					for _, t := range tasks {
						if t.Subject == subject {
							resolvedID = t.ID
							break
						}
					}
					items = append(items, convItem{
						kind:      convTask,
						task:      session.TaskItem{Subject: label, ID: resolvedID},
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

		if cronMsgSet[mi] {
			expandable := mi == lastCronMsgIdx
			for _, b := range m.entry.Content {
				if b.Type != "tool_use" || !isCronTool(b.ToolName) {
					continue
				}
				cron := session.CronItem{}
				label := "Cron"
				switch b.ToolName {
				case "CronCreate":
					var input struct {
						Cron      string `json:"cron"`
						Prompt    string `json:"prompt"`
						Recurring bool   `json:"recurring"`
					}
					if json.Unmarshal([]byte(b.ToolInput), &input) != nil {
						continue
					}
					cron = session.CronItem{Cron: input.Cron, Prompt: input.Prompt, Recurring: input.Recurring, Status: "active"}
					label = iconActive + " Create " + strings.TrimSpace(input.Cron)
					if label == iconActive+" Create" {
						label = iconActive + " Create cron"
					}
				case "CronDelete":
					var input struct {
						ID string `json:"id"`
					}
					if json.Unmarshal([]byte(b.ToolInput), &input) != nil || input.ID == "" {
						continue
					}
					cron = cronsByID[input.ID]
					if cron.ID == "" {
						cron.ID = input.ID
						cron.Status = "deleted"
					}
					label = iconStopped + " Delete #" + input.ID
					if cron.Cron != "" {
						label += "  " + cron.Cron
					}
				case "CronGet":
					label = iconTask + " Read cron"
				case "CronList":
					label = iconTask + " List crons"
				case "CronUpdate":
					label = iconActive + " Update cron"
				}
				items = append(items, convItem{
					kind:      convTask,
					cron:      cron,
					indent:    1,
					parentIdx: parentIdx,
					label:     truncate(label, 50),
				})
			}
			if expandable && len(crons) > 0 {
				items = append(items, convItem{
					kind:      convTask,
					groupTag:  "crons",
					count:     len(crons),
					folded:    true,
					indent:    1,
					parentIdx: parentIdx,
					label:     "Crons",
				})
				for _, c := range crons {
					items = append(items, convItem{
						kind:      convTask,
						cron:      c,
						indent:    2,
						parentIdx: parentIdx,
					})
				}
			}
		}
	}

	return items
}

func truncate(s string, maxW int) string {
	if lipgloss.Width(s) <= maxW || maxW <= 3 {
		return s
	}
	out := ""
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > maxW-3 {
			break
		}
		out += string(r)
		w += rw
	}
	return out + "..."
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
			icon := iconIdle
			switch input.Status {
			case "completed":
				icon = iconDone
			case "in_progress":
				icon = iconActive
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
			label, detail := resolveTaskLabel(iconWaiting, "Waiting", input.TaskID, agentsByID, bgTasks, 25)
			compactParts = append(compactParts, label)
			detailLines = append(detailLines, detail)
		case "TaskGet":
			var input struct {
				TaskID string `json:"taskId"`
			}
			json.Unmarshal([]byte(b.ToolInput), &input)
			label, detail := resolveTaskLabel(iconTask, "Read", input.TaskID, agentsByID, bgTasks, 25)
			compactParts = append(compactParts, label)
			detailLines = append(detailLines, detail)
		case "TaskStop":
			var input struct {
				TaskID string `json:"task_id"`
			}
			json.Unmarshal([]byte(b.ToolInput), &input)
			label, detail := resolveTaskLabel(iconStopped, "Stop", input.TaskID, agentsByID, bgTasks, 25)
			compactParts = append(compactParts, label)
			detailLines = append(detailLines, detail)
		case "TaskList":
			compactParts = append(compactParts, "list")
			detailLines = append(detailLines, iconTask+"  Listed tasks")
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

func isCronTool(name string) bool {
	switch name {
	case "CronCreate", "CronDelete", "CronList", "CronUpdate", "CronGet":
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

func newConvList(items []convItem, width, height int, contextActive ...*bool) list.Model {
	vis := visibleConvItems(items)
	listItems := make([]list.Item, len(vis))
	for i, ci := range vis {
		listItems[i] = ci
	}
	var active *bool
	if len(contextActive) > 0 {
		active = contextActive[0]
	}

	l := list.New(listItems, convDelegate{contextActive: active}, width, height)
	initListBase(&l)
	l.SetFilteringEnabled(true)
	l.Filter = substringFilter
	configureListSearch(&l)
	l.SetSize(width, height)
	return l
}
