package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

var (
	selectBg = lipgloss.NewStyle().Background(lipgloss.Color("#3B4D7A")).Foreground(lipgloss.Color("#E2E8F0"))
	cursorBg = lipgloss.NewStyle().Background(lipgloss.Color("#2A3A5C")).Foreground(lipgloss.Color("#E2E8F0"))
)

func (a *App) renderedInspectorText() string {
	if a.conv.inspector.Tab == inspectorConversation || a.conv.inspector.Rendered == "" {
		return ""
	}
	return strings.TrimRight(stripANSI(a.conv.inspector.Rendered), "\n")
}

func (a *App) enterCopyMode() {
	vp := a.activeDetailVP()
	if vp == nil {
		return
	}

	switch a.state {
	case viewConversation:
		var chunks []string
		if content := a.renderedInspectorText(); content != "" {
			chunks = strings.Split(content, "\n")
		} else if a.conv.rightPaneMode == previewText {
			item, ok := a.selectedConversationItem()
			if !ok {
				return
			}
			var entry session.Entry
			switch item.kind {
			case convMsg:
				entry = item.merged.entry
			case convAgent:
				entry = buildAgentPreviewEntry(item.agent)
			default:
				return
			}
			chunks = previewTextChunks(entry)
		} else if a.conv.split.Folds != nil {
			for _, block := range a.conv.split.Folds.Entry.Content {
				if text := blockPlainText(block); text != "" {
					chunks = append(chunks, strings.Split(text, "\n")...)
				}
			}
		}
		if len(chunks) == 0 {
			return
		}
		a.copyLines = chunks
		a.copyModeActive = true
		a.copyCursor = 0
		a.copyAnchor = -1
		a.renderCopyMode()
		return
	}
}

func (a *App) exitCopyMode() {
	a.copyModeActive = false
	if a.state != viewConversation {
		return
	}
	a.conv.split.CacheKey = ""
	a.updateConvPreview()
}

func (a *App) activeDetailVP() *viewport.Model {
	switch a.state {
	case viewConversation:
		if a.conv.split.Show && a.conv.split.Focus {
			return &a.conv.split.Preview
		}
		return nil
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

// copyConvSelection copies the currently selected conversation preview content
// to the clipboard. If blocks are explicitly selected (via space toggling), only
// those blocks are copied; otherwise the block under the cursor is copied. When
// no fold state exists yet, falls back to the message-level text.
func (a *App) copyConvSelection() {
	if text := a.renderedInspectorText(); text != "" {
		copyToClipboard(text)
		a.copiedMsg = "Copied inspector!"
		return
	}
	sp := &a.conv.split
	if sp.Folds == nil || len(sp.Folds.Entry.Content) == 0 {
		a.copyConvSelectedMessage()
		return
	}
	fs := sp.Folds
	if len(fs.Selected) > 0 {
		var parts []string
		count := 0
		for i, block := range fs.Entry.Content {
			if !fs.Selected[i] {
				continue
			}
			if text := blockPlainText(block); text != "" {
				parts = append(parts, text)
				count++
			}
		}
		if count == 0 {
			a.copiedMsg = "Nothing to copy"
			return
		}
		copyToClipboard(strings.Join(parts, "\n\n"))
		a.copiedMsg = fmt.Sprintf("Copied %d block", count)
		if count != 1 {
			a.copiedMsg += "s"
		}
		a.copiedMsg += "!"
		fs.Selected = nil
		sp.RefreshFoldPreview(a.width, a.splitRatio)
		return
	}
	if fs.BlockCursor >= 0 && fs.BlockCursor < len(fs.Entry.Content) {
		text := blockPlainText(fs.Entry.Content[fs.BlockCursor])
		if text != "" {
			copyToClipboard(text)
			a.copiedMsg = "Copied block!"
			return
		}
	}
	a.copyConvSelectedMessage()
}

// copyConvSelectedMessage copies the full text of the currently selected
// conversation list item, used when there is no block-level selection.
func (a *App) copyConvSelectedMessage() {
	item, ok := a.selectedConversationItem()
	if !ok {
		a.copiedMsg = "Nothing to copy"
		return
	}
	var entry session.Entry
	switch item.kind {
	case convMsg:
		entry = item.merged.entry
	case convAgent:
		entry = buildAgentPreviewEntry(item.agent)
	default:
		a.copiedMsg = "Nothing to copy"
		return
	}
	text := entryFullText(entry)
	if text == "" {
		a.copiedMsg = "Nothing to copy"
		return
	}
	copyToClipboard(text)
	a.copiedMsg = "Copied message!"
}
