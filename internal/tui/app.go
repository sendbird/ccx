package tui

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gavin-jeong/csb/internal/session"
)

type tickMsg time.Time
type liveTickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func liveTickCmd() tea.Cmd {
	return tea.Tick(1*time.Second, func(t time.Time) tea.Msg {
		return liveTickMsg(t)
	})
}

type viewState int

const (
	viewSessions viewState = iota
	viewMessages
	viewDetail
	viewAgents
	viewAgentMessages
	viewAgentDetail
	viewToolCalls
	viewMemory
)

type App struct {
	state  viewState
	width  int
	height int

	// Data
	sessions     []session.Session
	currentSess  session.Session
	messages     []session.Entry // all messages (unfiltered)
	agents       []session.Subagent
	currentAgent session.Subagent
	agentMsgs    []session.Entry
	toolCalls    []toolCallItem

	// List models (owned here, referenced by SplitPanes)
	sessionList  list.Model
	messageList  list.Model
	agentList    list.Model
	agentMsgList list.Model
	toolList     list.Model

	// Split panes (each holds a *list.Model pointer + viewport)
	sessSplit     SplitPane
	msgSplit      SplitPane
	agentSplit    SplitPane
	agentMsgSplit SplitPane
	toolSplit     SplitPane

	// Session-specific: pinned scroll state
	sessPreviewPinned bool

	// Detail viewports (full-screen, not split panes)
	detailVP         viewport.Model
	agentDetailVP    viewport.Model
	detailEntry      session.Entry
	agentDetailEntry session.Entry
	detailFrom       viewState

	// Message filtering & sorting
	msgFilter     filterMode
	filterPending bool // waiting for second key after 'f'
	msgReverse    bool // reverse sort (newest first)

	// Split pane ratio (list width as % of terminal width)
	splitRatio int

	// Content for clipboard/pager
	detailContent      string
	agentDetailContent string
	copiedMsg          string

	// Copy mode (detail view)
	copyModeActive bool
	copyLines      []string
	copyCursor     int
	copyAnchor     int

	// Live tracking
	lastMsgLoadTime time.Time
	lastMsgCount    int
	liveTail        bool // auto-scroll to latest message on tick
	detailLiveTrack bool // auto-update detail view to latest message

	// Memory view
	memoryVP   viewport.Model
	memoryFrom viewState
}

func NewApp(sessions []session.Session) *App {
	a := &App{
		state:      viewSessions,
		sessions:   sessions,
		splitRatio: 35,
		msgReverse: true,
	}
	a.sessSplit = SplitPane{List: &a.sessionList}
	a.msgSplit = SplitPane{List: &a.messageList, Show: true, Folds: &FoldState{}}
	a.agentSplit = SplitPane{List: &a.agentList, Show: true}
	a.agentMsgSplit = SplitPane{List: &a.agentMsgList, Show: true, Folds: &FoldState{}}
	a.toolSplit = SplitPane{List: &a.toolList, Folds: &FoldState{}}
	return a
}

func (a *App) Init() tea.Cmd {
	return tickCmd()
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.resizeAll()
		return a, nil

	case tickMsg:
		cmd := a.handleTick()
		return a, tea.Batch(cmd, tickCmd())

	case liveTickMsg:
		if a.liveTail || a.detailLiveTrack {
			a.handleLiveTail()
			return a, liveTickCmd()
		}
		return a, nil

	case tea.MouseMsg:
		return a.handleMouse(msg)

	case tea.KeyMsg:
		a.copiedMsg = ""
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}

		if a.isFiltering() {
			return a.updateActiveList(msg)
		}

		switch a.state {
		case viewSessions:
			return a.handleSessionKeys(msg)
		case viewMessages:
			return a.handleMessageKeys(msg)
		case viewDetail:
			return a.handleDetailKeys(msg)
		case viewAgents:
			return a.handleAgentKeys(msg)
		case viewAgentMessages:
			return a.handleAgentMsgKeys(msg)
		case viewAgentDetail:
			return a.handleAgentDetailKeys(msg)
		case viewToolCalls:
			return a.handleToolCallKeys(msg)
		case viewMemory:
			return a.handleMemoryKeys(msg)
		}
	}

	return a.updateActiveComponent(msg)
}

func (a *App) View() string {
	if a.width == 0 || a.height == 0 {
		return "Loading..."
	}

	var title, content, help string

	switch a.state {
	case viewSessions:
		title = titleStyle.Render("CSB — Claude Session Browser")
		content = a.renderSessionSplit()
		h := "↵open a:agents g:dir m:memory r:resume"
		if inTmux() {
			h += " J:jump"
		}
		if a.sessSplit.Show {
			h += " esc:close ←→:focus []:resize"
		} else {
			h += " →:preview"
		}
		help = helpStyle.Render("  " + h + " /:search q:quit")

	case viewMessages:
		title = a.breadcrumb("Sessions", a.currentSess.ShortID)
		content = a.renderMsgSplit(&a.msgSplit)
		badges := ""
		if fl := filterModeLabel(a.msgFilter); fl != "" {
			badges += fl + "  "
		}
		if a.liveTail {
			badges += liveBadge.Render(a.liveBadgeText()) + "  "
		}
		sortHint := "↑"
		if a.msgReverse {
			sortHint = "↓"
		}
		h := fmt.Sprintf("↵detail a:agents t:tools m:memory L:live S:sort(%s) !:filter(%s)", sortHint, filterModeShort(a.msgFilter))
		if inTmux() {
			h += " J:jump"
		}
		if a.msgSplit.Show {
			if a.msgSplit.Focus {
				h += " ↑↓:blocks ←→:fold f/F:all"
			} else {
				h += " →:preview"
			}
			h += " esc:close []:resize"
		} else {
			h += " →:preview"
		}
		help = badges + helpStyle.Render("  "+h+" /:search esc:back q:quit")

	case viewDetail:
		title = a.breadcrumb("Sessions", a.currentSess.ShortID, roleLabel(a.detailEntry))
		content = a.detailVP.View()
		pct := int(a.detailVP.ScrollPercent() * 100)
		detailBadge := ""
		if a.detailLiveTrack {
			detailBadge = liveBadge.Render(a.detailLiveBadgeText()) + "  "
		}
		if a.copyModeActive {
			help = detailBadge + helpStyle.Render(fmt.Sprintf("  ↑↓:move v/sp:select y/↵:copy home/end esc:cancel %d%%", pct))
		} else {
			help = detailBadge + helpStyle.Render(fmt.Sprintf("  ↑↓:scroll v:copy y:all o:pager esc:back q:quit %d%%", pct))
		}

	case viewAgents:
		title = a.breadcrumb("Sessions", a.currentSess.ShortID, "Agents")
		content = a.renderAgentSplit()
		if a.agentSplit.Show {
			help = helpStyle.Render("  ↵open esc:close ←→:focus []:resize /:search esc:back q:quit")
		} else {
			help = helpStyle.Render("  ↵open →:preview /:search esc:back q:quit")
		}

	case viewAgentMessages:
		title = a.breadcrumb("Sessions", a.currentSess.ShortID, "Agents", a.currentAgent.ShortID)
		content = a.renderMsgSplit(&a.agentMsgSplit)
		agentBadges := ""
		if a.liveTail {
			agentBadges = liveBadge.Render(a.liveBadgeText()) + "  "
		}
		h := "↵detail t:tools L:live"
		if inTmux() {
			h += " J:jump"
		}
		if a.agentMsgSplit.Show {
			if a.agentMsgSplit.Focus {
				h += " ↑↓:blocks ←→:fold f/F:all"
			} else {
				h += " →:preview"
			}
			h += " esc:close []:resize"
		} else {
			h += " →:preview"
		}
		help = agentBadges + helpStyle.Render("  "+h+" /:search esc:back q:quit")

	case viewAgentDetail:
		title = a.breadcrumb("Sessions", a.currentSess.ShortID, "Agents", a.currentAgent.ShortID, roleLabel(a.agentDetailEntry))
		content = a.agentDetailVP.View()
		pct := int(a.agentDetailVP.ScrollPercent() * 100)
		agentDetailBadge := ""
		if a.detailLiveTrack {
			agentDetailBadge = liveBadge.Render(a.detailLiveBadgeText()) + "  "
		}
		if a.copyModeActive {
			help = agentDetailBadge + helpStyle.Render(fmt.Sprintf("  ↑↓:move v/sp:select y/↵:copy home/end esc:cancel %d%%", pct))
		} else {
			help = agentDetailBadge + helpStyle.Render(fmt.Sprintf("  ↑↓:scroll v:copy y:all o:pager esc:back q:quit %d%%", pct))
		}

	case viewToolCalls:
		src := "Session"
		if a.currentAgent.ID != "" {
			src = "Agent " + a.currentAgent.ShortID
		}
		title = a.breadcrumb("Sessions", a.currentSess.ShortID, src, "Tools")
		content = a.renderMsgSplit(&a.toolSplit)
		h := "↵view"
		if a.toolSplit.Show {
			if a.toolSplit.Focus {
				h += " ↑↓:blocks ←→:fold f/F:all"
			} else {
				h += " →:preview"
			}
			h += " esc:close []:resize"
		} else {
			h += " →:preview"
		}
		help = helpStyle.Render("  " + h + " /:search esc:back q:quit")

	case viewMemory:
		src := "Sessions"
		if a.memoryFrom == viewMessages {
			src = a.currentSess.ShortID
		}
		title = a.breadcrumb(src, "Memory")
		content = a.memoryVP.View()
		pct := int(a.memoryVP.ScrollPercent() * 100)
		help = helpStyle.Render(fmt.Sprintf("  ↑↓:scroll esc:back q:quit %d%%", pct))
	}

	// Override help with search hints when filtering
	if a.isFiltering() {
		help = a.searchHints()
	}

	if a.copiedMsg != "" {
		help += "  " + lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(a.copiedMsg)
	}

	titleW := lipgloss.Width(title)
	if titleW < a.width {
		title += lipgloss.NewStyle().Background(colorPrimary).Render(strings.Repeat(" ", a.width-titleW))
	}

	return title + "\n" + content + "\n" + help
}

