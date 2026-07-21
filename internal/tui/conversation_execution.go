package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

const executionRailMaxItems = 8

type executionContext struct {
	Key    string
	Label  string
	Type   string
	Status string
	Agent  session.Subagent
}

type conversationViewState struct {
	Location      conversationLocation
	Inspector     conversationInspector
	SplitShow     bool
	SplitFocus    bool
	PreviewOnly   bool
	RightPaneMode int
	BlockCursor   int
	PreviewOffset int
	Filter        string
	BlockFilter   string
	LiveTail      bool
	BottomAlign   bool
}

type executionOriginVisibility struct {
	Raw          []session.Entry
	Visible      []session.Entry
	VisibleUUIDs map[string]bool
}

type executionRailState struct {
	Contexts         []executionContext
	ActiveKey        string
	CursorKey        string
	Focused          bool
	Saved            map[string]conversationViewState
	OriginVisibility map[string]executionOriginVisibility
}

func executionContextKey(path string) string {
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}

func (a *App) executionRailItemCount() int {
	if a.state != viewConversation || a.height < 12 || len(a.conv.execution.Contexts) == 0 {
		return 0
	}
	return min(len(a.conv.execution.Contexts), executionRailMaxItems, max(a.height-11, 1))
}

func (a *App) executionRailHeight() int {
	items := a.executionRailItemCount()
	if items == 0 {
		return 0
	}
	return 1 + items
}

func (a *App) conversationLayoutHeight() int {
	return max(a.height-a.executionRailHeight(), 10)
}

func (a *App) conversationContentHeight() int {
	return ContentHeight(a.conversationLayoutHeight())
}

func (a *App) initExecutionRail(agents []session.Subagent) {
	rootKey := executionContextKey(a.currentSess.FilePath)
	contexts := []executionContext{{Key: rootKey, Label: "main", Type: "session", Status: a.mainExecutionStatus()}}

	ordered := append([]session.Subagent(nil), agents...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Timestamp.Equal(ordered[j].Timestamp) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Timestamp.Before(ordered[j].Timestamp)
	})
	statuses := a.executionAgentStatuses(ordered)
	seen := map[string]bool{rootKey: true}
	for _, agent := range ordered {
		if agent.FilePath == "" || isSystemAgent(agent) {
			continue
		}
		if info, err := os.Stat(agent.FilePath); err != nil || info.IsDir() {
			continue
		}
		key := executionContextKey(agent.FilePath)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		label := agent.WorkflowLabel
		if label == "" {
			label = agent.ShortID
		}
		if label == "" {
			label = shortFlowID(agent.ID)
		}
		typ := agent.AgentType
		if typ == "" {
			typ = "agent"
		}
		contexts = append(contexts, executionContext{Key: key, Label: label, Type: typ, Status: statuses[agent.ID], Agent: agent})
	}

	rail := &a.conv.execution
	oldCursor := rail.CursorKey
	oldActive := rail.ActiveKey
	oldSaved := rail.Saved
	rail.Contexts = contexts
	// Context discovery is the cache boundary: refresh/rebuild may observe
	// rewritten transcript files, while ordinary pinned navigation reuses the
	// visibility index without touching disk.
	rail.OriginVisibility = nil
	if oldSaved == nil {
		oldSaved = make(map[string]conversationViewState)
	}
	rail.Saved = oldSaved
	if a.executionContextByKey(oldActive).Key != "" {
		rail.ActiveKey = oldActive
	} else {
		rail.ActiveKey = rootKey
	}
	if a.executionContextByKey(oldCursor).Key != "" {
		rail.CursorKey = oldCursor
	} else {
		rail.CursorKey = rail.ActiveKey
	}
}

func (a *App) mainExecutionStatus() string {
	switch {
	case a.currentSess.IsResponding:
		return "running"
	case a.currentSess.IsLive:
		return "live"
	default:
		return "idle"
	}
}

