package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

func renderPlainMessage(e session.Entry) string {
	var sb strings.Builder
	for _, block := range e.Content {
		switch block.Type {
		case "text":
			text := strings.TrimSpace(session.StripXMLTags(block.Text))
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
			resultText := session.StripXMLTags(block.Text)
			if block.IsError {
				sb.WriteString("Error: " + resultText + "\n\n")
			} else {
				sb.WriteString("Result: " + resultText + "\n\n")
			}
		case "thinking":
			sb.WriteString("(thinking) " + block.Text + "\n\n")
		}
	}
	return sb.String()
}

// renderConversationPreview renders merged messages in the same one-line style
// as the message list delegate. When expanded, the full text is shown below.
func renderConversationPreview(msgs []mergedMsg, width, cursor int, expanded map[int]bool, filterTerm string, _ ...bool) string {
	var sb strings.Builder
	idxW := 2
	for _, m := range msgs {
		var s string
		if m.endIdx > m.startIdx {
			s = fmt.Sprintf("#%d-%d", m.startIdx+1, m.endIdx+1)
		} else {
			s = fmt.Sprintf("#%d", m.startIdx+1)
		}
		if len(s) > idxW {
			idxW = len(s)
		}
	}

	for i, m := range msgs {
		e := m.entry
		selected := i == cursor

		// Classify message (same logic as messageDelegate)
		hasText := false
		hasTools := false
		for _, b := range e.Content {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				hasText = true
			}
			if b.Type == "tool_use" {
				hasTools = true
			}
		}
		isToolOnly := e.Role == "assistant" && hasTools && !hasText
		isCompacted := isAutoCompacted(e)

		// Turn separator
		isPrevDiffRole := false
		if i > 0 {
			isPrevDiffRole = msgs[i-1].entry.Role != e.Role
		}

		// Cursor
		cursorStr := "  "
		if selected {
			cursorStr = convCursorStyle.Render("> ")
		}

		// Role
		var role string
		if isToolOnly {
			role = toolOnlySepStyle.Render("│") + "   "
		} else if e.Role == "user" {
			if isPrevDiffRole && i > 0 {
				cursorStr = convSepStyle.Render("─ ")
				if selected {
					cursorStr = convCursorStyle.Render("> ")
				}
			}
			role = userLabelStyle.Render("USER")
		} else {
			role = assistantLabelStyle.Render("ASST")
		}

		// Time
		ts := "     "
		if !e.Timestamp.IsZero() {
			ts = dimStyle.Render(e.Timestamp.Format("15:04"))
		}

		// Index
		var idxRaw string
		if m.endIdx > m.startIdx {
			idxRaw = fmt.Sprintf("#%d-%d", m.startIdx+1, m.endIdx+1)
		} else {
			idxRaw = fmt.Sprintf("#%d", m.startIdx+1)
		}
		idxStr := dimStyle.Render(fmt.Sprintf("%-*s", idxW, idxRaw))

		// Prefix: cursor(2) + role(4) + sp(1) + time(5) + sp(1) + idx(idxW) + sp(2)
		prefix := cursorStr + role + " " + ts + " " + idxStr + "  "
		prefixW := 2 + 4 + 1 + 5 + 1 + idxW + 2

		preview := session.EntryPreview(e)

		// Tool-only rows (no text, just tool calls): show tool count instead
		toolCount := 0
		for _, b := range e.Content {
			if b.Type == "tool_use" {
				toolCount++
			}
		}
		var suffix string
		suffixW := 0
		if isToolOnly {
			preview = ""
			if toolCount > 0 {
				tc := fmt.Sprintf("(%d tools)", toolCount)
				suffix = " " + dimStyle.Render(tc)
				suffixW = 1 + len(tc)
			}
		}

		// Preview fills remaining space
		availW := width - prefixW - suffixW

		pStyle := dimStyle
		if selected {
			pStyle = selectedStyle
		} else if isCompacted {
			pStyle = acDimStyle
		}

		var styledPreview string
		if filterTerm != "" && availW > 0 && preview != "" {
			styledPreview = highlightSnippet(preview, filterTerm, availW, pStyle)
		} else {
			if availW > 0 && len(preview) > availW {
				// Wrap full text across multiple lines
				wrapped := wrapText(preview, availW)
				wrapLines := strings.Split(wrapped, "\n")
				styledPreview = pStyle.Render(wrapLines[0])
				pad := strings.Repeat(" ", prefixW)
				for _, wl := range wrapLines[1:] {
					styledPreview += "\n" + pad + pStyle.Render(wl)
				}
			} else if availW <= 0 {
				preview = ""
			}
			styledPreview = pStyle.Render(preview)
		}

		sb.WriteString(prefix + styledPreview + suffix + "\n")

		// Expanded: show full text below
		if expanded != nil && expanded[i] {
			textW := max(width-4, 10)
			text := entryFullText(e)
			if text != "" {
				wrapped := wrapText(text, textW)
				for _, line := range strings.Split(wrapped, "\n") {
					sb.WriteString("    " + line + "\n")
				}
			}
		}
	}

	return sb.String()
}

