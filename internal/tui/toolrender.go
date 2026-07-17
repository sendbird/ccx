package tui

// Tool renderer registry (design: docs/design-session-flow.md, Phase 2).
//
// Replaces the generic "Tool: <name>" + raw-JSON dump for known tools with a
// per-tool semantic rendering. Each registered tool provides:
//
//   - headline: one line carrying the essence of the call, shown even when
//     the block is folded (e.g. `$ git log --oneline`, `Edit messages.go -2/+3`).
//   - body: a semantic multi-line rendering shown when the block is unfolded
//     (full command, unified diff, prompt text, ...).
//
// Disclosure ladder per tool_use block (wired in messages.go):
//
//	folded            → ⏺ headline
//	unfolded          → ⏺ headline + semantic body
//	unfolded+format   → ⏺ headline + raw JSON input (deepest level)
//
// The raw-JSON level reuses the existing "Formatted" fold toggle (`l` on an
// expanded block) so no new keymap or fold mechanics are introduced.
//
// Unregistered tools keep the previous generic behavior unchanged.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/sendbird/ccx/internal/session"
)

// Status glyphs: ⏺ marks a tool_use headline; ✓/✗ mark tool_result lines.
const (
	iconToolUse   = "⏺"
	iconResultTee = "⎿"
	iconResultOK  = "✓"
	iconResultErr = "✗"
)

var (
	toolGlyphStyle  = lipgloss.NewStyle().Foreground(colorAccent)
	toolNameStyle   = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	resultOKStyle   = dimStyle
	resultErrStyle  = lipgloss.NewStyle().Foreground(colorError)
	webURLStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#7DD3FC"))
	agentTypeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Bold(true)
	wfHeadlineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
)

// toolRenderer renders one tool_use block semantically. input is the raw
// ToolInput JSON string (matching how ContentBlock stores it). result is the
// paired tool_result block when one could be matched within the same rendered
// entry (nil otherwise); renderers may use it to enrich the body, but result
// content itself is rendered on the tool_result block (see messages.go).
type toolRenderer interface {
	headline(input string, width int) string
	body(input string, result *session.ContentBlock, width int) []string
}

// toolRendererFuncs adapts plain functions to the toolRenderer interface.
type toolRendererFuncs struct {
	headlineFn func(input string, width int) string
	bodyFn     func(input string, result *session.ContentBlock, width int) []string
}

func (f toolRendererFuncs) headline(input string, width int) string {
	if f.headlineFn == nil {
		return ""
	}
	return f.headlineFn(input, width)
}

func (f toolRendererFuncs) body(input string, result *session.ContentBlock, width int) []string {
	if f.bodyFn == nil {
		return nil
	}
	return f.bodyFn(input, result, width)
}

// toolRenderers maps tool names to their semantic renderers. MCP tools
// (mcp__server__tool) are resolved dynamically in lookupToolRenderer.
var toolRenderers = map[string]toolRenderer{
	"Bash":       toolRendererFuncs{bashHeadline, bashBody},
	"Read":       toolRendererFuncs{readHeadline, readBody},
	"Edit":       toolRendererFuncs{editHeadline, editBody},
	"MultiEdit":  toolRendererFuncs{editHeadline, editBody},
	"Write":      toolRendererFuncs{writeHeadline, writeBody},
	"Grep":       toolRendererFuncs{grepHeadline, genericInputBody},
	"Glob":       toolRendererFuncs{globHeadline, genericInputBody},
	"WebFetch":   toolRendererFuncs{webFetchHeadline, webFetchBody},
	"WebSearch":  toolRendererFuncs{webSearchHeadline, genericInputBody},
	"Agent":      toolRendererFuncs{agentHeadline, agentBody},
	"Task":       toolRendererFuncs{agentHeadline, agentBody}, // legacy Task == Agent
	"Workflow":   toolRendererFuncs{workflowHeadline, workflowBody},
	"TaskCreate": toolRendererFuncs{taskCreateHeadline, genericInputBody},
	"TaskUpdate": toolRendererFuncs{taskUpdateHeadline, genericInputBody},
	// Migrated special cases (previously inline in messages.go):
	"Skill":   toolRendererFuncs{skillHeadline, genericInputBody},
	"Monitor": toolRendererFuncs{monitorHeadline, monitorBody},
}

