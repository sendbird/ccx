package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	selectBg = lipgloss.NewStyle().Background(lipgloss.Color("#3B4D7A")).Foreground(lipgloss.Color("#E2E8F0"))
	cursorBg = lipgloss.NewStyle().Background(lipgloss.Color("#2A3A5C")).Foreground(lipgloss.Color("#E2E8F0"))
)

func (a *App) enterCopyMode() {
	vp := a.activeDetailVP()
	if vp == nil {
		return
	}

	var content string
	switch a.state {
	case viewMessageFull:
		content = a.msgFull.content
	}

	a.copyLines = strings.Split(stripANSI(content), "\n")
	a.copyModeActive = true
	a.copyCursor = vp.YOffset
	a.copyAnchor = -1
	a.renderCopyMode()
}

func (a *App) exitCopyMode() {
	a.copyModeActive = false
	vp := a.activeDetailVP()
	if vp == nil {
		return
	}
	offset := vp.YOffset
	switch a.state {
	case viewMessageFull:
		a.refreshMsgFullPreview()
	}
	vp.YOffset = offset
}

func (a *App) activeDetailVP() *viewport.Model {
	switch a.state {
	case viewMessageFull:
		return &a.msgFull.vp
	default:
		return nil
	}
}

func (a *App) handleCopyModeKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	vp := a.activeDetailVP()
	if vp == nil {
		return a, nil
	}

	key := msg.String()
	if nav, _ := a.keymap.TranslateNav(key, msg); nav != "" {
		key = nav
	}

	switch key {
	case "esc":
		a.exitCopyMode()
		return a, nil
	case "v", " ":
		if a.copyAnchor == -1 {
			a.copyAnchor = a.copyCursor
		} else {
			a.copyAnchor = -1
		}
		a.renderCopyMode()
		return a, nil
	case "down":
		if a.copyCursor < len(a.copyLines)-1 {
			a.copyCursor++
			a.ensureCursorVisible(vp)
			a.renderCopyMode()
		}
		return a, nil
	case "up":
		if a.copyCursor > 0 {
			a.copyCursor--
			a.ensureCursorVisible(vp)
			a.renderCopyMode()
		}
		return a, nil
	case "pgdown":
		a.copyCursor = min(a.copyCursor+vp.Height, len(a.copyLines)-1)
		a.ensureCursorVisible(vp)
		a.renderCopyMode()
		return a, nil
	case "pgup":
		a.copyCursor = max(a.copyCursor-vp.Height, 0)
		a.ensureCursorVisible(vp)
		a.renderCopyMode()
		return a, nil
	case "home":
		a.copyCursor = 0
		a.ensureCursorVisible(vp)
		a.renderCopyMode()
		return a, nil
	case "end":
		a.copyCursor = max(len(a.copyLines)-1, 0)
		a.ensureCursorVisible(vp)
		a.renderCopyMode()
		return a, nil
	case "y", "enter":
		a.doCopySelection()
		a.exitCopyMode()
		return a, nil
	}
	return a, nil
}

func (a *App) ensureCursorVisible(vp *viewport.Model) {
	if a.copyCursor < vp.YOffset {
		vp.YOffset = a.copyCursor
	} else if a.copyCursor >= vp.YOffset+vp.Height {
		vp.YOffset = a.copyCursor - vp.Height + 1
	}
}

func (a *App) copySelRange() (start, end int) {
	if a.copyAnchor == -1 {
		return a.copyCursor, a.copyCursor
	}
	s, e := a.copyAnchor, a.copyCursor
	if s > e {
		s, e = e, s
	}
	return s, e
}

func (a *App) doCopySelection() {
	start, end := a.copySelRange()
	start = max(start, 0)
	end = min(end, len(a.copyLines)-1)

	selected := a.copyLines[start : end+1]
	text := strings.TrimRight(strings.Join(selected, "\n"), "\n ")
	copyToClipboard(text)
	n := end - start + 1
	a.copiedMsg = fmt.Sprintf("Copied %d line", n)
	if n != 1 {
		a.copiedMsg += "s"
	}
	a.copiedMsg += "!"
}

func (a *App) renderCopyMode() {
	vp := a.activeDetailVP()
	if vp == nil {
		return
	}

	offset := vp.YOffset
	selStart, selEnd := a.copySelRange()

	var sb strings.Builder
	for i, line := range a.copyLines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		padded := line + strings.Repeat(" ", max(a.width-lipgloss.Width(line), 0))
		if a.copyAnchor != -1 && i >= selStart && i <= selEnd {
			sb.WriteString(selectBg.Render(padded))
		} else if i == a.copyCursor {
			sb.WriteString(cursorBg.Render(padded))
		} else {
			sb.WriteString(line)
		}
	}

	vp.SetContent(sb.String())
	vp.YOffset = offset
}
