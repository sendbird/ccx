package tui

import (
	"io"
	"strings"

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
	kind        convItemKind
	merged      mergedMsg        // for convMsg
	task        session.TaskItem // for convTask
	agent       session.Subagent // for convAgent
	agentStatus string           // "running", "completed", "stopped" for convAgent
	bgTaskID    string           // background task ID for individual task op items
	indent      int              // 0=message, 1=sub-item
	folded      bool             // for expandable group headers (tasks/agents)
	parentIdx   int              // index of parent message in items slice
	groupTag    string           // "tasks" or "agents" — for group header rows
	count       int              // number of items in group (for header display)
	label       string           // optional compact label for tree items
}

func (c convItem) FilterValue() string {
	var parts []string
	if c.label != "" {
		parts = append(parts, c.label)
	}
	switch c.kind {
	case convMsg:
		parts = append(parts, entryFilterText(c.merged.entry))
	case convTask:
		parts = append(parts, c.task.Subject, c.task.Description, c.task.Status)
		if c.bgTaskID != "" {
			parts = append(parts, "is:bg", c.bgTaskID)
		} else {
			parts = append(parts, "is:task")
		}
	case convAgent:
		parts = append(parts, c.agent.FirstPrompt, c.agent.ShortID, c.agent.AgentType)
		parts = append(parts, "is:agent")
	}
	return strings.Join(parts, " ")
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
