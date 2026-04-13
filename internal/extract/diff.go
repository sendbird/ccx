package extract

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DiffStyles holds the ANSI color codes for diff rendering.
type DiffStyles struct {
	Add    func(string) string
	Del    func(string) string
	Hunk   func(string) string
	Header func(string) string
}

// DefaultDiffStyles returns plain text styling (no color).
func DefaultDiffStyles() DiffStyles {
	return DiffStyles{
		Add:    func(s string) string { return s },
		Del:    func(s string) string { return s },
		Hunk:   func(s string) string { return s },
		Header: func(s string) string { return s },
	}
}

type editJSON struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

type writeJSON struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// FormatDiff renders a colorized diff from a tool name and its JSON input.
// Returns empty string if not a change tool or unparseable.
func FormatDiff(toolName, toolInput string, width int, styles DiffStyles) string {
	switch toolName {
	case "Edit", "MultiEdit":
		return formatEditDiffShared(toolInput, width, styles)
	case "Write":
		return formatWriteDiffShared(toolInput, width, styles)
	default:
		return ""
	}
}

func formatEditDiffShared(toolInput string, width int, s DiffStyles) string {
	var e editJSON
	if json.Unmarshal([]byte(toolInput), &e) != nil || e.FilePath == "" {
		return ""
	}
	if e.OldString == "" && e.NewString == "" {
		return ""
	}

	var buf strings.Builder
	shortPath := ShortenPath(e.FilePath)
	buf.WriteString(s.Header("  "+shortPath) + "\n")
	if e.ReplaceAll {
		buf.WriteString(s.Header("  (replace all occurrences)") + "\n")
	}

	oldLines := splitDiffLines(e.OldString)
	newLines := splitDiffLines(e.NewString)

	hunk := fmt.Sprintf("  @@ -%d,%d +%d,%d @@", 1, len(oldLines), 1, len(newLines))
	buf.WriteString(s.Hunk(hunk) + "\n")

	maxW := width - 4
	if maxW < 20 {
		maxW = 60
	}
	for _, line := range oldLines {
		buf.WriteString(renderLine("-", line, maxW, s.Del) + "\n")
	}
	for _, line := range newLines {
		buf.WriteString(renderLine("+", line, maxW, s.Add) + "\n")
	}
	return buf.String()
}

func formatWriteDiffShared(toolInput string, width int, s DiffStyles) string {
	var w writeJSON
	if json.Unmarshal([]byte(toolInput), &w) != nil || w.FilePath == "" {
		return ""
	}

	var buf strings.Builder
	shortPath := ShortenPath(w.FilePath)
	buf.WriteString(s.Header("  "+shortPath) + "\n")

	lines := splitDiffLines(w.Content)
	hunk := fmt.Sprintf("  @@ +1,%d @@  (new file)", len(lines))
	buf.WriteString(s.Hunk(hunk) + "\n")

	maxW := width - 4
	if maxW < 20 {
		maxW = 60
	}
	maxLines := 50
	for i, line := range lines {
		if i >= maxLines {
			remaining := len(lines) - maxLines
			buf.WriteString(s.Header(fmt.Sprintf("  ... +%d more lines", remaining)) + "\n")
			break
		}
		buf.WriteString(renderLine("+", line, maxW, s.Add) + "\n")
	}
	return buf.String()
}

func renderLine(prefix, line string, maxWidth int, style func(string) string) string {
	display := "  " + prefix + " " + line
	if maxWidth > 0 && len(display) > maxWidth+4 {
		display = display[:maxWidth+1] + "..."
	}
	return style(display)
}

func splitDiffLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
