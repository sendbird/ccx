package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

type conversationRegion int

const (
	conversationRegionPinned conversationRegion = iota
	conversationRegionTimeline
)

type conversationLocation struct {
	Region conversationRegion
	ItemID string
	Index  int
}

// inspectorNavFrame stores one reversible inspector transition. Stable item
// identity is used instead of raw list indices so refreshes and filtering do not
// silently restore a different row.
type inspectorNavFrame struct {
	location       conversationLocation
	splitShow      bool
	splitFocus     bool
	previewOnly    bool
	tab            inspectorTab
	scope          session.Scope
	zoom           bool
	zoomPrevFocus  bool
	explicitTab    inspectorTab
	explicitNodeID string
	explicit       bool
	blockCursor    int
	previewOffset  int
	metaDrill      string
	rightPaneMode  int
}

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
	blockCursor      int
	previewOffset    int
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

// selectConvBody selects an item by its visible index. bubbles/list keeps
// selection indices in filtered-list coordinates, so callers must never pass an
// index from Items() while a filter is active.
func (a *App) selectConvBody(index int) bool {
	items := a.convList.VisibleItems()
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
	for i, raw := range a.convList.VisibleItems() {
		if item, ok := raw.(convItem); ok && convItemID(item) == id {
			return a.selectConvBody(i)
		}
	}
	return false
}

func (a *App) currentConversationLocation() conversationLocation {
	loc := conversationLocation{Region: conversationRegionTimeline, Index: a.convList.Index(), ItemID: a.selectedConversationItemID()}
	if a.conv.contextActive {
		loc.Region = conversationRegionPinned
		loc.Index = a.conv.contextIndex
	}
	return loc
}

func (a *App) restoreConversationLocation(loc conversationLocation) bool {
	if a.restoreConvSelection(loc.ItemID) {
		return true
	}
	if loc.Region == conversationRegionPinned && len(a.conv.contextItems) > 0 {
		return a.selectConvContext(min(max(loc.Index, 0), len(a.conv.contextItems)-1))
	}
	visible := a.convList.VisibleItems()
	if loc.Region == conversationRegionTimeline && len(visible) > 0 {
		return a.selectConvBody(min(max(loc.Index, 0), len(visible)-1))
	}
	if len(a.conv.contextItems) > 0 {
		return a.selectConvContext(0)
	}
	if len(visible) > 0 {
		return a.selectConvBody(0)
	}
	return false
}

func (a *App) captureInspectorNavFrame() inspectorNavFrame {
	sp := &a.conv.split
	frame := inspectorNavFrame{
		location:       a.currentConversationLocation(),
		splitShow:      sp.Show,
		splitFocus:     sp.Focus,
		previewOnly:    sp.PreviewOnly,
		tab:            a.conv.inspector.Tab,
		scope:          a.conv.inspector.Scope,
		zoom:           a.conv.inspector.Zoom,
		zoomPrevFocus:  a.conv.inspector.ZoomPrevFocus,
		explicitTab:    a.conv.inspector.ExplicitTab,
		explicitNodeID: a.conv.inspector.ExplicitNodeID,
		explicit:       a.conv.inspector.Explicit,
		blockCursor:    -1,
		previewOffset:  sp.Preview.YOffset,
		metaDrill:      a.conv.inspector.MetaDrill,
		rightPaneMode:  a.conv.rightPaneMode,
	}
	if sp.Folds != nil {
		frame.blockCursor = sp.Folds.BlockCursor
	}
	return frame
}

func (a *App) pushInspectorHistory() {
	frame := a.captureInspectorNavFrame()
	a.conv.inspector.History = append(a.conv.inspector.History, frame)
	a.conv.inspector.ReturnToID = frame.location.ItemID
}

func (a *App) restoreInspectorFrame(frame inspectorNavFrame) {
	sp := &a.conv.split
	a.conv.inspector.Tab = frame.tab
	a.conv.inspector.Scope = frame.scope
	a.conv.inspector.Zoom = frame.zoom
	a.conv.inspector.ZoomPrevFocus = frame.zoomPrevFocus
	a.conv.inspector.ExplicitTab = frame.explicitTab
	a.conv.inspector.ExplicitNodeID = frame.explicitNodeID
	a.conv.inspector.Explicit = frame.explicit
	a.conv.inspector.MetaDrill = frame.metaDrill
	a.conv.rightPaneMode = frame.rightPaneMode
	sp.Show = frame.splitShow
	sp.Focus = frame.splitFocus
	sp.PreviewOnly = frame.previewOnly
	sp.CacheKey = ""
	a.restoreConversationLocation(frame.location)
	a.updateConvPreview()
	if sp.Folds != nil && frame.blockCursor >= 0 && frame.blockCursor < len(sp.Folds.Entry.Content) {
		sp.Folds.BlockCursor = frame.blockCursor
		sp.RefreshFoldCursor(a.width, a.splitRatio)
	}
	maxOffset := max(sp.Preview.TotalLineCount()-sp.Preview.Height, 0)
	sp.Preview.YOffset = min(max(frame.previewOffset, 0), maxOffset)
}

