package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

var (
	cachedHome     string
	cachedHomeOnce sync.Once
)

func homeDir() string {
	cachedHomeOnce.Do(func() { cachedHome, _ = os.UserHomeDir() })
	return cachedHome
}

// Diff styles for colorized output
var (
	diffAddStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ADE80")) // green
	diffDelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#F87171")) // red
	diffHunkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#7DD3FC")) // cyan
	diffHeaderStyle = lipgloss.NewStyle().Foreground(colorDim)
	diffCtxStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")) // gray context lines
)

// editInput represents the JSON fields for an Edit tool call.
type editInput struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// writeInput represents the JSON fields for a Write tool call.
type writeInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// formatEditDiff renders an Edit tool_use input as a colorized unified diff.
// Returns empty string if the input cannot be parsed as an Edit.
func formatEditDiff(toolInput string, width int) string {
	var e editInput
	if err := json.Unmarshal([]byte(toolInput), &e); err != nil {
		return ""
	}
	if e.FilePath == "" || (e.OldString == "" && e.NewString == "") {
		return ""
	}

	var buf strings.Builder

	// Header
	shortPath := session.ShortenPath(e.FilePath, homeDir())
	buf.WriteString(diffHeaderStyle.Render("  "+shortPath) + "\n")
	if e.ReplaceAll {
		buf.WriteString(diffHeaderStyle.Render("  (replace all occurrences)") + "\n")
	}

	// Build unified diff lines
	oldLines := splitLines(e.OldString)
	newLines := splitLines(e.NewString)

	// Hunk header
	hunkHeader := fmt.Sprintf("  @@ -%d,%d +%d,%d @@", 1, len(oldLines), 1, len(newLines))
	buf.WriteString(diffHunkStyle.Render(hunkHeader) + "\n")

	// Removals
	for _, line := range oldLines {
		rendered := renderDiffLine("-", line, width-4, diffDelStyle)
		buf.WriteString(rendered + "\n")
	}
	// Additions
	for _, line := range newLines {
		rendered := renderDiffLine("+", line, width-4, diffAddStyle)
		buf.WriteString(rendered + "\n")
	}

	return buf.String()
}

// formatEditFolded returns a compact one-line summary for a folded Edit block.
func formatEditFolded(toolInput string) string {
	var e editInput
	if err := json.Unmarshal([]byte(toolInput), &e); err != nil {
		return ""
	}
	if e.FilePath == "" {
		return ""
	}

	shortPath := session.ShortenPath(e.FilePath, homeDir())
	oldCount := len(splitLines(e.OldString))
	newCount := len(splitLines(e.NewString))

	summary := fmt.Sprintf("%s  ", shortPath)
	summary += diffDelStyle.Render(fmt.Sprintf("-%d", oldCount))
	summary += dimStyle.Render("/")
	summary += diffAddStyle.Render(fmt.Sprintf("+%d", newCount))
	if e.ReplaceAll {
		summary += dimStyle.Render(" (all)")
	}
	return summary
}

// formatWriteDiff renders a Write tool_use input showing the file and content preview.
func formatWriteDiff(toolInput string, width int) string {
	var w writeInput
	if err := json.Unmarshal([]byte(toolInput), &w); err != nil {
		return ""
	}
	if w.FilePath == "" {
		return ""
	}

	var buf strings.Builder

	shortPath := session.ShortenPath(w.FilePath, homeDir())
	buf.WriteString(diffHeaderStyle.Render("  "+shortPath) + "\n")

	lines := splitLines(w.Content)
	hunkHeader := fmt.Sprintf("  @@ +1,%d @@  (new file)", len(lines))
	buf.WriteString(diffHunkStyle.Render(hunkHeader) + "\n")

	// Show all lines as additions (cap at reasonable limit for display)
	maxLines := 50
	for i, line := range lines {
		if i >= maxLines {
			remaining := len(lines) - maxLines
			buf.WriteString(diffHeaderStyle.Render(fmt.Sprintf("  ... +%d more lines", remaining)) + "\n")
			break
		}
		rendered := renderDiffLine("+", line, width-4, diffAddStyle)
		buf.WriteString(rendered + "\n")
	}

	return buf.String()
}

// formatWriteFolded returns a compact one-line summary for a folded Write block.
func formatWriteFolded(toolInput string) string {
	var w writeInput
	if err := json.Unmarshal([]byte(toolInput), &w); err != nil {
		return ""
	}
	if w.FilePath == "" {
		return ""
	}

	shortPath := session.ShortenPath(w.FilePath, homeDir())
	lineCount := len(splitLines(w.Content))
	return fmt.Sprintf("%s  %s",
		shortPath,
		diffAddStyle.Render(fmt.Sprintf("+%d lines", lineCount)),
	)
}

// renderDiffLine renders a single diff line with prefix and style, truncating if needed.
func renderDiffLine(prefix, line string, maxWidth int, style lipgloss.Style) string {
	display := "  " + prefix + " " + line
	if maxWidth > 0 && len(display) > maxWidth+4 {
		display = display[:maxWidth+1] + "..."
	}
	return style.Render(display)
}

