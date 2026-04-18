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
	convMsg         convItemKind = iota // user/assistant message turn
	convTask                            // task item (under assistant message)
	convAgent                           // agent reference (under assistant message)
	convSessionMeta                     // session-level memory/tasks-plan shortcuts
)

// convItem represents a single row in the conversation list.
type convItem struct {
	kind        convItemKind
	merged      mergedMsg         // for convMsg
	task        session.TaskItem  // for convTask
	cron        session.CronItem  // for cron-related convTask rows
	agent       session.Subagent  // for convAgent
	agentStatus string            // "running", "completed", "stopped" for convAgent
	sessionMeta string            // "memory" or "tasksplan" for convSessionMeta
	bgTaskID    string            // background task ID for individual task op items
	indent      int               // 0=message, 1=sub-item
	folded      bool              // for expandable group headers (tasks/agents)
	parentIdx   int               // index of parent message in items slice
	groupTag    string            // "tasks", "agents", "bgjobs", "crons"
	count       int               // number of items in group (for header display)
	label       string            // optional compact label for tree items
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
		parts = append(parts, c.cron.ID, c.cron.Cron, c.cron.Prompt, c.cron.Status)
		if c.bgTaskID != "" {
			parts = append(parts, "is:bg", c.bgTaskID)
		} else if c.cron.ID != "" || c.groupTag == "crons" {
			parts = append(parts, "is:cron")
		} else {
			parts = append(parts, "is:task")
		}
	case convAgent:
		parts = append(parts, c.agent.FirstPrompt, c.agent.ShortID, c.agent.AgentType)
		parts = append(parts, "is:agent")
	case convSessionMeta:
		parts = append(parts, c.label)
		switch c.sessionMeta {
		case "memory":
			parts = append(parts, "memory", "todos", "is:memory")
		case "tasksplan":
			parts = append(parts, "tasks", "plan", "agents", "crons", "is:tasksplan", "is:plan")
		}
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
	case convSessionMeta:
		renderConvSessionMeta(w, ci, selected, width, clamp, filterTerm)
	}
}