// --- Key handlers ---

func (a *App) handleSessionKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return a, tea.Quit
	case "esc":
		if a.sessSplit.Show {
			idx := a.sessionList.Index()
			a.sessSplit.Show = false
			a.sessSplit.Focus = false
			contentH := a.height - 3
			a.sessionList.SetSize(a.sessSplit.ListWidth(a.width, a.splitRatio), contentH)
			a.sessionList.Select(idx)
			return a, nil
		}
		return a, nil
	case "enter":
		item, ok := a.sessionList.SelectedItem().(sessionItem)
		if !ok {
			return a, nil
		}
		a.currentSess = item.sess
		return a, a.loadSessionMessages()
	case "a":
		item, ok := a.sessionList.SelectedItem().(sessionItem)
		if !ok {
			return a, nil
		}
		return a.openAgents(item.sess)
	case "g":
		item, ok := a.sessionList.SelectedItem().(sessionItem)
		if !ok {
			return a, nil
		}
		return a.openProjectDir(item.sess.ProjectPath)
	case "m":
		item, ok := a.sessionList.SelectedItem().(sessionItem)
		if !ok {
			return a, nil
		}
		return a.openMemory(item.sess, viewSessions)
	case "r":
		item, ok := a.sessionList.SelectedItem().(sessionItem)
		if !ok {
			return a, nil
		}
		return a.resumeSession(item.sess)
	case "J":
		item, ok := a.sessionList.SelectedItem().(sessionItem)
		if !ok {
			return a, nil
		}
		return a.jumpToTmuxPane(item.sess.ProjectPath)
	case "tab":
		if !a.sessSplit.Show {
			return a, nil
		}
		a.sessSplit.Focus = !a.sessSplit.Focus
		return a, nil
	case "left":
		if a.sessSplit.Focus && a.sessSplit.Show {
			a.sessSplit.Focus = false
		}
		return a, nil
	case "right":
		if !a.sessSplit.Show {
			idx := a.sessionList.Index()
			a.sessSplit.Show = true
			a.sessSplit.CacheKey = ""
			contentH := a.height - 3
			a.sessionList.SetSize(a.sessSplit.ListWidth(a.width, a.splitRatio), contentH)
			a.sessionList.Select(idx)
		}
		a.sessSplit.Focus = true
		return a, nil
	case "[":
		if a.sessSplit.Show {
			a.adjustSplitRatio(5)
		}
		return a, nil
	case "]":
		if a.sessSplit.Show {
			a.adjustSplitRatio(-5)
		}
		return a, nil
	}

	if a.sessSplit.Focus && a.sessSplit.Show {
		switch msg.String() {
		case "down", "up", "pgdown", "pgup", "home", "end":
			scrollPreview(&a.sessSplit.Preview, msg.String())
			// Pin if scrolled away from bottom, unpin if at bottom
			a.sessPreviewPinned = !a.sessPreviewAtBottom()
			return a, nil
		}
	}

	// Auto-trigger search on letter/digit keys
	if !a.sessSplit.Focus && isSearchTrigger(msg.String()) {
		cmd := startListSearch(&a.sessionList, msg.String())
		return a, cmd
	}

	// Page boundary: pgup on first page → first item, pgdown on last → last item
	if !a.sessSplit.Focus && listPageEdge(&a.sessionList, msg.String()) {
		a.updateSessionPreview()
		return a, nil
	}

	m, cmd := a.updateSessionList(msg)
	a.updateSessionPreview()
	return m, cmd
}

func (a *App) handleMessageKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sp := &a.msgSplit

	// Handle second key of filter sequence (f + u/a/t/s/g/k)
	if a.filterPending {
		a.filterPending = false
		target := filterNone
		switch msg.String() {
		case "u":
			target = filterUser
		case "a":
			target = filterAssistant
		case "t":
			target = filterToolCalls
		case "s":
			target = filterSummary
		case "g":
			target = filterAgents
		case "k":
			target = filterSkills
		default:
			a.copiedMsg = ""
			return a, nil
		}
		if a.msgFilter == target {
			a.msgFilter = filterNone // toggle off
		} else {
			a.msgFilter = target
		}
		a.applyMessageFilter()
		a.copiedMsg = filterModeTip(a.msgFilter)
		return a, nil
	}

	switch msg.String() {
	case "q":
		return a, tea.Quit
	case "esc":
		if sp.Show {
			idx := a.messageList.Index()
			sp.Show = false
			sp.Focus = false
			contentH := a.height - 3
			a.messageList.SetSize(sp.ListWidth(a.width, a.splitRatio), contentH)
			a.messageList.Select(idx)
			return a, nil
		}
		a.msgFilter = filterNone
		a.liveTail = false
		a.state = viewSessions
		return a, nil
	case "enter":
		item, ok := a.messageList.SelectedItem().(messageItem)
		if !ok {
			return a, nil
		}
		return a.openDetail(item.entry, viewMessages)
	case "a":
		return a.openAgents(a.currentSess)
	case "t":
		return a.openToolCalls(a.messages, "")
	case "g":
		return a.openProjectDir(a.currentSess.ProjectPath)
	case "m":
		return a.openMemory(a.currentSess, viewMessages)
	case "J":
		return a.jumpToTmuxPane(a.currentSess.ProjectPath)
	case "L":
		return a.toggleLiveTail()
	case "S":
		a.msgReverse = !a.msgReverse
		curIdx := a.messageList.Index()
		a.applyMessageFilter()
		// Flip cursor position to track the same logical position
		items := a.messageList.Items()
		newIdx := len(items) - 1 - curIdx
		if newIdx >= 0 && newIdx < len(items) {
			a.messageList.Select(newIdx)
		}
		if a.msgReverse {
			a.copiedMsg = "Sort: newest first"
		} else {
			a.copiedMsg = "Sort: oldest first"
		}
		return a, nil
	case "!":
		a.filterPending = true
		a.copiedMsg = "filter: u=user a=asst t=tools s=summary g=agents k=skills"
		return a, nil
	case "tab":
		if !sp.Show {
			return a, nil
		}
		sp.Focus = !sp.Focus
		if sp.Focus && a.liveTail {
			items := a.messageList.Items()
			if a.messageList.Index() >= len(items)-1 {
				a.snapListToEnd()
			} else {
				a.refreshMsgPreview(sp)
			}
		} else if sp.Folds != nil && sp.Folds.Collapsed != nil {
			a.refreshMsgPreview(sp)
		}
		return a, nil
	case "left":
		if !sp.Focus {
			return a, nil // no-op when list focused
		}
		// When preview focused, fall through to fold handling below
	case "right":
		if !sp.Focus {
			// Open and/or focus preview
			if !sp.Show {
				idx := a.messageList.Index()
				sp.Show = true
				sp.CacheKey = ""
				contentH := a.height - 3
				a.messageList.SetSize(sp.ListWidth(a.width, a.splitRatio), contentH)
				a.messageList.Select(idx)
			}
			sp.Focus = true
			if a.liveTail {
				items := a.messageList.Items()
				if a.messageList.Index() >= len(items)-1 {
					a.snapListToEnd()
				} else {
					a.refreshMsgPreview(sp)
				}
			} else {
				a.refreshMsgPreview(sp)
			}
			return a, nil
		}
		// When preview focused, fall through to fold handling below
	case "[":
		if sp.Show {
			a.adjustSplitRatio(5)
		}
		return a, nil
	case "]":
		if sp.Show {
			a.adjustSplitRatio(-5)
		}
		return a, nil
	}

	if sp.Focus && sp.Show {
		// Block cursor navigation (up/down/left/right/f/F) handled first
		if sp.Folds != nil {
			result := sp.Folds.HandleKey(msg.String())
			if result == foldHandled {
				a.refreshMsgPreview(sp)
				sp.ScrollToBlock()
				return a, nil
			}
			if result == foldSwitchToList {
				sp.Focus = false
				return a, nil
			}
		}
		if sp.HandlePreviewScroll(msg.String()) {
			return a, nil
		}
	}

	// Auto-trigger search on letter/digit keys (skip keys used for actions)
	if !sp.Focus && !a.filterPending && isSearchTrigger(msg.String()) {
		switch msg.String() {
		case "a", "t", "g", "p", "q", "r", "J", "L", "S":
			// handled above
		default:
			cmd := startListSearch(&a.messageList, msg.String())
			return a, cmd
		}
	}

	// Page boundary: pgup on first page → first item, pgdown on last → last item
	if !sp.Focus && listPageEdge(&a.messageList, msg.String()) {
		if sp.Show {
			a.updateMsgPreview(sp)
		}
		return a, nil
	}

	m, cmd := a.updateMessageList(msg)
	if sp.Show {
		a.updateMsgPreview(sp)
	}
	return m, cmd
}

