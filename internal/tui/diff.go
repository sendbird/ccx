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

// toolFoldedSummary returns a diff-aware folded summary for Edit/Write tool_use blocks.
// Returns empty string for other tool types.
func toolFoldedSummary(block session.ContentBlock) string {
	if isEditTool(block.ToolName) {
		return formatEditFolded(block.ToolInput)
	}
	if isWriteTool(block.ToolName) {
		return formatWriteFolded(block.ToolInput)
	}
	return ""
}

// toolDiffOutput returns diff-formatted output for Edit/Write tool_use blocks.
// Returns empty string for other tool types.
func toolDiffOutput(block session.ContentBlock, width int) string {
	if isEditTool(block.ToolName) {
		return formatEditDiff(block.ToolInput, width)
	}
	if isWriteTool(block.ToolName) {
		return formatWriteDiff(block.ToolInput, width)
	}
	return ""
}
