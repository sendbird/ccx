package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// inputModal is a simple multi-line text input shown as a modal overlay.
// Supports basic editing. Ctrl+E toggles to $EDITOR via tmux display-popup.
type inputModal struct {
	lines     []string
	cursorRow int
	cursorCol int
	scrollOff int    // vertical scroll offset
	title     string // custom title (empty = "Send Input")
}

func newInputModal() inputModal {
	return inputModal{
		lines: []string{""},
	}
}

func (m *inputModal) Text() string {
	return strings.Join(m.lines, "\n")
}

func (m *inputModal) clampCursor() {
	if m.cursorRow < 0 {
		m.cursorRow = 0
	}
	if m.cursorRow >= len(m.lines) {
		m.cursorRow = len(m.lines) - 1
	}
	lineLen := utf8.RuneCountInString(m.lines[m.cursorRow])
	if m.cursorCol > lineLen {
		m.cursorCol = lineLen
	}
	if m.cursorCol < 0 {
		m.cursorCol = 0
	}
}

// handleKey processes a key press. Returns "send", "cancel", "editor", or "".
func (m *inputModal) handleKey(key string) string {
	switch key {
	case "esc":
		return "cancel"
	case "ctrl+s":
		return "send"
	case "ctrl+e":
		return "editor"
	case "enter":
		line := m.lines[m.cursorRow]
		runes := []rune(line)
		before := string(runes[:m.cursorCol])
		after := string(runes[m.cursorCol:])
		m.lines[m.cursorRow] = before
		newLines := make([]string, 0, len(m.lines)+1)
		newLines = append(newLines, m.lines[:m.cursorRow+1]...)
		newLines = append(newLines, after)
		newLines = append(newLines, m.lines[m.cursorRow+1:]...)
		m.lines = newLines
		m.cursorRow++
		m.cursorCol = 0
		return ""
	case "backspace":
		if m.cursorCol > 0 {
			runes := []rune(m.lines[m.cursorRow])
			m.lines[m.cursorRow] = string(runes[:m.cursorCol-1]) + string(runes[m.cursorCol:])
			m.cursorCol--
		} else if m.cursorRow > 0 {
			prevLen := utf8.RuneCountInString(m.lines[m.cursorRow-1])
			m.lines[m.cursorRow-1] += m.lines[m.cursorRow]
			m.lines = append(m.lines[:m.cursorRow], m.lines[m.cursorRow+1:]...)
			m.cursorRow--
			m.cursorCol = prevLen
		}
		return ""
	case "left":
		if m.cursorCol > 0 {
			m.cursorCol--
		}
		return ""
	case "right":
		lineLen := utf8.RuneCountInString(m.lines[m.cursorRow])
		if m.cursorCol < lineLen {
			m.cursorCol++
		}
		return ""
	case "up":
		if m.cursorRow > 0 {
			m.cursorRow--
			m.clampCursor()
		}
		return ""
	case "down":
		if m.cursorRow < len(m.lines)-1 {
			m.cursorRow++
			m.clampCursor()
		}
		return ""
	case "home", "ctrl+a":
		m.cursorCol = 0
		return ""
	case "end":
		m.cursorCol = utf8.RuneCountInString(m.lines[m.cursorRow])
		return ""
	case "tab":
		m.insertChar('\t')
		return ""
	default:
		if len(key) == 1 || (len(key) > 1 && !strings.HasPrefix(key, "ctrl+") && !strings.HasPrefix(key, "alt+")) {
			for _, r := range key {
				m.insertChar(r)
			}
		}
		return ""
	}
}

func (m *inputModal) insertChar(r rune) {
	runes := []rune(m.lines[m.cursorRow])
	newRunes := make([]rune, 0, len(runes)+1)
	newRunes = append(newRunes, runes[:m.cursorCol]...)
	newRunes = append(newRunes, r)
	newRunes = append(newRunes, runes[m.cursorCol:]...)
	m.lines[m.cursorRow] = string(newRunes)
	m.cursorCol++
}

// inputTabWidth matches lipgloss's default tab expansion, so wrapping math
// agrees with what the bordered style finally renders.
const inputTabWidth = 4