func (a *App) handleDetailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.copyModeActive {
		return a.handleCopyModeKeys(msg)
	}
	switch msg.String() {
	case "q":
		return a, tea.Quit
	case "esc":
		wasTracking := a.detailLiveTrack
		a.detailLiveTrack = false
		switch a.detailFrom {
		case viewToolCalls:
			a.state = viewToolCalls
		case viewAgentMessages:
			a.state = viewAgentMessages
		default:
			a.state = viewMessages
			if wasTracking {
				a.applyMessageFilter()
				items := a.messageList.Items()
				if len(items) > 0 {
					a.messageList.Select(len(items) - 1)
				}
			}
		}
		return a, nil
	case "v":
		a.enterCopyMode()
		return a, nil
	case "y":
		copyToClipboard(renderPlainMessage(a.detailEntry))
		a.copiedMsg = "Copied!"
		return a, nil
	case "o":
		return a, openInPager(a.detailContent)
	}
	var cmd tea.Cmd
	a.detailVP, cmd = a.detailVP.Update(msg)
	return a, cmd
}

func (a *App) handleAgentKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return a, tea.Quit
	case "esc":
		if a.agentSplit.Show {
			idx := a.agentList.Index()
			a.agentSplit.Show = false
			a.agentSplit.Focus = false
			contentH := max(a.height-3, 1)
			a.agentList.SetSize(a.agentSplit.ListWidth(a.width, a.splitRatio), contentH)
			a.agentList.Select(idx)
			return a, nil
		}
		a.state = viewMessages
		return a, nil
	case "enter":
		item, ok := a.agentList.SelectedItem().(agentItem)
		if !ok {
			return a, nil
		}
		return a.openAgentMessages(item.agent)
	case "tab":
		if !a.agentSplit.Show {
			return a, nil
		}
		a.agentSplit.Focus = !a.agentSplit.Focus
		return a, nil
	case "left":
		if a.agentSplit.Focus && a.agentSplit.Show {
			a.agentSplit.Focus = false
		}
		return a, nil
	case "right":
		if !a.agentSplit.Show {
			idx := a.agentList.Index()
			a.agentSplit.Show = true
			a.agentSplit.CacheKey = ""
			contentH := max(a.height-3, 1)
			a.agentList.SetSize(a.agentSplit.ListWidth(a.width, a.splitRatio), contentH)
			a.agentList.Select(idx)
		}
		a.agentSplit.Focus = true
		return a, nil
	case "[":
		if a.agentSplit.Show {
			a.adjustSplitRatio(5)
		}
		return a, nil
	case "]":
		if a.agentSplit.Show {
			a.adjustSplitRatio(-5)
		}
		return a, nil
	}

	if a.agentSplit.Focus && a.agentSplit.Show {
		switch msg.String() {
		case "down", "up", "pgdown", "pgup", "home", "end":
			scrollPreview(&a.agentSplit.Preview, msg.String())
			return a, nil
		}
	}

	// Auto-trigger search on letter/digit keys
	if !a.agentSplit.Focus && isSearchTrigger(msg.String()) {
		switch msg.String() {
		case "p", "q":
			// handled above
		default:
			cmd := startListSearch(&a.agentList, msg.String())
			return a, cmd
		}
	}

	// Page boundary
	if !a.agentSplit.Focus && listPageEdge(&a.agentList, msg.String()) {
		if a.agentSplit.Show {
			a.updateAgentPreview()
		}
		return a, nil
	}

	m, cmd := a.updateAgentList(msg)
	if a.agentSplit.Show {
		a.updateAgentPreview()
	}
	return m, cmd
}

func (a *App) handleAgentMsgKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sp := &a.agentMsgSplit

	switch msg.String() {
	case "q":
		return a, tea.Quit
	case "esc":
		if sp.Show {
			idx := a.agentMsgList.Index()
			sp.Show = false
			sp.Focus = false
			contentH := a.height - 3
			a.agentMsgList.SetSize(sp.ListWidth(a.width, a.splitRatio), contentH)
			a.agentMsgList.Select(idx)
			return a, nil
		}
		a.liveTail = false
		a.state = viewAgents
		return a, nil
	case "enter":
		item, ok := a.agentMsgList.SelectedItem().(messageItem)
		if !ok {
			return a, nil
		}
		return a.openAgentDetail(item.entry)
	case "t":
		return a.openToolCalls(a.agentMsgs, a.currentAgent.ShortID)
	case "J":
		return a.jumpToTmuxPane(a.currentSess.ProjectPath)
	case "L":
		return a.toggleLiveTail()
	case "tab":
		if !sp.Show {
			return a, nil
		}
		sp.Focus = !sp.Focus
		if sp.Focus && a.liveTail {
			items := a.agentMsgList.Items()
			if a.agentMsgList.Index() >= len(items)-1 {
				a.snapListToEnd()
			} else {
				a.refreshMsgPreview(sp)
			}
		} else if sp.Folds != nil && sp.Folds.Collapsed != nil {
			a.refreshMsgPreview(sp)
		}
		return a, nil
	case "left":
		if !sp.Focus {
			return a, nil
		}
		// When preview focused, fall through to fold handling below
	case "right":
		if !sp.Focus {
			if !sp.Show {
				idx := a.agentMsgList.Index()
				sp.Show = true
				sp.CacheKey = ""
				contentH := a.height - 3
				a.agentMsgList.SetSize(sp.ListWidth(a.width, a.splitRatio), contentH)
				a.agentMsgList.Select(idx)
			}
			sp.Focus = true
			if a.liveTail {
				items := a.agentMsgList.Items()
				if a.agentMsgList.Index() >= len(items)-1 {
					a.snapListToEnd()
				} else {
					a.refreshMsgPreview(sp)
				}
			} else {
				a.refreshMsgPreview(sp)
			}
			return a, nil
		}
		// When preview focused, fall through to fold handling below
	case "[":
		if sp.Show {
			a.adjustSplitRatio(5)
		}
		return a, nil
	case "]":
		if sp.Show {
			a.adjustSplitRatio(-5)
		}
		return a, nil
	}

	if sp.Focus && sp.Show {
		// Block cursor navigation (up/down/left/right/f/F) handled first
		if sp.Folds != nil {
			result := sp.Folds.HandleKey(msg.String())
			if result == foldHandled {
				a.refreshMsgPreview(sp)
				sp.ScrollToBlock()
				return a, nil
			}
			if result == foldSwitchToList {
				sp.Focus = false
				return a, nil
			}
		}
		if sp.HandlePreviewScroll(msg.String()) {
			return a, nil
		}
	}

	// Auto-trigger search on letter/digit keys
	if !sp.Focus && isSearchTrigger(msg.String()) {
		switch msg.String() {
		case "t", "p", "q", "J", "L":
			// handled above
		default:
			cmd := startListSearch(&a.agentMsgList, msg.String())
			return a, cmd
		}
	}

	// Page boundary
	if !sp.Focus && listPageEdge(&a.agentMsgList, msg.String()) {
		if sp.Show {
			a.updateMsgPreview(sp)
		}
		return a, nil
	}

	m, cmd := a.updateAgentMsgList(msg)
	if sp.Show {
		a.updateMsgPreview(sp)
	}
	return m, cmd
}

