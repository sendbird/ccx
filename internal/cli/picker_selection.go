package cli

import (
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/selection"
)

// View is the picker frame with the drag-selection highlight drawn last, then
// the Kitty image layer.
func (m pickerModel) View() string {
	frame, image := m.screen()
	if m.sel.Active() {
		frame = m.sel.Render(frame, m.selectionPanes(), selection.DefaultStyle())
	}
	return frame + image
}

// previewTextRect is the preview's text cells: previewRect minus the border
// column and padding the preview box is drawn with, capped at the viewport's
// height.
func (m pickerModel) previewTextRect() mouse.Rect {
	r := m.previewRect().InsetStyle(pickerPreviewFrame)
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

// handleSelectionMouse feeds the drag selection, or the auto-scroll tick of a
// drag resting past the preview's edge, before the picker's click handling.
// done means the selection owns the event; a press that does not move stays
// the picker's click.
func (m *pickerModel) handleSelectionMouse(msg tea.Msg) (cmd tea.Cmd, done bool) {
	if mm, ok := msg.(tea.MouseMsg); ok && mouse.IsLeftPress(mm) {
		m.notice = ""
	}
	now := time.Now
	if m.clicks.now != nil {
		now = m.clicks.now
	}
	frame := func() string { f, _ := m.screen(); return f }
	r, cmd := m.sel.Update(msg, m.selectionPanes, frame, now())
	if r.Scroll != 0 && r.ScrollPane == "preview" {
		m.preview.SetYOffset(r.Offset)
	}
	return cmd, r.Consumed
}