// lookupToolRenderer returns the renderer for a tool name, resolving MCP
// mcp__server__tool names dynamically. Returns nil for unregistered tools
// (callers fall back to the generic rendering).
func lookupToolRenderer(name string) toolRenderer {
	if r, ok := toolRenderers[name]; ok {
		return r
	}
	if server, tool, ok := session.MCPToolLabel(name); ok {
		return mcpRenderer{server: server, tool: tool}
	}
	return nil
}

// --- shared helpers ---

// truncateMiddle shortens s to maxW display columns by replacing the middle
// with "…". The tail is weighted (2/3 of the budget) so path basenames and
// command arguments survive truncation.
func truncateMiddle(s string, maxW int) string {
	if maxW <= 0 || runewidth.StringWidth(s) <= maxW {
		return s
	}
	if maxW <= 2 {
		return "…"
	}
	keep := maxW - 1 // budget minus the ellipsis
	front := keep / 3
	back := keep - front

	runes := []rune(s)
	// Collect the front segment.
	var head strings.Builder
	w := 0
	i := 0
	for ; i < len(runes); i++ {
		rw := runewidth.RuneWidth(runes[i])
		if w+rw > front {
			break
		}
		head.WriteRune(runes[i])
		w += rw
	}
	// Collect the back segment (walk from the end).
	var tailRunes []rune
	w = 0
	for j := len(runes) - 1; j > i; j-- {
		rw := runewidth.RuneWidth(runes[j])
		if w+rw > back {
			break
		}
		tailRunes = append(tailRunes, runes[j])
		w += rw
	}
	// Reverse tail.
	for l, r := 0, len(tailRunes)-1; l < r; l, r = l+1, r-1 {
		tailRunes[l], tailRunes[r] = tailRunes[r], tailRunes[l]
	}
	return head.String() + "…" + string(tailRunes)
}