func (a *App) handleAgentDetailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.copyModeActive {
		return a.handleCopyModeKeys(msg)
	}
	switch msg.String() {
	case "q":
		return a, tea.Quit
	case "esc":
		wasTracking := a.detailLiveTrack
		a.detailLiveTrack = false
		a.state = viewAgentMessages
		if wasTracking {
			merged := mergeConversationTurns(a.agentMsgs)
			contentH := a.height - 3
			a.agentMsgList = newMessageList(merged, a.agentMsgSplit.ListWidth(a.width, a.splitRatio), contentH)
			items := a.agentMsgList.Items()
			if len(items) > 0 {
				a.agentMsgList.Select(len(items) - 1)
			}
		}
		return a, nil
	case "v":
		a.enterCopyMode()
		return a, nil
	case "y":
		copyToClipboard(renderPlainMessage(a.agentDetailEntry))
		a.copiedMsg = "Copied!"
		return a, nil
	case "o":
		return a, openInPager(a.agentDetailContent)
	}
	var cmd tea.Cmd
	a.agentDetailVP, cmd = a.agentDetailVP.Update(msg)
	return a, cmd
}

func (a *App) handleMemoryKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return a, tea.Quit
	case "esc":
		a.state = a.memoryFrom
		return a, nil
	}
	var cmd tea.Cmd
	a.memoryVP, cmd = a.memoryVP.Update(msg)
	return a, cmd
}

func (a *App) handleToolCallKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sp := &a.toolSplit

	switch msg.String() {
	case "q":
		return a, tea.Quit
	case "esc":
		if sp.Show {
			idx := a.toolList.Index()
			sp.Show = false
			sp.Focus = false
			contentH := a.height - 3
			a.toolList.SetSize(sp.ListWidth(a.width, a.splitRatio), contentH)
			a.toolList.Select(idx)
			return a, nil
		}
		if a.currentAgent.ID != "" {
			a.state = viewAgentMessages
		} else {
			a.state = viewMessages
		}
		return a, nil
	case "enter":
		item, ok := a.toolList.SelectedItem().(toolCallItem)
		if !ok {
			return a, nil
		}
		// For Task tool calls, jump to agent messages
		if item.toolName == "Task" {
			if agent, found := a.findAgentForMessage(item.entry); found {
				return a.openAgentMessages(agent)
			}
		}
		return a.openDetail(item.entry, viewToolCalls)
	case "tab":
		if !sp.Show {
			return a, nil
		}
		sp.Focus = !sp.Focus
		if sp.Focus && sp.Folds != nil && sp.Folds.Collapsed != nil {
			a.refreshMsgPreview(sp)
		}
		return a, nil
	case "left":
		if !sp.Focus {
			return a, nil
		}
		// When preview focused, fall through to fold handling below
	case "right":
		if !sp.Focus {
			if !sp.Show {
				idx := a.toolList.Index()
				sp.Show = true
				sp.CacheKey = ""
				contentH := a.height - 3
				a.toolList.SetSize(sp.ListWidth(a.width, a.splitRatio), contentH)
				a.toolList.Select(idx)
			}
			sp.Focus = true
			a.refreshMsgPreview(sp)
			return a, nil
		}
		// When preview focused, fall through to fold handling below
	case "[":
		if sp.Show {
			a.adjustSplitRatio(5)
		}
		return a, nil
	case "]":
		if sp.Show {
			a.adjustSplitRatio(-5)
		}
		return a, nil
	}

	if sp.Focus && sp.Show {
		if sp.Folds != nil {
			result := sp.Folds.HandleKey(msg.String())
			if result == foldHandled {
				a.refreshMsgPreview(sp)
				sp.ScrollToBlock()
				return a, nil
			}
			if result == foldSwitchToList {
				sp.Focus = false
				return a, nil
			}
		}
		if sp.HandlePreviewScroll(msg.String()) {
			return a, nil
		}
	}

	if listPageEdge(&a.toolList, msg.String()) {
		if sp.Show {
			a.updateToolPreview(sp)
		}
		return a, nil
	}

	// Auto-trigger search on letter/digit keys
	if !sp.Focus && isSearchTrigger(msg.String()) {
		switch msg.String() {
		case "q":
			// handled above
		default:
			cmd := startListSearch(&a.toolList, msg.String())
			return a, cmd
		}
	}

	if a.listReady(&a.toolList) {
		var cmd tea.Cmd
		a.toolList, cmd = a.toolList.Update(msg)
		if sp.Show {
			a.updateToolPreview(sp)
		}
		return a, cmd
	}
	return a, nil
}

// --- Actions ---

