package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/gavin-jeong/csb/internal/session"
)

type filterMode int

const (
	filterNone      filterMode = iota
	filterUser                 // show only user messages
	filterAssistant            // show only assistant messages
	filterToolCalls            // show only messages with tool calls
	filterSummary              // first user prompt + last assistant per turn
	filterAgents               // show only messages with Task tool calls
	filterSkills               // show only messages with Skill tool calls
	filterModeCount            // sentinel for cycling
)

type messageItem struct {
	entry    session.Entry
	index    int // first original entry index (0-based)
	endIndex int // last original entry index; > index when merged
}

func (m messageItem) FilterValue() string {
	var parts []string

	// Metadata prefixes for structured search (role=user, tool=Bash)
	role := m.entry.Role
	if role == "assistant" {
		parts = append(parts, "role=assistant", "role=asst")
	} else {
		parts = append(parts, "role="+role)
	}

	// Collect unique tool names
	toolSeen := make(map[string]bool)
	for _, block := range m.entry.Content {
		if block.Type == "tool_use" && !toolSeen[block.ToolName] {
			parts = append(parts, "tool="+block.ToolName)
			toolSeen[block.ToolName] = true
		}
	}

	// Content for plain text search
	for _, block := range m.entry.Content {
		switch block.Type {
		case "text":
			text := block.Text
			if len(text) > 300 {
				text = text[:300]
			}
			parts = append(parts, text)
		case "tool_use":
			parts = append(parts, block.ToolName)
			inp := block.ToolInput
			if len(inp) > 200 {
				inp = inp[:200]
			}
			parts = append(parts, inp)
		case "tool_result":
			text := block.Text
			if len(text) > 200 {
				text = text[:200]
			}
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
}

type messageDelegate struct {
	idxWidth int // fixed column width for the index field
}

func (d messageDelegate) Height() int                             { return 1 }
func (d messageDelegate) Spacing() int                            { return 0 }
func (d messageDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d messageDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	mi, ok := item.(messageItem)
	if !ok {
		return
	}

	e := mi.entry
	selected := index == m.Index()
	width := m.Width()

	cursor := "  "
	if selected {
		cursor = "> "
	}

	var role string
	if e.Role == "user" {
		role = userLabelStyle.Render("USER")
	} else {
		role = assistantLabelStyle.Render("ASST")
	}

	ts := "     " // 5 chars placeholder for "HH:MM"
	if !e.Timestamp.IsZero() {
		ts = e.Timestamp.Format("15:04")
	}
	ts = dimStyle.Render(ts)

	// Fixed-width index column
	var idxRaw string
	if mi.endIndex > mi.index {
		idxRaw = fmt.Sprintf("#%d-%d", mi.index+1, mi.endIndex+1)
	} else {
		idxRaw = fmt.Sprintf("#%d", mi.index+1)
	}
	idxW := d.idxWidth
	if idxW < len(idxRaw) {
		idxW = len(idxRaw)
	}
	idxStr := dimStyle.Render(fmt.Sprintf("%-*s", idxW, idxRaw))

	// Fixed prefix: "> ASST 03:58 #2-159  "
	// Columns: cursor(2) + role(4) + sp(1) + time(5) + sp(1) + idx(idxW) + sp(2)
	prefix := cursor + role + " " + ts + " " + idxStr + "  "
	prefixW := 2 + 4 + 1 + 5 + 1 + idxW + 2 // plain char width

	preview := session.EntryPreview(e)
	tools := mergedToolSummary(e)

	// Build suffix (agent badge + tools)
	var suffix string
	suffixW := 0

	if isAutoCompacted(e) {
		badge := " " + compactBadgeStyle.Render("[AC]")
		suffix = badge
		suffixW = 1 + 4 // " [AC]"
	}

	var hasAgent, hasMCP bool
	for _, block := range e.Content {
		if block.Type == "tool_use" {
			if block.ToolName == "Task" {
				hasAgent = true
			}
			if strings.HasPrefix(block.ToolName, "mcp__") {
				hasMCP = true
			}
		}
	}
	if hasAgent {
		suffix += " " + agentBadgeStyle.Render("[A]")
		suffixW += 1 + 3
	}
	if hasMCP {
		suffix += " " + mcpBadgeStyle.Render("[M]")
		suffixW += 1 + 3
	}

	if tools != "" {
		toolPart := " " + toolStyle.Render(tools)
		suffix += toolPart
		suffixW += 1 + len(tools) // plain width
	}

	// Preview fills remaining space
	availW := width - prefixW - suffixW
	if availW > 3 && len(preview) > availW {
		preview = preview[:availW-3] + "..."
	} else if availW <= 3 {
		preview = ""
	}

	pStyle := dimStyle
	if selected {
		pStyle = selectedStyle
	}

	fmt.Fprint(w, prefix+pStyle.Render(preview)+suffix)
}

// maxIndexWidth calculates the column width needed for the widest index string.
func maxIndexWidth(msgs []mergedMsg) int {
	w := 2 // minimum "#N"
	for _, m := range msgs {
		var s string
		if m.endIdx > m.startIdx {
			s = fmt.Sprintf("#%d-%d", m.startIdx+1, m.endIdx+1)
		} else {
			s = fmt.Sprintf("#%d", m.startIdx+1)
		}
		if len(s) > w {
			w = len(s)
		}
	}
	return w
}

func newMessageList(msgs []mergedMsg, width, height int) list.Model {
	items := make([]list.Item, len(msgs))
	for i, m := range msgs {
		items[i] = messageItem{entry: m.entry, index: m.startIdx, endIndex: m.endIdx}
	}

	l := list.New(items, messageDelegate{idxWidth: maxIndexWidth(msgs)}, width, height)
	l.SetShowTitle(false)
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.Filter = substringFilter
	l.DisableQuitKeybindings()
	configureListSearch(&l)
	return l
}

func filterModeShort(mode filterMode) string {
	switch mode {
	case filterUser:
		return "user"
	case filterAssistant:
		return "asst"
	case filterToolCalls:
		return "tools"
	case filterSummary:
		return "summary"
	case filterAgents:
		return "agents"
	case filterSkills:
		return "skills"
	default:
		return "all"
	}
}

func filterModeTip(mode filterMode) string {
	switch mode {
	case filterUser:
		return "Filter: user messages only"
	case filterAssistant:
		return "Filter: assistant messages only"
	case filterToolCalls:
		return "Filter: messages with tool calls"
	case filterSummary:
		return "Filter: summary (first prompt + last reply per turn)"
	case filterAgents:
		return "Filter: agent (Task) calls only"
	case filterSkills:
		return "Filter: skill invocations only"
	default:
		return "Filter: showing all messages"
	}
}

func filterModeLabel(mode filterMode) string {
	switch mode {
	case filterUser:
		return filterBadge.Render("[USER]")
	case filterAssistant:
		return filterBadge.Render("[ASSISTANT]")
	case filterToolCalls:
		return filterBadge.Render("[TOOLS]")
	case filterSummary:
		return filterBadge.Render("[SUMMARY]")
	case filterAgents:
		return filterBadge.Render("[AGENTS]")
	case filterSkills:
		return filterBadge.Render("[SKILLS]")
	default:
		return ""
	}
}

func renderPlainMessage(e session.Entry) string {
	var sb strings.Builder
	for _, block := range e.Content {
		switch block.Type {
		case "text":
			text := strings.TrimSpace(block.Text)
			if text != "" {
				sb.WriteString(text + "\n\n")
			}
		case "tool_use":
			sb.WriteString("Tool: " + block.ToolName + "\n")
			if block.ToolInput != "" {
				sb.WriteString(block.ToolInput + "\n")
			}
			sb.WriteString("\n")
		case "tool_result":
			if block.IsError {
				sb.WriteString("Error: " + block.Text + "\n\n")
			} else {
				sb.WriteString("Result: " + block.Text + "\n\n")
			}
		case "thinking":
			sb.WriteString("(thinking) " + block.Text + "\n\n")
		}
	}
	return sb.String()
}

// isAutoCompacted returns true if the entry is a context auto-compaction summary.
func isAutoCompacted(e session.Entry) bool {
	for _, block := range e.Content {
		if block.Type == "text" && strings.HasPrefix(block.Text, "This session is being continued from a previous conversation") {
			return true
		}
	}
	return false
}

// foldSet tracks which content block indices are folded (collapsed).
// nil means "use defaults" (tool inputs/results folded).
type foldSet map[int]bool

func defaultFolds(e session.Entry) foldSet {
	fs := make(foldSet)
	for i, block := range e.Content {
		if block.Type == "tool_use" || block.Type == "tool_result" || block.Type == "thinking" {
			fs[i] = true
		}
	}
	return fs
}

// renderedPreview holds the rendered content and block line-start positions.
type renderedPreview struct {
	content     string
	blockStarts []int // line number where each content block begins
}

func renderFullMessage(e session.Entry, width int) string {
	rp := renderFullMessageWithCursor(e, width, nil, nil, -1)
	return rp.content
}

// renderCompactMessage renders a condensed message for session preview.
// Format: "ROLE  HH:MM  content_preview..." (max maxLines lines)
func renderCompactMessage(e session.Entry, width, maxLines int) string {
	var sb strings.Builder
	w := max(width, 10)

	role := userLabelStyle.Render("USER")
	if e.Role == "assistant" {
		role = assistantLabelStyle.Render("ASST")
	}

	ts := "     "
	if !e.Timestamp.IsZero() {
		ts = dimStyle.Render(e.Timestamp.Format("15:04"))
	}

	// "ROLE  HH:MM  "
	sb.WriteString(role + "  " + ts + "  ")
	prefixW := 4 + 2 + 5 + 2 // role + sp + time + sp

	// Collect text content and tool names
	var textParts []string
	var tools []string
	for _, block := range e.Content {
		switch block.Type {
		case "text":
			text := strings.TrimSpace(stripANSI(block.Text))
			if text != "" {
				textParts = append(textParts, text)
			}
		case "tool_use":
			tools = append(tools, block.ToolName)
		case "tool_result":
			text := strings.TrimSpace(stripANSI(block.Text))
			if text != "" {
				// Truncate long tool results
				if len(text) > 200 {
					text = text[:197] + "..."
				}
				textParts = append(textParts, dimStyle.Render(text))
			}
		}
	}

	contentW := w - prefixW
	if contentW < 10 {
		contentW = w
	}

	if len(tools) > 0 {
		toolStr := toolStyle.Render("[" + strings.Join(tools, ", ") + "]")
		sb.WriteString(toolStr)
		if len(textParts) > 0 {
			sb.WriteString("\n")
		}
	}

	if len(textParts) > 0 {
		joined := strings.Join(textParts, " ")
		// Replace newlines with spaces for compact view
		joined = strings.ReplaceAll(joined, "\n", " ")
		// Collapse multiple spaces
		for strings.Contains(joined, "  ") {
			joined = strings.ReplaceAll(joined, "  ", " ")
		}
		lines := truncateLines(joined, contentW, maxLines)
		for i, line := range lines {
			if i > 0 {
				sb.WriteString(strings.Repeat(" ", prefixW))
			}
			sb.WriteString(line)
			if i < len(lines)-1 {
				sb.WriteString("\n")
			}
		}
	} else if len(tools) == 0 {
		sb.WriteString(dimStyle.Render("(no content)"))
	}

	sb.WriteString("\n")
	return sb.String()
}

// truncateLines wraps text to fit within width, returning at most maxLines lines.
func truncateLines(text string, width, maxLines int) []string {
	if width <= 0 {
		return nil
	}
	var lines []string
	remaining := text
	for len(remaining) > 0 && len(lines) < maxLines {
		if len(remaining) <= width {
			lines = append(lines, remaining)
			break
		}
		lines = append(lines, remaining[:width])
		remaining = remaining[width:]
	}
	if len(remaining) > 0 && len(lines) == maxLines {
		last := lines[maxLines-1]
		if len(last) > 3 {
			lines[maxLines-1] = last[:len(last)-3] + "..."
		}
	}
	return lines
}

func renderFullMessageFolded(e session.Entry, width int, folds foldSet) string {
	rp := renderFullMessageWithCursor(e, width, folds, nil, -1)
	return rp.content
}

func renderFullMessageWithCursor(e session.Entry, width int, folds foldSet, formats foldSet, blockCursor int) renderedPreview {
	var sb strings.Builder
	w := max(width, 10)

	var label string
	if e.Role == "user" {
		label = userLabelStyle.Render("USER")
	} else {
		label = assistantLabelStyle.Render("ASSISTANT")
	}

	ts := ""
	if !e.Timestamp.IsZero() {
		ts = "  " + dimStyle.Render(e.Timestamp.Format("2006-01-02 15:04:05"))
	}
	if e.Model != "" {
		ts += "  " + dimStyle.Render("model="+e.Model)
	}

	sb.WriteString(label + ts + "\n")
	ruler := max(min(w, 80), 0)
	sb.WriteString(strings.Repeat("─", ruler) + "\n\n")

	lineCount := 3 // header lines
	blockStarts := make([]int, len(e.Content))

	for i, block := range e.Content {
		blockStarts[i] = lineCount
		folded := folds != nil && folds[i]
		formatted := formats != nil && formats[i]

		// Block cursor marker
		cursorPrefix := "  "
		if blockCursor == i {
			cursorPrefix = blockCursorStyle.Render("▶ ")
		}

		switch block.Type {
		case "text":
			text := strings.TrimSpace(block.Text)
			if text != "" {
				if blockCursor >= 0 {
					sb.WriteString(cursorPrefix)
				}
				if formatted {
					text = tryFormatJSON(text)
				}
				wrapped := wrapText(text, max(w-2, 10))
				sb.WriteString(wrapped + "\n\n")
				lineCount += strings.Count(wrapped, "\n") + 2
			}
		case "tool_use":
			if blockCursor >= 0 {
				sb.WriteString(cursorPrefix)
			}
			sb.WriteString(toolBlockStyle.Render("Tool: " + block.ToolName))
			if folded {
				summary := stripANSI(block.ToolInput)
				if len(summary) > 60 {
					summary = summary[:57] + "..."
				}
				sb.WriteString("  " + dimStyle.Render(summary) + "\n")
				lineCount++
			} else {
				sb.WriteString("\n")
				lineCount++
				if block.ToolInput != "" {
					input := stripANSI(block.ToolInput)
					if formatted {
						input = tryFormatJSON(input)
					}
					wrapped := wrapText(input, w)
					sb.WriteString(dimStyle.Render(wrapped) + "\n")
					lineCount += strings.Count(wrapped, "\n") + 1
				}
				sb.WriteString("\n")
				lineCount++
			}
		case "tool_result":
			prefix := "Result: "
			style := dimStyle
			if block.IsError {
				prefix = "Error: "
				style = errorStyle
			}
			if blockCursor >= 0 {
				sb.WriteString(cursorPrefix)
			}
			if folded {
				summary := stripANSI(block.Text)
				if len(summary) > 60 {
					summary = summary[:57] + "..."
				}
				sb.WriteString(style.Render(prefix+summary) + "\n")
				lineCount++
			} else {
				sb.WriteString(style.Render(prefix) + "\n")
				lineCount++
				text := stripANSI(block.Text)
				if formatted {
					text = tryFormatJSON(text)
				}
				wrapped := wrapText(text, w)
				sb.WriteString(style.Render(wrapped) + "\n\n")
				lineCount += strings.Count(wrapped, "\n") + 2
			}
		case "thinking":
			if blockCursor >= 0 {
				sb.WriteString(cursorPrefix)
			}
			if folded {
				summary := block.Text
				if len(summary) > 60 {
					summary = summary[:57] + "..."
				}
				sb.WriteString(dimStyle.Render("(thinking) "+summary) + "\n")
				lineCount++
			} else {
				sb.WriteString(dimStyle.Render("(thinking)") + "\n")
				lineCount++
				wrapped := wrapText(block.Text, w)
				sb.WriteString(dimStyle.Render(wrapped) + "\n\n")
				lineCount += strings.Count(wrapped, "\n") + 2
			}
		}
	}

	return renderedPreview{content: sb.String(), blockStarts: blockStarts}
}

// wrapText wraps text to the given width, preserving existing line breaks.
func wrapText(text string, width int) string {
	if width <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	var result []string
	for _, line := range lines {
		if len(line) <= width {
			result = append(result, line)
			continue
		}
		// Word wrap long lines
		for len(line) > width {
			// Find last space before width
			cut := width
			if idx := strings.LastIndex(line[:width], " "); idx > 0 {
				cut = idx + 1
			}
			result = append(result, line[:cut])
			line = line[cut:]
		}
		if line != "" {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// tryFormatJSON attempts to pretty-print s as JSON. Returns s unchanged if not valid JSON.
func tryFormatJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if s[0] != '{' && s[0] != '[' {
		return s
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		return s
	}
	return buf.String()
}
