package cli

import (
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/selection"
	"github.com/muesli/termenv"
)

// pickerSelectionStyle is reverse video on a renderer pinned to the ANSI
// profile: the default renderer drops every SGR under NO_COLOR, reverse
// included, and the selection would be invisible.
var pickerSelectionStyle = func() lipgloss.Style {
	// SetColorProfile, not termenv.WithProfile: the renderer re-detects the
	// profile from the environment unless it is set explicitly.
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.ANSI)
	return r.NewStyle().Reverse(true)
}()

// View is the picker frame with the drag-selection highlight drawn last, then
// the Kitty image layer.
func (m pickerModel) View() string {
	frame, image := m.screen()
	if m.sel.Active() {
		frame = m.sel.Render(frame, m.selectionPanes(), pickerSelectionStyle)
	}
	return frame + image
}

// previewTextRect is the preview's text cells: previewRect minus the border
// column and the one-cell left padding View draws the preview box with.
func (m pickerModel) previewTextRect() mouse.Rect {
	r := m.previewRect()
	r.X += 2
	r.W = max(r.W-2, 0)
	r.H = min(r.H, m.preview.Height)
	return r
}

// selectionPanes is the list (read from the frame) and the preview with its
// whole content, so a drag past the preview's edge scrolls it. While the ref
// picker is open it owns the screen like an overlay, and only the preview it
// is drawn in can be selected.
func (m pickerModel) selectionPanes() []selection.Pane {
	if m.width == 0 || m.height == 0 {
		return nil
	}
	preview := selection.Pane{
		ID: "preview", Rect: m.previewTextRect(), Kind: selection.Text,
		Lines: viewportLines(m.preview), Offset: m.preview.YOffset,
	}
	if m.refPicking {
		return []selection.Pane{preview}
	}
	list := selection.Pane{
		ID: "list", Kind: selection.List,
		Rect: mouse.Rect{X: 0, Y: 1, W: m.listWidth(), H: max(m.height-2, 0)},
	}
	return []selection.Pane{list, preview}
}

// viewportLines is every line of vp's content as the viewport draws it: a
// copy tall enough to show everything, asked for its visible lines (bubbles
// v1 has no content accessor).
func viewportLines(vp viewport.Model) []string {
	full := vp
	full.Height = vp.TotalLineCount() + vp.Style.GetVerticalFrameSize()
	return full.GotoBottom()
}

// handleSelectionMouse feeds the drag selection before the picker's click
// handling. done means the selection owns the event; a press that does not
// move stays the picker's click.
func (m *pickerModel) handleSelectionMouse(msg tea.MouseMsg) (cmd tea.Cmd, done bool) {
	if !mouse.IsLeftPress(msg) && !m.sel.Dragging() {
		return nil, false
	}
	if mouse.IsLeftPress(msg) {
		m.notice = ""
	}
	now := time.Now
	if m.clicks.now != nil {
		now = m.clicks.now
	}
	frame, _ := m.screen()
	r := m.sel.Mouse(msg, m.selectionPanes(), frame, now())
	if r.Scroll != 0 && r.ScrollPane == "preview" {
		// The same viewport scroll the wheel uses over the preview.
		if r.Scroll < 0 {
			m.preview.LineUp(-r.Scroll)
		} else {
			m.preview.LineDown(r.Scroll)
		}
	}
	if r.Copy != "" {
		cmd = selection.Copy(r.Copy)
	}
	return cmd, r.Consumed
}

func copiedNotice(msg selection.CopiedMsg) string {
	if msg.Err != nil {
		return "copy failed: " + msg.Err.Error()
	}
	return fmt.Sprintf("copied %d chars", utf8.RuneCountInString(msg.Text))
}