func (a *App) openProjectDir(projectPath string) (tea.Model, tea.Cmd) {
	if projectPath == "" {
		return a, nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	c := exec.Command(shell)
	c.Dir = projectPath
	return a, tea.ExecProcess(c, func(err error) tea.Msg {
		return tea.QuitMsg{}
	})
}

func (a *App) openMemory(sess session.Session, from viewState) (tea.Model, tea.Cmd) {
	if sess.ProjectPath == "" {
		return a, nil
	}

	home, _ := os.UserHomeDir()
	var sb strings.Builder

	// 1. Todos from session
	if len(sess.Todos) > 0 {
		sb.WriteString(dimStyle.Render("── Todos ──"))
		sb.WriteString("\n\n")
		for _, t := range sess.Todos {
			icon := "○"
			style := dimStyle
			switch t.Status {
			case "completed":
				icon = "✓"
				style = lipgloss.NewStyle().Foreground(colorAccent)
			case "in_progress":
				icon = "◉"
				style = lipgloss.NewStyle().Foreground(colorAssistant)
			}
			sb.WriteString(style.Render(fmt.Sprintf("  %s %s", icon, t.Content)))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// 2. Auto-memory from ~/.claude/projects/<encoded>/memory/
	encoded := session.EncodeProjectPath(sess.ProjectPath)
	memDir := home + "/.claude/projects/" + encoded + "/memory"
	if entries, err := os.ReadDir(memDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			fp := memDir + "/" + e.Name()
			data, err := os.ReadFile(fp)
			if err != nil || len(data) == 0 {
				continue
			}
			sb.WriteString(dimStyle.Render("── " + e.Name() + " ──"))
			sb.WriteString("\n\n")
			sb.WriteString(strings.TrimRight(string(data), "\n"))
			sb.WriteString("\n\n")
		}
	}

	if sb.Len() == 0 {
		sb.WriteString(dimStyle.Render("No memory or todos found for this session."))
	}

	contentH := a.height - 3
	a.memoryVP = viewport.New(a.width, contentH)
	a.memoryVP.SetContent(sb.String())
	a.memoryFrom = from
	a.state = viewMemory
	return a, nil
}

func (a *App) resumeSession(sess session.Session) (tea.Model, tea.Cmd) {
	dir := sess.ProjectPath
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	c := exec.Command("claude", "--resume", sess.ID)
	c.Dir = dir
	return a, tea.ExecProcess(c, func(err error) tea.Msg {
		return tea.QuitMsg{}
	})
}

func (a *App) openDetail(entry session.Entry, from viewState) (tea.Model, tea.Cmd) {
	a.detailEntry = entry
	a.detailFrom = from
	content := renderFullMessage(entry, a.width)
	a.detailContent = content
	contentH := a.height - 3
	a.detailVP = viewport.New(a.width, contentH)
	a.detailVP.SetContent(content)
	a.state = viewDetail

	// Enable live tracking if opening the latest message of a live session
	a.detailLiveTrack = false
	if from == viewMessages && a.currentSess.IsLive {
		items := a.messageList.Items()
		if a.messageList.Index() >= len(items)-1 {
			a.detailLiveTrack = true
			if !a.liveTail {
				return a, liveTickCmd()
			}
		}
	}

	return a, nil
}

func (a *App) openAgentDetail(entry session.Entry) (tea.Model, tea.Cmd) {
	a.agentDetailEntry = entry
	a.detailFrom = viewAgentMessages
	content := renderFullMessage(entry, a.width)
	a.agentDetailContent = content
	contentH := a.height - 3
	a.agentDetailVP = viewport.New(a.width, contentH)
	a.agentDetailVP.SetContent(content)
	a.state = viewAgentDetail

	// Enable live tracking if opening the latest agent message of a live agent
	a.detailLiveTrack = false
	if a.agentMsgList.Width() > 0 {
		items := a.agentMsgList.Items()
		if a.agentMsgList.Index() >= len(items)-1 {
			if info, err := os.Stat(a.currentAgent.FilePath); err == nil {
				if time.Since(info.ModTime()) < 60*time.Second {
					a.detailLiveTrack = true
					if !a.liveTail {
						return a, liveTickCmd()
					}
				}
			}
		}
	}

	return a, nil
}

func (a *App) jumpToTmuxPane(projectPath string) (tea.Model, tea.Cmd) {
	pane, found := findTmuxPane(projectPath)
	if !found {
		a.copiedMsg = "No tmux pane found"
		return a, nil
	}
	if err := moveWithAndSwitchPane(pane); err != nil {
		a.copiedMsg = "Switch failed"
		return a, nil
	}
	return a, nil
}

func (a *App) openAgents(sess session.Session) (tea.Model, tea.Cmd) {
	a.currentSess = sess
	if len(a.agents) == 0 {
		agents, err := session.FindSubagents(sess.FilePath)
		if err != nil || len(agents) == 0 {
			return a, nil
		}
		a.agents = agents
	}
	contentH := max(a.height-3, 1)
	a.agentSplit.Focus = false
	a.agentSplit.CacheKey = ""
	a.agentList = newAgentList(a.agents, a.agentSplit.ListWidth(a.width, a.splitRatio), contentH)
	a.state = viewAgents
	return a, nil
}

func (a *App) openAgentMessages(agent session.Subagent) (tea.Model, tea.Cmd) {
	a.currentAgent = agent
	entries, err := session.LoadMessages(agent.FilePath)
	if err != nil {
		return a, nil
	}
	a.agentMsgs = entries
	a.agentMsgSplit.Focus = false
	a.agentMsgSplit.CacheKey = ""
	if info, err := os.Stat(agent.FilePath); err == nil {
		a.lastMsgLoadTime = info.ModTime()
	}
	contentH := a.height - 3
	merged := mergeConversationTurns(entries)
	a.agentMsgList = newMessageList(merged, a.agentMsgSplit.ListWidth(a.width, a.splitRatio), contentH)
	a.state = viewAgentMessages
	return a, nil
}

func (a *App) openToolCalls(msgs []session.Entry, agentCtx string) (tea.Model, tea.Cmd) {
	a.toolCalls = extractToolCalls(msgs)
	if len(a.toolCalls) == 0 {
		return a, nil
	}
	if agentCtx == "" {
		a.currentAgent = session.Subagent{}
	}
	contentH := a.height - 3
	a.toolSplit.CacheKey = ""
	a.toolSplit.Focus = false
	a.toolList = newToolList(a.toolCalls, a.toolSplit.ListWidth(a.width, a.splitRatio), contentH)
	a.state = viewToolCalls
	return a, nil
}

func (a *App) loadSessionMessages() tea.Cmd {
	entries, err := session.LoadMessages(a.currentSess.FilePath)
	if err != nil {
		return nil
	}
	a.messages = entries
	a.msgFilter = filterNone
	a.msgSplit.Focus = false
	a.msgSplit.CacheKey = ""
	if info, err := os.Stat(a.currentSess.FilePath); err == nil {
		a.lastMsgLoadTime = info.ModTime()
	}

	// Pre-load subagents for direct agent jump
	agents, _ := session.FindSubagents(a.currentSess.FilePath)
	a.agents = agents

	a.applyMessageFilter()
	// Select latest message on open
	if !a.msgReverse {
		items := a.messageList.Items()
		if len(items) > 0 {
			a.messageList.Select(len(items) - 1)
		}
	}
	a.state = viewMessages
	return nil
}

func (a *App) applyMessageFilter() {
	merged := mergeConversationTurns(a.messages)
	filtered := filterMerged(merged, a.msgFilter)
	if a.msgReverse {
		reverseMerged(filtered)
	}
	contentH := a.height - 3
	a.messageList = newMessageList(filtered, a.msgSplit.ListWidth(a.width, a.splitRatio), contentH)
	a.msgSplit.CacheKey = ""
}

// --- Agent matching ---

// findAgentForMessage checks if a message has a Task tool_use and finds the
// matching subagent by timestamp proximity.
func (a *App) findAgentForMessage(entry session.Entry) (session.Subagent, bool) {
	if len(a.agents) == 0 {
		return session.Subagent{}, false
	}

	hasTask := false
	for _, block := range entry.Content {
		if block.Type == "tool_use" && block.ToolName == "Task" {
			hasTask = true
			break
		}
	}
	if !hasTask || entry.Timestamp.IsZero() {
		return session.Subagent{}, false
	}

	// Find agent with closest timestamp after the Task tool call
	var best session.Subagent
	bestDiff := float64(math.MaxFloat64)
	for _, ag := range a.agents {
		if ag.Timestamp.IsZero() {
			continue
		}
		diff := ag.Timestamp.Sub(entry.Timestamp).Seconds()
		// Agent should start after or very close to the Task call (within 60s)
		if diff >= -5 && diff < 60 {
			absDiff := math.Abs(diff)
			if absDiff < bestDiff {
				bestDiff = absDiff
				best = ag
			}
		}
	}
	if bestDiff < math.MaxFloat64 {
		return best, true
	}
	return session.Subagent{}, false
}

// --- Live refresh ---

func (a *App) handleTick() tea.Cmd {
	now := time.Now()

	switch a.state {
	case viewSessions:
		// Update IsLive flags by re-statting files
		changed := false
		for i := range a.sessions {
			info, err := os.Stat(a.sessions[i].FilePath)
			if err != nil {
				continue
			}
			live := now.Sub(info.ModTime()) < 60*time.Second
			if a.sessions[i].IsLive != live {
				a.sessions[i].IsLive = live
				changed = true
			}
			if !info.ModTime().Equal(a.sessions[i].ModTime) {
				a.sessions[i].ModTime = info.ModTime()
				changed = true
			}
		}
		if changed && !a.isFiltering() {
			// Remember currently selected session so cursor follows it after re-sort
			selectedID := ""
			if sel, ok := a.sessionList.SelectedItem().(sessionItem); ok {
				selectedID = sel.sess.ID
			}

			sort.Slice(a.sessions, func(i, j int) bool {
				return a.sessions[i].ModTime.After(a.sessions[j].ModTime)
			})

			items := make([]list.Item, len(a.sessions))
			newIdx := 0
			for i, s := range a.sessions {
				items[i] = sessionItem{sess: s}
				if s.ID == selectedID {
					newIdx = i
				}
			}
			a.sessionList.SetItems(items)
			a.sessionList.Select(newIdx)
		}
		// Refresh preview for live sessions (auto-scroll to bottom)
		a.refreshSessionPreviewLive()

	case viewMessages:
		a.refreshLiveMessages(a.currentSess.FilePath, &a.messages, &a.messageList)

	case viewDetail:
		if a.detailLiveTrack {
			a.refreshDetailLive()
		}

	case viewAgentMessages:
		a.refreshLiveMessages(a.currentAgent.FilePath, &a.agentMsgs, &a.agentMsgList)

	case viewAgentDetail:
		if a.detailLiveTrack {
			a.refreshAgentDetailLive()
		}
	}

	return nil
}

func (a *App) refreshLiveMessages(filePath string, msgs *[]session.Entry, msgList *list.Model) {
	if a.isFiltering() {
		return
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return
	}
	if !info.ModTime().After(a.lastMsgLoadTime) {
		return
	}

	entries, err := session.LoadMessages(filePath)
	if err != nil || len(entries) == len(*msgs) {
		return
	}

	curIdx := msgList.Index()
	oldItems := msgList.Items()
	wasAtEnd := curIdx >= len(oldItems)-1
	*msgs = entries

	merged := mergeConversationTurns(entries)
	filtered := filterMerged(merged, a.msgFilter)
	if a.msgReverse {
		reverseMerged(filtered)
	}

	items := make([]list.Item, len(filtered))
	for i, m := range filtered {
		items[i] = messageItem{entry: m.entry, index: m.startIdx, endIndex: m.endIdx}
	}
	msgList.SetItems(items)

	if a.msgReverse {
		// Reversed: newest is at index 0, "end" means index 0
		if wasAtEnd {
			msgList.Select(0)
		} else if curIdx < len(items) {
			msgList.Select(curIdx)
		}
	} else {
		if wasAtEnd {
			msgList.Select(len(items) - 1)
		} else if curIdx < len(items) {
			msgList.Select(curIdx)
		}
	}

	a.lastMsgLoadTime = info.ModTime()
}

// handleLiveTail refreshes messages and snaps to the latest entry + updates preview.
func (a *App) handleLiveTail() {
	switch a.state {
	case viewMessages:
		a.refreshLiveMessages(a.currentSess.FilePath, &a.messages, &a.messageList)
		if a.msgSplit.Show {
			a.updateMsgPreview(&a.msgSplit)
		}
	case viewAgentMessages:
		a.refreshLiveMessages(a.currentAgent.FilePath, &a.agentMsgs, &a.agentMsgList)
		if a.agentMsgSplit.Show {
			a.updateMsgPreview(&a.agentMsgSplit)
		}
	case viewDetail:
		if a.detailLiveTrack {
			a.refreshDetailLive()
		}
	case viewAgentDetail:
		if a.detailLiveTrack {
			a.refreshAgentDetailLive()
		}
	}
}

func (a *App) toggleLiveTail() (tea.Model, tea.Cmd) {
	a.liveTail = !a.liveTail
	if a.liveTail {
		a.copiedMsg = "Live tail ON"
		// Always snap list to end, regardless of new messages
		a.snapListToEnd()
		a.handleLiveTail()
		return a, liveTickCmd()
	}
	a.copiedMsg = "Live tail OFF"
	return a, nil
}

// snapListToEnd selects the last item in the active message list and refreshes preview.
func (a *App) snapListToEnd() {
	switch a.state {
	case viewMessages:
		items := a.messageList.Items()
		if len(items) > 0 {
			a.messageList.Select(len(items) - 1)
		}
		a.msgSplit.CacheKey = ""
		if a.msgSplit.Show {
			a.updateMsgPreview(&a.msgSplit)
		}
	case viewAgentMessages:
		items := a.agentMsgList.Items()
		if len(items) > 0 {
			a.agentMsgList.Select(len(items) - 1)
		}
		a.agentMsgSplit.CacheKey = ""
		if a.agentMsgSplit.Show {
			a.updateMsgPreview(&a.agentMsgSplit)
		}
	}
}

// refreshDetailLive reloads messages and updates the detail view to show the latest entry.
func (a *App) refreshDetailLive() {
	if a.copyModeActive {
		return
	}
	info, err := os.Stat(a.currentSess.FilePath)
	if err != nil {
		return
	}
	if time.Since(info.ModTime()) > 60*time.Second {
		a.detailLiveTrack = false
		return
	}
	if !info.ModTime().After(a.lastMsgLoadTime) {
		return
	}
	entries, err := session.LoadMessages(a.currentSess.FilePath)
	if err != nil || len(entries) == len(a.messages) {
		return
	}
	a.messages = entries
	a.lastMsgLoadTime = info.ModTime()

	merged := mergeConversationTurns(entries)
	if len(merged) == 0 {
		return
	}
	latest := merged[len(merged)-1]

	atBottom := vpAtBottom(&a.detailVP)
	a.detailEntry = latest.entry
	content := renderFullMessage(latest.entry, a.width)
	a.detailContent = content
	a.detailVP.SetContent(content)
	if atBottom {
		a.detailVP.GotoBottom()
	}
}

// refreshAgentDetailLive reloads agent messages and updates the agent detail view.
func (a *App) refreshAgentDetailLive() {
	if a.copyModeActive {
		return
	}
	info, err := os.Stat(a.currentAgent.FilePath)
	if err != nil {
		return
	}
	if time.Since(info.ModTime()) > 60*time.Second {
		a.detailLiveTrack = false
		return
	}
	if !info.ModTime().After(a.lastMsgLoadTime) {
		return
	}
	entries, err := session.LoadMessages(a.currentAgent.FilePath)
	if err != nil || len(entries) == len(a.agentMsgs) {
		return
	}
	a.agentMsgs = entries
	a.lastMsgLoadTime = info.ModTime()

	merged := mergeConversationTurns(entries)
	if len(merged) == 0 {
		return
	}
	latest := merged[len(merged)-1]

	atBottom := vpAtBottom(&a.agentDetailVP)
	a.agentDetailEntry = latest.entry
	content := renderFullMessage(latest.entry, a.width)
	a.agentDetailContent = content
	a.agentDetailVP.SetContent(content)
	if atBottom {
		a.agentDetailVP.GotoBottom()
	}
}

func vpAtBottom(vp *viewport.Model) bool {
	total := vp.TotalLineCount()
	h := vp.Height
	if total <= h {
		return true
	}
	return vp.YOffset >= total-h
}

// liveBadgeText returns "[LIVE ▼]" when auto-scroll is active, "[LIVE]" otherwise.
func (a *App) liveBadgeText() string {
	var sp *SplitPane
	var msgList *list.Model
	switch a.state {
	case viewMessages:
		sp = &a.msgSplit
		msgList = &a.messageList
	case viewAgentMessages:
		sp = &a.agentMsgSplit
		msgList = &a.agentMsgList
	default:
		return "[LIVE]"
	}

	if !sp.Show {
		return "[LIVE]"
	}

	items := msgList.Items()
	if msgList.Index() < len(items)-1 {
		return "[LIVE]"
	}
	following := false
	if sp.Focus {
		if sp.Folds != nil {
			following = sp.Folds.BlockCursor >= len(sp.Folds.Entry.Content)-1
		}
	} else {
		following = vpAtBottom(&sp.Preview)
	}
	if following {
		return "[LIVE ▼]"
	}
	return "[LIVE]"
}

// detailLiveBadgeText returns "[LIVE ▼]" when auto-scroll is active in detail view.
func (a *App) detailLiveBadgeText() string {
	var vp *viewport.Model
	switch a.state {
	case viewDetail:
		vp = &a.detailVP
	case viewAgentDetail:
		vp = &a.agentDetailVP
	default:
		return "[LIVE]"
	}
	if vpAtBottom(vp) {
		return "[LIVE ▼]"
	}
	return "[LIVE]"
}

// --- Message split pane ---

func (a *App) renderMsgSplit(sp *SplitPane) string {
	if !sp.Show || a.width < 40 || a.height < 10 {
		return sp.List.View()
	}

	listW := sp.ListWidth(a.width, a.splitRatio)
	previewW := max(a.width-listW-1, 1)
	contentH := max(a.height-3, 1)

	if sp.List.Width() > 0 && (sp.List.Width() != listW || sp.List.Height() != contentH) {
		sp.List.SetSize(listW, contentH)
	}

	a.updateMsgPreview(sp)

	if sp.Preview.Width != previewW || sp.Preview.Height != contentH {
		sp.Preview.Width = previewW
		sp.Preview.Height = max(contentH, 1)
		// Re-wrap content at new width and clamp offset
		if sp.Folds != nil && sp.Folds.Collapsed != nil {
			cursor := -1
			if sp.Focus {
				cursor = sp.Folds.BlockCursor
			}
			rp := renderFullMessageWithCursor(sp.Folds.Entry, previewW, sp.Folds.Collapsed, sp.Folds.Formatted, cursor)
			sp.Folds.BlockStarts = rp.blockStarts
			sp.Preview.SetContent(rp.content)
		}
		sp.Preview.YOffset = min(sp.Preview.YOffset, max(sp.Preview.TotalLineCount()-contentH, 0))
	}

	borderColor := colorBorderDim
	if sp.Focus {
		borderColor = colorBorderFocused
	}

	leftStyle := lipgloss.NewStyle().Width(listW).MaxWidth(listW).Height(contentH).MaxHeight(contentH)
	rightStyle := lipgloss.NewStyle().Width(previewW).MaxWidth(previewW).Height(contentH).MaxHeight(contentH)
	borderStyle := lipgloss.NewStyle().Foreground(borderColor).Height(contentH).MaxHeight(contentH)

	left := leftStyle.Render(sp.List.View())
	border := borderStyle.Render(strings.Repeat("│\n", max(contentH-1, 0)) + "│")
	right := rightStyle.Render(sp.Preview.View())

	return lipgloss.JoinHorizontal(lipgloss.Top, left, border, right)
}

func (a *App) updateMsgPreview(sp *SplitPane) {
	item, ok := sp.List.SelectedItem().(messageItem)
	if !ok {
		return
	}
	// Build cache key from index + block count
	cacheKey := fmt.Sprintf("%d:%d", item.index, len(item.entry.Content))
	if cacheKey == sp.CacheKey {
		return
	}

	// Check if this is a different entry or same entry with more content
	oldCacheKey := sp.CacheKey
	isNewEntry := true
	if oldCacheKey != "" {
		// Parse old cache key to compare entry index
		var oldIdx int
		fmt.Sscanf(oldCacheKey, "%d:", &oldIdx)
		isNewEntry = oldIdx != item.index
	}

	if isNewEntry {
		// Different entry: reset everything
		sp.CacheKey = cacheKey
		if sp.Folds != nil {
			sp.Folds.Reset(item.entry)
		}

		items := sp.List.Items()
		isLast := sp.List.Index() >= len(items)-1
		isLive := a.currentSess.IsLive || a.liveTail

		if sp.Folds != nil && isLast && isLive {
			// Live + last message: start at the last block, scroll to bottom
			sp.Folds.BlockCursor = max(len(item.entry.Content)-1, 0)
		}

		a.refreshMsgPreview(sp)

		if isLast && isLive {
			sp.Preview.GotoBottom()
		} else {
			sp.Preview.GotoTop()
		}
	} else {
		// Same entry but content grew: preserve view state
		sp.CacheKey = cacheKey

		if sp.Folds != nil {
			oldBlockCount := len(sp.Folds.Entry.Content)
			wasAtLastBlock := sp.Folds.BlockCursor >= oldBlockCount-1

			sp.Folds.GrowBlocks(item.entry, oldBlockCount)

			// Determine if we should auto-scroll to bottom
			shouldFollow := false
			if sp.Focus {
				shouldFollow = wasAtLastBlock
				if shouldFollow {
					sp.Folds.BlockCursor = max(len(item.entry.Content)-1, 0)
				}
			} else {
				shouldFollow = vpAtBottom(&sp.Preview)
			}
			a.refreshMsgPreview(sp)
			if shouldFollow {
				sp.Preview.GotoBottom()
			}
		}
	}
}

func (a *App) refreshMsgPreview(sp *SplitPane) {
	if sp.Folds != nil {
		sp.RefreshFoldPreview(a.width, a.splitRatio)
	}
}

// updateToolPreview updates the preview for tool call items.
func (a *App) updateToolPreview(sp *SplitPane) {
	item, ok := sp.List.SelectedItem().(toolCallItem)
	if !ok {
		return
	}
	cacheKey := fmt.Sprintf("%d:%d", item.msgIndex, len(item.entry.Content))
	if cacheKey == sp.CacheKey {
		return
	}
	sp.CacheKey = cacheKey
	if sp.Folds != nil {
		sp.Folds.Reset(item.entry)
	}
	a.refreshMsgPreview(sp)
	sp.Preview.GotoTop()
}

// --- Session split pane ---

func (a *App) renderSessionSplit() string {
	if !a.sessSplit.Show || a.width < 40 || a.height < 10 {
		return a.sessionList.View()
	}

	listW := a.sessSplit.ListWidth(a.width, a.splitRatio)
	previewW := max(a.width-listW-1, 1)
	contentH := max(a.height-3, 1)

	if a.sessionList.Width() > 0 && (a.sessionList.Width() != listW || a.sessionList.Height() != contentH) {
		a.sessionList.SetSize(listW, contentH)
	}

	a.updateSessionPreview()

	if a.sessSplit.Preview.Width != previewW || a.sessSplit.Preview.Height != contentH {
		a.sessSplit.Preview.Width = previewW
		a.sessSplit.Preview.Height = max(contentH, 1)
		a.sessSplit.CacheKey = "" // force re-render at new width
		a.updateSessionPreview()
	}

	borderColor := colorBorderDim
	if a.sessSplit.Focus {
		borderColor = colorBorderFocused
	}

	leftStyle := lipgloss.NewStyle().Width(listW).MaxWidth(listW).Height(contentH).MaxHeight(contentH)
	rightStyle := lipgloss.NewStyle().Width(previewW).MaxWidth(previewW).Height(contentH).MaxHeight(contentH)
	borderStyle := lipgloss.NewStyle().Foreground(borderColor).Height(contentH).MaxHeight(contentH)

	left := leftStyle.Render(a.sessionList.View())
	border := borderStyle.Render(strings.Repeat("│\n", max(contentH-1, 0)) + "│")
	right := rightStyle.Render(a.sessSplit.Preview.View())

	return lipgloss.JoinHorizontal(lipgloss.Top, left, border, right)
}

func (a *App) updateSessionPreview() {
	if !a.sessSplit.Show {
		return
	}
	item, ok := a.sessionList.SelectedItem().(sessionItem)
	if !ok {
		return
	}
	if item.sess.ID == a.sessSplit.CacheKey {
		return
	}
	a.sessSplit.CacheKey = item.sess.ID
	a.sessPreviewPinned = false

	const headN, tailN = 3, 2
	head, tail, total, err := session.LoadMessagesSummary(item.sess.FilePath, headN, tailN)
	if err != nil || total == 0 {
		a.sessSplit.Preview.SetContent(dimStyle.Render("(no messages)"))
		return
	}

	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	var sb strings.Builder

	for _, e := range head {
		sb.WriteString(renderCompactMessage(e, previewW, 2))
	}
	if len(tail) > 0 {
		skipped := total - len(head) - len(tail)
		if skipped > 0 {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("  ... %d more messages ...\n", skipped)))
		}
		for _, e := range tail {
			sb.WriteString(renderCompactMessage(e, previewW, 2))
		}
	}

	contentH := max(a.height-3, 1)
	a.sessSplit.Preview = viewport.New(previewW, contentH)
	a.sessSplit.Preview.SetContent(sb.String())
	if item.sess.IsLive {
		a.sessSplit.Preview.GotoBottom()
	}
}

