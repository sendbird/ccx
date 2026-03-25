package tui

import (
	"io"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// convItemKind classifies conversation list items.
type convItemKind int

const (
	convMsg   convItemKind = iota // user/assistant message turn
	convTask                      // task item (under assistant message)
	convAgent                     // agent reference (under assistant message)
)

// convItem represents a single row in the conversation list.
type convItem struct {
	kind      convItemKind
	merged    mergedMsg          // for convMsg
	task      session.TaskItem   // for convTask
	agent     session.Subagent   // for convAgent
	indent    int                // 0=message, 1=sub-item
	folded    bool               // for expandable group headers (tasks/agents)
	parentIdx int                // index of parent message in items slice
	groupTag  string             // "tasks" or "agents" — for group header rows
	count     int                // number of items in group (for header display)
}

func (c convItem) FilterValue() string {
	switch c.kind {
	case convMsg:
		return entryFilterText(c.merged.entry)
	case convTask:
		return c.task.Subject + " " + c.task.Status
	case convAgent:
		return c.agent.FirstPrompt + " " + c.agent.ShortID + " " + c.agent.AgentType
	}
	return ""
}

// convDelegate renders conversation list items.
type convDelegate struct{}

func (d convDelegate) Height() int                             { return 1 }
func (d convDelegate) Spacing() int                            { return 0 }
func (d convDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d convDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	ci, ok := item.(convItem)
	if !ok {
		return
	}
	selected := index == m.Index()
	width := m.Width()
	clamp := lipgloss.NewStyle().MaxWidth(width)
	filterTerm := listFilterTerm(m)

	switch ci.kind {
	case convMsg:
		renderConvMsg(w, ci, selected, width, clamp, filterTerm)
	case convTask:
		renderConvTaskOrAgent(w, ci, selected, width, clamp, filterTerm)
	case convAgent:
		renderConvTaskOrAgent(w, ci, selected, width, clamp, filterTerm)
	}
}

