package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
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

// ensureVisible adjusts scrollOff so the cursor row is visible.
func (m *inputModal) ensureVisible(visibleH int) {
	if m.cursorRow < m.scrollOff {
		m.scrollOff = m.cursorRow
	}
	if m.cursorRow >= m.scrollOff+visibleH {
		m.scrollOff = m.cursorRow - visibleH + 1
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
	m.ensureVisible(contentH)

	var body strings.Builder
	body.WriteString(title + "\n")
	body.WriteString(strings.Repeat("─", innerW) + "\n")

	endRow := m.scrollOff + contentH
	if endRow > len(m.lines) {
		endRow = len(m.lines)
	}
	cursorStyle := lipgloss.NewStyle().Reverse(true)
	for i := m.scrollOff; i < endRow; i++ {
		line := m.lines[i]
		runes := []rune(line)
		if i == m.cursorRow {
			col := m.cursorCol
			if col > len(runes) {
				col = len(runes)
			}
			before := string(runes[:col])
			var ch, after string
			if col < len(runes) {
				ch = string(runes[col])
				after = string(runes[col+1:])
			} else {
				ch = " "
			}
			body.WriteString(before + cursorStyle.Render(ch) + after + "\n")
		} else {
			if utf8.RuneCountInString(line) > innerW {
				line = string([]rune(line)[:innerW])
			}
			body.WriteString(line + "\n")
		}
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
