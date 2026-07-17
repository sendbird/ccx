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
	convAgent                           // agent lifecycle node
	convSessionMeta                     // session-level memory/tasks-plan shortcuts
	convWorkflow                        // workflow run lifecycle node
	convPhase                           // workflow phase node
	convShell                           // monitor/background shell lifecycle node
	convDecision                        // decision marker
)

// convItem represents a single row in the unified conversation flow spine.
type convItem struct {
	kind        convItemKind
	merged      mergedMsg
	task        session.TaskItem
	cron        session.CronItem
	agent       session.Subagent
	agentStatus string
	workflow    session.WorkflowRun
	phase       session.WorkflowPhase
	shell       session.ShellJob
	decision    session.Artifact
	origin      session.FlowOrigin
	facets      session.FacetSummary
	aggregate   bool
	summaryOnly bool
	steering    bool
	sessionMeta string
	bgTaskID    string
	indent      int
	folded      bool
	parentIdx   int
	groupTag    string
	count       int
	label       string
}

// convItemID returns a stable identity string for a convItem.
func convItemID(c convItem) string {
	switch c.kind {
	case convMsg:
		if c.merged.entry.UUID != "" {
			return "msg:" + c.merged.entry.UUID
		}
	case convTask:
		if c.task.ID != "" {
			return "task:" + c.task.ID
		}
		if c.bgTaskID != "" {
			return "bgtask:" + c.bgTaskID
		}
		if c.cron.ID != "" {
			return "cron:" + c.cron.ID
		}
	case convAgent:
		if c.agent.ID != "" {
			return "agent:" + c.agent.ID
		}
		if c.agent.ShortID != "" {
			return "agent:" + c.agent.ShortID
		}
	case convWorkflow:
		return "workflow:" + c.workflow.RunID
	case convPhase:
		return "phase:" + c.workflow.RunID + ":" + c.label
	case convShell:
		return "shell:" + c.shell.ID
	case convDecision:
		return "decision:" + c.decision.ID
	case convSessionMeta:
		return "meta:" + c.sessionMeta
	}
	return "lbl:" + c.groupTag + ":" + c.label
}

func selectedConvItemID(l *list.Model) string {
	ci, ok := l.SelectedItem().(convItem)
	if !ok {
		return ""
	}
	return convItemID(ci)
}

func (c convItem) FilterValue() string {
	parts := make([]string, 0, 12)
	if c.label != "" {
		parts = append(parts, c.label)
	}
	switch c.kind {
	case convMsg:
		parts = append(parts, entryFilterText(c.merged.entry))
		if c.steering {
			parts = append(parts, "is:decision", "is:steering")
		}
	case convTask:
		parts = append(parts, c.task.Subject, c.task.Description, c.task.Status)
		parts = append(parts, c.cron.ID, c.cron.Cron, c.cron.Prompt, c.cron.Status)
		if c.bgTaskID != "" {
			parts = append(parts, "is:bg")
		} else if c.cron.ID != "" || c.groupTag == "crons" {
			parts = append(parts, "is:cron")
		} else {
			parts = append(parts, "is:task")
		}
	case convAgent:
		parts = append(parts, c.agent.FirstPrompt, c.agent.ShortID, c.agent.AgentType, c.agent.WorkflowLabel, "is:agent")
		if c.agent.WorkflowRunID != "" {
			parts = append(parts, "is:workflow")
		}
	case convWorkflow:
		parts = append(parts, c.workflow.Name, c.workflow.Status, c.workflow.Summary, "is:workflow")
	case convPhase:
		parts = append(parts, c.phase.Title, c.phase.Detail, "is:phase", "is:workflow")
	case convShell:
		parts = append(parts, c.shell.ToolName, c.shell.Command, c.shell.Description, c.shell.Status, "is:shell")
		if c.shell.ToolName == "Monitor" {
			parts = append(parts, "is:monitor")
		}
	case convDecision:
		parts = append(parts, "is:decision")
		if data, ok := c.decision.Data.(session.DecisionData); ok {
			parts = append(parts, string(data.Kind), data.Label)
		}
	case convSessionMeta:
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
	case convTask, convAgent:
		renderConvTaskOrAgent(w, ci, selected, width, clamp, filterTerm)
	case convWorkflow, convPhase, convShell, convDecision:
		renderConvFlowNode(w, ci, selected, width, clamp, filterTerm)
	case convSessionMeta:
		renderConvSessionMeta(w, ci, selected, width, clamp, filterTerm)
	}
}