func (a *App) popInspectorHistory() bool {
	history := a.conv.inspector.History
	if len(history) == 0 {
		return false
	}
	frame := history[len(history)-1]
	history = history[:len(history)-1]
	a.conv.inspector.History = history
	a.restoreInspectorFrame(frame)
	if len(history) > 0 {
		a.conv.inspector.ReturnToID = history[len(history)-1].location.ItemID
	} else {
		a.conv.inspector.ReturnToID = ""
	}
	return true
}

func convRegionHeader(label string, active bool, width int) string {
	marker := "  "
	style := dimStyle
	if active {
		marker = "› "
		style = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	}
	text := marker + label + " "
	return style.Render(text + strings.Repeat("─", max(width-lipgloss.Width(text), 0)))
}

func (a *App) updateConvHeader() {
	sp := &a.conv.split
	if len(a.conv.contextItems) == 0 {
		sp.Header = ""
		sp.HeaderHeight = 0
		return
	}
	width := sp.ListWidth(a.width, a.splitRatio)
	lines := []string{convRegionHeader("PINNED", a.conv.contextActive, width)}
	for i, item := range a.conv.contextItems {
		var row strings.Builder
		renderConvSessionMeta(&row, item, a.conv.contextActive && i == a.conv.contextIndex, width, lipgloss.NewStyle().MaxWidth(width), "")
		lines = append(lines, row.String())
	}
	lines = append(lines, convRegionHeader("CONVERSATION", !a.conv.contextActive, width))
	sp.Header = strings.Join(lines, "\n")
	sp.HeaderHeight = len(lines)
}

func (a *App) convContextIndexAtHeaderLine(line int) (int, bool) {
	// Header line 0 is the PINNED label; the final line is CONVERSATION.
	index := line - 1
	return index, index >= 0 && index < len(a.conv.contextItems)
}

func (a *App) selectLastConvMessage() bool {
	items := a.convList.VisibleItems()
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
	body := len(a.convList.VisibleItems())
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

func cloneConversationInspector(src conversationInspector) conversationInspector {
	dst := src
	dst.History = append([]inspectorNavFrame(nil), src.History...)
	dst.MetaTargets = append([]metaEntryTarget(nil), src.MetaTargets...)
	return dst
}

func (a *App) resetDrilldownInspector() {
	a.conv.inspector = conversationInspector{Scope: session.ScopeNode}
	a.conv.split.Show = true
	a.conv.split.Focus = false
	a.conv.split.PreviewOnly = false
	a.conv.split.CacheKey = ""
}

func (a *App) clearInspectorHistory() {
	a.conv.inspector.History = nil
	a.conv.inspector.ReturnToID = ""
	a.conv.inspector.MetaDrill = ""
}

func (a *App) pushNavFrame() {
	blockCursor := -1
	if a.conv.split.Folds != nil {
		blockCursor = a.conv.split.Folds.BlockCursor
	}
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
		inspector:        cloneConversationInspector(a.conv.inspector),
		rightPaneMode:    a.conv.rightPaneMode,
		selectedID:       a.selectedConversationItemID(),
		agent:            a.conv.agent,
		task:             a.conv.task,
		cron:             a.conv.cron,
		splitShow:        a.conv.split.Show,
		splitFocus:       a.conv.split.Focus,
		splitPreviewOnly: a.conv.split.PreviewOnly,
		blockCursor:      blockCursor,
		previewOffset:    a.conv.split.Preview.YOffset,
	})
}

func (a *App) drillIntoAgentConversation(agent session.Subagent) (tea.Model, tea.Cmd) {
	depth := len(a.navStack)
	a.pushNavFrame()
	model, cmd := a.openAgentConversation(agent)
	app, ok := model.(*App)
	if !ok || app.conv.agent.FilePath == "" || app.conv.agent.FilePath != agent.FilePath {
		a.navStack = a.navStack[:depth]
	}
	return model, cmd
}

func (a *App) drillIntoTaskConversation(task session.TaskItem) (tea.Model, tea.Cmd) {
	depth := len(a.navStack)
	a.pushNavFrame()
	model, cmd := a.openTaskConversation(task)
	app, ok := model.(*App)
	if !ok || app.conv.task.ID == "" || app.conv.task.ID != task.ID {
		a.navStack = a.navStack[:depth]
	}
	return model, cmd
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
	a.conv.inspector = cloneConversationInspector(frame.inspector)
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
	if a.conv.split.Folds != nil && frame.blockCursor >= 0 && frame.blockCursor < len(a.conv.split.Folds.Entry.Content) {
		a.conv.split.Folds.BlockCursor = frame.blockCursor
		a.conv.split.RefreshFoldCursor(a.width, a.splitRatio)
	}
	maxOffset := max(a.conv.split.Preview.TotalLineCount()-a.conv.split.Preview.Height, 0)
	a.conv.split.Preview.YOffset = min(max(frame.previewOffset, 0), maxOffset)
	return a, nil
}