// --- Agent split pane ---

func (a *App) adjustSplitRatio(delta int) {
	a.splitRatio += delta
	if a.splitRatio < 15 {
		a.splitRatio = 15
	}
	if a.splitRatio > 85 {
		a.splitRatio = 85
	}
	// Ensure minimum widths: list >= 30, preview >= 20
	listW := a.width * a.splitRatio / 100
	previewW := a.width - listW - 1
	if listW < 30 {
		a.splitRatio = (30*100 + a.width - 1) / a.width
	}
	if previewW < 20 {
		a.splitRatio = ((a.width - 21) * 100) / a.width
	}
	a.resizeAll()
}

func (a *App) renderAgentSplit() string {
	if !a.agentSplit.Show || a.width < 40 || a.height < 10 {
		return a.agentList.View()
	}

	listW := a.agentSplit.ListWidth(a.width, a.splitRatio)
	previewW := max(a.width-listW-1, 1)
	contentH := max(a.height-3, 1)

	if a.agentList.Width() > 0 && (a.agentList.Width() != listW || a.agentList.Height() != contentH) {
		a.agentList.SetSize(listW, contentH)
	}

	a.updateAgentPreview()

	if a.agentSplit.Preview.Width != previewW || a.agentSplit.Preview.Height != contentH {
		a.agentSplit.Preview.Width = previewW
		a.agentSplit.Preview.Height = max(contentH, 1)
		a.agentSplit.CacheKey = "" // force re-render at new width
		a.updateAgentPreview()
	}

	borderColor := colorBorderDim
	if a.agentSplit.Focus {
		borderColor = colorBorderFocused
	}

	leftStyle := lipgloss.NewStyle().Width(listW).MaxWidth(listW).Height(contentH).MaxHeight(contentH)
	rightStyle := lipgloss.NewStyle().Width(previewW).MaxWidth(previewW).Height(contentH).MaxHeight(contentH)
	borderStyle := lipgloss.NewStyle().Foreground(borderColor).Height(contentH).MaxHeight(contentH)

	left := leftStyle.Render(a.agentList.View())
	border := borderStyle.Render(strings.Repeat("│\n", max(contentH-1, 0)) + "│")
	right := rightStyle.Render(a.agentSplit.Preview.View())

	return lipgloss.JoinHorizontal(lipgloss.Top, left, border, right)
}

