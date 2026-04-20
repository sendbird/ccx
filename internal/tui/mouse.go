package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

const doubleClickThreshold = 400 * time.Millisecond

const mouseScrollLines = 1

func (a *App) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// In copy mode, only allow scroll on the detail viewport
	if a.copyModeActive {
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			if vp := a.activeDetailVP(); vp != nil {
				mouseScrollVP(vp, msg.Button == tea.MouseButtonWheelUp)
			}
		}
		return a, nil
	}

	// Handle drag-to-resize: motion while dragging
	if a.dragResizing && msg.Action == tea.MouseActionMotion {
		return a.handleDragResize(msg)
	}
	// End drag on release
	if a.dragResizing && msg.Action == tea.MouseActionRelease {
		a.dragResizing = false
		return a, nil
	}

	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		return a.handleMouseScroll(msg)
	case tea.MouseButtonLeft:
		switch msg.Action {
		case tea.MouseActionPress:
			// Check if clicking on the split border to start resize
			if a.tryStartDragResize(msg) {
				return a, nil
			}
			return a.handleMouseClick(msg)
		}
	}
	return a, nil
}

// tryStartDragResize checks if the click is on the split pane border and starts drag.
func (a *App) tryStartDragResize(msg tea.MouseMsg) bool {
	sp := a.activeSplitPane()
	if sp == nil || !sp.Show {
		return false
	}
	borderX := sp.ListWidth(a.width, a.splitRatio)
	// Allow 1px tolerance on each side of the border
	if msg.X >= borderX-1 && msg.X <= borderX+1 {
		a.dragResizing = true
		return true
	}
	return false
}

// handleDragResize updates the split ratio based on mouse X position during drag.
func (a *App) handleDragResize(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if a.width <= 0 {
		return a, nil
	}
	// Clamp to 20%-80% range
	newRatio := msg.X * 100 / a.width
	if newRatio < 20 {
		newRatio = 20
	}
	if newRatio > 80 {
		newRatio = 80
	}
	if newRatio != a.splitRatio {
		a.splitRatio = newRatio
		sp := a.activeSplitPane()
		if sp != nil {
			sp.Resize(a.width, a.height, a.splitRatio)
		}
	}
	return a, nil
}

// activeSplitPane returns the split pane for the current view state.
func (a *App) activeSplitPane() *SplitPane {
	switch a.state {
	case viewSessions:
		return &a.sessSplit
	case viewConversation:
		return &a.conv.split
	case viewConfig:
		return &a.cfgSplit
	case viewPlugins:
		if a.plgDetailActive {
			return &a.plgDetailSplit
		}
		return &a.plgSplit
	default:
		return nil
	}
}

func (a *App) handleMouseScroll(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	up := msg.Button == tea.MouseButtonWheelUp
	sp := a.activeSplitPane()
	scrolledPreview := sp != nil && sp.Show && msg.X > sp.ListWidth(a.width, a.splitRatio)

	switch a.state {
	case viewSessions:
		// Live preview: no local scroll, use J/enter to jump to pane for scrollback
		if scrolledPreview && a.sessPreviewMode == sessPreviewLive && a.paneProxy != nil {
			return a, nil
		}
		a.sessSplit.HandleMouseScroll(msg.X, up, a.width, a.splitRatio)
		if scrolledPreview {
			a.sessPreviewPinned = !a.sessPreviewAtBottom()
		} else {
			a.updateSessionPreview()
		}

	case viewConversation:
		// If tooltip is visible and scrolling on list side, scroll tooltip instead
		if !scrolledPreview && a.convTooltipOn && !a.conv.split.Focus && a.convTooltip() != "" {
			if up {
				a.convTooltipScroll = max(a.convTooltipScroll-3, 0)
			} else {
				a.convTooltipScroll += 3
			}
			return a, nil
		}
		a.conv.split.HandleMouseScroll(msg.X, up, a.width, a.splitRatio)
		if !scrolledPreview && a.conv.split.Show {
			a.updateConvPreview()
		}

	case viewMessageFull:
		mouseScrollVP(&a.msgFull.vp, up)

	case viewConfig:
		a.cfgSplit.HandleMouseScroll(msg.X, up, a.width, a.splitRatio)
		if !scrolledPreview {
			a.updateConfigPreview()
		}

	case viewPlugins:
		if a.plgDetailActive {
			a.plgDetailSplit.HandleMouseScroll(msg.X, up, a.width, a.splitRatio)
			if !scrolledPreview {
				a.updatePluginDetailPreview()
			}
		} else {
			a.plgSplit.HandleMouseScroll(msg.X, up, a.width, a.splitRatio)
			if !scrolledPreview {
				a.updatePluginPreview()
			}
		}
	}

	// Re-render fold preview after scroll moved the block cursor
	if scrolledPreview && sp != nil && sp.Folds != nil && sp.Focus {
		return a, a.refreshActivePreview()
	}

	return a, nil
}

