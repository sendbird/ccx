package tui

import (
	"strings"

	"github.com/sendbird/ccx/internal/session"
)

// blockPlainText extracts the plain text content of a single block.
func blockPlainText(b session.ContentBlock) string {
	switch b.Type {
	case "text":
		return strings.TrimSpace(session.StripXMLTags(b.Text))
	case "tool_use":
		header := "Tool: " + b.ToolName
		if b.ToolInput != "" {
			header += "  " + b.ToolInput
		}
		return header
	case "tool_result":
		return strings.TrimSpace(b.Text)
	case "thinking":
		return strings.TrimSpace(b.Text)
	default:
		return strings.TrimSpace(b.Text)
	}
}

// highlightSearchMatches wraps occurrences of the search term with a
// highlight background in the rendered viewport content.
// currentLine is the line number of the active match (-1 for no active highlight).
func highlightSearchMatches(content, term string, currentLine int) string {
	if term == "" {
		return content
	}
	return highlightSearchTerms(content, []string{term}, currentLine)
}

// highlightSearchTerms is highlightSearchMatches for several terms at once.
// Filter expressions are AND-ed sets of words, so every one of them is a reason
// the block is on screen and all of them get painted.
//
// Terms are applied one after another over the already-highlighted line. That
// is safe because highlightLine walks visible characters and copies ANSI
// sequences through untouched, so an earlier term's escapes neither shift the
// match positions of a later term nor get matched themselves.
func highlightSearchTerms(content string, terms []string, currentLine int) string {
	if len(terms) == 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		plain := strings.ToLower(stripANSI(line))
		for _, term := range terms {
			if term == "" || !strings.Contains(plain, strings.ToLower(term)) {
				continue
			}
			lines[i] = highlightLine(lines[i], term, i == currentLine)
		}
	}
	return strings.Join(lines, "\n")
}

// highlightLine inserts ANSI highlight escapes around case-insensitive matches
// in a line that may contain existing ANSI sequences.
// If isCurrent is true, uses a brighter style for the active match line.
func highlightLine(line, term string, isCurrent bool) string {
	hlStart := "\x1b[43;30m" // yellow bg, black fg
	if isCurrent {
		hlStart = "\x1b[46;30m" // cyan bg, black fg (current match)
	}
	const hlEnd = "\x1b[0m"

	lowerTerm := strings.ToLower(term)
	termLen := len(lowerTerm)

	// Walk the line tracking visible character position vs ANSI escapes.
	// Build a map from visible-char index to byte positions in the original line.
	type charPos struct {
		byteStart int
		byteEnd   int
	}
	var visChars []charPos
	i := 0
	for i < len(line) {
		if line[i] == '\x1b' && i+1 < len(line) && line[i+1] == '[' {
			// Skip ANSI escape sequence
			j := i + 2
			for j < len(line) && line[j] != 'm' {
				j++
			}
			if j < len(line) {
				j++ // skip 'm'
			}
			i = j
			continue
		}
		visChars = append(visChars, charPos{i, i + 1})
		i++
	}

	if len(visChars) == 0 {
		return line
	}

	// Find matches in visible text
	visText := make([]byte, len(visChars))
	for idx, cp := range visChars {
		visText[idx] = line[cp.byteStart]
	}
	lowerVis := strings.ToLower(string(visText))

	type matchRange struct{ start, end int } // visible char indices
	var matches []matchRange
	pos := 0
	for {
		idx := strings.Index(lowerVis[pos:], lowerTerm)
		if idx < 0 {
			break
		}
		mStart := pos + idx
		matches = append(matches, matchRange{mStart, mStart + termLen})
		pos = mStart + termLen
	}

	if len(matches) == 0 {
		return line
	}

	// Rebuild the line, inserting highlight codes around matched visible chars.
	// Track which visible chars are highlighted.
	hlSet := make([]bool, len(visChars))
	for _, m := range matches {
		for j := m.start; j < m.end && j < len(visChars); j++ {
			hlSet[j] = true
		}
	}

	var sb strings.Builder
	visIdx := 0
	inHL := false
	i = 0
	for i < len(line) {
		if line[i] == '\x1b' && i+1 < len(line) && line[i+1] == '[' {
			// Copy ANSI escape through
			j := i + 2
			for j < len(line) && line[j] != 'm' {
				j++
			}
			if j < len(line) {
				j++
			}
			sb.WriteString(line[i:j])
			i = j
			continue
		}
		// Visible character
		if visIdx < len(hlSet) {
			if hlSet[visIdx] && !inHL {
				sb.WriteString(hlStart)
				inHL = true
			} else if !hlSet[visIdx] && inHL {
				sb.WriteString(hlEnd)
				inHL = false
			}
		}
		sb.WriteByte(line[i])
		visIdx++
		i++
	}
	if inHL {
		sb.WriteString(hlEnd)
	}
	return sb.String()
}
