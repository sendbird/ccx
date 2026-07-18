package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// navFrame stores unified conversation state for agent/task/cron drill-down.
type navFrame struct {
	sess             session.Session
	messages         []session.Entry
	merged           []mergedMsg
	agents           []session.Subagent
	items            []convItem
	flow             *session.FlowIndex
	inspector        conversationInspector
	rightPaneMode    int
	listIdx          int
	agent            session.Subagent
	task             session.TaskItem
	cron             session.CronItem
	splitShow        bool
	splitFocus       bool
	splitPreviewOnly bool
}

func (a *App) pushNavFrame() {
	a.navStack = append(a.navStack, navFrame{
		sess:             a.conv.sess,
		messages:         a.conv.messages,
		merged:           a.conv.merged,
		agents:           a.conv.agents,
		items:            a.conv.items,
		flow:             a.conv.flow,
		inspector:        a.conv.inspector,
		rightPaneMode:    a.conv.rightPaneMode,
		listIdx:          a.convList.Index(),
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
	a.conv.items = frame.items
	a.conv.flow = frame.flow
	a.conv.inspector = frame.inspector
	a.conv.rightPaneMode = frame.rightPaneMode
	a.conv.agent = frame.agent
	a.conv.task = frame.task
	a.conv.cron = frame.cron
	a.conv.split.Show = frame.splitShow
	a.conv.split.Focus = frame.splitFocus
	a.conv.split.PreviewOnly = frame.splitPreviewOnly
	a.conv.split.CacheKey = ""
	a.rebuildConversationList(frame.listIdx)
	a.state = viewConversation
	a.updateConvPreview()
	return a, nil
}