func (a *App) handleMouseClick(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Title bar click: navigate via breadcrumb
	if msg.Y == 0 {
		return a.handleBreadcrumbClick(msg.X)
	}
	// Ignore help bar
	if msg.Y >= a.height-1 {
		return a, nil
	}

	// Double-click detection: same Y within threshold
	now := time.Now()
	isDoubleClick := msg.Y == a.lastClickY && now.Sub(a.lastClickTime) < doubleClickThreshold
	a.lastClickTime = now
	a.lastClickY = msg.Y

	if isDoubleClick {
		// Double-click in preview → toggle fold
		sp := a.activeSplitPane()
		if sp != nil && sp.HandleMouseDoubleClick(msg.X, a.width, a.splitRatio) {
			return a, a.refreshActivePreview()
		}
		// Double-click in list → enter
		enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
		return a.Update(enterMsg)
	}

	contentY := msg.Y - 1 // adjust for title bar
	sp := a.activeSplitPane()
	clickedPreview := sp != nil && sp.Show && msg.X > sp.ListWidth(a.width, a.splitRatio)

	var previewCmd tea.Cmd
	switch a.state {
	case viewSessions:
		a.sessSplit.HandleMouseClick(msg.X, contentY, a.width, a.splitRatio)
		previewCmd = a.updateSessionPreview()

	case viewConversation:
		a.conv.split.HandleMouseClick(msg.X, contentY, a.width, a.splitRatio)
		if a.conv.split.Show {
			a.updateConvPreview()
		}

	case viewConfig:
		a.cfgSplit.HandleMouseClick(msg.X, contentY, a.width, a.splitRatio)
		a.updateConfigPreview()

	case viewPlugins:
		if a.plgDetailActive {
			a.plgDetailSplit.HandleMouseClick(msg.X, contentY, a.width, a.splitRatio)
			a.updatePluginDetailPreview()
		} else {
			a.plgSplit.HandleMouseClick(msg.X, contentY, a.width, a.splitRatio)
			a.updatePluginPreview()
		}
	}

	// Re-render fold preview after clicking in preview to update block cursor highlight
	if clickedPreview && sp != nil && sp.Folds != nil {
		if c := a.refreshActivePreview(); c != nil {
			previewCmd = c
		}
	}

	return a, previewCmd
}

// mouseScrollVP scrolls a viewport by mouseScrollLines.
func mouseScrollVP(vp *viewport.Model, up bool) {
	if up {
		vp.ScrollUp(mouseScrollLines)
	} else {
		vp.ScrollDown(mouseScrollLines)
	}
}

// mouseScrollList scrolls a list by simulating up/down key presses.
// This correctly handles filtering, pagination, and all list states.
func mouseScrollList(l *list.Model, up bool) {
	keyType := tea.KeyDown
	if up {
		keyType = tea.KeyUp
	}
	msg := tea.KeyMsg{Type: keyType}
	for i := 0; i < mouseScrollLines; i++ {
		*l, _ = l.Update(msg)
	}
}

// mouseClickList selects the list item at the given content Y position.
// All list chrome (status bar, filter bar, pagination) is disabled,
// so items start at Y=0 within the list widget.
func mouseClickList(l *list.Model, contentY int, itemHeight int) {
	if contentY < 0 || itemHeight < 1 {
		return
	}

	visible := l.VisibleItems()
	if len(visible) == 0 {
		return
	}

	clickOffset := contentY / itemHeight
	pageStart := l.Paginator.Page * l.Paginator.PerPage
	target := pageStart + clickOffset
	if target >= 0 && target < len(visible) {
		l.Select(target)
	}
}