// splitLines splits text into lines, returning at least one line for empty input.
func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	lines := strings.Split(s, "\n")
	// Remove trailing empty line from trailing newline
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// isEditTool returns true if the tool name is an edit-like tool.
func isEditTool(name string) bool {
	return name == "Edit" || name == "MultiEdit"
}

// isWriteTool returns true if the tool name is a write tool.
func isWriteTool(name string) bool {
	return name == "Write"
}

// --- Bash pretty-print ---

type bashInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	Timeout     int    `json:"timeout"`
}

var bashCmdStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBF24")).Bold(true) // yellow

func formatBashFolded(toolInput string) string {
	var b bashInput
	if json.Unmarshal([]byte(toolInput), &b) != nil || b.Command == "" {
		return ""
	}
	cmd := b.Command
	if len(cmd) > 80 {
		cmd = cmd[:77] + "..."
	}
	// Replace newlines with semicolons for compact display
	cmd = strings.ReplaceAll(cmd, "\n", "; ")
	s := bashCmdStyle.Render("$ " + cmd)
	if b.Description != "" {
		s = dimStyle.Render(b.Description+"  ") + s
	}
	return s
}

func formatBashExpanded(toolInput string, width int) string {
	var b bashInput
	if json.Unmarshal([]byte(toolInput), &b) != nil || b.Command == "" {
		return ""
	}
	var buf strings.Builder
	if b.Description != "" {
		buf.WriteString(dimStyle.Render("  # "+b.Description) + "\n")
	}
	for _, line := range splitLines(b.Command) {
		buf.WriteString(bashCmdStyle.Render("  $ "+line) + "\n")
	}
	return buf.String()
}

// --- Read pretty-print ---

type readInput struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

func formatReadFolded(toolInput string) string {
	var r readInput
	if json.Unmarshal([]byte(toolInput), &r) != nil || r.FilePath == "" {
		return ""
	}
	shortPath := session.ShortenPath(r.FilePath, homeDir())
	s := shortPath
	if r.Offset > 0 || r.Limit > 0 {
		if r.Offset > 0 && r.Limit > 0 {
			s += dimStyle.Render(fmt.Sprintf(" L%d-%d", r.Offset, r.Offset+r.Limit))
		} else if r.Limit > 0 {
			s += dimStyle.Render(fmt.Sprintf(" L1-%d", r.Limit))
		}
	}
	return s
}

// --- Grep pretty-print ---

type grepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	OutputMode string `json:"output_mode"`
}

var grepPatStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F472B6")).Bold(true) // pink

func formatGrepFolded(toolInput string) string {
	var g grepInput
	if json.Unmarshal([]byte(toolInput), &g) != nil || g.Pattern == "" {
		return ""
	}
	s := grepPatStyle.Render("/"+g.Pattern+"/")
	if g.Path != "" {
		s += " " + dimStyle.Render(session.ShortenPath(g.Path, homeDir()))
	}
	if g.Glob != "" {
		s += " " + dimStyle.Render(g.Glob)
	}
	return s
}

// --- Glob pretty-print ---

type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func formatGlobFolded(toolInput string) string {
	var g globInput
	if json.Unmarshal([]byte(toolInput), &g) != nil || g.Pattern == "" {
		return ""
	}
	s := grepPatStyle.Render(g.Pattern)
	if g.Path != "" {
		s += " " + dimStyle.Render(session.ShortenPath(g.Path, homeDir()))
	}
	return s
}

// --- Agent pretty-print ---

type agentInput struct {
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type"`
	Prompt       string `json:"prompt"`
}

func formatAgentFolded(toolInput string) string {
	var a agentInput
	if json.Unmarshal([]byte(toolInput), &a) != nil {
		return ""
	}
	parts := []string{}
	if a.SubagentType != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorAccent).Render(a.SubagentType))
	}
	if a.Description != "" {
		parts = append(parts, a.Description)
	} else if a.Prompt != "" {
		prompt := a.Prompt
		if len(prompt) > 60 {
			prompt = prompt[:57] + "..."
		}
		parts = append(parts, dimStyle.Render(prompt))
	}
	return strings.Join(parts, "  ")
}

// --- Dispatch ---

// toolFoldedSummary returns a pretty folded summary for tool_use blocks.
func toolFoldedSummary(block session.ContentBlock) string {
	switch {
	case isEditTool(block.ToolName):
		return formatEditFolded(block.ToolInput)
	case isWriteTool(block.ToolName):
		return formatWriteFolded(block.ToolInput)
	case block.ToolName == "Bash":
		return formatBashFolded(block.ToolInput)
	case block.ToolName == "Read":
		return formatReadFolded(block.ToolInput)
	case block.ToolName == "Grep":
		return formatGrepFolded(block.ToolInput)
	case block.ToolName == "Glob":
		return formatGlobFolded(block.ToolInput)
	case block.ToolName == "Agent":
		return formatAgentFolded(block.ToolInput)
	}
	return ""
}

// toolDiffOutput returns pretty-printed output for tool_use blocks.
func toolDiffOutput(block session.ContentBlock, width int) string {
	switch {
	case isEditTool(block.ToolName):
		return formatEditDiff(block.ToolInput, width)
	case isWriteTool(block.ToolName):
		return formatWriteDiff(block.ToolInput, width)
	case block.ToolName == "Bash":
		return formatBashExpanded(block.ToolInput, width)
	}
	return ""
}
