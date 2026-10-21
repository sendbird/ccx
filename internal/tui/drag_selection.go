package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/selection"
	"github.com/sendbird/ccx/internal/kitty"
)

// View is the frame plus the drag-selection highlight and the Kitty image
// layer. The highlight is drawn last over the text so it lands on exactly the
// cells the selection covers; the image layer is cursor-positioned escape
// output and is appended after it so the highlight never cuts through it.
func (a *App) View() string {
	screen := a.renderScreen()
	if a.sel.Active() {
		screen = a.sel.Render(screen, a.selectionPanes(), selection.DefaultStyle())
	}
	if kitty.Supported() {
		screen += a.kittyImageLayer()
	}
	return screen
}

// splitRects is the geometry a SplitPane is drawn with (SplitPane.layout,
// which Render and renderSessionSplit also cut by). preview is empty when the
// preview is not drawn; list is empty when the zoomed preview hides it.
func (a *App) splitRects(sp *SplitPane, layoutH int) (list, preview mouse.Rect) {
	list, _, preview = sp.layout(a.width, layoutH, a.split)
	return list, preview
}

// viewportLines is every line of vp's content, as the viewport would draw it.
// bubbles v1 has no content accessor, so a copy of vp is made tall enough to
// show everything and asked for its visible lines; that is the viewport's own
// slicing and horizontal cut, without rendering a frame.
func viewportLines(vp viewport.Model) []string {
	full := vp
	full.Height = vp.TotalLineCount() + vp.Style.GetVerticalFrameSize()
	return full.GotoBottom()
}

// previewKind decides what a double click means in a preview. A fold-aware
// preview already uses the second click to toggle the block under the cursor
// (its Enter), so it stays a List; a plain preview selects words and lines.
func previewKind(sp *SplitPane) selection.Kind {
	if sp.Folds != nil && len(sp.Folds.BlockStarts) > 0 {
		return selection.List
	}
	return selection.Text
}

// splitPanes is the selectable panes of a split view: the list (read from the
// frame) and the preview with its full content, so a drag past its edge
// scrolls it and keeps the lines that scroll out.
func (a *App) splitPanes(prefix string, sp *SplitPane, layoutH int) []selection.Pane {
	list, preview := a.splitRects(sp, layoutH)
	var panes []selection.Pane
	if list.W > 0 {
		panes = append(panes, selection.Pane{ID: prefix + "/list", Rect: list, Kind: selection.List})
	}
	if preview.W > 0 {
		panes = append(panes, selection.Pane{
			ID: prefix + "/preview", Rect: preview, Kind: previewKind(sp),
			Lines: viewportLines(sp.Preview), Offset: sp.Preview.YOffset,
		})
	}
	return panes
}

// selectionPanes is every region a drag may select in, in drawing order. While
// an overlay is open it is the only pane, so nothing under a modal can be
// selected; copy mode has its own keyboard selection and gets none.
func (a *App) selectionPanes() []selection.Pane {
	if a.width == 0 || a.height == 0 || a.copyModeActive {
		return nil
	}
	if o, ok := a.activeOverlay(); ok {
		r := a.overlayRect()
		if r.W == 0 {
			return nil
		}
		kind := selection.List
		switch o.name {
		case "help", "full text", "confirm":
			kind = selection.Text
		}
		return []selection.Pane{{ID: "overlay/" + o.name, Rect: r, Kind: kind}}
	}
	switch a.state {
	case viewSessions:
		if a.sessionList.Width() == 0 || (a.sessionsLoading && len(a.sessions) == 0) || len(a.sessions) == 0 {
			return nil
		}
		return a.splitPanes("sessions", &a.sessSplit, a.height)
	case viewConversation:
		panes := a.splitPanes("conversation", &a.conv.split, a.conversationLayoutHeight())
		if h := a.executionRailHeight(); h > 0 {
			panes = append(panes, selection.Pane{
				ID: "conversation/rail", Kind: selection.List,
				Rect: mouse.Rect{X: 0, Y: a.executionRailTop(), W: a.width, H: h},
			})
		}
		return panes
	case viewConfig, viewPlugins:
		if sp := a.activeSplitPane(); sp != nil && sp.List != nil && sp.List.Width() > 0 {
			return a.splitPanes(fmt.Sprintf("v%d", a.state), sp, a.height)
		}
	case viewGlobalStats:
		if a.globalStatsLoading {
			return nil
		}
		vp := &a.globalStatsVP
		if a.statsDetail != statsDetailNone {
			vp = &a.statsDetailVP
		}
		return []selection.Pane{{
			ID: "stats", Kind: selection.Text,
			Rect:  mouse.Rect{X: 0, Y: 1, W: a.width, H: min(vp.Height, ContentHeight(a.height))},
			Lines: viewportLines(*vp), Offset: vp.YOffset,
		}}
	}
	return nil
}

// handleSelectionMouse feeds a mouse event, or the auto-scroll tick of a drag
// resting past a pane's edge, to the drag selection before the app's own mouse
// handling. done is true when the selection owns the event and the app must
// not also treat it as a click; a press that does not move stays the app's
// click. cmd carries the copy and the next auto-scroll tick. A press on the
// split divider never reaches here: handleMouse gives it to the split first.
func (a *App) handleSelectionMouse(msg tea.Msg) (cmd tea.Cmd, done bool) {
	r, cmd := a.sel.Update(msg, a.selectionPanes, a.renderScreen, a.selectionNow())
	if r.Scroll != 0 {
		a.setSelectionPaneOffset(r.ScrollPane, r.Offset)
	}
	return cmd, r.Consumed
}

// selectionNow shares the click detector's injectable clock, so tests time
// double clicks for rows and words with the same hand-advanced clock.
func (a *App) selectionNow() time.Time {
	if a.clicks.now != nil {
		return a.clicks.now()
	}
	return time.Now()
}

// setSelectionPaneOffset moves the pane a drag went past to offset, the
// selection's own Pane.Offset+Scroll. Setting it, rather than scrolling by
// Scroll, keeps an offset the app stored past the last page from swallowing
// the first lines of an upward drag.
func (a *App) setSelectionPaneOffset(id string, offset int) {
	var vp *viewport.Model
	switch id {
	case "stats":
		vp = &a.globalStatsVP
		if a.statsDetail != statsDetailNone {
			vp = &a.statsDetailVP
		}
	case "sessions/preview":
		// The live pane mirror is not scrollable locally (the wheel ignores
		// it too); its scrollback lives in tmux.
		if a.sessPreviewMode == sessPreviewLive && a.paneProxy != nil {
			return
		}
		vp = &a.sessSplit.Preview
	default:
		if sp := a.activeSplitPane(); sp != nil && id == a.selectionPrefix()+"/preview" {
			vp = &sp.Preview
		}
	}
	if vp == nil {
		return
	}
	vp.SetYOffset(offset)
	if id == "sessions/preview" {
		a.sessPreviewPinned = !a.sessPreviewAtBottom()
	}
}

// selectionPrefix is the pane-ID prefix selectionPanes uses for the current
// split view.
func (a *App) selectionPrefix() string {
	switch a.state {
	case viewSessions:
		return "sessions"
	case viewConversation:
		return "conversation"
	}
	return fmt.Sprintf("v%d", a.state)
}

// reportCopied shows the result of a selection copy in the footer toast, in
// ccx's capitalized toast wording.
func (a *App) reportCopied(msg selection.CopiedMsg) {
	a.flash(msg.NoteWith("Copied %d chars", "Copy failed: %s"))
}
