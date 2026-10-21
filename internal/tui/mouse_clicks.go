package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/mouse"
)

// mouseClickState remembers the previous press so a second press on the same
// item reads as a double click. now is injectable so tests do not depend on
// wall-clock timing; nil means time.Now.
type mouseClickState struct {
	clicks mouse.Clicks
	now    func() time.Time
}

// registerClick records a left press that just selected something and reports
// whether it completed a double click on the same item. Selection must have
// already happened: the key is derived from the post-click selection, so the
// first press of a pair can never match a stale target. A press that landed on
// no item (empty rows below a short list, a view without a list) clears the
// pair, so two clicks on empty space never read as Enter.
func (a *App) registerClick(msg tea.MouseMsg) bool {
	key, ok := a.clickTargetKey(msg)
	if !ok {
		a.clicks.clicks = mouse.Clicks{}
		return false
	}
	now := time.Now
	if a.clicks.now != nil {
		now = a.clicks.now
	}
	var double bool
	a.clicks.clicks, double = a.clicks.clicks.Press(key, now())
	return double
}

// clickTargetKey identifies the item under a press in a way that is stable
// across the two presses of a double click: view, pane side, and the item the
// press selected. Sessions use their ID; other lists fall back to the cursor
// index, which only a key press could change between the two presses, and a
// key press resets the pair (Update's KeyMsg arm). ok is false when
// the press hit no item.
func (a *App) clickTargetKey(msg tea.MouseMsg) (key string, ok bool) {
	sp := a.activeSplitPane()
	if sp == nil {
		return "", false
	}
	if sp.inPreview(msg.X, a.width, a.split) {
		if sp.Folds != nil && len(sp.Folds.BlockStarts) > 0 {
			return fmt.Sprintf("v%d:preview:block%d", a.state, sp.Folds.BlockCursor), true
		}
		return fmt.Sprintf("v%d:preview:line%d", a.state, sp.Preview.YOffset+msg.Y), true
	}
	row := msg.Y - 1 - sp.headerInset // same offsets selectByClick feeds the split pane
	if row < 0 {
		// Conversation header rows (context list) select without a list index.
		if a.state == viewConversation && msg.Y-1 < sp.headerInset {
			return fmt.Sprintf("v%d:header:%d", a.state, msg.Y), true
		}
		return "", false
	}
	if _, hit := listRowAt(sp.List, row, sp.ItemHeight); !hit {
		return "", false
	}
	if a.state == viewSessions {
		if sess, found := a.selectedSession(); found {
			return fmt.Sprintf("v%d:list:%s", a.state, sess.ID), true
		}
	}
	return fmt.Sprintf("v%d:list:%d", a.state, sp.List.Index()), true
}

// listRowAt maps a row inside a paginated one-or-more-lines-per-item list to
// the visible item index, reporting false for the empty rows below a short
// page. It is what mouseClickList selects with, shared so the click that
// selects and the check that it hit something cannot disagree.
func listRowAt(l *list.Model, contentY, itemHeight int) (int, bool) {
	if l == nil || contentY < 0 || itemHeight < 1 {
		return -1, false
	}
	total := len(l.VisibleItems())
	start := l.Paginator.Page * l.Paginator.PerPage
	n := min(l.Paginator.PerPage, total-start)
	if n <= 0 {
		return -1, false
	}
	heights := make([]int, n)
	for i := range heights {
		heights[i] = itemHeight
	}
	rect := mouse.Rect{W: 1, H: n * itemHeight}
	c := mouse.BlockHit(rect, 0, heights, tea.MouseMsg{X: 0, Y: contentY})
	if !c.HasItem {
		return -1, false
	}
	return start + c.Index, true
}

// activateClicked runs the app's Enter path for a double click, so the mouse
// does exactly what the key does. selectCmd is the async work the selecting
// click produced; it must not be dropped.
func (a *App) activateClicked(msg tea.MouseMsg, selectCmd tea.Cmd) (tea.Model, tea.Cmd) {
	sp := a.activeSplitPane()
	// Double-click in preview toggles the block fold.
	if sp != nil && sp.HandleMouseDoubleClick(msg.X, a.width, a.split) {
		return a, tea.Batch(selectCmd, a.refreshActivePreview())
	}
	// A zoomed preview has no list target behind it.
	if sp != nil && sp.PreviewOnly {
		return a, selectCmd
	}
	m, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return m, tea.Batch(selectCmd, cmd)
}