// entryFullText extracts all text content from an entry.
func entryFullText(e session.Entry) string {
	var parts []string
	for _, b := range e.Content {
		if b.Type == "text" {
			text := strings.TrimSpace(session.StripXMLTags(b.Text))
			if text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// entryFilterText returns a searchable string for filtering conversation items.
// Includes text content, tool names, and role for comprehensive matching.
func entryFilterText(e session.Entry) string {
	var parts []string
	parts = append(parts, "role:"+e.Role)
	if e.Role == "user" {
		parts = append(parts, "role:user")
	} else if e.Role == "assistant" {
		parts = append(parts, "role:asst")
	}
	hasImage, hasTask, hasBg, hasAgent, hasThinking, hasCron := false, false, false, false, false, false
	for _, b := range e.Content {
		switch b.Type {
		case "text":
			text := strings.TrimSpace(session.StripXMLTags(b.Text))
			if text != "" {
				parts = append(parts, text)
			}
		case "tool_use":
			parts = append(parts, b.ToolName, "tool:"+b.ToolName)
			if b.ToolName == "Agent" {
				hasAgent = true
			}
			if isTaskTool(b.ToolName) {
				hasTask = true
			}
			if isCronTool(b.ToolName) {
				hasCron = true
			}
		case "tool_result":
			if b.IsError {
				parts = append(parts, "is:error")
			}
			if strings.Contains(b.Text, "Command running in background with ID:") {
				hasBg = true
			}
		case "image":
			hasImage = true
		case "thinking":
			hasThinking = true
		}
	}
	if hasImage {
		parts = append(parts, "has:image")
	}
	if hasTask {
		parts = append(parts, "has:task")
	}
	if hasBg {
		parts = append(parts, "has:bg")
	}
	if hasAgent {
		parts = append(parts, "has:agent")
	}
	if hasThinking {
		parts = append(parts, "has:thinking")
	}
	if hasCron {
		parts = append(parts, "has:cron")
	}
	return strings.Join(parts, " ")
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
		if block.Type == "tool_use" || block.Type == "tool_result" || block.Type == "thinking" || block.Type == "system_tag" {
			fs[i] = true
		}
	}
	return fs
}

// renderedPreview holds the rendered content and block line-start positions.
type renderedPreview struct {
	content     string
	blockStarts []int // line number where each content block begins
	lineCount   int   // total line count (avoids redundant strings.Count)
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
			text := strings.TrimSpace(session.StripXMLTags(stripANSI(block.Text)))
			if text != "" {
				textParts = append(textParts, text)
			}
		case "tool_use":
			tools = append(tools, block.ToolName)
		case "tool_result":
			text := strings.TrimSpace(session.StripXMLTags(stripANSI(block.Text)))
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

// renderAllMessages renders all merged messages into one concatenated string with separators.
func renderAllMessages(merged []mergedMsg, width int) string {
	var sb strings.Builder
	ruler := strings.Repeat("─", max(min(width, 80), 0))
	for i, m := range merged {
		if i > 0 {
			sb.WriteString("\n" + dimStyle.Render(ruler) + "\n\n")
		}
		sb.WriteString(renderFullMessage(m.entry, width))
	}
	return sb.String()
}

func renderFullMessageFolded(e session.Entry, width int, folds foldSet) string {
	rp := renderFullMessageWithCursor(e, width, folds, nil, -1)
	return rp.content
}

// renderOpts bundles optional rendering flags for renderFullMessageImpl.
type renderOpts struct {
	visible   []bool  // per-block visibility (nil = all visible)
	hideHooks bool    // suppress hook badges/details
	selected  foldSet // blocks selected for copy (nil = none)
}

func renderFullMessageWithCursor(e session.Entry, width int, folds foldSet, formats foldSet, blockCursor int, opts ...renderOpts) renderedPreview {
	var o renderOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	return renderFullMessageImpl(e, width, folds, formats, blockCursor, o)
}

func renderFullMessageImpl(e session.Entry, width int, folds foldSet, formats foldSet, blockCursor int, opts renderOpts) renderedPreview {
	w := max(width, 10)

	// nlWriter counts actual newlines written, so blockStarts match the
	// viewport's line indices exactly (lipgloss Render can add extra \n).
	var nw nlWriter

	var label string
	if isAutoCompacted(e) {
		label = compactBadgeStyle.Render("COMPACTION SUMMARY")
	} else if e.Role == "user" {
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

	nw.WriteString(label + ts + "\n")
	ruler := max(min(w, 80), 0)
	nw.WriteString(strings.Repeat("─", ruler) + "\n\n")

	blockStarts := make([]int, len(e.Content))

	for i, block := range e.Content {
		blockStarts[i] = nw.nl
		// Skip blocks hidden by filter
		if opts.visible != nil && i < len(opts.visible) && !opts.visible[i] {
			continue
		}
		folded := folds != nil && folds[i]
		formatted := formats != nil && formats[i]
		isCursor := blockCursor == i
		isMarked := opts.selected != nil && opts.selected[i]

		// Fold/format indicators (only in block-navigation mode)
		isFoldable := block.Type == "tool_use" || block.Type == "tool_result" || block.Type == "thinking" || block.Type == "system_tag"
		var cursorPrefix string
		if blockCursor >= 0 {
			indicator := " "
			if isMarked {
				// Selected block: ✓ replaces the arrow/indicator
				if isCursor {
					indicator = blockCursorStyle.Render("✓")
				} else {
					indicator = lipgloss.NewStyle().Foreground(colorAccent).Render("✓")
				}
			} else if isCursor {
				// Cursor block: bright indicator
				if formatted {
					indicator = blockCursorStyle.Render("✦")
				} else if isFoldable {
					if folded {
						indicator = blockCursorStyle.Render("▸")
					} else {
						indicator = blockCursorStyle.Render("▾")
					}
				} else {
					indicator = blockCursorStyle.Render("›")
				}
			} else {
				// Non-cursor block: dim indicator
				if formatted {
					indicator = dimStyle.Render("✦")
				} else if isFoldable {
					if folded {
						indicator = dimStyle.Render("▸")
					} else {
						indicator = dimStyle.Render("▾")
					}
				}
			}
			cursorPrefix = indicator + " "
		} else {
			cursorPrefix = ""
		}

		// Write block content to a temp buffer; if selected, apply background
		var buf strings.Builder

		switch block.Type {
		case "text":
			text := strings.TrimSpace(session.StripXMLTags(block.Text))
			if text != "" && !isSystemText(text) {
				buf.WriteString(cursorPrefix)
				if formatted {
					text = tryFormatJSON(text)
				}
				text = formatMarkdownTables(text)
				wrapped := wrapText(text, max(w-2, 10))
				buf.WriteString(wrapped + "\n\n")
			}
		case "tool_use":
			buf.WriteString(cursorPrefix)
			// Show skill name prominently for Skill tool_use blocks
			if block.ToolName == "Skill" {
				skillName := extractSkillFromInput(block.ToolInput)
				if skillName != "" {
					buf.WriteString(skillBlockStyle.Render("Skill: " + skillName))
				} else {
					buf.WriteString(toolBlockStyle.Render("Tool: Skill"))
				}
			} else {
				buf.WriteString(toolBlockStyle.Render("Tool: " + block.ToolName))
			}
			// Show hook badges inline (unless hidden)
			if len(block.Hooks) > 0 && !opts.hideHooks {
				buf.WriteString(renderHookBadges(block.Hooks))
			}
			if folded {
				// Use diff-aware folded summaries for Edit/Write
				if summary := toolFoldedSummary(block); summary != "" {
					buf.WriteString("  " + summary + "\n")
				} else {
					summary := session.StripXMLTags(stripANSI(block.ToolInput))
					if len(summary) > 60 {
						summary = summary[:57] + "..."
					}
					buf.WriteString("  " + dimStyle.Render(summary) + "\n")
				}
			} else {
				buf.WriteString("\n")
				if block.ToolInput != "" {
					// Use diff rendering for Edit/Write tools
					if diffOut := toolDiffOutput(block, w); diffOut != "" {
						buf.WriteString(diffOut)
					} else {
						input := session.StripXMLTags(stripANSI(block.ToolInput))
						if formatted {
							input = tryFormatJSON(input)
						}
						wrapped := wrapText(input, w)
						buf.WriteString(dimStyle.Render(wrapped) + "\n")
					}
				}
				// Show hook details when unfolded (unless hidden)
				if len(block.Hooks) > 0 && !opts.hideHooks {
					buf.WriteString(renderHookDetails(block.Hooks))
				}
				buf.WriteString("\n")
			}
		case "tool_result":
			prefix := "Result: "
			style := dimStyle
			if block.IsError {
				prefix = "Error: "
				style = errorStyle
			}
			buf.WriteString(cursorPrefix)
			if folded {
				summary := session.StripXMLTags(stripANSI(block.Text))
				if len(summary) > 60 {
					summary = summary[:57] + "..."
				}
				buf.WriteString(style.Render(prefix+summary) + "\n")
			} else {
				buf.WriteString(style.Render(prefix) + "\n")
				text := session.StripXMLTags(stripANSI(block.Text))
				if formatted {
					text = tryFormatJSON(text)
				}
				wrapped := wrapText(text, w)
				buf.WriteString(style.Render(wrapped) + "\n\n")
			}
		case "thinking":
			buf.WriteString(cursorPrefix)
			if folded {
				summary := block.Text
				if len(summary) > 60 {
					summary = summary[:57] + "..."
				}
				buf.WriteString(dimStyle.Render("(thinking) "+summary) + "\n")
			} else {
				buf.WriteString(dimStyle.Render("(thinking)") + "\n")
				wrapped := wrapText(block.Text, w)
				buf.WriteString(dimStyle.Render(wrapped) + "\n\n")
			}
		case "system_tag":
			buf.WriteString(cursorPrefix)
			label := "<" + block.TagName + ">"
			if folded {
				summary := session.StripXMLTags(stripANSI(block.Text))
				summary = strings.ReplaceAll(summary, "\n", " ")
				summary = strings.Join(strings.Fields(summary), " ")
				if len(summary) > 60 {
					summary = summary[:57] + "..."
				}
				buf.WriteString(dimStyle.Render(label+"  "+summary) + "\n")
			} else {
				buf.WriteString(dimStyle.Render(label) + "\n")
				text := session.StripXMLTags(stripANSI(block.Text))
				wrapped := wrapText(text, w)
				buf.WriteString(dimStyle.Render(wrapped) + "\n\n")
			}
		case "image":
			buf.WriteString(cursorPrefix)
			label := block.Text
			if block.ImagePasteID > 0 {
				label = fmt.Sprintf("🖼 %s  (paste #%d — Enter to open)", block.Text, block.ImagePasteID)
			}
			buf.WriteString(dimStyle.Render(label) + "\n\n")
		}

		// Apply background highlight to the cursor block (or marked blocks)
		if (isCursor || isMarked) && buf.Len() > 0 {
			raw := buf.String()
			// Remove final newline so Split doesn't create an extra empty element
			hasFinalNL := strings.HasSuffix(raw, "\n")
			if hasFinalNL {
				raw = raw[:len(raw)-1]
			}
			lines := strings.Split(raw, "\n")
			for j, line := range lines {
				nw.WriteString(applyBgToLine(line, w))
				if j < len(lines)-1 || hasFinalNL {
					nw.WriteString("\n")
				}
			}
		} else {
			nw.WriteString(buf.String())
		}
	}

	return renderedPreview{content: nw.sb.String(), blockStarts: blockStarts, lineCount: nw.nl}
}

// nlWriter wraps strings.Builder and counts newlines written,
// so blockStarts always match the viewport's actual line indices.
type nlWriter struct {
	sb strings.Builder
	nl int
}

func (w *nlWriter) WriteString(s string) {
	w.sb.WriteString(s)
	w.nl += strings.Count(s, "\n")
}

// padToWidth pads a line with spaces so background color fills the full width.
// Uses lipgloss.Width for ANSI-aware measurement.
func padToWidth(line string, width int) string {
	visible := lipgloss.Width(line)
	if visible >= width {
		return line
	}
	return line + strings.Repeat(" ", width-visible)
}

// applyBgToLine applies the selected-block background to a line that may
// contain inner ANSI resets. It re-applies the background after every \x1b[0m
// so the highlight covers the full width without gaps.
func applyBgToLine(line string, width int) string {
	const bgCode = "\x1b[48;2;30;41;59m" // #1E293B
	const resetCode = "\x1b[0m"
	padded := padToWidth(line, width)
	inner := strings.ReplaceAll(padded, resetCode, resetCode+bgCode)
	return bgCode + inner + resetCode
}

// formatMarkdownTables detects markdown tables in text and re-renders them
// with aligned columns. Non-table lines pass through unchanged.
func formatMarkdownTables(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	i := 0
	for i < len(lines) {
		// Detect table start: line with | and next line is separator (|---|...)
		if isTableRow(lines[i]) && i+1 < len(lines) && isTableSeparator(lines[i+1]) {
			// Collect all contiguous table lines
			start := i
			for i < len(lines) && (isTableRow(lines[i]) || isTableSeparator(lines[i])) {
				i++
			}
			result = append(result, alignTable(lines[start:i])...)
			continue
		}
		result = append(result, lines[i])
		i++
	}
	return strings.Join(result, "\n")
}

func isTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.Contains(trimmed, "|") && len(trimmed) > 1
}

func isTableSeparator(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.Contains(trimmed, "|") || !strings.Contains(trimmed, "-") {
		return false
	}
	for _, ch := range trimmed {
		if ch != '|' && ch != '-' && ch != ':' && ch != ' ' {
			return false
		}
	}
	return true
}

func parseTableCells(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.Trim(trimmed, "|")
	parts := strings.Split(trimmed, "|")
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}

func alignTable(lines []string) []string {
	// Parse all rows into cells
	type row struct {
		cells []string
		isSep bool
	}
	var rows []row
	maxCols := 0
	for _, line := range lines {
		if isTableSeparator(line) {
			rows = append(rows, row{isSep: true})
			continue
		}
		cells := parseTableCells(line)
		if len(cells) > maxCols {
			maxCols = len(cells)
		}
		rows = append(rows, row{cells: cells})
	}

	if maxCols == 0 {
		return lines
	}

	// Calculate max width per column
	colWidths := make([]int, maxCols)
	for _, r := range rows {
		if r.isSep {
			continue
		}
		for j, cell := range r.cells {
			if j < maxCols && len(cell) > colWidths[j] {
				colWidths[j] = len(cell)
			}
		}
	}

	// Render aligned rows
	var result []string
	for _, r := range rows {
		var sb strings.Builder
		sb.WriteString("|")
		if r.isSep {
			for j := range maxCols {
				sb.WriteString(" ")
				sb.WriteString(strings.Repeat("-", colWidths[j]))
				sb.WriteString(" |")
			}
		} else {
			for j := range maxCols {
				cell := ""
				if j < len(r.cells) {
					cell = r.cells[j]
				}
				sb.WriteString(" ")
				sb.WriteString(cell)
				sb.WriteString(strings.Repeat(" ", colWidths[j]-len(cell)))
				sb.WriteString(" |")
			}
		}
		result = append(result, sb.String())
	}
	return result
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

// extractSkillFromInput parses the "skill" field from a Skill tool_use input JSON.
func extractSkillFromInput(input string) string {
	var parsed struct {
		Skill string `json:"skill"`
	}
	if json.Unmarshal([]byte(input), &parsed) == nil && parsed.Skill != "" {
		return parsed.Skill
	}
	return ""
}

// renderHookBadges returns inline hook summary for tool_use blocks.
// Shows short script names e.g. "⚡ go_vet.py, ts_lint.py"
func renderHookBadges(hooks []session.HookInfo) string {
	if len(hooks) == 0 {
		return ""
	}
	var names []string
	for _, h := range hooks {
		names = append(names, hookScriptName(h.Command))
	}
	return "  " + hookBadgeStyle.Render("⚡"+strings.Join(names, ", "))
}

// renderHookDetails returns expanded hook info lines for unfolded tool_use blocks.
func renderHookDetails(hooks []session.HookInfo) string {
	var sb strings.Builder
	for _, h := range hooks {
		event := h.Event
		script := hookScriptName(h.Command)
		sb.WriteString(hookDetailStyle.Render(fmt.Sprintf("  ⚡ %s  %s", event, script)) + "\n")
		// Show full command if different from script name
		if h.Command != script {
			sb.WriteString(hookDetailStyle.Render("    "+h.Command) + "\n")
		}
	}
	return sb.String()
}

// hookScriptName extracts a short script name from a hook command string.
// e.g. "uv run ~/.claude/hooks/go_vet.py" → "go_vet.py"
func hookScriptName(command string) string {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return command
	}
	// Take the last part that looks like a file path
	for i := len(parts) - 1; i >= 0; i-- {
		p := parts[i]
		if strings.Contains(p, "/") || strings.Contains(p, ".") {
			// Extract basename
			if idx := strings.LastIndex(p, "/"); idx >= 0 {
				return p[idx+1:]
			}
			return p
		}
	}
	return parts[len(parts)-1]
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