func (a *App) executionAgentStatuses(agents []session.Subagent) map[string]string {
	statuses := make(map[string]string)
	var runs []session.WorkflowRun
	if a.conv.flow != nil {
		runs = a.conv.flow.Workflows()
	}
	for _, run := range runs {
		for _, agent := range run.Agents {
			if agent.AgentID != "" {
				statuses[agent.AgentID] = workflowAgentStatus(agent.State)
			}
		}
	}
	for _, agent := range agents {
		if statuses[agent.ID] == "" {
			statuses[agent.ID] = "unknown"
		}
	}
	return statuses
}

func (a *App) executionContextByKey(key string) executionContext {
	for _, context := range a.conv.execution.Contexts {
		if context.Key == key {
			return context
		}
	}
	return executionContext{}
}

func (a *App) executionContextForTranscript(path string) (executionContext, bool) {
	key := executionContextKey(path)
	context := a.executionContextByKey(key)
	return context, context.Key != ""
}

func (a *App) executionCursorIndex() int {
	for i, context := range a.conv.execution.Contexts {
		if context.Key == a.conv.execution.CursorKey {
			return i
		}
	}
	return 0
}

func (a *App) saveExecutionViewState() {
	rail := &a.conv.execution
	if rail.ActiveKey == "" {
		return
	}
	blockCursor := -1
	if a.conv.split.Folds != nil {
		blockCursor = a.conv.split.Folds.BlockCursor
	}
	filter := a.convList.FilterInput.Value()
	blockFilter := ""
	if a.conv.split.Folds != nil {
		blockFilter = a.conv.split.Folds.BlockFilter
	}
	rail.Saved[rail.ActiveKey] = conversationViewState{
		Location:      a.currentConversationLocation(),
		Inspector:     cloneConversationInspector(a.conv.inspector),
		SplitShow:     a.conv.split.Show,
		SplitFocus:    a.conv.split.Focus,
		PreviewOnly:   a.conv.split.PreviewOnly,
		RightPaneMode: a.conv.rightPaneMode,
		BlockCursor:   blockCursor,
		PreviewOffset: a.conv.split.Preview.YOffset,
		Filter:        filter,
		BlockFilter:   blockFilter,
		LiveTail:      a.liveTail,
		BottomAlign:   a.conv.split.BottomAlign,
	}
}

func (a *App) restoreExecutionViewState(key string, isMain bool) {
	state, ok := a.conv.execution.Saved[key]
	if !ok {
		a.conv.inspector = conversationInspector{Scope: session.ScopeNode}
		a.conv.split.Show = true
		a.conv.split.Focus = false
		a.conv.split.PreviewOnly = false
		a.conv.split.BottomAlign = false
		a.conv.rightPaneMode = previewText
		a.liveTail = false
		if a.conv.split.Folds != nil {
			a.conv.split.Folds.BlockFilter = ""
			a.conv.split.Folds.BlockVisible = nil
		}
		if isMain && len(a.conv.contextItems) > 0 {
			a.selectConvContext(0)
		} else if len(a.convList.VisibleItems()) > 0 {
			a.selectConvBody(0)
		}
		a.updateConvPreview()
		return
	}

	a.conv.inspector = cloneConversationInspector(state.Inspector)
	a.conv.split.Show = state.SplitShow
	a.conv.split.Focus = state.SplitFocus
	a.conv.split.PreviewOnly = state.PreviewOnly
	a.conv.rightPaneMode = state.RightPaneMode
	a.liveTail = state.LiveTail
	a.conv.split.BottomAlign = state.BottomAlign
	if a.conv.split.Folds != nil {
		a.conv.split.Folds.BlockFilter = state.BlockFilter
		a.conv.split.Folds.BlockVisible = nil
	}
	if state.Filter != "" {
		applyListFilter(&a.convList, state.Filter)
	}
	a.restoreConversationLocation(state.Location)
	a.conv.split.CacheKey = ""
	a.updateConvPreview()
	if a.conv.split.Folds != nil && state.BlockCursor >= 0 && state.BlockCursor < len(a.conv.split.Folds.Entry.Content) {
		a.conv.split.Folds.BlockCursor = state.BlockCursor
		a.conv.split.RefreshFoldCursor(a.width, a.splitRatio)
	}
	maxOffset := max(a.conv.split.Preview.TotalLineCount()-a.conv.split.Preview.Height, 0)
	a.conv.split.Preview.YOffset = min(max(state.PreviewOffset, 0), maxOffset)
}

