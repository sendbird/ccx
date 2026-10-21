package tui

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
	"github.com/sendbird/ccx/internal/kitty"
)

// selectionStyle is reverse video rather than a theme color, on a renderer
// pinned to the ANSI profile: the default renderer picks the Ascii profile
// under NO_COLOR and drops every SGR, reverse included, which would make the
// selection invisible.
var selectionStyle = func() lipgloss.Style {
	// SetColorProfile, not termenv.WithProfile: the renderer re-detects the
	// profile from the environment unless it is set explicitly.
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.ANSI)
	return r.NewStyle().Reverse(true)
}()

// View is the frame plus the drag-selection highlight and the Kitty image
// layer. The highlight is drawn last over the text so it lands on exactly the
// cells the selection covers; the image layer is cursor-positioned escape
// output and is appended after it so the highlight never cuts through it.
func (a *App) View() string {
	screen := a.renderScreen()
	if a.sel.Active() {
		screen = a.sel.Render(screen, a.selectionPanes(), selectionStyle)
	}
	if kitty.Supported() {
		screen += a.kittyImageLayer()
	}
	return screen
}

// splitRects is the geometry a SplitPane is drawn with: the same ListWidth,
// PreviewWidth, ContentHeight and narrow-terminal fallback SplitPane.Render
// and renderSessionSplit use, offset by the title row. preview is empty when
// the preview is not drawn; list is empty when the zoomed preview hides it.
func (a *App) splitRects(sp *SplitPane, layoutH int) (list, preview mouse.Rect) {
	contentH := ContentHeight(layoutH)
	if sp.Show && sp.PreviewOnly {
		return mouse.Rect{}, mouse.Rect{X: 0, Y: 1, W: max(a.width, 1), H: contentH}
	}
	listW := sp.ListWidth(a.width, a.splitRatio)
	list = mouse.Rect{X: 0, Y: 1, W: listW, H: contentH}
	if !sp.Show || a.width < 40 || layoutH < 10 {
		return list, mouse.Rect{}
	}
	// One border column sits between the list and the preview.
	return list, mouse.Rect{X: listW + 1, Y: 1, W: sp.PreviewWidth(a.width, a.splitRatio), H: contentH}
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

// handleSelectionMouse feeds a mouse event to the drag selection before the
// app's own mouse handling. done is true when the selection owns the event and
// the app must not also treat it as a click; a press that does not move stays
// the app's click.
func (a *App) handleSelectionMouse(msg tea.MouseMsg) (cmd tea.Cmd, done bool) {
	// Only a left press, or motion/release during a drag, can change the
	// selection; skip building the frame for wheel and stray motion.
	if !mouse.IsLeftPress(msg) && !a.sel.Dragging() {
		return nil, false
	}
	// A press in the divider's grab zone (which reaches one cell into each
	// pane) starts a resize after this returns; the motion that follows
	// belongs to the resize, never to a selection.
	if a.dragResizing {
		return nil, false
	}
	panes := a.selectionPanes()
	r := a.sel.Mouse(msg, panes, a.renderScreen(), a.selectionNow())
	if r.Scroll != 0 {
		a.scrollSelectionPane(r.ScrollPane, r.Scroll)
	}
	if r.Copy != "" {
		cmd = selection.Copy(r.Copy)
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

// scrollSelectionPane scrolls the pane a drag went past by n lines, through
// the same viewport scroll the wheel uses for plain previews.
func (a *App) scrollSelectionPane(id string, n int) {
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
	if n < 0 {
		vp.ScrollUp(-n)
	} else {
		vp.ScrollDown(n)
	}
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

// reportCopied shows the result of a selection copy in the footer toast.
func (a *App) reportCopied(msg selection.CopiedMsg) {
	if msg.Err != nil {
		a.flash("Copy failed: " + msg.Err.Error())
		return
	}
	a.flash(fmt.Sprintf("Copied %d chars", utf8.RuneCountInString(msg.Text)))
}
