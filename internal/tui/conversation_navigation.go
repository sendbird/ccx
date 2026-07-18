package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// navFrame stores unified conversation state for agent/task/cron drill-down.
type navFrame struct {
	sess             session.Session
	messages         []session.Entry
	merged           []mergedMsg
	agents           []session.Subagent
	contextItems     []convItem
	contextIndex     int
	contextActive    bool
	items            []convItem
	flow             *session.FlowIndex
	toolUseToAgent   map[string]string
	inspector        conversationInspector
	rightPaneMode    int
	selectedID       string
	agent            session.Subagent
	task             session.TaskItem
	cron             session.CronItem
	splitShow        bool
	splitFocus       bool
	splitPreviewOnly bool
}

func (a *App) selectedConversationItem() (convItem, bool) {
	if a.conv.contextActive && a.conv.contextIndex >= 0 && a.conv.contextIndex < len(a.conv.contextItems) {
		return a.conv.contextItems[a.conv.contextIndex], true
	}
	item, ok := a.convList.SelectedItem().(convItem)
	return item, ok
}

func (a *App) selectedConversationItemID() string {
	item, ok := a.selectedConversationItem()
	if !ok {
		return ""
	}
	return convItemID(item)
}

func (a *App) selectConvContext(index int) bool {
	if index < 0 || index >= len(a.conv.contextItems) {
		return false
	}
	a.conv.contextIndex = index
	a.conv.contextActive = true
	a.updateConvHeader()
	return true
}

func (a *App) selectConvBody(index int) bool {
	items := a.convList.Items()
	if index < 0 || index >= len(items) {
		return false
	}
	a.conv.contextActive = false
	a.convList.Select(index)
	a.updateConvHeader()
	return true
}

func (a *App) restoreConvSelection(id string) bool {
	if id == "" {
		return false
	}
	for i, item := range a.conv.contextItems {
		if convItemID(item) == id {
			return a.selectConvContext(i)
		}
	}
	for i, raw := range a.convList.Items() {
		if item, ok := raw.(convItem); ok && convItemID(item) == id {
			return a.selectConvBody(i)
		}
	}
	return false
}

func (a *App) updateConvHeader() {
	sp := &a.conv.split
	if len(a.conv.contextItems) == 0 {
		sp.Header = ""
		sp.HeaderHeight = 0
		return
	}
	width := sp.ListWidth(a.width, a.splitRatio)
	var lines []string
	for i, item := range a.conv.contextItems {
		var row strings.Builder
		renderConvSessionMeta(&row, item, a.conv.contextActive && i == a.conv.contextIndex, width, lipgloss.NewStyle().MaxWidth(width), "")
		lines = append(lines, row.String())
	}
	lines = append(lines, dimStyle.Render(fmt.Sprintf("%s timeline", strings.Repeat("─", max(width-11, 1)))))
	sp.Header = strings.Join(lines, "\n")
	sp.HeaderHeight = len(lines)
}

func (a *App) selectLastConvMessage() bool {
	items := a.convList.Items()
	for i := len(items) - 1; i >= 0; i-- {
		if item, ok := items[i].(convItem); ok && item.kind == convMsg {
			return a.selectConvBody(i)
		}
	}
	return false
}

// handleConvListNavigation implements one selection sequence spanning fixed
// context rows and the paginated chronological body.
func (a *App) handleConvListNavigation(key string) bool {
	if a.convList.FilterState() == list.Filtering { // Filter input owns navigation keys.
		return false
	}
	contexts := len(a.conv.contextItems)
	body := len(a.convList.Items())
	switch key {
	case "home":
		if contexts > 0 {
			a.selectConvContext(0)
		} else if body > 0 {
			a.selectConvBody(0)
		}
		return true
	case "end":
		if body > 0 {
			a.selectConvBody(body - 1)
		} else if contexts > 0 {
			a.selectConvContext(contexts - 1)
		}
		return true
	case "down":
		if a.conv.contextActive {
			if a.conv.contextIndex+1 < contexts {
				a.selectConvContext(a.conv.contextIndex + 1)
			} else if body > 0 {
				a.selectConvBody(0)
			}
			return true
		}
	case "up":
		if !a.conv.contextActive && a.convList.Index() == 0 && contexts > 0 {
			a.selectConvContext(contexts - 1)
			return true
		}
		if a.conv.contextActive {
			if a.conv.contextIndex > 0 {
				a.selectConvContext(a.conv.contextIndex - 1)
			}
			return true
		}
	case "pgdown":
		if a.conv.contextActive {
			if body > 0 {
				a.selectConvBody(0)
			}
			return true
		}
	case "pgup":
		if a.conv.contextActive {
			a.selectConvContext(0)
			return true
		}
		if a.convList.Index() == 0 && contexts > 0 {
			a.selectConvContext(contexts - 1)
			return true
		}
	}
	return false
}

func (a *App) pushNavFrame() {
	a.navStack = append(a.navStack, navFrame{
		sess:             a.conv.sess,
		messages:         a.conv.messages,
		merged:           a.conv.merged,
		agents:           a.conv.agents,
		contextItems:     a.conv.contextItems,
		contextIndex:     a.conv.contextIndex,
		contextActive:    a.conv.contextActive,
		items:            a.conv.items,
		flow:             a.conv.flow,
		toolUseToAgent:   a.conv.toolUseToAgent,
		inspector:        a.conv.inspector,
		rightPaneMode:    a.conv.rightPaneMode,
		selectedID:       a.selectedConversationItemID(),
		agent:            a.conv.agent,
		task:             a.conv.task,
		cron:             a.conv.cron,
		splitShow:        a.conv.split.Show,
		splitFocus:       a.conv.split.Focus,
		splitPreviewOnly: a.conv.split.PreviewOnly,
	})
}

func (a *App) popNavFrame() (tea.Model, tea.Cmd) {
	if len(a.navStack) == 0 {
		a.conv.agent = session.Subagent{}
		a.conv.task = session.TaskItem{}
		a.conv.cron = session.CronItem{}
		a.state = viewSessions
		return a, nil
	}

	frame := a.navStack[len(a.navStack)-1]
	a.navStack = a.navStack[:len(a.navStack)-1]
	a.conv.sess = frame.sess
	a.conv.messages = frame.messages
	a.conv.merged = frame.merged
	a.conv.agents = frame.agents
	a.conv.contextItems = frame.contextItems
	a.conv.contextIndex = frame.contextIndex
	a.conv.contextActive = frame.contextActive
	a.conv.items = frame.items
	a.conv.flow = frame.flow
	a.conv.toolUseToAgent = frame.toolUseToAgent
	a.conv.inspector = frame.inspector
	a.conv.rightPaneMode = frame.rightPaneMode
	a.conv.agent = frame.agent
	a.conv.task = frame.task
	a.conv.cron = frame.cron
	a.conv.split.Show = frame.splitShow
	a.conv.split.Focus = frame.splitFocus
	a.conv.split.PreviewOnly = frame.splitPreviewOnly
	a.conv.split.CacheKey = ""
	a.rebuildConversationList(0)
	a.restoreConvSelection(frame.selectedID)
	a.state = viewConversation
	a.updateConvPreview()
	return a, nil
}
