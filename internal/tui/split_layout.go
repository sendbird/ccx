package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/split"
)

// The list|preview split of the sessions, conversation, config and plugins
// views is one tuikit split.Model on App (a.split), shared by all of them as
// the old splitRatio percentage was: moving the divider in one view moves it
// in every view. The model owns the ratio, its clamping, the divider drag
// and the < > = keys; SplitPane.layout is the only place widths come from,
// so Render, selection panes and click hit tests read the same rects.

// minSplitHeight is the terminal height below which a split view shows only
// its list, as before the split model (the kit collapses by width only).
const minSplitHeight = 10

// defaultSplitRatio is the list's share of the width before the user moves
// the divider (the old default of 35 percent).
const defaultSplitRatio = 0.35

// newSplitModel is ccx's split: list at least 30 columns, preview at least
// 20, ratio 15-85% in 5% steps, one divider column, and below 40 columns the
// list alone. Grab stays 0: the old drag grabbed one cell of each pane, which
// now belongs to the pane so a drag there selects text.
func newSplitModel() split.Model {
	return split.Model{
		Divider:  1,
		MinA:     30,
		MinB:     20,
		Collapse: 40,
		Primary:  split.A,
		Lo:       0.15,
		Hi:       0.85,
		Default:  defaultSplitRatio,
	}
}

// layout cuts a split view of totalW x totalH into list, divider and preview
// in screen coordinates: the content rows start below the title row. When
// the preview is closed, the terminal is too short, or the split collapses,
// list is the whole body; when the preview is zoomed, preview is.
func (sp *SplitPane) layout(totalW, totalH int, sm split.Model) (list, div, preview mouse.Rect) {
	body := splitBody(totalW, totalH)
	if body.W <= 0 {
		return mouse.Rect{}, mouse.Rect{}, mouse.Rect{}
	}
	if sp.Show && sp.PreviewOnly {
		return mouse.Rect{}, mouse.Rect{}, body
	}
	if !sp.Show || totalH < minSplitHeight {
		return body, mouse.Rect{}, mouse.Rect{}
	}
	return sm.Layout(body)
}

// splitBody is the area a split view's panes share: full width, the content
// rows between the title row and the footer.
func splitBody(totalW, totalH int) mouse.Rect {
	return mouse.Rect{X: 0, Y: 1, W: max(totalW, 0), H: ContentHeight(totalH)}
}

// inPreview reports whether column x is in the drawn preview (a zoomed
// preview is everywhere). The divider column is in neither pane.
func (sp *SplitPane) inPreview(x, totalW int, sm split.Model) bool {
	if !sp.Show {
		return false
	}
	if sp.PreviewOnly {
		return true
	}
	_, _, preview := sm.Layout(mouse.Rect{W: totalW, H: 1})
	return preview.W > 0 && x >= preview.X
}

// dividerCell is one row of the divider: the kit's glyph (heavy while a drag
// is in progress, so the highlight survives NO_COLOR) in the border color.
func dividerCell(sm split.Model, c lipgloss.Color) string {
	return sm.DrawDivider(mouse.Rect{W: 1, H: 1}, lipgloss.NewStyle().Foreground(c))
}

// splitLayoutHeight is the height the active split view is laid out in: the
// conversation view gives rows to its execution rail.
func (a *App) splitLayoutHeight() int {
	if a.state == viewConversation {
		return a.conversationLayoutHeight()
	}
	return a.height
}

// activeSplitBody is the area the active view's split is laid out in, or the
// zero Rect when no split is on screen (no split view, the preview closed or
// zoomed, an overlay or copy mode owning the mouse). The zero Rect has no
// divider, so a press is never the split's; a drag already in progress still
// ends on release.
func (a *App) activeSplitBody() mouse.Rect {
	sp := a.activeSplitPane()
	if sp == nil || !sp.Show || sp.PreviewOnly || a.copyModeActive {
		return mouse.Rect{}
	}
	if _, open := a.activeOverlay(); open {
		return mouse.Rect{}
	}
	h := a.splitLayoutHeight()
	if h < minSplitHeight {
		return mouse.Rect{}
	}
	return splitBody(a.width, h)
}

// handleSplitMouse feeds a mouse event to the split before selection and
// click handling. consumed is true for a press on the divider and for the
// drag that follows: those never reach selection or the click handlers.
func (a *App) handleSplitMouse(msg tea.MouseMsg) (tea.Cmd, bool) {
	var r split.Result
	a.split, r = a.split.UpdateAt(msg, a.activeSplitBody(), a.selectionNow())
	if r.Changed {
		a.resizeActiveSplit()
	}
	// The ratio is saved by Update's saveUIState, which runs after every
	// message and writes only when the state changed.
	return nil, r.Consumed
}

// splitActionFor returns the callback HandleSplitKey runs for a resize key,
// laid out in a view of height layoutH.
func (a *App) splitActionFor(layoutH int) func(string) {
	return func(action string) {
		var r split.Result
		a.split, r = a.split.Handle(action, splitBody(a.width, layoutH))
		if !r.Consumed {
			return
		}
		if !r.Changed {
			if action != split.ActionReset {
				// At a bound: say so rather than ignore the key.
				a.flash("Split at its limit")
			}
			return
		}
		a.resizeAll()
	}
}

// resizeActiveSplit re-sizes the active view's list and preview to the
// current ratio. A drag moves the divider many times a second, so only the
// pane on screen is resized; the others pick the ratio up when drawn.
func (a *App) resizeActiveSplit() {
	sp := a.activeSplitPane()
	if sp == nil {
		return
	}
	sp.Resize(a.width, a.splitLayoutHeight(), a.split)
	if a.state == viewSessions {
		a.syncSessionSplitLayout()
	}
}

// setSplitPercent is `set:ratio N`: N percent of the width after the
// divider, clamped like every other write.
func (a *App) setSplitPercent(n int) {
	a.split = a.split.Set(float64(n)/100, splitBody(a.width, a.splitLayoutHeight()))
	a.resizeAll()
}

// restoreSplit applies the saved split. The current field (split.State, a
// fraction) wins; a ui.json or config.yaml from an older build carries only
// split_ratio, a percentage, which is read once here and written back as
// split.State on the next save. Anything out of range keeps the default.
func (a *App) restoreSplit(st split.State, legacyPercent int) {
	if m, ok := a.split.Restore(st); ok {
		a.split = m
		return
	}
	if legacyPercent >= 15 && legacyPercent <= 85 {
		a.split, _ = a.split.Restore(split.State{Ratio: float64(legacyPercent) / 100})
	}
}