func (a *App) updateAgentPreview() {
	item, ok := a.agentList.SelectedItem().(agentItem)
	if !ok {
		return
	}
	if item.agent.ID == a.agentSplit.CacheKey {
		return
	}
	a.agentSplit.CacheKey = item.agent.ID

	entries, err := session.LoadMessages(item.agent.FilePath)
	if err != nil || len(entries) == 0 {
		a.agentSplit.Preview.SetContent(dimStyle.Render("(no messages)"))
		return
	}

	previewW := max(a.width-a.agentSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(renderCompactMessage(e, previewW, 2))
	}
	contentH := max(a.height-3, 1)
	a.agentSplit.Preview = viewport.New(previewW, contentH)
	a.agentSplit.Preview.SetContent(sb.String())
}

// --- Helpers ---

func (a *App) sessPreviewAtBottom() bool {
	total := a.sessSplit.Preview.TotalLineCount()
	h := a.sessSplit.Preview.Height
	if total <= h {
		return true
	}
	return a.sessSplit.Preview.YOffset >= total-h
}

// refreshSessionPreviewLive reloads and re-renders the session preview for a live session.
// Auto-scrolls to bottom unless the user has pinned (scrolled up).
func (a *App) refreshSessionPreviewLive() {
	if !a.sessSplit.Show {
		return
	}
	item, ok := a.sessionList.SelectedItem().(sessionItem)
	if !ok || !item.sess.IsLive {
		return
	}

	const headN, tailN = 3, 5
	head, tail, total, err := session.LoadMessagesSummary(item.sess.FilePath, headN, tailN)
	if err != nil || total == 0 {
		return
	}

	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	var sb strings.Builder

	for _, e := range head {
		sb.WriteString(renderCompactMessage(e, previewW, 2))
	}
	if len(tail) > 0 {
		skipped := total - len(head) - len(tail)
		if skipped > 0 {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("  ... %d more messages ...\n", skipped)))
		}
		for _, e := range tail {
			sb.WriteString(renderCompactMessage(e, previewW, 2))
		}
	}

	a.sessSplit.Preview.SetContent(sb.String())
	if !a.sessPreviewPinned {
		a.sessSplit.Preview.GotoBottom()
	}
}