func inputCellWidth(r rune) int {
	if r == '\t' {
		return inputTabWidth
	}
	return max(runewidth.RuneWidth(r), 0)
}

// inputVisualRow is one on-screen row: runes [start,end) of logical line.
type inputVisualRow struct {
	line, start, end int
}

// layout soft-wraps every logical line into rows of at most width terminal
// cells and returns the index of the row holding the cursor. Wrapping is by
// cell width, not rune count: a Hangul/CJK rune occupies two cells, and
// counting runes let such lines overflow the modal border.
func (m *inputModal) layout(width int) ([]inputVisualRow, int) {
	width = max(width, 2) // a wide rune must always fit on a row
	var rows []inputVisualRow
	cursorVis := 0
	for li, line := range m.lines {
		runes := []rune(line)
		first := len(rows)
		start, w := 0, 0
		for i, r := range runes {
			cw := inputCellWidth(r)
			if w+cw > width && i > start {
				rows = append(rows, inputVisualRow{li, start, i})
				start, w = i, 0
			}
			w += cw
		}
		rows = append(rows, inputVisualRow{li, start, len(runes)})
		// A cursor at end-of-line draws a one-cell block; if the last row
		// is already full the block belongs on a fresh row.
		if li == m.cursorRow && m.cursorCol >= len(runes) && w+1 > width {
			rows = append(rows, inputVisualRow{li, len(runes), len(runes)})
		}
		if li == m.cursorRow {
			cursorVis = first
			for vi := first; vi < len(rows); vi++ {
				r := rows[vi]
				if m.cursorCol >= r.start && (m.cursorCol < r.end || vi == len(rows)-1) {
					cursorVis = vi
					break
				}
			}
		}
	}
	return rows, cursorVis
}

// ensureVisible adjusts scrollOff (in visual rows) so cursorVis is visible.
func (m *inputModal) ensureVisible(cursorVis, visibleH int) {
	if cursorVis < m.scrollOff {
		m.scrollOff = cursorVis
	}
	if cursorVis >= m.scrollOff+visibleH {
		m.scrollOff = cursorVis - visibleH + 1
	}
}

// render returns the modal overlay string.
func (m *inputModal) render(bg string, screenW, screenH int) string {
	modalW := min(screenW*3/4, 72)
	if modalW < 30 {
		modalW = screenW - 4
	}
	innerW := modalW - 4 // border(2) + padding(2)

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	titleText := "Send Input"
	if m.title != "" {
		titleText = m.title
	}
	title := titleStyle.Render(titleText)

	helpText := dimStyle.Render("^S:send  ^E:$EDITOR  esc:cancel")

	contentH := min(screenH/2, 20)
	m.clampCursor()
	rows, cursorVis := m.layout(innerW)
	m.ensureVisible(cursorVis, contentH)

	var body strings.Builder
	body.WriteString(title + "\n")
	body.WriteString(strings.Repeat("─", innerW) + "\n")

	endRow := min(m.scrollOff+contentH, len(rows))
	cursorStyle := lipgloss.NewStyle().Reverse(true)
	for vi := m.scrollOff; vi < endRow; vi++ {
		r := rows[vi]
		runes := []rune(m.lines[r.line])[r.start:r.end]
		if vi != cursorVis {
			body.WriteString(string(runes) + "\n")
			continue
		}
		col := m.cursorCol - r.start
		before := string(runes[:col])
		ch, after := " ", ""
		if col < len(runes) {
			ch = string(runes[col])
			after = string(runes[col+1:])
		}
		body.WriteString(before + cursorStyle.Render(ch) + after + "\n")
	}

	for i := endRow - m.scrollOff; i < contentH; i++ {
		body.WriteString(dimStyle.Render("~") + "\n")
	}

	body.WriteString(strings.Repeat("─", innerW) + "\n")
	body.WriteString(helpText)

	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Width(modalW).
		Padding(0, 1)

	modal := modalStyle.Render(body.String())
	return overlayCenter(bg, modal, screenW, screenH)
}