// flattenLine collapses newlines/whitespace runs into single spaces.
// (Distinct from app.go's oneLine, which cuts at the first newline.)
func flattenLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// baseName returns the last path segment.
func baseName(p string) string {
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

// genericInputBody renders the tool input as dim wrapped text — the semantic
// body for tools whose input is already human-readable enough.
func genericInputBody(input string, _ *session.ContentBlock, width int) []string {
	in := strings.TrimSpace(session.StripXMLTags(stripANSI(input)))
	if in == "" {
		return nil
	}
	return strings.Split(dimStyle.Render(wrapText(in, width)), "\n")
}

// cappedLines truncates a line slice to maxLines, appending a dim "+N more" marker.
func cappedLines(lines []string, maxLines int) []string {
	if len(lines) <= maxLines {
		return lines
	}
	out := append([]string{}, lines[:maxLines]...)
	return append(out, dimStyle.Render(fmt.Sprintf("  … +%d more lines", len(lines)-maxLines)))
}

// --- Bash ---

func bashHeadline(input string, width int) string {
	var b bashInput
	if json.Unmarshal([]byte(input), &b) != nil || b.Command == "" {
		return ""
	}
	cmd := flattenLine(strings.ReplaceAll(b.Command, "\n", "; "))
	suffix := ""
	if b.RunInBackground {
		suffix = " &"
	}
	budget := width - 2 - runewidth.StringWidth(suffix) // "$ " prefix
	cmd = truncateMiddle(cmd, max(budget, 20))
	return bashCmdStyle.Render("$ "+cmd) + bashBgBadge.Render(suffix)
}

func bashBody(input string, _ *session.ContentBlock, width int) []string {
	out := formatBashExpanded(input, width)
	if out == "" {
		return nil
	}
	return splitLines(strings.TrimRight(out, "\n"))
}

// --- Read ---

func readHeadline(input string, width int) string {
	var r readInput
	if json.Unmarshal([]byte(input), &r) != nil || r.FilePath == "" {
		return ""
	}
	path := session.ShortenPath(r.FilePath, homeDir())
	rng := ""
	switch {
	case r.Offset > 0 && r.Limit > 0:
		rng = fmt.Sprintf(":%d-%d", r.Offset, r.Offset+r.Limit)
	case r.Offset > 0:
		rng = fmt.Sprintf(":%d-", r.Offset)
	case r.Limit > 0:
		rng = fmt.Sprintf(":1-%d", r.Limit)
	}
	budget := width - 5 - runewidth.StringWidth(rng) // "Read "
	path = truncateMiddle(path, max(budget, 20))
	return toolNameStyle.Render("Read ") + path + dimStyle.Render(rng)
}

func readBody(input string, _ *session.ContentBlock, width int) []string {
	var r readInput
	if json.Unmarshal([]byte(input), &r) != nil || r.FilePath == "" {
		return nil
	}
	lines := []string{"  " + dimStyle.Render(wrapText(r.FilePath, max(width-2, 10)))}
	if r.Offset > 0 || r.Limit > 0 {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  offset=%d limit=%d", r.Offset, r.Limit)))
	}
	return lines
}

// --- Edit / MultiEdit ---

func editHeadline(input string, width int) string {
	var e editInput
	if json.Unmarshal([]byte(input), &e) != nil || e.FilePath == "" {
		return ""
	}
	stat := diffDelStyle.Render(fmt.Sprintf("-%d", len(splitLines(e.OldString)))) +
		dimStyle.Render("/") +
		diffAddStyle.Render(fmt.Sprintf("+%d", len(splitLines(e.NewString))))
	if e.ReplaceAll {
		stat += dimStyle.Render(" (all)")
	}
	name := truncateMiddle(baseName(e.FilePath), max(width-16, 12))
	return toolNameStyle.Render("Edit ") + name + "  " + stat
}

func editBody(input string, _ *session.ContentBlock, width int) []string {
	out := formatEditDiff(input, width)
	if out == "" {
		return nil
	}
	return splitLines(strings.TrimRight(out, "\n"))
}

// --- Write ---

func writeHeadline(input string, width int) string {
	var w writeInput
	if json.Unmarshal([]byte(input), &w) != nil || w.FilePath == "" {
		return ""
	}
	count := dimStyle.Render(fmt.Sprintf(" (%d lines)", len(splitLines(w.Content))))
	name := truncateMiddle(baseName(w.FilePath), max(width-20, 12))
	return toolNameStyle.Render("Write ") + name + count
}

func writeBody(input string, _ *session.ContentBlock, width int) []string {
	// formatWriteDiff already renders a content preview capped at ~50 lines.
	out := formatWriteDiff(input, width)
	if out == "" {
		return nil
	}
	return splitLines(strings.TrimRight(out, "\n"))
}

// --- Grep / Glob ---

func grepHeadline(input string, width int) string {
	s := formatGrepFolded(input)
	if s == "" {
		return ""
	}
	return toolNameStyle.Render("Grep ") + s
}

func globHeadline(input string, width int) string {
	s := formatGlobFolded(input)
	if s == "" {
		return ""
	}
	return toolNameStyle.Render("Glob ") + s
}

// --- WebFetch / WebSearch ---

type webFetchInput struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

func webFetchHeadline(input string, width int) string {
	var wf webFetchInput
	if json.Unmarshal([]byte(input), &wf) != nil || wf.URL == "" {
		return ""
	}
	url := truncateMiddle(wf.URL, max(width-9, 20))
	return toolNameStyle.Render("WebFetch ") + webURLStyle.Render(url)
}

func webFetchBody(input string, _ *session.ContentBlock, width int) []string {
	var wf webFetchInput
	if json.Unmarshal([]byte(input), &wf) != nil || wf.URL == "" {
		return nil
	}
	lines := []string{"  " + webURLStyle.Render(wrapText(wf.URL, max(width-2, 10)))}
	if wf.Prompt != "" {
		lines = append(lines, strings.Split(dimStyle.Render(wrapText("  "+wf.Prompt, max(width-2, 10))), "\n")...)
	}
	return lines
}

func webSearchHeadline(input string, width int) string {
	var ws struct {
		Query string `json:"query"`
	}
	if json.Unmarshal([]byte(input), &ws) != nil || ws.Query == "" {
		return ""
	}
	q := truncateMiddle(flattenLine(ws.Query), max(width-13, 20))
	return toolNameStyle.Render("WebSearch ") + "\"" + q + "\""
}

// --- Agent / legacy Task ---

func agentHeadline(input string, width int) string {
	var a agentInput
	if json.Unmarshal([]byte(input), &a) != nil {
		return ""
	}
	label := a.Description
	if label == "" {
		label = flattenLine(a.Prompt)
	}
	if label == "" && a.SubagentType == "" {
		return ""
	}
	head := toolNameStyle.Render("Agent")
	if a.SubagentType != "" {
		head += agentTypeStyle.Render("[" + a.SubagentType + "]")
	}
	if label != "" {
		budget := width - lipgloss.Width(head) - 3
		head += " \"" + truncateMiddle(label, max(budget, 20)) + "\""
	}
	return head
}

func agentBody(input string, _ *session.ContentBlock, width int) []string {
	var a agentInput
	if json.Unmarshal([]byte(input), &a) != nil {
		return nil
	}
	var lines []string
	if a.SubagentType != "" {
		lines = append(lines, "  "+dimStyle.Render("type: ")+agentTypeStyle.Render(a.SubagentType))
	}
	if a.Description != "" {
		lines = append(lines, "  "+dimStyle.Render("desc: ")+a.Description)
	}
	if a.Prompt != "" {
		wrapped := strings.Split(dimStyle.Render(wrapText(a.Prompt, max(width-2, 10))), "\n")
		lines = append(lines, cappedLines(wrapped, 40)...)
	}
	return lines
}

// --- Workflow ---

type workflowInput struct {
	Script      string `json:"script"`
	Description string `json:"description"`
}

// wfMetaNameRe extracts the `name` field from `export const meta = {...}` in a
// workflow script. Best-effort: matches name: 'x' / name: "x".
var wfMetaNameRe = regexp.MustCompile(`name\s*:\s*['"]([^'"]+)['"]`)

func workflowHeadline(input string, width int) string {
	var wf workflowInput
	if json.Unmarshal([]byte(input), &wf) != nil {
		return ""
	}
	name := ""
	if m := wfMetaNameRe.FindStringSubmatch(wf.Script); m != nil {
		name = m[1]
	}
	if name == "" {
		name = wf.Description
	}
	if name == "" {
		return wfHeadlineStyle.Render("Workflow")
	}
	return wfHeadlineStyle.Render("Workflow: ") + truncateMiddle(flattenLine(name), max(width-11, 20))
}

func workflowBody(input string, _ *session.ContentBlock, width int) []string {
	var wf workflowInput
	if json.Unmarshal([]byte(input), &wf) != nil {
		return nil
	}
	var lines []string
	if wf.Description != "" {
		lines = append(lines, "  "+dimStyle.Render(wf.Description))
	}
	if wf.Script != "" {
		wrapped := strings.Split(dimStyle.Render(wrapText(wf.Script, max(width-2, 10))), "\n")
		lines = append(lines, cappedLines(wrapped, 30)...)
	}
	return lines
}

// --- TaskCreate / TaskUpdate ---

func taskCreateHeadline(input string, width int) string {
	var t struct {
		Subject string `json:"subject"`
	}
	if json.Unmarshal([]byte(input), &t) != nil || t.Subject == "" {
		return ""
	}
	subj := truncateMiddle(flattenLine(t.Subject), max(width-14, 20))
	return taskBadgeStyle.Render("TaskCreate ") + "\"" + subj + "\""
}

func taskUpdateHeadline(input string, width int) string {
	var t struct {
		TaskID  string `json:"task_id"`
		TaskID2 string `json:"taskId"`
		Status  string `json:"status"`
		Subject string `json:"subject"`
	}
	if json.Unmarshal([]byte(input), &t) != nil {
		return ""
	}
	id := t.TaskID
	if id == "" {
		id = t.TaskID2
	}
	if id == "" && t.Status == "" && t.Subject == "" {
		return ""
	}
	head := taskBadgeStyle.Render("TaskUpdate")
	if id != "" {
		head += " " + dimStyle.Render("#"+id)
	}
	if t.Status != "" {
		head += dimStyle.Render(" → ") + t.Status
	}
	if t.Subject != "" {
		head += "  \"" + truncateMiddle(flattenLine(t.Subject), max(width-24, 16)) + "\""
	}
	return head
}

// --- Skill (migrated from messages.go special case) ---

func skillHeadline(input string, width int) string {
	skillName := extractSkillFromInput(input)
	if skillName == "" {
		return ""
	}
	return skillBlockStyle.Render("Skill: " + skillName)
}

// --- Monitor (migrated from messages.go special case) ---

func monitorHeadline(input string, width int) string {
	// Preserve the previous inline rendering: "Monitor [persistent]: desc".
	desc, persistent, ok := session.MonitorInputSummary(input)
	if !ok {
		return ""
	}
	s := monitorBlockStyle.Render("Monitor")
	if persistent {
		s += dimStyle.Render(" [persistent]")
	}
	if desc != "" {
		s += monitorBlockStyle.Render(": " + truncateMiddle(flattenLine(desc), max(width-24, 20)))
	}
	return s
}

func monitorBody(input string, _ *session.ContentBlock, width int) []string {
	out := formatMonitorExpanded(input, width)
	if out == "" {
		return nil
	}
	return splitLines(strings.TrimRight(out, "\n"))
}

// --- MCP (migrated from messages.go special case) ---

// mcpRenderer renders mcp__server__tool calls, preserving the previous
// "MCP: server / tool" label as the headline.
type mcpRenderer struct {
	server, tool string
}

func (m mcpRenderer) headline(input string, width int) string {
	return toolBlockStyle.Render("MCP: "+m.server) + dimStyle.Render(" / "+m.tool)
}

func (m mcpRenderer) body(input string, result *session.ContentBlock, width int) []string {
	return genericInputBody(input, result, width)
}

// --- tool_result summary helpers (used by messages.go) ---

// resultLines returns the cleaned, non-empty-trimmed lines of a tool_result.
func resultLines(text string) []string {
	clean := strings.TrimRight(session.StripXMLTags(stripANSI(text)), "\n")
	if clean == "" {
		return nil
	}
	return strings.Split(clean, "\n")
}

// renderResultSummary renders the folded (attached) form of a tool_result:
//
//	success: ⎿ ✓ <first line> (+N lines)      — one dim summary line
//	error:   ⎿ ✗ <first line> + last ≤4 lines — errors auto-expand their tail
func renderResultSummary(block session.ContentBlock, width int) string {
	lines := resultLines(block.Text)
	style := resultOKStyle
	glyph := iconResultOK
	if block.IsError {
		style = resultErrStyle
		glyph = iconResultErr
	}
	prefix := iconResultTee + " " + glyph + " "
	if len(lines) == 0 {
		return style.Render(prefix+"(no output)") + "\n"
	}

	var sb strings.Builder
	first := truncateMiddle(flattenLine(lines[0]), max(width-8, 20))
	if !block.IsError {
		suffix := ""
		if len(lines) > 1 {
			suffix = fmt.Sprintf(" (+%d lines)", len(lines)-1)
		}
		sb.WriteString(style.Render(prefix+first) + dimStyle.Render(suffix) + "\n")
		return sb.String()
	}

	// Error: auto-expand — first line plus the last few lines of output, so
	// the failure reason is visible without unfolding.
	sb.WriteString(style.Render(prefix+first) + "\n")
	const tailN = 4
	tail := lines
	if len(tail) > 1 {
		tail = tail[1:] // first line already shown
	} else {
		tail = nil
	}
	if len(tail) > tailN {
		sb.WriteString(dimStyle.Render(fmt.Sprintf("    … %d lines …", len(tail)-tailN)) + "\n")
		tail = tail[len(tail)-tailN:]
	}
	for _, line := range tail {
		sb.WriteString(style.Render("    "+truncateMiddle(line, max(width-4, 20))) + "\n")
	}
	return sb.String()
}

// findToolResult locates the tool_result paired with the tool_use at index
// useIdx inside the same entry content: matched by tool_use ID when present,
// otherwise the immediately-following tool_result block.
func findToolResult(content []session.ContentBlock, useIdx int, toolUseID string) *session.ContentBlock {
	for i := useIdx + 1; i < len(content); i++ {
		b := &content[i]
		if b.Type != "tool_result" {
			continue
		}
		if toolUseID != "" {
			if b.ID == toolUseID {
				return b
			}
			continue
		}
		return b // no ID to match: nearest following result
	}
	return nil
}