func (a *App) searchHints() string {
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8"))
	switch a.state {
	case viewMessages, viewAgentMessages:
		return helpStyle.Render("  hints: ") +
			hintStyle.Render("role=user") + helpStyle.Render("  ") +
			hintStyle.Render("role=asst") + helpStyle.Render("  ") +
			hintStyle.Render("tool=Bash") + helpStyle.Render("  ") +
			hintStyle.Render("tool=Read") + helpStyle.Render("  ") +
			hintStyle.Render("tool=Edit") + helpStyle.Render("  ") +
			hintStyle.Render("tool=Write") +
			helpStyle.Render("  (space = AND)  enter: apply  esc: cancel")
	case viewSessions:
		return helpStyle.Render("  search by project, branch, session ID, or prompt text  enter: apply  esc: cancel")
	default:
		return helpStyle.Render("  enter: apply  esc: cancel")
	}
}

func (a *App) isFiltering() bool {
	switch a.state {
	case viewSessions:
		return a.sessionList.FilterState() == list.Filtering
	case viewMessages:
		return a.messageList.FilterState() == list.Filtering
	case viewAgents:
		return a.agentList.FilterState() == list.Filtering
	case viewAgentMessages:
		return a.agentMsgList.FilterState() == list.Filtering
	case viewToolCalls:
		return a.toolList.FilterState() == list.Filtering
	}
	return false
}

func (a *App) updateActiveList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch a.state {
	case viewSessions:
		m, cmd := a.updateSessionList(msg)
		a.updateSessionPreview()
		return m, cmd
	case viewMessages:
		return a.updateMessageList(msg)
	case viewAgents:
		m, cmd := a.updateAgentList(msg)
		if a.agentSplit.Show {
			a.updateAgentPreview()
		}
		return m, cmd
	case viewAgentMessages:
		return a.updateAgentMsgList(msg)
	case viewToolCalls:
		if a.listReady(&a.toolList) {
			var cmd tea.Cmd
			a.toolList, cmd = a.toolList.Update(msg)
			return a, cmd
		}
		return a, nil
	}
	return a, nil
}

func (a *App) updateActiveComponent(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch a.state {
	case viewSessions:
		m, cmd := a.updateSessionList(msg)
		a.updateSessionPreview()
		return m, cmd
	case viewMessages:
		m, cmd := a.updateMessageList(msg)
		a.updateMsgPreview(&a.msgSplit)
		return m, cmd
	case viewDetail:
		var cmd tea.Cmd
		a.detailVP, cmd = a.detailVP.Update(msg)
		return a, cmd
	case viewAgents:
		m, cmd := a.updateAgentList(msg)
		if a.agentSplit.Show {
			a.updateAgentPreview()
		}
		return m, cmd
	case viewAgentMessages:
		m, cmd := a.updateAgentMsgList(msg)
		a.updateMsgPreview(&a.agentMsgSplit)
		return m, cmd
	case viewAgentDetail:
		var cmd tea.Cmd
		a.agentDetailVP, cmd = a.agentDetailVP.Update(msg)
		return a, cmd
	case viewToolCalls:
		if a.listReady(&a.toolList) {
			var cmd tea.Cmd
			a.toolList, cmd = a.toolList.Update(msg)
			if a.toolSplit.Show {
				a.updateToolPreview(&a.toolSplit)
			}
			return a, cmd
		}
		return a, nil
	case viewMemory:
		var cmd tea.Cmd
		a.memoryVP, cmd = a.memoryVP.Update(msg)
		return a, cmd
	}
	return a, nil
}

func (a *App) resizeAll() {
	contentH := a.height - 3

	sessW := a.sessSplit.ListWidth(a.width, a.splitRatio)
	if a.sessionList.Width() == 0 {
		a.sessionList = newSessionList(a.sessions, sessW, contentH)
		a.sessSplit.CacheKey = ""
	} else if a.state == viewSessions {
		idx := a.sessionList.Index()
		a.sessionList.SetSize(sessW, contentH)
		a.sessionList.Select(idx)
		a.sessSplit.CacheKey = ""
	} else {
		idx := a.sessionList.Index()
		a.sessionList.SetSize(a.width, contentH)
		a.sessionList.Select(idx)
	}
	if a.messageList.Width() > 0 {
		idx := a.messageList.Index()
		a.messageList.SetSize(a.msgSplit.ListWidth(a.width, a.splitRatio), contentH)
		a.messageList.Select(idx)
		a.msgSplit.CacheKey = ""
	}
	if a.agentList.Width() > 0 {
		idx := a.agentList.Index()
		a.agentList.SetSize(a.agentSplit.ListWidth(a.width, a.splitRatio), contentH)
		a.agentList.Select(idx)
		a.agentSplit.CacheKey = ""
	}
	if a.agentMsgList.Width() > 0 {
		idx := a.agentMsgList.Index()
		a.agentMsgList.SetSize(a.agentMsgSplit.ListWidth(a.width, a.splitRatio), contentH)
		a.agentMsgList.Select(idx)
		a.agentMsgSplit.CacheKey = ""
	}
	if a.detailVP.Width > 0 {
		a.detailVP.Width = a.width
		a.detailVP.Height = contentH
	}
	if a.agentDetailVP.Width > 0 {
		a.agentDetailVP.Width = a.width
		a.agentDetailVP.Height = contentH
	}
	if a.toolList.Width() > 0 {
		idx := a.toolList.Index()
		a.toolList.SetSize(a.toolSplit.ListWidth(a.width, a.splitRatio), contentH)
		a.toolList.Select(idx)
		a.toolSplit.CacheKey = ""
	}
	if a.memoryVP.Width > 0 {
		a.memoryVP.Width = a.width
		a.memoryVP.Height = contentH
	}
}

// listPageEdge handles pgup on first page → select first item,
// pgdown on last page → select last item. Returns true if handled.
func listPageEdge(l *list.Model, key string) bool {
	switch key {
	case "pgup":
		if l.Paginator.Page == 0 {
			l.Select(0)
			return true
		}
	case "pgdown":
		if l.Paginator.Page >= l.Paginator.TotalPages-1 {
			items := l.Items()
			if len(items) > 0 {
				l.Select(len(items) - 1)
			}
			return true
		}
	}
	return false
}

func (a *App) listReady(l *list.Model) bool {
	return l.Width() > 0
}

func (a *App) updateSessionList(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !a.listReady(&a.sessionList) {
		return a, nil
	}
	var cmd tea.Cmd
	a.sessionList, cmd = a.sessionList.Update(msg)
	return a, cmd
}

func (a *App) updateMessageList(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !a.listReady(&a.messageList) {
		return a, nil
	}
	var cmd tea.Cmd
	a.messageList, cmd = a.messageList.Update(msg)
	return a, cmd
}

func (a *App) updateAgentList(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !a.listReady(&a.agentList) {
		return a, nil
	}
	var cmd tea.Cmd
	a.agentList, cmd = a.agentList.Update(msg)
	return a, cmd
}

func (a *App) updateAgentMsgList(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !a.listReady(&a.agentMsgList) {
		return a, nil
	}
	var cmd tea.Cmd
	a.agentMsgList, cmd = a.agentMsgList.Update(msg)
	return a, cmd
}

func (a *App) breadcrumb(parts ...string) string {
	var rendered []string
	for i, p := range parts {
		if i == len(parts)-1 {
			rendered = append(rendered, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Render(p))
		} else {
			rendered = append(rendered, lipgloss.NewStyle().Foreground(lipgloss.Color("#D1D5DB")).Render(p))
		}
	}
	sep := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render(" > ")
	text := strings.Join(rendered, sep)
	return lipgloss.NewStyle().Background(colorPrimary).Padding(0, 1).Render(text)
}

func scrollPreview(vp *viewport.Model, key string) {
	switch key {
	case "down":
		vp.ScrollDown(1)
	case "up":
		vp.ScrollUp(1)
	case "pgdown":
		vp.ScrollDown(vp.Height)
	case "pgup":
		vp.ScrollUp(vp.Height)
	case "home":
		vp.GotoTop()
	case "end":
		vp.GotoBottom()
	}
}

func roleLabel(e session.Entry) string {
	if e.Role == "user" {
		return "User"
	}
	return "Assistant"
}
