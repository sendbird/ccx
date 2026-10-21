package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/mouse"
	"github.com/mattn/go-runewidth"
)

// mouseOverlay describes one modal/menu/palette that owns the mouse while it
// is open. open mirrors the flag View uses to draw it. scroll marks overlays
// whose own keys treat up/down as navigation, so the wheel can drive them
// through that same key path; overlays where any key dismisses (help, confirm,
// the letter menus) must not receive synthetic arrows, or one wheel notch
// would close them.
type mouseOverlay struct {
	name   string
	open   func(a *App) bool
	close  func(a *App) // clears the flag on a View-only copy; see overlayRect
	scroll bool
}

// mouseOverlays lists every overlay View can draw, topmost first. Keep it in
// step with the overlay blocks in App.View; TestEveryOverlayOwnsTheMouse
// drives each entry.
var mouseOverlays = []mouseOverlay{
	{"live input", func(a *App) bool { return a.liveInputActive }, func(a *App) { a.liveInputActive = false }, false},
	{"cross-session search", func(a *App) bool { return a.searchActive }, func(a *App) { a.searchActive = false }, true},
	{"confirm", func(a *App) bool { return a.confirmMsg != "" }, func(a *App) { a.confirmMsg = "" }, false},
	{"help", func(a *App) bool { return a.showHelp }, func(a *App) { a.showHelp = false }, false},
	{"command palette", func(a *App) bool { return a.cmdMode }, func(a *App) { a.cmdMode = false }, true},
	{"url menu", func(a *App) bool { return a.urlMenu }, func(a *App) { a.urlMenu = false }, true},
	{"full text", func(a *App) bool { return a.state == viewSessions && a.sessConvFullText != "" }, func(a *App) { a.sessConvFullText = "" }, true},
	{"project picker", func(a *App) bool { return a.state == viewConfig && a.cfgProjectPicker }, func(a *App) { a.cfgProjectPicker = false }, true},
	{"tag menu", func(a *App) bool { return a.tagMenu }, func(a *App) { a.tagMenu = false }, false},
	{"share ref", func(a *App) bool { return a.shareRefMenu }, func(a *App) { a.shareRefMenu = false }, true},
	{"actions menu", func(a *App) bool { return a.actionsMenu && a.state == viewSessions }, func(a *App) { a.actionsMenu = false }, false},
	{"page menu", func(a *App) bool { return a.sessPageMenu && a.state == viewSessions }, func(a *App) { a.sessPageMenu = false }, false},
	{"state menu", func(a *App) bool { return a.stateMenu && a.state == viewSessions }, func(a *App) { a.stateMenu = false }, false},
	{"views menu", func(a *App) bool { return a.viewsMenu }, func(a *App) { a.viewsMenu = false }, false},
	{"edit menu", func(a *App) bool { return a.editMenu }, func(a *App) { a.editMenu = false }, false},
	{"stats page menu", func(a *App) bool { return a.statsPageMenu && a.state == viewGlobalStats }, func(a *App) { a.statsPageMenu = false }, false},
	{"inspector menu", func(a *App) bool { return a.inspectorMenu && a.state == viewConversation }, func(a *App) { a.inspectorMenu = false }, false},
	{"conversation actions", func(a *App) bool { return a.convActionsMenu && a.state == viewConversation }, func(a *App) { a.convActionsMenu = false }, false},
	{"execution context menu", func(a *App) bool { return a.executionContextMenu && a.state == viewConversation }, func(a *App) { a.executionContextMenu = false }, false},
	{"config actions", func(a *App) bool { return a.cfgActionsMenu && a.state == viewConfig }, func(a *App) { a.cfgActionsMenu = false }, false},
	{"plugin actions", func(a *App) bool { return a.plgActionsMenu && a.state == viewConfig && a.cfgPluginsPage }, func(a *App) { a.plgActionsMenu = false }, false},
	{"plugin component actions", func(a *App) bool {
		return a.plgCompActionsMenu && a.state == viewConfig && a.cfgPluginsPage && a.plgDetailActive
	}, func(a *App) { a.plgCompActionsMenu = false }, false},
}

// activeOverlay returns the topmost open overlay, if any.
func (a *App) activeOverlay() (mouseOverlay, bool) {
	for _, o := range mouseOverlays {
		if o.open(a) {
			return o, true
		}
	}
	return mouseOverlay{}, false
}

// handleOverlayMouse gives an open overlay the whole mouse. handled is true
// whenever an overlay is open, even for events it ignores, so nothing reaches
// the base view underneath.
//
//   - wheel: drives scrollable overlays through their own up/down keys.
//   - left click outside the overlay: the overlay's Esc key.
//   - left click inside, right/middle click, release, motion: ignored.
func (a *App) handleOverlayMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	o, ok := a.activeOverlay()
	if !ok {
		return a, nil, false
	}
	if d := mouse.Delta(msg); d != 0 {
		if !o.scroll {
			return a, nil, true
		}
		key := tea.KeyMsg{Type: tea.KeyDown}
		if d < 0 {
			key = tea.KeyMsg{Type: tea.KeyUp}
		}
		var cmds []tea.Cmd
		for range mouse.WheelRows {
			_, cmd := a.Update(key)
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...), true
	}
	if !mouse.IsLeftPress(msg) {
		return a, nil, true
	}
	if a.overlayRect().Contains(msg.X, msg.Y) {
		return a, nil, true
	}
	m, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	return m, cmd, true
}

// overlayRect is the screen rectangle the open overlay covers. Overlays are
// drawn by a dozen render paths with their own sizing, so rather than repeat
// that arithmetic it renders the screen twice, with and without the overlay, and
// takes the bounding box of the cells that differ. The rows reserved for the
// title and footer are skipped because overlays rewrite the footer hint.
func (a *App) overlayRect() mouse.Rect {
	with := screenCells(a.renderScreen())
	base := *a
	for _, o := range mouseOverlays {
		o.close(&base)
	}
	without := screenCells(base.renderScreen())

	minX, minY, maxX, maxY := -1, -1, -1, -1
	for y := 1; y < len(with)-1; y++ {
		for x, c := range with[y] {
			if y < len(without) && x < len(without[y]) && without[y][x] == c {
				continue
			}
			if minX < 0 || x < minX {
				minX = x
			}
			if minY < 0 || y < minY {
				minY = y
			}
			maxX = max(maxX, x)
			maxY = max(maxY, y)
		}
	}
	if minX < 0 {
		return mouse.Rect{}
	}
	return mouse.Rect{X: minX, Y: minY, W: maxX - minX + 1, H: maxY - minY + 1}
}

// screenCells splits a rendered screen into rows of terminal cells with ANSI
// stripped. A wide rune fills two cells so columns match the terminal's.
func screenCells(screen string) [][]string {
	lines := strings.Split(ansi.Strip(screen), "\n")
	rows := make([][]string, len(lines))
	for i, l := range lines {
		for _, r := range l {
			rows[i] = append(rows[i], string(r))
			if runewidth.RuneWidth(r) == 2 {
				rows[i] = append(rows[i], "")
			}
		}
	}
	return rows
}
