package tui

import (
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/mouse"
)

// mouseScrollLines is one wheel notch. It is the fleet-wide step (tuikit
// mouse.WheelRows) so ccx scrolls like every other keyboard-first TUI.
const mouseScrollLines = mouse.WheelRows

func (a *App) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// The split divider sees the event before selection: a press on it
	// starts a resize, and must never start a text selection or a click.
	if cmd, consumed := a.handleSplitMouse(msg); consumed {
		return a, cmd
	}
	// Drag selection sees the event next, so a drag inside an overlay or a
	// pane selects text instead of reaching the click handlers below.
	selCmd, consumed := a.handleSelectionMouse(msg)
	if consumed {
		return a, selCmd
	}
	// Overlays own the mouse while open: nothing below may see the event.
	if m, cmd, handled := a.handleOverlayMouse(msg); handled {
		return m, cmd
	}
	// In copy mode, only allow scroll on the focused inspector viewport.
	if a.copyModeActive {
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			if vp := a.activeDetailVP(); vp != nil {
				mouseScrollVP(vp, msg.Button == tea.MouseButtonWheelUp)
			}
		}
		return a, nil
	}

	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		return a.handleMouseScroll(msg)
	case tea.MouseButtonLeft:
		switch msg.Action {
		case tea.MouseActionPress:
			return a.handleMouseClick(msg)
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
		if a.cfgPluginsPage {
			if a.plgDetailActive {
				return &a.plgDetailSplit
			}
			return &a.plgSplit
		}
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
	if a.state == viewConversation && a.mouseInExecutionRail(msg.Y) {
		a.conv.execution.Focused = true
		key := "down"
		if up {
			key = "up"
		}
		for range mouseScrollLines {
			a.handleExecutionRailKey(key)
		}
		return a, nil
	}
	sp := a.activeSplitPane()
	scrolledPreview := sp != nil && sp.inPreview(msg.X, a.width, a.split)

	// previewCmd carries async preview work (the conversation transcript read)
	// out to the runtime; dropping it would leave the pane on "(loading…)".
	var previewCmd tea.Cmd

	switch a.state {
	case viewSessions:
		// Live preview: no local scroll, use J/enter to jump to pane for scrollback
		if scrolledPreview && a.sessPreviewMode == sessPreviewLive && a.paneProxy != nil {
			return a, nil
		}
		a.sessSplit.HandleMouseScroll(msg.X, up, a.width, a.split)
		if scrolledPreview {
			a.sessPreviewPinned = !a.sessPreviewAtBottom()
		} else {
			previewCmd = a.updateSessionPreview()
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
		a.conv.split.HandleMouseScroll(msg.X, up, a.width, a.split)
		if !scrolledPreview {
			a.conv.contextActive = false
			a.updateConvHeader()
		}
		if !scrolledPreview && a.conv.split.Show {
			a.updateConvPreview()
		}

	case viewConfig:
		if a.cfgPluginsPage {
			if a.plgDetailActive {
				a.plgDetailSplit.HandleMouseScroll(msg.X, up, a.width, a.split)
				if !scrolledPreview {
					a.updatePluginDetailPreview()
				}
			} else {
				a.plgSplit.HandleMouseScroll(msg.X, up, a.width, a.split)
				if !scrolledPreview {
					a.updatePluginPreview()
				}
			}
		} else {
			a.cfgSplit.HandleMouseScroll(msg.X, up, a.width, a.split)
			if !scrolledPreview {
				a.updateConfigPreview()
			}
		}

	case viewPlugins:
		if a.plgDetailActive {
			a.plgDetailSplit.HandleMouseScroll(msg.X, up, a.width, a.split)
			if !scrolledPreview {
				a.updatePluginDetailPreview()
			}
		} else {
			a.plgSplit.HandleMouseScroll(msg.X, up, a.width, a.split)
			if !scrolledPreview {
				a.updatePluginPreview()
			}
		}
	}

	// Re-render fold preview after scroll moved the block cursor
	if scrolledPreview && sp != nil && sp.Folds != nil && sp.Focus {
		return a, tea.Batch(previewCmd, a.refreshActivePreview())
	}

	return a, previewCmd
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

	if a.state == viewConversation && a.mouseInExecutionRail(msg.Y) {
		a.conv.execution.Focused = true
		// Clicking a context row activates it (switches the conversation) and
		// keeps the rail focused so further clicks/scrolling continue to browse.
		if key, ok := a.executionContextAtY(msg.Y); ok {
			a.moveExecutionCursor(key)
		}
		return a, nil
	}

	m, cmd := a.selectByClick(msg)
	if a.registerClick(msg) {
		return a.activateClicked(msg, cmd)
	}
	return m, cmd
}

// selectByClick is the single-click half of handleMouseClick: focus the pane
// under the pointer and select the row there. It never activates anything.
func (a *App) selectByClick(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	contentY := msg.Y - 1 // adjust for title bar
	sp := a.activeSplitPane()
	clickedPreview := sp != nil && sp.inPreview(msg.X, a.width, a.split)

	var previewCmd tea.Cmd
	switch a.state {
	case viewSessions:
		a.sessSplit.HandleMouseClick(msg.X, contentY, a.width, a.split)
		previewCmd = a.updateSessionPreview()

	case viewConversation:
		if !clickedPreview && contentY >= 0 && contentY < a.conv.split.headerInset {
			switch {
			case contentY == 0 && len(a.conv.contextItems) > 0:
				a.selectConvContext(min(max(a.conv.contextIndex, 0), len(a.conv.contextItems)-1))
			case contentY == len(a.conv.contextItems)+1 && len(a.convList.VisibleItems()) > 0:
				a.selectConvBody(min(max(a.convList.Index(), 0), len(a.convList.VisibleItems())-1))
			default:
				if index, ok := a.convContextIndexAtHeaderLine(contentY); ok {
					a.selectConvContext(index)
				}
			}
			a.conv.split.Focus = false
		} else {
			a.conv.split.HandleMouseClick(msg.X, contentY, a.width, a.split)
			if !clickedPreview {
				a.conv.contextActive = false
				a.updateConvHeader()
			}
		}
		if a.conv.split.Show {
			a.updateConvPreview()
		}

	case viewConfig:
		if a.cfgPluginsPage {
			if a.plgDetailActive {
				a.plgDetailSplit.HandleMouseClick(msg.X, contentY, a.width, a.split)
				a.updatePluginDetailPreview()
			} else {
				a.plgSplit.HandleMouseClick(msg.X, contentY, a.width, a.split)
				a.updatePluginPreview()
			}
		} else {
			a.cfgSplit.HandleMouseClick(msg.X, contentY, a.width, a.split)
			a.updateConfigPreview()
		}

	case viewPlugins:
		if a.plgDetailActive {
			a.plgDetailSplit.HandleMouseClick(msg.X, contentY, a.width, a.split)
			a.updatePluginDetailPreview()
		} else {
			a.plgSplit.HandleMouseClick(msg.X, contentY, a.width, a.split)
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
	if target, ok := listRowAt(l, contentY, itemHeight); ok {
		l.Select(target)
	}
}