func (a *App) activateExecutionContext(key string, restore bool) bool {
	context := a.executionContextByKey(key)
	if context.Key == "" {
		a.copiedMsg = "Execution context is unavailable"
		return false
	}
	if context.Key == a.conv.execution.ActiveKey {
		a.conv.execution.CursorKey = key
		return true
	}

	entries, err := session.LoadMessages(context.Key)
	if err != nil || len(entries) == 0 {
		a.copiedMsg = "No messages in execution context"
		return false
	}
	if context.Agent.ID != "" {
		entries = filterAgentContextEntries(entries)
		if context.Agent.AgentType == "aside_question" {
			entries = filterSideQuestionContext(entries)
		}
	}
	if len(entries) == 0 {
		a.copiedMsg = "No messages in execution context"
		return false
	}

	a.saveExecutionViewState()
	// FoldState is shared by the split pane, so clear context-local transient
	// state before rendering the destination. restoreExecutionViewState reapplies
	// the destination values after the new entry is selected.
	a.liveTail = false
	a.conv.split.BottomAlign = false
	if a.conv.split.Folds != nil {
		a.conv.split.Folds.BlockFilter = ""
		a.conv.split.Folds.BlockVisible = nil
	}
	merged := filterConversation(mergeConversationTurns(entries))
	visibleSess := a.currentSess
	var timelineTasks []session.TaskItem
	var timelineCrons []session.CronItem
	if context.Agent.ID == "" {
		timelineTasks = a.currentSess.Tasks
		timelineCrons = a.currentSess.Crons
	} else {
		visibleSess.ID = context.Agent.ID
		visibleSess.FilePath = context.Agent.FilePath
	}

	a.conv.sess = visibleSess
	a.conv.messages = entries
	a.conv.merged = merged
	a.conv.items = buildConvItems(visibleSess, merged, a.conv.agents, timelineTasks, timelineCrons, a.conv.flow)
	a.conv.contextItems = buildConvContextItems(visibleSess, merged, a.conv.flow)
	a.conv.contextIndex = 0
	a.conv.contextActive = len(a.conv.contextItems) > 0
	a.conv.agent = context.Agent
	a.conv.task = session.TaskItem{}
	a.conv.cron = session.CronItem{}
	a.conv.toolUseToAgent = buildToolUseToAgentMap(entries)
	a.conv.execution.ActiveKey = context.Key
	a.conv.execution.CursorKey = context.Key
	a.conv.execution.Focused = false
	a.rebuildConversationList(0)
	if restore {
		a.restoreExecutionViewState(context.Key, context.Agent.ID == "")
	} else {
		a.conv.inspector = conversationInspector{Scope: session.ScopeNode}
		a.conv.split.Show = true
		a.conv.split.Focus = false
		a.conv.split.PreviewOnly = false
		if context.Agent.ID == "" && len(a.conv.contextItems) > 0 {
			a.selectConvContext(0)
		} else if len(a.convList.VisibleItems()) > 0 {
			a.selectConvBody(0)
		}
		a.updateConvPreview()
	}
	return true
}

func (a *App) focusExecutionRail() {
	a.conv.execution.Focused = true
	a.conv.execution.CursorKey = a.conv.execution.ActiveKey
}

