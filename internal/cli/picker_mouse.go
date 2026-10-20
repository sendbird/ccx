package cli

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/mouse"
)

// pickerClicks remembers the previous press for double-click detection. now is
// injectable so tests do not depend on wall-clock timing.
type pickerClicks struct {
	clicks mouse.Clicks
	now    func() time.Time
}

// listRect is the screen rectangle of the list rows, the same numbers View
// lays out: row 0 is the title, the list pane starts on row 1, and an active
// search line or applied term takes the first row of the pane.
func (m pickerModel) listRect() mouse.Rect {
	contentH := m.height - 2
	y := 1
	if m.searching || m.searchTerm != "" {
		y++
	}
	return mouse.Rect{X: 0, Y: y, W: m.listWidth(), H: max(contentH-2, 3)}
}

// previewRect is the preview pane including its left border column.
func (m pickerModel) previewRect() mouse.Rect {
	listW := m.listWidth()
	return mouse.Rect{X: listW, Y: 1, W: max(m.width-listW, 0), H: max(m.height-2, 0)}
}

// listItemAt maps a mouse event to a filtered-item index using the drawn row
// heights, so multi-line conversation rows hit the right item.
func (m pickerModel) listItemAt(msg tea.MouseMsg) (int, bool) {
	sel := lipgloss.NewStyle()
	_, heights := m.renderListLines(m.height-2, m.listWidth(), sel, sel, sel)
	c := mouse.BlockHit(m.listRect(), 0, heights, msg)
	if !c.HasItem {
		return -1, false
	}
	return m.listFirstVisible(m.height-2) + c.Index, true
}

// handleMouse is the picker's mouse path. Every gesture ends in the same key
// handlers the keyboard uses:
//
//   - left press on a row: select it; a second press on the same row is Enter.
//   - wheel over the list: three Down/Up presses; over the preview: scroll it
//     three lines (or move the artifact cursor three, which j/k does).
//   - ref picker (opened by Enter on a multi-ref item): wheel moves its cursor,
//     a click outside the preview is its Esc.
//   - everything else, and anything while the search line owns input: ignored.
func (m pickerModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.width == 0 || m.height == 0 || m.searching {
		return m, nil
	}
	if m.refPicking {
		return m.handleRefMouse(msg)
	}
	if d := mouse.Delta(msg); d != 0 {
		return m.handleWheel(msg, d)
	}
	if !mouse.IsLeftPress(msg) {
		return m, nil
	}
	idx, ok := m.listItemAt(msg)
	if !ok {
		m.clicks.clicks = mouse.Clicks{}
		return m, nil
	}
	m.cursor = idx
	m.previewFocused = false
	m.updatePreview()

	now := time.Now
	if m.clicks.now != nil {
		now = m.clicks.now
	}
	var double bool
	m.clicks.clicks, double = m.clicks.clicks.Press(fmt.Sprintf("item:%d", m.realIndex(idx)), now())
	if double {
		return m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	}
	return m, nil
}

func (m pickerModel) handleWheel(msg tea.MouseMsg, d int) (tea.Model, tea.Cmd) {
	switch {
	case m.previewRect().Contains(msg.X, msg.Y):
		m.previewFocused = true
		if m.kind == "conversation" && m.cursor >= 0 && m.cursor < len(m.items) && len(m.items[m.cursor].ConversationArtifacts) > 0 {
			return m.repeatKey(d, pickerModel.handlePreviewKey)
		}
		if d < 0 {
			m.preview.LineUp(mouse.WheelRows)
		} else {
			m.preview.LineDown(mouse.WheelRows)
		}
		return m, nil
	case (mouse.Rect{X: 0, Y: 1, W: m.listWidth(), H: max(m.height-2, 0)}).Contains(msg.X, msg.Y):
		m.previewFocused = false
		return m.repeatKey(d, pickerModel.handleKey)
	}
	return m, nil
}

func (m pickerModel) handleRefMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if d := mouse.Delta(msg); d != 0 {
		return m.repeatKey(d, pickerModel.handleRefKey)
	}
	if mouse.IsLeftPress(msg) && !m.previewRect().Contains(msg.X, msg.Y) {
		return m.handleRefKey(tea.KeyMsg{Type: tea.KeyEsc})
	}
	return m, nil
}

// repeatKey sends |d| Up or Down presses through a key handler, threading the
// model through each press because the handlers have value receivers.
func (m pickerModel) repeatKey(d int, handle func(pickerModel, tea.KeyMsg) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	key := tea.KeyMsg{Type: tea.KeyDown}
	if d < 0 {
		key = tea.KeyMsg{Type: tea.KeyUp}
	}
	var cmds []tea.Cmd
	cur := m
	var model tea.Model = m
	for range max(d, -d) {
		var cmd tea.Cmd
		model, cmd = handle(cur, key)
		cur = model.(pickerModel)
		cmds = append(cmds, cmd)
	}
	return model, tea.Batch(cmds...)
}