func (a *App) handleExecutionRailKey(key string) bool {
	rail := &a.conv.execution
	if !rail.Focused {
		return false
	}
	contexts := rail.Contexts
	if len(contexts) == 0 {
		rail.Focused = false
		return true
	}
	idx := a.executionCursorIndex()
	switch key {
	case a.keymap.Conversation.ExecutionContexts, "esc", "left":
		rail.Focused = false
	case "home":
		rail.CursorKey = contexts[0].Key
	case "end":
		rail.CursorKey = contexts[len(contexts)-1].Key
	case "up", "k":
		if idx > 0 {
			rail.CursorKey = contexts[idx-1].Key
		}
	case "down", "j":
		if idx+1 < len(contexts) {
			rail.CursorKey = contexts[idx+1].Key
		}
	case "enter", "right":
		rail.Focused = false
		a.activateExecutionContext(rail.CursorKey, true)
	default:
		return false
	}
	return true
}

func executionStatusGlyph(status string) string {
	switch status {
	case "running":
		return taskInProgressStyle.Render("●")
	case "completed", "live":
		return taskDoneStyle.Render("●")
	case "stopped":
		return errorStyle.Render("●")
	default:
		return dimStyle.Render("○")
	}
}

func (a *App) executionRailCells() []string {
	cells := make([]string, 0, len(a.conv.execution.Contexts))
	labelWidth := max(a.width-5, 8)
	for _, context := range a.conv.execution.Contexts {
		label := context.Status + " · " + context.Type + " · " + context.Label
		if context.Agent.ID != "" && context.Agent.FirstPrompt != "" {
			prompt := strings.ReplaceAll(context.Agent.FirstPrompt, "\n", " ")
			label += " · " + prompt
		}
		plain, _ := truncateExact(label, labelWidth)
		prefix := "  "
		if context.Key == a.conv.execution.CursorKey && a.conv.execution.Focused {
			prefix = "> "
		}
		cell := prefix + executionStatusGlyph(context.Status) + " " + plain
		if context.Key == a.conv.execution.ActiveKey {
			cell = lipgloss.NewStyle().Foreground(colorBorderFocused).Bold(true).Render(cell)
		} else if context.Key != a.conv.execution.CursorKey {
			cell = dimStyle.Render(cell)
		}
		cells = append(cells, cell)
	}
	return cells
}

func (a *App) executionRailWindow() (int, int) {
	count := a.executionRailItemCount()
	if count == 0 {
		return 0, 0
	}
	cursor := a.executionCursorIndex()
	start := min(max(cursor-count+1, 0), max(len(a.conv.execution.Contexts)-count, 0))
	return start, min(start+count, len(a.conv.execution.Contexts))
}

func (a *App) renderExecutionRail() string {
	if a.executionRailHeight() == 0 {
		return ""
	}
	focus := ""
	if a.conv.execution.Focused {
		focus = " · focused"
	}
	header := convRegionHeader("EXECUTION CONTEXTS"+focus, a.conv.execution.Focused, a.width)
	cells := a.executionRailCells()
	start, end := a.executionRailWindow()
	return header + "\n" + strings.Join(cells[start:end], "\n")
}

func (a *App) executionRailTop() int {
	return 1 + a.conversationContentHeight()
}

func (a *App) mouseInExecutionRail(screenY int) bool {
	height := a.executionRailHeight()
	return height > 0 && screenY >= a.executionRailTop() && screenY < a.executionRailTop()+height
}

func (a *App) executionContextAtY(screenY int) (string, bool) {
	row := screenY - a.executionRailTop() - 1
	start, end := a.executionRailWindow()
	idx := start + row
	if row < 0 || idx < start || idx >= end {
		return "", false
	}
	return a.conv.execution.Contexts[idx].Key, true
}

func (a *App) refreshExecutionRail() {
	active := a.conv.execution.ActiveKey
	a.initExecutionRail(a.conv.agents)
	desired := active
	if a.executionContextByKey(desired).Key == "" {
		desired = executionContextKey(a.currentSess.FilePath)
	}
	if executionContextKey(a.conv.sess.FilePath) != desired {
		a.conv.execution.ActiveKey = ""
		a.activateExecutionContext(desired, true)
		return
	}
	a.conv.execution.ActiveKey = desired
	if a.executionContextByKey(a.conv.execution.CursorKey).Key == "" {
		a.conv.execution.CursorKey = desired
	}
}
