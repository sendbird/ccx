package tui

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

type tickMsg time.Time
type liveTickMsg time.Time
type spinnerTickMsg time.Time
type globalStatsMsg session.GlobalStats

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinnerTickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
}

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
	viewConversation
	viewMessageFull
	viewGlobalStats

)

type App struct {
	state  viewState
	width  int
	height int
	config Config

	// Data
	sessions    []session.Session
	currentSess session.Session

	// List models
	sessionList list.Model

	// Split panes
	sessSplit SplitPane

	// Session-specific: pinned scroll state
	sessPreviewPinned bool

	// Split pane ratio (list width as % of terminal width)
	splitRatio int

	// Content for clipboard/pager
	copiedMsg string

	// Copy mode (detail view)
	copyModeActive bool
	copyLines      []string
	copyCursor     int
	copyAnchor     int

	// Live tracking
	lastMsgLoadTime time.Time
	liveTail        bool // auto-scroll to latest message on tick

	// Mouse state
	dragResizing   bool
	lastClickTime  time.Time
	lastClickY     int
	breadcrumbSegs []breadcrumbSegment

	// Global stats view
	globalStatsVP      viewport.Model
	globalStatsCache   *session.GlobalStats
	globalStatsLoading bool
	spinnerFrame       int

	// Session preview mode
	sessPreviewMode    sessPreview
	sessStatsCache     *session.SessionStats
	sessStatsCacheKey  string
	sessMemoryCache    string // rendered memory content
	sessMemoryCacheKey string
	sessTasksCache     string
	sessTasksCacheKey  string

	// Conversation preview state
	sessConvEntries    []mergedMsg  // merged conversation messages
	sessConvCursor     int          // current message cursor
	sessConvCacheID    string       // session ID for which convEntries are loaded
	sessConvExpanded   map[int]bool // which messages are expanded
	sessConvSearching  bool             // typing in preview search
	sessConvSearchInput textinput.Model // search input for preview
	sessConvFiltered   []int            // indices into sessConvEntries matching search
	sessConvFilterTerm string           // applied filter term

	// Group mode: groupFlat=0, groupProject=1, groupTree=2
	sessGroupMode int
	liveUpdate    bool // auto-refresh disabled by default

	// Edit file menu
	editMenu    bool
	editSess    session.Session

	// Actions menu (x key)
	actionsMenu bool
	actionsSess session.Session
	editChoices []editChoice // available files to edit

	// Help overlay (? key)
	showHelp bool

	// Move project
	moveMode      bool
	moveInput     textinput.Model
	moveSess      session.Session

	// Worktree creation
	worktreeMode  bool
	worktreeInput textinput.Model
	worktreeSess  session.Session

	// Inline live input (bottom prompt, no modal)
	liveInputActive bool
	liveInputModel  textinput.Model
	liveInputPane   tmuxPane

	// Live preview (tmux capture in split preview)
	livePreviewPane   tmuxPane
	livePreviewSessID string

	// Conversation split view (viewConversation)
	conv struct {
		sess     session.Session
		messages []session.Entry
		merged   []mergedMsg
		agents   []session.Subagent
		items    []convItem
		split    SplitPane
		agent session.Subagent  // non-zero when viewing agent conversation
		task  session.TaskItem  // non-zero when viewing task conversation
	}
	convList list.Model

	// Full-screen message detail (viewMessageFull)
	msgFull struct {
		sess        session.Session
		agent       session.Subagent
		messages    []session.Entry
		merged      []mergedMsg
		agents      []session.Subagent
		idx         int
		vp          viewport.Model
		folds       FoldState
		content     string
		allMessages bool // true when showing full conversation (all messages concatenated)

		// Viewport search
		searching   bool
		searchInput textinput.Model
		searchTerm  string   // committed search term
		searchLines []int    // line numbers that match
		searchIdx   int      // current match index in searchLines
	}

	// Navigation stack for agent drill-down
	navStack []navFrame
}
// selectedSession returns the currently selected session from the session list.
func (a *App) selectedSession() (session.Session, bool) {
	item, ok := a.sessionList.SelectedItem().(sessionItem)
	if !ok {
		return session.Session{}, false
	}
	return item.sess, true
}


type sessPreview int

const (
	sessPreviewConversation sessPreview = iota // text-only, expandable
	sessPreviewStats
	sessPreviewMemory
	sessPreviewTasksPlan
	sessPreviewLive // tmux pane capture
	numSessPreviewModes = 5
)

// Config holds application configuration from CLI flags.
type Config struct {
	TmuxEnabled  bool   // enable tmux integration (I, J, live modal)
	TmuxAutoLive bool   // auto-enter live session in same tmux window on startup
	WorktreeDir  string // subdirectory name for worktrees (default ".worktree")
	SearchQuery  string // initial search filter for session list
}

func NewApp(sessions []session.Session, cfg Config) *App {
	// Set IsLive by matching running Claude processes to sessions.
	// In tmux: match by session ID in process args, fallback to most recent for path.
	// Outside tmux: match by project path.
	markLiveSessions(sessions)

	if cfg.WorktreeDir == "" {
		cfg.WorktreeDir = ".worktree"
	}

	a := &App{
		state:      viewSessions,
		sessions:   sessions,
		config:     cfg,
		splitRatio: 35,
	}
	a.sessSplit = SplitPane{List: &a.sessionList, ItemHeight: 2}
	a.conv.split = SplitPane{List: &a.convList, Show: true, Folds: &FoldState{}, ItemHeight: 1}
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
		cmd := a.resizeAll()
		return a, cmd

	case editorDoneMsg:
		return a, nil

	case tickMsg:
		cmd := a.handleTick()
		return a, tea.Batch(cmd, tickCmd())

	case liveTickMsg:
		if a.state == viewSessions && a.sessPreviewMode == sessPreviewLive && a.livePreviewSessID != "" {
			a.refreshLivePreview()
			return a, liveTickCmd()
		}
		if a.liveTail {
			a.handleLiveTail()
			return a, liveTickCmd()
		}
		return a, nil

	case spinnerTickMsg:
		if a.globalStatsLoading {
			a.spinnerFrame = (a.spinnerFrame + 1) % len(spinnerFrames)
			return a, spinnerTickCmd()
		}
		return a, nil

	case globalStatsMsg:
		stats := session.GlobalStats(msg)
		a.globalStatsCache = &stats
		a.globalStatsLoading = false
		// Switch to global stats view now that data is ready
		contentH := a.height - 3
		a.globalStatsVP = viewport.New(a.width, contentH)
		a.globalStatsVP.SetContent(renderGlobalStats(stats, a.width))
		a.state = viewGlobalStats
		return a, nil

	case tea.MouseMsg:
		return a.handleMouse(msg)

	case tea.KeyMsg:
		a.copiedMsg = ""
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}

		// Inline live input intercepts all keys when active
		if a.liveInputActive {
			return a.handleLiveInput(msg)
		}

		if a.isFiltering() {
			m, cmd := a.updateActiveList(msg)
			a.syncAllFilterVisibility()
			return m, cmd
		}

		// Esc clears an applied search filter before doing normal navigation
		if msg.String() == "esc" && a.hasFilterApplied() {
			a.resetActiveFilter()
			a.syncAllFilterVisibility()
			return a, nil
		}

		switch a.state {
		case viewSessions:
			return a.handleSessionKeys(msg)
		case viewGlobalStats:
			return a.handleGlobalStatsKeys(msg)
		case viewConversation:
			return a.handleConversationKeys(msg)
		case viewMessageFull:
			return a.handleMessageFullKeys(msg)
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
		title = a.renderBreadcrumb()
		content = a.renderSessionSplit()
		if a.showHelp {
			content = renderHelpOverlay(a.width, ContentHeight(a.height))
			help = formatHelp("press any key to close")
		} else if a.moveMode {
			help = "  " + a.moveInput.View() + helpStyle.Render("  enter:move esc:cancel")
		} else if a.worktreeMode {
			help = "  " + a.worktreeInput.View() + helpStyle.Render("  enter:create esc:cancel")
		} else if a.sessConvSearching {
			help = "  " + a.sessConvSearchInput.View() + helpStyle.Render("  enter:apply esc:cancel")
		} else {
			h := "↵open g:dir e:edit x:actions R:refresh G:group S:stats"
			if !a.sessSplit.Show {
				h += " tab:preview →:preview"
			} else if a.sessSplit.Focus && a.sessPreviewMode == sessPreviewConversation {
				h += " ↑↓:nav ↵:jump →←:fold f/F:all /:search tab:mode"
			} else {
				h += " tab:mode esc:close ←→:focus []:resize"
			}
			if a.config.TmuxEnabled && inTmux() {
				h += " L:live"
				if sess, ok := a.selectedSession(); ok && sess.IsLive {
					h += " I:input J:jump"
				}
			}
			help = formatHelp(h + " /:search ?:help q:quit")
		}

	case viewGlobalStats:
		title = a.renderBreadcrumb()
		content = a.globalStatsVP.View()
		help = formatHelp("↑↓:scroll esc:back q:quit")

	case viewConversation:
		title = a.renderBreadcrumb()
		content = a.renderConvSplit()
		badges := ""
		if a.liveTail {
			badgeStyle := liveBadge
			if a.currentSess.IsResponding {
				badgeStyle = busyBadge
			}
			badges = badgeStyle.Render(a.liveBadgeText()) + "  "
		}
		sp := &a.conv.split
		h := "↵open c:full e:edit L:live R:refresh"
		if a.config.TmuxEnabled && inTmux() && a.currentSess.IsLive {
			h += " I:input J:jump"
		}
		if sp.Show {
			if sp.Focus {
				h += " ↑↓:blocks ←→:fold f/F:all"
			} else {
				h += " tab:preview →:focus"
			}
			h += " esc:close []:resize"
		} else {
			h += " tab:preview →:preview"
		}
		help = badges + formatHelp(h+" /:search esc:back q:quit")

	case viewMessageFull:
		title = a.renderBreadcrumb()
		content = a.renderMessageFull()
		if a.msgFull.searching {
			help = "  " + a.msgFull.searchInput.View() + helpStyle.Render("  enter:search esc:cancel")
		} else if a.msgFull.allMessages {
			if a.copyModeActive {
				help = formatHelp("all messages  ↑↓:move v/sp:select y/↵:copy home/end esc:cancel")
			} else {
				sh := "all messages  ↑↓:scroll v:copy y:all o:pager /:search"
				if a.msgFull.searchTerm != "" {
					sh += fmt.Sprintf(" [%d/%d] n/N:match", a.msgFull.searchIdx+1, len(a.msgFull.searchLines))
				}
				help = formatHelp(sh + " esc:back q:quit")
			}
		} else {
			pos := fmt.Sprintf("#%d/%d", a.msgFull.idx+1, len(a.msgFull.merged))
			if a.copyModeActive {
				help = formatHelp(pos + "  ↑↓:move v/sp:select y/↵:copy home/end esc:cancel")
			} else {
				sh := pos + "  ↑↓:blocks ←→:fold n/N:msg f/F:all v:copy y:all o:pager /:search"
				if a.msgFull.searchTerm != "" {
					sh = pos + fmt.Sprintf("  [%d/%d] n/N:match ↑↓:blocks ←→:fold f/F:all v:copy y:all o:pager", a.msgFull.searchIdx+1, len(a.msgFull.searchLines))
				}
				help = formatHelp(sh + " esc:back q:quit")
			}
		}
	}

	// Inline live input prompt
	if a.liveInputActive {
		help = "  " + a.liveInputModel.View() + helpStyle.Render("  enter:send esc:cancel")
	}

	// Override help with filter input + search hints when filtering
	if a.isFiltering() {
		val := a.activeFilterValue()
		prompt := helpKeyStyle.Render("Search: ") + val + blockCursorStyle.Render("▏")
		help = "  " + prompt + a.searchHints()
	} else if a.hasFilterApplied() {
		help = "  " + filterBadge.Render("[filtered]") + " " + help
	}

	if a.copiedMsg != "" {
		help += "  " + lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(a.copiedMsg)
	}

	screen := title + "\n" + content + "\n" + help

	return screen
}

// --- Key handlers ---

func (a *App) handleSessionKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sp := &a.sessSplit
	key := msg.String()

	// Help overlay: any key closes it
	if a.showHelp {
		a.showHelp = false
		return a, nil
	}

	// Clear actions menu on any unrelated key
	if a.actionsMenu {
		return a.handleActionsMenu(key)
	}

	// Edit menu: pick file to open
	if a.editMenu {
		return a.handleEditMenu(key)
	}

	// While conv search is active, route all keys to it
	if a.sessConvSearching {
		return a.handleConvSearch(msg)
	}

	// Move mode: text input for new project path
	if a.moveMode {
		return a.handleMoveInput(msg)
	}

	// Worktree mode: text input for worktree name
	if a.worktreeMode {
		return a.handleWorktreeInput(msg)
	}

	// View-specific keys
	switch key {
	case "q":
		return a, tea.Quit
	case "esc":
		if sp.Show {
			// If in a non-default preview mode, go back to messages first
			if a.sessPreviewMode != sessPreviewConversation {
				a.sessPreviewMode = sessPreviewConversation
				sp.CacheKey = ""
				sp.Focus = false
				return a, nil
			}
			idx := a.sessionList.Index()
			sp.Show = false
			sp.Focus = false
			contentH := max(a.height-3, 1)
			a.sessionList.SetSize(sp.ListWidth(a.width, a.splitRatio), contentH)
			a.sessionList.Select(idx)
			return a, nil
		}
		return a, nil
	case "enter":
		// If conversation preview is focused, jump to the selected message
		if sp.Focus && sp.Show && a.sessPreviewMode == sessPreviewConversation && len(a.sessConvEntries) > 0 {
			return a.jumpToConvMessage()
		}
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		a.currentSess = sess
		return a, a.openConversation(sess)
	case "g":
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		return a.openProjectDir(sess.ProjectPath)
	case "x":
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		a.actionsMenu = true
		a.actionsSess = sess
		a.copiedMsg = "Actions: d:delete  m:move  w:worktree  r:resume"
		return a, nil
	case "I":
		if !a.config.TmuxEnabled {
			return a, nil
		}
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		return a.sendInputToLive(sess.ProjectPath, sess.ID)
	case "J":
		if !a.config.TmuxEnabled {
			return a, nil
		}
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		return a.jumpToTmuxPane(sess.ProjectPath, sess.ID)
	case "L":
		if !a.config.TmuxEnabled {
			return a, nil
		}
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		return a.openLivePreview(sess)
	case "e":
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		return a.openEditMenu(sess)
	case "G":
		a.sessGroupMode = (a.sessGroupMode + 1) % 3
		a.rebuildSessionList()
		return a, nil
	case "R":
		cmd := a.doRefresh()
		a.copiedMsg = "Refreshed"
		return a, cmd
	case "S":
		return a.openGlobalStats()
	case "?":
		a.showHelp = true
		return a, nil
	// Session has custom tab/shift+tab (mode cycling)
	case "tab":
		if !sp.Show {
			idx := a.sessionList.Index()
			sp.Show = true
			sp.CacheKey = ""
			contentH := a.height - 3
			a.sessionList.SetSize(sp.ListWidth(a.width, a.splitRatio), contentH)
			a.sessionList.Select(idx)
		} else {
			a.cycleSessionPreviewMode()
		}
		return a, nil
	case "shift+tab":
		if sp.Show {
			a.cycleSessionPreviewModeReverse()
		}
		return a, nil
	case "left":
		if sp.Focus && sp.Show && a.sessPreviewMode == sessPreviewConversation {
			// Delegate to conversation handler (collapse), fall through below
		} else if sp.Focus && sp.Show {
			sp.Focus = false
			return a, nil
		} else if !sp.Focus && sp.Show {
			idx := a.sessionList.Index()
			sp.Show = false
			contentH := max(a.height-3, 1)
			a.sessionList.SetSize(sp.ListWidth(a.width, a.splitRatio), contentH)
			a.sessionList.Select(idx)
			return a, nil
		}
	case "right":
		if sp.Focus && sp.Show && a.sessPreviewMode == sessPreviewConversation {
			// Delegate to conversation handler (expand), fall through below
		} else {
			if !sp.Show {
				idx := a.sessionList.Index()
				sp.Show = true
				sp.CacheKey = ""
				contentH := max(a.height-3, 1)
				a.sessionList.SetSize(sp.ListWidth(a.width, a.splitRatio), contentH)
				a.sessionList.Select(idx)
			}
			sp.Focus = true
			return a, nil
		}
	case "[":
		if sp.Show {
			a.adjustSplitRatio(-5) // preview larger
		}
		return a, nil
	case "]":
		if sp.Show {
			a.adjustSplitRatio(5) // preview smaller
		}
		return a, nil
	}

	// Focused preview: custom conversation nav or simple scroll
	if sp.Focus && sp.Show {
		if m, cmd, handled := a.handleFocusedPreviewKeys(sp, key); handled {
			return m, cmd
		}
	}

	// List boundary (up/down always navigate list, scroll preview at edges)
	if !sp.Focus && sp.HandleListBoundary(key) {
		a.updateSessionPreview()
		return a, nil
	}

	// Default list update
	oldIdx := a.sessionList.Index()
	m, cmd := a.updateSessionList(msg)
	newIdx := a.sessionList.Index()
	if sp.Show && oldIdx == newIdx {
		switch key {
		case "down", "up", "pgdown", "pgup":
			scrollPreview(&sp.Preview, key)
			return a, nil
		}
	}
	a.updateSessionPreview()
	return m, cmd
}

// handleFocusedPreviewKeys handles keys when the session preview pane is focused.
// Returns (model, cmd, handled). If handled is false, the caller should continue processing.
func (a *App) handleFocusedPreviewKeys(sp *SplitPane, key string) (tea.Model, tea.Cmd, bool) {
	if a.sessPreviewMode == sessPreviewConversation && len(a.sessConvEntries) > 0 {
		return a.handleConvPreviewKeys(sp, key)
	}
	switch key {
	case "/":
		sp.Focus = false
		return a, startListSearch(&a.sessionList), true
	case "up", "down", "pgdown", "pgup", "home", "end":
		scrollPreview(&sp.Preview, key)
		a.sessPreviewPinned = !a.sessPreviewAtBottom()
		return a, nil, true
	}
	return a, nil, false
}

// handleConvPreviewKeys handles keys for the conversation preview navigation.
func (a *App) handleConvPreviewKeys(sp *SplitPane, key string) (tea.Model, tea.Cmd, bool) {
	visible := a.convVisibleEntries()
	switch key {
	case "/":
		a.startConvSearch()
		return a, nil, true
	case "esc":
		if a.sessConvFilterTerm != "" {
			a.clearConvFilter()
			return a, nil, true
		}
	case "up":
		if a.sessConvCursor > 0 {
			previewW := max(a.width-sp.ListWidth(a.width, a.splitRatio)-1, 1)
			curLine := convCursorLine(visible, a.sessConvCursor, a.sessConvExpanded, previewW)
			vpTop := sp.Preview.YOffset
			vpBottom := vpTop + sp.Preview.Height
			if curLine < vpTop || curLine >= vpBottom {
				if curLine >= vpBottom {
					a.sessConvCursor = convLastVisible(visible, a.sessConvExpanded, previewW, vpTop, vpBottom)
				} else {
					a.sessConvCursor = convFirstVisible(visible, a.sessConvExpanded, previewW, vpTop, vpBottom)
				}
			} else {
				a.sessConvCursor--
			}
			a.sessPreviewPinned = true
			a.refreshConvPreview()
		}
		return a, nil, true
	case "down":
		if a.sessConvCursor < len(visible)-1 {
			previewW := max(a.width-sp.ListWidth(a.width, a.splitRatio)-1, 1)
			curLine := convCursorLine(visible, a.sessConvCursor, a.sessConvExpanded, previewW)
			vpTop := sp.Preview.YOffset
			vpBottom := vpTop + sp.Preview.Height
			if curLine < vpTop || curLine >= vpBottom {
				if curLine < vpTop {
					a.sessConvCursor = convFirstVisible(visible, a.sessConvExpanded, previewW, vpTop, vpBottom)
				} else {
					a.sessConvCursor = convLastVisible(visible, a.sessConvExpanded, previewW, vpTop, vpBottom)
				}
			} else {
				a.sessConvCursor++
			}
			a.refreshConvPreview()
		}
		a.sessPreviewPinned = a.sessConvCursor < len(visible)-1
		return a, nil, true
	case "enter":
		m, cmd := a.jumpToConvMessage()
		return m, cmd, true
	case "right":
		if a.sessConvExpanded == nil {
			a.sessConvExpanded = make(map[int]bool)
		}
		a.sessConvExpanded[a.sessConvCursor] = true
		a.refreshConvPreview()
		return a, nil, true
	case "left":
		if a.sessConvExpanded != nil && a.sessConvExpanded[a.sessConvCursor] {
			delete(a.sessConvExpanded, a.sessConvCursor)
			a.refreshConvPreview()
		} else {
			sp.Focus = false
		}
		return a, nil, true
	case "f":
		a.sessConvExpanded = nil
		a.refreshConvPreview()
		return a, nil, true
	case "F":
		a.sessConvExpanded = make(map[int]bool)
		for i := range visible {
			a.sessConvExpanded[i] = true
		}
		a.refreshConvPreview()
		return a, nil, true
	case "pgdown":
		scrollPreview(&sp.Preview, "pgdown")
		a.sessPreviewPinned = !a.sessPreviewAtBottom()
		return a, nil, true
	case "pgup":
		scrollPreview(&sp.Preview, "pgup")
		a.sessPreviewPinned = true
		return a, nil, true
	case "home":
		a.sessConvCursor = 0
		a.sessPreviewPinned = true
		a.refreshConvPreview()
		return a, nil, true
	case "end":
		a.sessConvCursor = len(visible) - 1
		a.sessPreviewPinned = false
		a.refreshConvPreview()
		return a, nil, true
	}
	return a, nil, false
}

// --- Actions ---

func (a *App) openProjectDir(projectPath string) (tea.Model, tea.Cmd) {
	if projectPath == "" {
		a.copiedMsg = "no project path"
		return a, nil
	}
	if _, err := os.Stat(projectPath); os.IsNotExist(err) {
		a.copiedMsg = "dir not found"
		return a, nil
	}

	// In tmux: open a split pane at the project path, keep CSB running
	if inTmux() {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/bash"
		}
		cmd := "cd " + shellQuote(projectPath) + " && " + shell
		err := exec.Command("tmux", "split-window", "-h", "-l", "50%", cmd).Run()
		if err != nil {
			a.copiedMsg = "tmux split failed"
		}
		return a, nil
	}

	// Non-tmux: take over CSB with a shell
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

func (a *App) openGlobalStats() (tea.Model, tea.Cmd) {
	if a.globalStatsCache != nil {
		// Already computed, reuse
		contentH := a.height - 3
		a.globalStatsVP = viewport.New(a.width, contentH)
		a.globalStatsVP.SetContent(renderGlobalStats(*a.globalStatsCache, a.width))
		a.state = viewGlobalStats
		return a, nil
	}

	a.globalStatsLoading = true
	a.spinnerFrame = 0
	// Stay on current view while scanning — loading shows in title bar

	sessions := a.sessions
	return a, tea.Batch(
		spinnerTickCmd(),
		func() tea.Msg {
			return globalStatsMsg(session.AggregateStats(sessions))
		},
	)
}

func (a *App) handleGlobalStatsKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return a, tea.Quit
	case "esc":
		a.state = viewSessions
		return a, nil
	}
	var cmd tea.Cmd
	a.globalStatsVP, cmd = a.globalStatsVP.Update(msg)
	return a, cmd
}

// --- Live Preview (tmux capture in split pane) ---

func (a *App) openLivePreview(sess session.Session) (tea.Model, tea.Cmd) {
	if !sess.IsLive {
		a.copiedMsg = "not a live session"
		return a, nil
	}
	pane, found := findTmuxPane(sess.ProjectPath, sess.ID)
	if !found {
		a.copiedMsg = "tmux pane not found"
		return a, nil
	}
	a.livePreviewPane = pane
	a.livePreviewSessID = sess.ID
	a.toggleSessionPreviewMode(sessPreviewLive)
	a.refreshLivePreview()
	return a, liveTickCmd()
}

func (a *App) refreshLivePreview() {
	content, err := tmuxCapturePane(a.livePreviewPane)
	if err != nil {
		a.sessSplit.Preview.SetContent(dimStyle.Render("(capture failed)"))
		return
	}
	a.sessSplit.Preview.SetContent(content)
	a.sessSplit.Preview.GotoBottom()
}

func (a *App) resumeSession(sess session.Session) (tea.Model, tea.Cmd) {
	dir := sess.ProjectPath
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}

	if sess.IsLive {
		// Live session: jump to existing pane
		pane, found := findTmuxPane(sess.ProjectPath, sess.ID)
		if found {
			if err := switchToTmuxPane(pane); err != nil {
				a.copiedMsg = "Switch failed"
			}
			return a, nil
		}
		// Fallback: take over CSB
		c := exec.Command("claude", "--resume", sess.ID)
		c.Dir = dir
		return a, tea.ExecProcess(c, func(err error) tea.Msg {
			return tea.QuitMsg{}
		})
	}

	// Non-live session in tmux: spawn a new tmux window
	if inTmux() {
		windowName := sess.ProjectName
		if windowName == "" {
			windowName = "claude"
		}
		if err := tmuxNewWindowClaude(windowName, dir, sess.ID); err != nil {
			a.copiedMsg = "Spawn failed"
		} else {
			a.copiedMsg = "Resumed in new window"
		}
		return a, nil
	}

	// Non-tmux: take over CSB
	c := exec.Command("claude", "--resume", sess.ID)
	c.Dir = dir
	return a, tea.ExecProcess(c, func(err error) tea.Msg {
		return tea.QuitMsg{}
	})
}

// --- Edit file with $EDITOR ---

type editChoice struct {
	key   string // "m", "t", "k", "p"
	label string // "memory", "todos", "tasks", "plan"
	path  string // file path to open
}

func editableFiles(sess session.Session) []editChoice {
	home, _ := os.UserHomeDir()
	var choices []editChoice

	if sess.HasMemory {
		encoded := session.EncodeProjectPath(sess.ProjectPath)
		p := filepath.Join(home, ".claude", "projects", encoded, "memory", "MEMORY.md")
		if _, err := os.Stat(p); err == nil {
			choices = append(choices, editChoice{"m", "memory", p})
		}
	}
	if sess.HasTodos {
		p := filepath.Join(home, ".claude", "todos", sess.ID+"-agent-"+sess.ID+".json")
		if _, err := os.Stat(p); err == nil {
			choices = append(choices, editChoice{"t", "todos", p})
		}
	}
	if sess.HasTasks {
		dir := filepath.Join(home, ".claude", "tasks", sess.ID)
		if _, err := os.Stat(dir); err == nil {
			choices = append(choices, editChoice{"k", "tasks", dir})
		}
	}
	for i, slug := range sess.PlanSlugs {
		p := filepath.Join(home, ".claude", "plans", slug+".md")
		if _, err := os.Stat(p); err == nil {
			key := "p"
			label := "plan"
			if len(sess.PlanSlugs) > 1 {
				key = fmt.Sprintf("p%d", i+1)
				label = fmt.Sprintf("plan %d (%s)", i+1, slug)
			}
			choices = append(choices, editChoice{key, label, p})
		}
	}
	// Session JSONL file itself
	choices = append(choices, editChoice{"s", "session", sess.FilePath})

	return choices
}

func (a *App) openEditMenu(sess session.Session) (tea.Model, tea.Cmd) {
	choices := editableFiles(sess)
	if len(choices) == 0 {
		a.copiedMsg = "No editable files"
		return a, nil
	}
	// If only the session file is available, open it directly
	if len(choices) == 1 {
		return a.openInEditor(choices[0].path)
	}
	a.editMenu = true
	a.editSess = sess
	a.editChoices = choices

	parts := make([]string, len(choices))
	for i, c := range choices {
		parts[i] = c.key + ":" + c.label
	}
	a.copiedMsg = "Edit: " + strings.Join(parts, " ")
	return a, nil
}

func (a *App) handleEditMenu(key string) (tea.Model, tea.Cmd) {
	a.editMenu = false
	for _, c := range a.editChoices {
		if c.key == key {
			return a.openInEditor(c.path)
		}
	}
	a.copiedMsg = ""
	return a, nil
}

func (a *App) handleActionsMenu(key string) (tea.Model, tea.Cmd) {
	a.actionsMenu = false
	a.copiedMsg = ""
	sess := a.actionsSess
	switch key {
	case "d":
		if sess.IsLive {
			a.copiedMsg = "Cannot delete live session"
			return a, nil
		}
		return a.deleteSession(sess)
	case "r":
		return a.resumeSession(sess)
	case "m":
		if sess.ProjectPath == "" {
			a.copiedMsg = "No project path"
			return a, nil
		}
		if sess.IsLive {
			a.copiedMsg = "Cannot move live session"
			return a, nil
		}
		a.moveSess = sess
		a.moveMode = true
		ti := textinput.New()
		ti.Prompt = "Move to: "
		ti.SetValue(sess.ProjectPath)
		ti.Width = a.width - 12
		ti.Focus()
		a.moveInput = ti
		return a, ti.Cursor.BlinkCmd()
	case "w":
		if sess.ProjectPath == "" {
			a.copiedMsg = "Not a git repo"
			return a, nil
		}
		if sess.IsLive {
			a.copiedMsg = "Cannot create worktree for live session"
			return a, nil
		}
		if sess.IsWorktree {
			a.copiedMsg = "Already a worktree"
			return a, nil
		}
		gitPath := filepath.Join(sess.ProjectPath, ".git")
		info, err := os.Stat(gitPath)
		if err != nil || !info.IsDir() {
			a.copiedMsg = "Not a git repo"
			return a, nil
		}
		a.worktreeSess = sess
		a.worktreeMode = true
		ti := textinput.New()
		ti.Prompt = "Worktree name: "
		name := sess.GitBranch
		if name == "" {
			name = sess.ShortID
		}
		name = strings.NewReplacer("/", "-", " ", "-").Replace(name)
		ti.SetValue(name)
		ti.Width = a.width - 20
		ti.Focus()
		a.worktreeInput = ti
		return a, ti.Cursor.BlinkCmd()
	}
	return a, nil
}

func (a *App) openInEditor(path string) (tea.Model, tea.Cmd) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	return a, tea.ExecProcess(c, func(err error) tea.Msg {
		return editorDoneMsg{}
	})
}

type editorDoneMsg struct{}

func (a *App) deleteSession(sess session.Session) (tea.Model, tea.Cmd) {
	if err := os.Remove(sess.FilePath); err != nil && !os.IsNotExist(err) {
		a.copiedMsg = "Delete failed: " + err.Error()
		return a, nil
	}

	// Remove from in-memory list and update the list widget
	idx := a.sessionList.Index()
	var remaining []session.Session
	for _, s := range a.sessions {
		if s.ID != sess.ID {
			remaining = append(remaining, s)
		}
	}
	a.sessions = remaining

	// Clear any active filter before rebuilding — the deleted item may have
	// been the last match, leaving an empty "No items" filtered view.
	if a.hasFilterApplied() {
		a.sessionList.ResetFilter()
	}

	items := buildGroupedItems(remaining, a.sessGroupMode)
	a.sessionList.SetItems(items)
	if idx >= len(items) {
		idx = len(items) - 1
	}
	if idx >= 0 {
		a.sessionList.Select(idx)
	}
	a.sessSplit.CacheKey = ""
	a.copiedMsg = "Session deleted"
	return a, nil
}

func (a *App) handleMoveInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		newPath := strings.TrimSpace(a.moveInput.Value())
		a.moveMode = false
		if newPath == "" || newPath == a.moveSess.ProjectPath {
			return a, nil
		}
		return a.executeMove(a.moveSess, newPath)
	case "esc":
		a.moveMode = false
		return a, nil
	}
	var cmd tea.Cmd
	a.moveInput, cmd = a.moveInput.Update(msg)
	return a, cmd
}

func (a *App) executeMove(sess session.Session, newPath string) (tea.Model, tea.Cmd) {
	oldPath := sess.ProjectPath
	if err := session.MoveProject(oldPath, newPath); err != nil {
		a.copiedMsg = "Move failed: " + err.Error()
		return a, nil
	}

	home, _ := os.UserHomeDir()
	newName := session.ShortenPath(newPath, home)

	// Update all in-memory sessions that share the old project path
	for i := range a.sessions {
		if a.sessions[i].ProjectPath == oldPath {
			a.sessions[i].ProjectPath = newPath
			a.sessions[i].ProjectName = newName
			// Update FilePath: ~/.claude/projects/<new-encoded>/<filename>
			oldEncoded := session.EncodeProjectPath(oldPath)
			newEncoded := session.EncodeProjectPath(newPath)
			a.sessions[i].FilePath = strings.Replace(a.sessions[i].FilePath, oldEncoded, newEncoded, 1)
		}
	}

	// Rebuild list items
	items := make([]list.Item, len(a.sessions))
	for i, s := range a.sessions {
		items[i] = sessionItem{sess: s}
	}
	idx := a.sessionList.Index()
	a.sessionList.SetItems(items)
	a.sessionList.Select(idx)
	a.sessSplit.CacheKey = ""
	a.copiedMsg = fmt.Sprintf("Moved → %s", newName)
	return a, nil
}

func (a *App) handleWorktreeInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		name := strings.TrimSpace(a.worktreeInput.Value())
		a.worktreeMode = false
		if name == "" {
			return a, nil
		}
		return a.executeWorktree(a.worktreeSess, name)
	case "esc":
		a.worktreeMode = false
		return a, nil
	}
	var cmd tea.Cmd
	a.worktreeInput, cmd = a.worktreeInput.Update(msg)
	return a, cmd
}

func (a *App) executeWorktree(sess session.Session, name string) (tea.Model, tea.Cmd) {
	// Get repo root
	out, err := exec.Command("git", "-C", sess.ProjectPath, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		a.copiedMsg = "Not a git repo"
		return a, nil
	}
	repoRoot := strings.TrimSpace(string(out))

	// Determine branch
	branch := sess.GitBranch
	if branch == "" {
		bOut, err := exec.Command("git", "-C", sess.ProjectPath, "branch", "--show-current").Output()
		if err != nil {
			a.copiedMsg = "Cannot determine branch"
			return a, nil
		}
		branch = strings.TrimSpace(string(bOut))
	}

	wtPath := filepath.Join(repoRoot, a.config.WorktreeDir, name)

	// Try adding worktree on existing branch first; if it fails because
	// the branch is already checked out, create a new branch from it.
	if err := exec.Command("git", "-C", repoRoot, "worktree", "add", wtPath, branch).Run(); err != nil {
		if err2 := exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", name, wtPath, branch).Run(); err2 != nil {
			a.copiedMsg = "Worktree failed: " + err2.Error()
			return a, nil
		}
	}

	// Move session data to the new worktree path
	oldPath := sess.ProjectPath
	if err := session.MoveProject(oldPath, wtPath); err != nil {
		a.copiedMsg = "Move failed: " + err.Error()
		return a, nil
	}

	home, _ := os.UserHomeDir()
	newName := session.ShortenPath(wtPath, home)

	// Update in-memory sessions
	for i := range a.sessions {
		if a.sessions[i].ProjectPath == oldPath {
			a.sessions[i].ProjectPath = wtPath
			a.sessions[i].ProjectName = newName
			a.sessions[i].IsWorktree = true
			oldEncoded := session.EncodeProjectPath(oldPath)
			newEncoded := session.EncodeProjectPath(wtPath)
			a.sessions[i].FilePath = strings.Replace(a.sessions[i].FilePath, oldEncoded, newEncoded, 1)
		}
	}

	a.rebuildSessionList()
	a.copiedMsg = fmt.Sprintf("Worktree created → %s/%s", a.config.WorktreeDir, name)
	return a, nil
}

func (a *App) jumpToTmuxPane(projectPath string, sessionID ...string) (tea.Model, tea.Cmd) {
	pane, found := findTmuxPane(projectPath, sessionID...)
	if !found {
		a.copiedMsg = "No tmux pane found"
		return a, nil
	}
	if err := switchToTmuxPane(pane); err != nil {
		a.copiedMsg = "Switch failed"
		return a, nil
	}
	return a, nil
}

func (a *App) sendInputToLive(projectPath string, sessionID ...string) (tea.Model, tea.Cmd) {
	if !inTmux() {
		a.copiedMsg = "Requires tmux"
		return a, nil
	}
	pane, found := findTmuxPane(projectPath, sessionID...)
	if !found || !hasClaude(pane.PID) {
		a.copiedMsg = "No live Claude pane"
		return a, nil
	}
	a.liveInputActive = true
	a.liveInputPane = pane
	ti := textinput.New()
	ti.Prompt = "Send to Claude: "
	ti.Width = a.width - 20
	ti.Focus()
	a.liveInputModel = ti
	return a, ti.Cursor.BlinkCmd()
}

func (a *App) handleLiveInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		text := a.liveInputModel.Value()
		a.liveInputActive = false
		if text == "" {
			return a, nil
		}
		if err := tmuxSendKeys(a.liveInputPane, text); err != nil {
			a.copiedMsg = "Send failed"
			return a, nil
		}
		a.copiedMsg = "Sent!"
		return a, nil
	case "esc":
		a.liveInputActive = false
		return a, nil
	}
	var cmd tea.Cmd
	a.liveInputModel, cmd = a.liveInputModel.Update(msg)
	return a, cmd
}

// --- Live refresh ---

func (a *App) handleTick() tea.Cmd {
	// Always refresh conversation preview for live sessions (regardless of liveUpdate)
	if a.state == viewSessions && a.sessSplit.Show && a.sessPreviewMode == sessPreviewConversation {
		if sess, ok := a.selectedSession(); ok && sess.IsLive {
			a.sessSplit.CacheKey = "" // invalidate to force re-fetch
			a.updateSessionPreview()
		}
	}
	if !a.liveUpdate {
		return nil
	}
	return a.doRefresh()
}

func (a *App) doRefresh() tea.Cmd {
	switch a.state {
	case viewSessions:
		// Update ModTime and IsLive flags
		needsSort := false
		needsRefresh := false
		for i := range a.sessions {
			info, err := os.Stat(a.sessions[i].FilePath)
			if err != nil {
				continue
			}
			if !info.ModTime().Equal(a.sessions[i].ModTime) {
				a.sessions[i].ModTime = info.ModTime()
				needsSort = true
			}
		}
		// Snapshot old live state, clear, and re-detect
		type liveState struct{ live, responding bool }
		oldLive := make([]liveState, len(a.sessions))
		for i := range a.sessions {
			oldLive[i] = liveState{a.sessions[i].IsLive, a.sessions[i].IsResponding}
			a.sessions[i].IsLive = false
			a.sessions[i].IsResponding = false
		}
		markLiveSessions(a.sessions)
		for i := range a.sessions {
			if a.sessions[i].IsLive != oldLive[i].live {
				needsSort = true
			}
			if a.sessions[i].IsResponding != oldLive[i].responding {
				needsRefresh = true
			}
		}
		if needsSort && !a.isFiltering() && !a.hasFilterApplied() {
			// Remember currently selected session so cursor follows it after re-sort
			selectedID := ""
			if sess, ok := a.selectedSession(); ok {
				selectedID = sess.ID
			}

			sort.Slice(a.sessions, func(i, j int) bool {
				return a.sessions[i].ModTime.After(a.sessions[j].ModTime)
			})

			items := buildGroupedItems(a.sessions, a.sessGroupMode)
			newIdx := 0
			for i, item := range items {
				if si, ok := item.(sessionItem); ok && si.sess.ID == selectedID {
					newIdx = i
					break
				}
			}
			a.sessionList.SetItems(items)
			a.sessionList.Select(newIdx)
		} else if needsRefresh && !a.isFiltering() && !a.hasFilterApplied() {
			// Badge-only change: update items without re-sorting
			items := buildGroupedItems(a.sessions, a.sessGroupMode)
			idx := a.sessionList.Index()
			a.sessionList.SetItems(items)
			a.sessionList.Select(idx)
		}
		// Refresh preview for live sessions (auto-scroll to bottom)
		a.refreshSessionPreviewLive()
	}

	return nil
}


// handleLiveTail refreshes messages and snaps to the latest message + updates preview.
func (a *App) handleLiveTail() {
	switch a.state {
	case viewConversation:
		sp := &a.conv.split
		oldCK := sp.CacheKey
		oldIdx := a.convList.Index()

		a.refreshConversation()
		visItems := a.convList.Items()
		if len(visItems) == 0 {
			debugLog.Printf("handleLiveTail: no visItems")
			return
		}
		// Select the last convMsg item (skip trailing agent/task sub-items)
		lastMsg := len(visItems) - 1
		for i := len(visItems) - 1; i >= 0; i-- {
			if ci, ok := visItems[i].(convItem); ok && ci.kind == convMsg {
				lastMsg = i
				break
			}
		}
		a.convList.Select(lastMsg)

		debugLog.Printf("handleLiveTail: oldIdx=%d newIdx=%d visItems=%d show=%v oldCK=%q",
			oldIdx, lastMsg, len(visItems), sp.Show, oldCK)

		a.updateConvPreview()

		debugLog.Printf("handleLiveTail: after updateConvPreview CK=%q YOffset=%d blockCursor=%d totalLines=%d height=%d",
			sp.CacheKey, sp.Preview.YOffset,
			func() int { if sp.Folds != nil { return sp.Folds.BlockCursor }; return -1 }(),
			sp.Preview.TotalLineCount(), sp.Preview.Height)

		a.scrollConvPreviewToTail()

		debugLog.Printf("handleLiveTail: after scrollToTail YOffset=%d blockCursor=%d",
			sp.Preview.YOffset,
			func() int { if sp.Folds != nil { return sp.Folds.BlockCursor }; return -1 }())
	}
}

func (a *App) toggleLiveTail() (tea.Model, tea.Cmd) {
	a.liveTail = !a.liveTail
	if a.liveTail {
		a.copiedMsg = "Live tail ON"
		a.handleLiveTail()
		return a, liveTickCmd()
	}
	a.copiedMsg = "Live tail OFF"
	return a, nil
}



func vpAtBottom(vp *viewport.Model) bool {
	total := vp.TotalLineCount()
	h := vp.Height
	if total <= h {
		return true
	}
	return vp.YOffset >= total-h
}

// liveBadgeText returns "[LIVE]" badge text for the conversation view.
func (a *App) liveBadgeText() string {
	return "[LIVE]"
}

// refreshActivePreview re-renders the preview for the current view state.
func (a *App) refreshActivePreview() {
	switch a.state {
	case viewSessions:
		a.updateSessionPreview()
	case viewConversation:
		a.conv.split.RefreshFoldPreview(a.width, a.splitRatio)
	}
}

// --- Session split pane ---

func (a *App) renderSessionSplit() string {
	if a.sessionList.Width() == 0 {
		return ""
	}
	clampPaginator(&a.sessionList)
	if !a.sessSplit.Show || a.width < 40 || a.height < 10 {
		return a.sessionList.View()
	}

	listW := a.sessSplit.ListWidth(a.width, a.splitRatio)
	previewW := max(a.width-listW-1, 1)
	contentH := max(a.height-3, 1)

	if a.sessionList.Width() > 0 && (a.sessionList.Width() != listW || a.sessionList.Height() != contentH) {
		idx := a.sessionList.Index()
		a.sessionList.SetSize(listW, contentH)
		a.sessionList.Select(idx)
	}

	a.updateSessionPreview()

	if a.sessSplit.Preview.Width != previewW || a.sessSplit.Preview.Height != contentH {
		oldOffset := a.sessSplit.Preview.YOffset
		oldTotal := a.sessSplit.Preview.TotalLineCount()
		a.sessSplit.Preview.Width = previewW
		a.sessSplit.Preview.Height = max(contentH, 1)
		// Re-render at new size without reloading data or resetting cursor
		if a.sessPreviewMode == sessPreviewConversation && len(a.sessConvEntries) > 0 {
			a.refreshConvPreview()
		} else {
			a.sessSplit.CacheKey = ""
			a.updateSessionPreview()
			if a.sessPreviewMode == sessPreviewLive {
				a.sessSplit.Preview.GotoBottom()
			} else {
				// Restore scroll position proportionally after re-render
				newTotal := a.sessSplit.Preview.TotalLineCount()
				maxOff := max(newTotal-a.sessSplit.Preview.Height, 0)
				if oldTotal > 0 {
					prop := float64(oldOffset) / float64(oldTotal)
					a.sessSplit.Preview.YOffset = min(int(prop*float64(newTotal)+0.5), maxOff)
				} else {
					a.sessSplit.Preview.YOffset = min(oldOffset, maxOff)
				}
			}
		}
	}

	// Live preview: always snap to bottom after all updates/resizes
	if a.sessPreviewMode == sessPreviewLive && a.livePreviewSessID != "" {
		a.sessSplit.Preview.GotoBottom()
	}

	borderColor := colorBorderDim
	if a.sessSplit.Focus {
		borderColor = colorBorderFocused
	}

	leftStyle := lipgloss.NewStyle().Width(listW).MaxWidth(listW).Height(contentH).MaxHeight(contentH)
	rightStyle := lipgloss.NewStyle().Width(previewW).MaxWidth(previewW).Height(contentH).MaxHeight(contentH)
	borderStyle := lipgloss.NewStyle().Foreground(borderColor).Height(contentH).MaxHeight(contentH)

	clampPaginator(&a.sessionList)

	left := leftStyle.Render(a.sessionList.View())
	border := borderStyle.Render(strings.Repeat("│\n", max(contentH-1, 0)) + "│")
	right := rightStyle.Render(a.sessSplit.Preview.View())

	return lipgloss.JoinHorizontal(lipgloss.Top, left, border, right)
}

// toggleSessionPreviewMode switches session preview to the given mode,
// or back to messages if already in that mode. Opens preview if closed.
func (a *App) toggleSessionPreviewMode(mode sessPreview) {
	if !a.sessSplit.Show {
		idx := a.sessionList.Index()
		a.sessSplit.Show = true
		a.sessSplit.CacheKey = ""
		contentH := max(a.height-3, 1)
		a.sessionList.SetSize(a.sessSplit.ListWidth(a.width, a.splitRatio), contentH)
		a.sessionList.Select(idx)
	}
	if a.sessPreviewMode == mode {
		a.sessPreviewMode = sessPreviewConversation
	} else {
		a.sessPreviewMode = mode
	}
	a.sessSplit.CacheKey = "" // force re-render
}

// cycleSessionPreviewMode advances to the next preview tab.
// Skips sessPreviewLive — it's only entered via the L key.
func (a *App) cycleSessionPreviewMode() {
	a.sessPreviewMode = (a.sessPreviewMode + 1) % numSessPreviewModes
	if a.sessPreviewMode == sessPreviewLive {
		a.sessPreviewMode = (a.sessPreviewMode + 1) % numSessPreviewModes
	}
	a.livePreviewSessID = ""
	a.sessSplit.CacheKey = ""
}

// cycleSessionPreviewModeReverse goes to the previous preview tab.
// Skips sessPreviewLive — it's only entered via the L key.
func (a *App) cycleSessionPreviewModeReverse() {
	a.sessPreviewMode = (a.sessPreviewMode + numSessPreviewModes - 1) % numSessPreviewModes
	if a.sessPreviewMode == sessPreviewLive {
		a.sessPreviewMode = (a.sessPreviewMode + numSessPreviewModes - 1) % numSessPreviewModes
	}
	a.livePreviewSessID = ""
	a.sessSplit.CacheKey = ""
}

func (a *App) updateSessionPreview() {
	if !a.sessSplit.Show {
		return
	}
	sess, ok := a.selectedSession()
	if !ok {
		return
	}

	cacheKey := fmt.Sprintf("%d:%s", a.sessPreviewMode, sess.ID)
	if cacheKey == a.sessSplit.CacheKey {
		return
	}

	// If conversation data is already loaded for this session, just re-render
	// at the new size without reloading data or resetting the cursor.
	if a.sessPreviewMode == sessPreviewConversation && len(a.sessConvEntries) > 0 && a.sessConvCacheID == sess.ID {
		a.sessSplit.CacheKey = cacheKey
		a.refreshConvPreview()
		return
	}

	a.sessSplit.CacheKey = cacheKey
	a.sessPreviewPinned = false

	switch a.sessPreviewMode {
	case sessPreviewStats:
		a.updateSessionStatsPreview(sess)
	case sessPreviewMemory:
		a.updateSessionMemoryPreview(sess)
	case sessPreviewTasksPlan:
		a.updateSessionTasksPlanPreview(sess)
	case sessPreviewLive:
		if sess.IsLive {
			if pane, found := findTmuxPane(sess.ProjectPath, sess.ID); found {
				a.livePreviewPane = pane
				a.livePreviewSessID = sess.ID
				a.refreshLivePreview()
			} else {
				a.livePreviewSessID = ""
				a.sessSplit.Preview.SetContent(dimStyle.Render("(tmux pane not found)"))
			}
		} else {
			a.livePreviewSessID = ""
			a.sessSplit.Preview.SetContent(dimStyle.Render("(not a live session)"))
		}
	default:
		a.updateSessionConvPreview(sess)
	}
}

func (a *App) updateSessionConvPreview(sess session.Session) {
	const previewHead, previewTail = 50, 50
	head, tail, total, err := session.LoadMessagesSummary(sess.FilePath, previewHead, previewTail)
	if err != nil || total == 0 {
		a.sessSplit.Preview.SetContent(dimStyle.Render("(no messages)"))
		a.sessConvEntries = nil
		a.sessConvFiltered = nil
		a.sessConvFilterTerm = ""
		return
	}

	// Merge head and tail separately, join with gap indicator
	headMerged := mergeConversationTurns(head)
	var merged []mergedMsg
	if len(tail) == 0 {
		merged = headMerged
	} else {
		tailMerged := mergeConversationTurns(tail)
		// Adjust tail startIdx/endIdx to reflect position in full file
		tailOffset := total - len(tail)
		for i := range tailMerged {
			tailMerged[i].startIdx += tailOffset
			tailMerged[i].endIdx += tailOffset
		}
		merged = append(headMerged, tailMerged...)
	}
	a.sessConvEntries = filterConversation(merged)
	a.sessConvCacheID = sess.ID

	if len(a.sessConvEntries) == 0 {
		a.sessSplit.Preview.SetContent(dimStyle.Render("(no messages)"))
		return
	}

	// Reset state; start cursor at bottom for live sessions
	a.sessConvExpanded = nil
	a.sessConvFiltered = nil
	a.sessConvFilterTerm = ""
	a.sessConvSearching = false

	visible := a.sessConvEntries
	if sess.IsLive {
		a.sessConvCursor = len(visible) - 1
	} else {
		a.sessConvCursor = 0
	}

	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)
	rendered := renderConversationPreview(visible, previewW, a.sessConvCursor, a.sessConvExpanded, a.sessConvFilterTerm, sess.IsLive)

	// Prepend todo progress header if session has todos
	content := rendered
	if len(sess.Todos) > 0 {
		completed := 0
		for _, t := range sess.Todos {
			if t.Status == "completed" {
				completed++
			}
		}
		header := dimStyle.Render(fmt.Sprintf("── Todos [%d/%d] ──", completed, len(sess.Todos))) + "\n\n"
		content = header + content
	}

	a.sessSplit.Preview = viewport.New(previewW, contentH)
	a.sessSplit.Preview.SetContent(content)
	if sess.IsLive {
		a.sessSplit.Preview.GotoBottom()
	}
}

// convVisibleEntries returns the entries to display: filtered if a filter is
// applied, otherwise all entries. When a filter term is set but nothing matches,
// returns an empty slice (not all entries).
func (a *App) convVisibleEntries() []mergedMsg {
	if a.sessConvFilterTerm != "" {
		visible := make([]mergedMsg, len(a.sessConvFiltered))
		for i, idx := range a.sessConvFiltered {
			visible[i] = a.sessConvEntries[idx]
		}
		return visible
	}
	return a.sessConvEntries
}

// refreshConvPreview re-renders the conversation preview without reloading entries.
func (a *App) refreshConvPreview() {
	visible := a.convVisibleEntries()
	isLive := false
	if sess, ok := a.selectedSession(); ok {
		isLive = sess.IsLive
	}
	if len(visible) == 0 {
		a.sessSplit.Preview.SetContent(dimStyle.Render("(no matches)"))
		return
	}
	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	content := renderConversationPreview(visible, previewW, a.sessConvCursor, a.sessConvExpanded, a.sessConvFilterTerm, isLive)
	oldOffset := a.sessSplit.Preview.YOffset
	a.sessSplit.Preview.SetContent(content)

	// Scroll to keep cursor visible: estimate cursor line position
	cursorLine := convCursorLine(visible, a.sessConvCursor, a.sessConvExpanded, previewW)
	vpH := a.sessSplit.Preview.Height
	totalLines := strings.Count(content, "\n") + 1
	maxOffset := max(totalLines-vpH, 0)

	if cursorLine < oldOffset {
		a.sessSplit.Preview.YOffset = max(cursorLine-1, 0)
	} else if cursorLine >= oldOffset+vpH {
		a.sessSplit.Preview.YOffset = min(cursorLine-vpH/2, maxOffset)
	} else {
		a.sessSplit.Preview.YOffset = min(oldOffset, maxOffset)
	}
}

// startConvSearch activates the search input for the conversation preview.
func (a *App) startConvSearch() {
	a.sessConvSearching = true
	ti := textinput.New()
	ti.Prompt = "Search: "
	ti.Focus()
	a.sessConvSearchInput = ti
}

// handleConvSearch processes keys while the conversation preview search is active.
func (a *App) handleConvSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		term := a.sessConvSearchInput.Value()
		a.sessConvSearching = false
		if term == "" {
			a.clearConvFilter()
		} else {
			a.sessConvFilterTerm = term
			a.applyConvFilter(term)
		}
		return a, nil
	case "esc":
		a.sessConvSearching = false
		return a, nil
	}
	var cmd tea.Cmd
	a.sessConvSearchInput, cmd = a.sessConvSearchInput.Update(msg)
	// Live filter as user types
	term := a.sessConvSearchInput.Value()
	if term != "" {
		a.applyConvFilter(term)
	} else {
		a.sessConvFiltered = nil
		a.sessConvCursor = 0
		a.refreshConvPreview()
	}
	return a, cmd
}

// applyConvFilter filters conversation entries by the search term.
func (a *App) applyConvFilter(term string) {
	lower := strings.ToLower(term)
	a.sessConvFiltered = nil
	for i, m := range a.sessConvEntries {
		text := strings.ToLower(session.EntryPreview(m.entry))
		role := m.entry.Role
		tools := strings.ToLower(mergedToolSummary(m.entry))
		if strings.Contains(text, lower) || strings.Contains(role, lower) || strings.Contains(tools, lower) {
			a.sessConvFiltered = append(a.sessConvFiltered, i)
		}
	}
	visible := a.convVisibleEntries()
	if a.sessConvCursor >= len(visible) {
		a.sessConvCursor = max(len(visible)-1, 0)
	}
	a.sessConvExpanded = nil
	a.refreshConvPreview()
}

// clearConvFilter removes the conversation preview filter.
func (a *App) clearConvFilter() {
	a.sessConvFilterTerm = ""
	a.sessConvFiltered = nil
	a.sessConvCursor = 0
	a.sessConvExpanded = nil
	a.refreshConvPreview()
}

// convCursorLine estimates the line number where the cursor entry starts.
// Each entry is 1 line, plus wrapped text lines if expanded.
// convMsgLines returns how many rendered lines a single conversation message takes.
func convMsgLines(entry mergedMsg, idx int, expanded map[int]bool, width int) int {
	lines := 1 // the one-line summary
	if expanded != nil && expanded[idx] {
		text := entryFullText(entry.entry)
		if text != "" {
			textW := max(width-4, 10)
			lines += strings.Count(wrapText(text, textW), "\n") + 1
		}
	}
	return lines
}

// convPageDown returns the cursor position after scrolling down by approximately vpHeight lines.
func convPageDown(entries []mergedMsg, cursor int, expanded map[int]bool, width, vpHeight int) int {
	budget := max(vpHeight-2, 1)
	i := cursor
	for i < len(entries)-1 && budget > 0 {
		i++
		budget -= convMsgLines(entries[i], i, expanded, width)
	}
	if i == cursor && i < len(entries)-1 {
		i++ // always move at least one
	}
	return i
}

// convPageUp returns the cursor position after scrolling up by approximately vpHeight lines.
func convPageUp(entries []mergedMsg, cursor int, expanded map[int]bool, width, vpHeight int) int {
	budget := max(vpHeight-2, 1)
	i := cursor
	for i > 0 && budget > 0 {
		i--
		budget -= convMsgLines(entries[i], i, expanded, width)
	}
	if i == cursor && i > 0 {
		i-- // always move at least one
	}
	return i
}

// convFirstVisible returns the index of the first message whose summary line is within [vpTop, vpBottom).
func convFirstVisible(entries []mergedMsg, expanded map[int]bool, width, vpTop, vpBottom int) int {
	line := 0
	textW := max(width-4, 10)
	for i, e := range entries {
		if line >= vpTop && line < vpBottom {
			return i
		}
		line++
		if expanded != nil && expanded[i] {
			text := entryFullText(e.entry)
			if text != "" {
				line += strings.Count(wrapText(text, textW), "\n") + 1
			}
		}
	}
	return len(entries) - 1
}

// convLastVisible returns the index of the last message whose summary line is within [vpTop, vpBottom).
func convLastVisible(entries []mergedMsg, expanded map[int]bool, width, vpTop, vpBottom int) int {
	line := 0
	textW := max(width-4, 10)
	last := 0
	for i, e := range entries {
		if line >= vpBottom {
			break
		}
		if line >= vpTop {
			last = i
		}
		line++
		if expanded != nil && expanded[i] {
			text := entryFullText(e.entry)
			if text != "" {
				line += strings.Count(wrapText(text, textW), "\n") + 1
			}
		}
	}
	return last
}

func convCursorLine(entries []mergedMsg, cursor int, expanded map[int]bool, width int) int {
	line := 0
	textW := max(width-4, 10)
	for i := 0; i < cursor && i < len(entries); i++ {
		line++ // the one-line summary
		if expanded != nil && expanded[i] {
			text := entryFullText(entries[i].entry)
			if text != "" {
				line += strings.Count(wrapText(text, textW), "\n") + 1
			}
		}
	}
	return line
}

// jumpToConvMessage opens the conversation view and selects the message
// corresponding to the current conversation preview cursor.
func (a *App) jumpToConvMessage() (tea.Model, tea.Cmd) {
	visible := a.convVisibleEntries()
	if len(visible) == 0 || a.sessConvCursor >= len(visible) {
		return a, nil
	}

	sess, ok := a.selectedSession()
	if !ok {
		return a, nil
	}

	target := visible[a.sessConvCursor]

	// Clear conversation filter state before switching views
	a.sessConvFilterTerm = ""
	a.sessConvFiltered = nil
	a.sessConvSearching = false

	// Open conversation (loads messages, builds items, creates list)
	cmd := a.openConversation(sess)

	// Find the target message in the conversation list by UUID or timestamp
	bestIdx := 0
	items := a.convList.Items()
	found := false

	if target.entry.UUID != "" {
		for i, ci := range a.conv.items {
			if ci.kind == convMsg && ci.merged.entry.UUID == target.entry.UUID {
				bestIdx = i
				found = true
				break
			}
		}
	}
	if !found && !target.entry.Timestamp.IsZero() {
		bestDist := time.Duration(math.MaxInt64)
		for i, ci := range a.conv.items {
			if ci.kind != convMsg || ci.merged.entry.Role != target.entry.Role {
				continue
			}
			dist := ci.merged.entry.Timestamp.Sub(target.entry.Timestamp)
			if dist < 0 {
				dist = -dist
			}
			if dist < bestDist {
				bestDist = dist
				bestIdx = i
			}
		}
	}

	if bestIdx < len(items) {
		a.convList.Select(bestIdx)
	}
	// Don't auto-snap for targeted jumps
	a.liveTail = false
	a.conv.split.BottomAlign = false
	a.updateConvPreview()

	return a, cmd
}

func (a *App) updateSessionStatsPreview(sess session.Session) {
	// Use cached stats if available for this session
	if a.sessStatsCacheKey != sess.ID || a.sessStatsCache == nil {
		stats, err := session.ScanSessionStats(sess.FilePath)
		if err != nil {
			a.sessSplit.Preview.SetContent(dimStyle.Render("(stats error)"))
			return
		}
		a.sessStatsCache = &stats
		a.sessStatsCacheKey = sess.ID
	}

	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)
	content := renderSessionStats(*a.sessStatsCache, previewW)
	a.sessSplit.Preview = viewport.New(previewW, contentH)
	a.sessSplit.Preview.SetContent(content)
}

func (a *App) updateSessionMemoryPreview(sess session.Session) {
	if a.sessMemoryCacheKey != sess.ID {
		a.sessMemoryCache = a.buildMemoryContent(sess)
		a.sessMemoryCacheKey = sess.ID
	}

	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)
	a.sessSplit.Preview = viewport.New(previewW, contentH)
	a.sessSplit.Preview.SetContent(a.sessMemoryCache)
}

func (a *App) updateSessionTasksPlanPreview(sess session.Session) {
	if a.sessTasksCacheKey != sess.ID {
		a.sessTasksCache = a.buildTasksPlanContent(sess)
		a.sessTasksCacheKey = sess.ID
	}

	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)
	a.sessSplit.Preview = viewport.New(previewW, contentH)
	a.sessSplit.Preview.SetContent(a.sessTasksCache)
}

func (a *App) buildTasksPlanContent(sess session.Session) string {
	home, _ := os.UserHomeDir()
	var sb strings.Builder

	// Tasks
	if len(sess.Tasks) > 0 {
		completed := 0
		for _, t := range sess.Tasks {
			if t.Status == "completed" {
				completed++
			}
		}
		sb.WriteString(dimStyle.Render(fmt.Sprintf("── Tasks [%d/%d] ──", completed, len(sess.Tasks))) + "\n\n")
		for _, t := range sess.Tasks {
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
			sb.WriteString(style.Render(fmt.Sprintf("  %s %s", icon, t.Subject)) + "\n")
			if t.Description != "" {
				desc := t.Description
				if idx := strings.Index(desc, "\n"); idx > 0 {
					desc = desc[:idx]
				}
				if len(desc) > 80 {
					desc = desc[:77] + "..."
				}
				sb.WriteString(dimStyle.Render("    "+desc) + "\n")
			}
		}
		sb.WriteString("\n")
	}

	// Plans (show all distinct plans in order)
	for i, slug := range sess.PlanSlugs {
		path := filepath.Join(home, ".claude", "plans", slug+".md")
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		label := fmt.Sprintf("── Plan %d/%d: %s ──", i+1, len(sess.PlanSlugs), slug)
		if len(sess.PlanSlugs) == 1 {
			label = "── Plan: " + slug + " ──"
		}
		sb.WriteString(dimStyle.Render(label) + "\n\n")
		sb.WriteString(strings.TrimRight(string(data), "\n") + "\n\n")
	}

	if sb.Len() == 0 {
		return dimStyle.Render("No tasks or plans found for this session.")
	}
	return sb.String()
}

// buildMemoryContent produces the styled memory text for a session.
func (a *App) buildMemoryContent(sess session.Session) string {
	if sess.ProjectPath == "" {
		return dimStyle.Render("(no project path)")
	}

	home, _ := os.UserHomeDir()
	var sb strings.Builder

	// Todos
	if len(sess.Todos) > 0 {
		completed := 0
		for _, t := range sess.Todos {
			if t.Status == "completed" {
				completed++
			}
		}
		sb.WriteString(dimStyle.Render(fmt.Sprintf("── Todos [%d/%d] ──", completed, len(sess.Todos))) + "\n\n")
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
			sb.WriteString(style.Render(fmt.Sprintf("  %s %s", icon, t.Content)) + "\n")
		}
		sb.WriteString("\n")
	}

	// Auto-memory files
	encoded := session.EncodeProjectPath(sess.ProjectPath)
	memDir := home + "/.claude/projects/" + encoded + "/memory"
	if entries, err := os.ReadDir(memDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := os.ReadFile(memDir + "/" + e.Name())
			if err != nil || len(data) == 0 {
				continue
			}
			sb.WriteString(dimStyle.Render("── "+e.Name()+" ──") + "\n\n")
			sb.WriteString(strings.TrimRight(string(data), "\n") + "\n\n")
		}
	}

	if sb.Len() == 0 {
		return dimStyle.Render("No memory or todos found.")
	}
	return sb.String()
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
	sess, ok := a.selectedSession()
	if !ok || !sess.IsLive {
		return
	}

	if a.sessPreviewMode != sessPreviewConversation {
		// Re-render non-message preview for live session
		a.sessSplit.CacheKey = ""
		if a.sessPreviewMode == sessPreviewStats {
			a.sessStatsCache = nil
			a.sessStatsCacheKey = ""
			a.updateSessionStatsPreview(sess)
		} else if a.sessPreviewMode == sessPreviewTasksPlan {
			a.sessTasksCacheKey = ""
			a.updateSessionTasksPlanPreview(sess)
		} else {
			a.sessMemoryCacheKey = ""
			a.updateSessionMemoryPreview(sess)
		}
		return
	}

	// Reload entries (head+tail) and refresh conversation preview for live session
	const liveHead, liveTail = 50, 50
	head, tail, total, err := session.LoadMessagesSummary(sess.FilePath, liveHead, liveTail)
	if err != nil || total == 0 {
		return
	}
	headMerged := mergeConversationTurns(head)
	var newConv []mergedMsg
	if len(tail) == 0 {
		newConv = headMerged
	} else {
		tailMerged := mergeConversationTurns(tail)
		tailOffset := total - len(tail)
		for i := range tailMerged {
			tailMerged[i].startIdx += tailOffset
			tailMerged[i].endIdx += tailOffset
		}
		newConv = append(headMerged, tailMerged...)
	}
	if len(newConv) == 0 {
		return
	}

	oldCount := len(a.sessConvEntries)
	a.sessConvEntries = filterConversation(newConv)

	// Re-apply filter if active
	if a.sessConvFilterTerm != "" {
		a.applyConvFilter(a.sessConvFilterTerm)
		return
	}

	// If new messages appeared and user hasn't scrolled up, move cursor to end
	visible := a.convVisibleEntries()
	if len(newConv) > oldCount && !a.sessPreviewPinned {
		a.sessConvCursor = len(visible) - 1
	}
	if a.sessConvCursor >= len(visible) {
		a.sessConvCursor = max(len(visible)-1, 0)
	}

	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	content := renderConversationPreview(visible, previewW, a.sessConvCursor, a.sessConvExpanded, a.sessConvFilterTerm, true)
	a.sessSplit.Preview.SetContent(content)
	if !a.sessPreviewPinned {
		a.sessSplit.Preview.GotoBottom()
	}
}

// formatHelp renders help text with highlighted shortcut keys.
// Tokens with "key:desc" get the key part highlighted; others stay dim.
func formatHelp(h string) string {
	var sb strings.Builder
	sb.WriteString("  ")
	for i, token := range strings.Split(h, " ") {
		if i > 0 {
			sb.WriteString(" ")
		}
		if idx := strings.Index(token, ":"); idx > 0 && idx < len(token)-1 {
			sb.WriteString(helpKeyStyle.Render(token[:idx]))
			sb.WriteString(helpStyle.Render(":" + token[idx+1:]))
		} else {
			sb.WriteString(helpStyle.Render(token))
		}
	}
	return sb.String()
}

func (a *App) searchHints() string {
	h := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8"))
	d := helpStyle
	sp := d.Render(" ")
	tail := d.Render("  (space=AND)  enter:apply  esc:cancel")
	switch a.state {
	case viewSessions:
		return d.Render("  ") +
			h.Render("is:live") + sp + h.Render("is:wt") + sp + h.Render("is:team") + sp +
			h.Render("has:mem") + sp + h.Render("has:todo") + sp + h.Render("has:task") + sp +
			h.Render("has:plan") + sp + h.Render("has:agent") + sp + h.Render("has:compact") + sp +
			h.Render("has:skill") + sp + h.Render("has:mcp") +
			d.Render("  project  branch  prompt") + tail
	case viewConversation:
		return d.Render("  ") +
			h.Render("role=user") + sp + h.Render("role=asst") + sp +
			h.Render("tool=Bash") + sp + h.Render("tool=Read") + sp +
			h.Render("tool=Edit") + sp + h.Render("tool=Write") + tail
	default:
		return d.Render("  enter:apply  esc:cancel")
	}
}

func (a *App) syncAllFilterVisibility() {
	syncFilterVisibility(&a.sessionList)
	syncFilterVisibility(&a.convList)
}

func (a *App) isFiltering() bool {
	switch a.state {
	case viewSessions:
		return a.sessionList.FilterState() == list.Filtering
	case viewConversation:
		return a.convList.FilterState() == list.Filtering
	}
	return false
}

func (a *App) hasFilterApplied() bool {
	switch a.state {
	case viewSessions:
		return a.sessionList.FilterState() == list.FilterApplied
	case viewConversation:
		return a.convList.FilterState() == list.FilterApplied
	}
	return false
}

func (a *App) activeFilterValue() string {
	switch a.state {
	case viewSessions:
		return a.sessionList.FilterInput.Value()
	case viewConversation:
		return a.convList.FilterInput.Value()
	}
	return ""
}

func (a *App) resetActiveFilter() {
	switch a.state {
	case viewSessions:
		a.sessionList.ResetFilter()
	case viewConversation:
		a.convList.ResetFilter()
	}
}

func (a *App) updateActiveList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch a.state {
	case viewSessions:
		m, cmd := a.updateSessionList(msg)
		a.updateSessionPreview()
		return m, cmd
	case viewConversation:
		if a.listReady(&a.convList) {
			var cmd tea.Cmd
			a.convList, cmd = a.convList.Update(msg)
			return a, cmd
		}
		return a, nil
	}
	return a, nil
}

func (a *App) updateActiveComponent(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch a.state {
	case viewSessions:
		if a.moveMode {
			var cmd tea.Cmd
			a.moveInput, cmd = a.moveInput.Update(msg)
			return a, cmd
		}
		if a.worktreeMode {
			var cmd tea.Cmd
			a.worktreeInput, cmd = a.worktreeInput.Update(msg)
			return a, cmd
		}
		m, cmd := a.updateSessionList(msg)
		a.updateSessionPreview()
		return m, cmd
	case viewConversation:
		if a.listReady(&a.convList) {
			var cmd tea.Cmd
			a.convList, cmd = a.convList.Update(msg)
			return a, cmd
		}
		return a, nil
	case viewMessageFull:
		var cmd tea.Cmd
		a.msgFull.vp, cmd = a.msgFull.vp.Update(msg)
		return a, cmd
	case viewGlobalStats:
		var cmd tea.Cmd
		a.globalStatsVP, cmd = a.globalStatsVP.Update(msg)
		return a, cmd
	}
	return a, nil
}

// autoSelectSession selects the session matching a Claude process in the same tmux window.
// When multiple sessions share the same project path, prefer the most recently modified
// one (sessions are sorted by ModTime descending, so first match wins).
// If the matched session is live, auto-enters it with live tail enabled.
func (a *App) autoSelectSession() tea.Cmd {
	for _, projPath := range currentTmuxWindowClaudes() {
		absProj, _ := filepath.Abs(projPath)
		if absProj == "" {
			absProj = projPath
		}
		for i, s := range a.sessions {
			sp := s.ProjectPath
			absSP, _ := filepath.Abs(sp)
			if absSP == "" {
				absSP = sp
			}
			if absSP == absProj {
				a.sessionList.Select(i)
				// Auto-enter live sessions (only if TmuxAutoLive is enabled)
				if s.IsLive && a.config.TmuxAutoLive {
					a.currentSess = s
					return a.openConversation(s)
				}
				return nil
			}
		}
	}
	return nil
}

func (a *App) resizeAll() tea.Cmd {
	contentH := a.height - 3
	var cmd tea.Cmd

	sessW := a.sessSplit.ListWidth(a.width, a.splitRatio)
	if a.sessionList.Width() == 0 {
		a.sessionList = newSessionList(a.sessions, sessW, contentH, a.sessGroupMode)
		a.sessSplit.CacheKey = ""
		if a.config.SearchQuery != "" {
			applyListFilter(&a.sessionList, a.config.SearchQuery)
		}
		cmd = a.autoSelectSession()
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
	if a.globalStatsVP.Width > 0 {
		a.globalStatsVP.Width = a.width
		a.globalStatsVP.Height = contentH
	}
	// Conversation split view
	if a.convList.Width() > 0 {
		idx := a.convList.Index()
		a.convList.SetSize(a.conv.split.ListWidth(a.width, a.splitRatio), contentH)
		a.convList.Select(idx)
		// Re-render preview content at new dimensions (preserves folds/scroll)
		if a.conv.split.Show {
			a.conv.split.cachedRP = nil
			if a.conv.split.Folds != nil && len(a.conv.split.Folds.Entry.Content) > 0 {
				a.conv.split.RefreshFoldPreview(a.width, a.splitRatio)
			}
		}
	}
	// Message full view
	if a.msgFull.vp.Width > 0 {
		a.msgFull.vp.Width = a.width
		a.msgFull.vp.Height = contentH
		if a.msgFull.allMessages {
			// Re-render all messages for new width
			content := renderAllMessages(a.msgFull.merged, a.width)
			a.msgFull.content = content
			a.msgFull.vp.SetContent(content)
		} else {
			a.refreshMsgFullPreview()
		}
	}
	return cmd
}

func (a *App) rebuildSessionList() {
	selectedID := ""
	if sess, ok := a.selectedSession(); ok {
		selectedID = sess.ID
	}

	contentH := max(a.height-3, 1)
	sessW := a.sessSplit.ListWidth(a.width, a.splitRatio)
	a.sessionList = newSessionList(a.sessions, sessW, contentH, a.sessGroupMode)
	a.sessSplit.CacheKey = ""

	// Restore cursor to previously selected session
	if selectedID != "" {
		for i, item := range a.sessionList.Items() {
			if si, ok := item.(sessionItem); ok && si.sess.ID == selectedID {
				a.sessionList.Select(i)
				break
			}
		}
	}
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


// breadcrumbSegment tracks the X range and target for a clickable breadcrumb part.
type breadcrumbSegment struct {
	startX int
	endX   int
	state  viewState
	action string // empty = navigate to state, non-empty = named action
}

// renderBreadcrumb builds the title bar with clickable segments and right-aligned item count.
func (a *App) renderBreadcrumb() string {
	type crumb struct {
		label string
		state viewState
	}

	var crumbs []crumb

	switch a.state {
	case viewSessions:
		crumbs = []crumb{{" Sessions", viewSessions}}
		// Show selected project name in breadcrumb
		if sess, ok := a.selectedSession(); ok && a.sessionList.Width() > 0 {
			proj := sess.ProjectName
			if sess.GitBranch != "" {
				proj += " (" + sess.GitBranch + ")"
			}
			crumbs = append(crumbs, crumb{proj, viewSessions})
		}
	case viewGlobalStats:
		crumbs = []crumb{
			{" Sessions", viewSessions},
			{"Global Stats", viewGlobalStats},
		}
	case viewConversation:
		crumbs = []crumb{
			{" Sessions", viewSessions},
			{a.currentSess.ShortID, viewConversation},
		}
		if a.conv.agent.ShortID != "" {
			crumbs = append(crumbs, crumb{
				"agent:" + a.conv.agent.ShortID,
				viewConversation,
			})
		}
		if a.conv.task.ID != "" {
			label := "task:" + a.conv.task.ID
			if len(a.conv.task.Subject) > 30 {
				label += " " + a.conv.task.Subject[:27] + "..."
			} else if a.conv.task.Subject != "" {
				label += " " + a.conv.task.Subject
			}
			crumbs = append(crumbs, crumb{label, viewConversation})
		}
	case viewMessageFull:
		crumbs = []crumb{
			{" Sessions", viewSessions},
			{a.currentSess.ShortID, viewConversation},
		}
		// Add nav stack context
		if a.msgFull.agent.ShortID != "" {
			crumbs = append(crumbs, crumb{
				"agent:" + a.msgFull.agent.ShortID,
				viewMessageFull,
			})
		}
		if a.msgFull.allMessages {
			crumbs = append(crumbs, crumb{"Full", viewMessageFull})
		} else if a.msgFull.idx < len(a.msgFull.merged) {
			m := a.msgFull.merged[a.msgFull.idx]
			crumbs = append(crumbs, crumb{
				fmt.Sprintf("#%d %s", m.startIdx+1, strings.ToUpper(m.entry.Role)),
				viewMessageFull,
			})
		}
	}

	// Build the styled breadcrumb and track click regions
	a.breadcrumbSegs = a.breadcrumbSegs[:0]
	sepText := " > "
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Background(colorPrimary)
	parentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#D1D5DB")).Background(colorPrimary)
	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(colorPrimary)

	var text string
	x := 0
	for i, c := range crumbs {
		var part string
		if i == len(crumbs)-1 {
			part = activeStyle.Render(c.label)
		} else {
			part = parentStyle.Render(c.label)
		}
		partW := lipgloss.Width(part)
		a.breadcrumbSegs = append(a.breadcrumbSegs, breadcrumbSegment{
			startX: x,
			endX:   x + partW,
			state:  c.state,
		})
		text += part
		x += partW
		if i < len(crumbs)-1 {
			sep := sepStyle.Render(sepText)
			text += sep
			x += lipgloss.Width(sep)
		}
	}

	// Context action links (e.g. Agents, Tools, Memory)
	type actionLink struct {
		label  string
		action string
	}
	var actions []actionLink
	switch a.state {
	case viewSessions:
		if a.sessSplit.Show && a.sessPreviewMode != sessPreviewConversation {
			label := "[Stats]"
			if a.sessPreviewMode == sessPreviewMemory {
				label = "[Memory]"
			} else if a.sessPreviewMode == sessPreviewTasksPlan {
				label = "[Tasks]"
			}
			actions = []actionLink{{label, ""}}
		}
	}

	if len(actions) > 0 {
		actionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Background(colorPrimary)
		sepAction := lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563")).Background(colorPrimary).Render("  ")
		text += sepAction
		x += lipgloss.Width(sepAction)
		for i, act := range actions {
			if i > 0 {
				divider := lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563")).Background(colorPrimary).Render(" ")
				text += divider
				x += lipgloss.Width(divider)
			}
			label := actionStyle.Render(act.label)
			labelW := lipgloss.Width(label)
			a.breadcrumbSegs = append(a.breadcrumbSegs, breadcrumbSegment{
				startX: x,
				endX:   x + labelW,
				action: act.action,
			})
			text += label
			x += labelW
		}
	}

	// Right-aligned status: item count + scroll % + loading
	rightParts := a.breadcrumbRightStatus()
	if rightParts != "" {
		countStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A1A1AA")).Background(colorPrimary)
		rightStr := countStyle.Render(rightParts + " ")
		rightW := lipgloss.Width(rightStr)
		gap := max(a.width-x-rightW, 1)
		text += lipgloss.NewStyle().Background(colorPrimary).Render(strings.Repeat(" ", gap)) + rightStr
	}

	// Fill remaining width
	titleW := lipgloss.Width(text)
	if titleW < a.width {
		text += lipgloss.NewStyle().Background(colorPrimary).Render(strings.Repeat(" ", a.width-titleW))
	}

	return text
}

// breadcrumbRightStatus returns the right-aligned status text for the title bar.
// Shows: item count, scroll %, and loading indicators.
func (a *App) breadcrumbRightStatus() string {
	var parts []string

	// Session group mode badge
	if a.state == viewSessions {
		modeLabel := []string{"flat", "proj", "tree"}
		parts = append(parts, modeLabel[a.sessGroupMode])
	}

	// Loading indicator
	if a.globalStatsLoading {
		idx := a.spinnerFrame % len(spinnerFrames)
		frame := spinnerFrames[idx]
		spinnerColors := []lipgloss.Color{"#10B981", "#3B82F6", "#F59E0B", "#7C3AED", "#EC4899"}
		c := spinnerColors[a.spinnerFrame/len(spinnerFrames)%len(spinnerColors)]
		s := lipgloss.NewStyle().Foreground(c).Bold(true)
		parts = append(parts, s.Render(fmt.Sprintf("%s scanning %d sessions", frame, len(a.sessions))))
	}

	// Item count + page indicator for list views
	count := a.activeListItemCount()
	if count >= 0 {
		l := a.activeList()
		if l != nil && l.Paginator.TotalPages > 1 {
			parts = append(parts, fmt.Sprintf("%d items  %d/%d", count, l.Paginator.Page+1, l.Paginator.TotalPages))
		} else {
			parts = append(parts, fmt.Sprintf("%d items", count))
		}
	}

	// Scroll position for viewports
	var pct int = -1
	switch a.state {
	case viewSessions:
		if a.sessSplit.Show {
			pct = int(a.sessSplit.Preview.ScrollPercent() * 100)
		}
	case viewConversation:
		if a.conv.split.Show {
			pct = int(a.conv.split.Preview.ScrollPercent() * 100)
		}
	case viewMessageFull:
		pct = int(a.msgFull.vp.ScrollPercent() * 100)
	case viewGlobalStats:
		pct = int(a.globalStatsVP.ScrollPercent() * 100)
	}
	if pct >= 0 {
		parts = append(parts, fmt.Sprintf("%d%%", pct))
	}

	return strings.Join(parts, "  ")
}

func (a *App) activeList() *list.Model {
	switch a.state {
	case viewSessions:
		if a.sessionList.Width() > 0 {
			return &a.sessionList
		}
	case viewConversation:
		if a.convList.Width() > 0 {
			return &a.convList
		}
	}
	return nil
}

func (a *App) activeListItemCount() int {
	if l := a.activeList(); l != nil {
		return len(l.Items())
	}
	return -1
}

// handleBreadcrumbClick checks if a title bar click is on a breadcrumb segment
// and navigates to that view using proper open/load functions.
func (a *App) handleBreadcrumbClick(mouseX int) (tea.Model, tea.Cmd) {
	for _, seg := range a.breadcrumbSegs {
		if mouseX >= seg.startX && mouseX < seg.endX {
			if seg.action != "" {
				return a.handleBreadcrumbAction(seg.action)
			}
			if seg.state != a.state {
				return a.navigateTo(seg.state)
			}
		}
	}
	return a, nil
}

// handleBreadcrumbAction handles clicks on action links in the breadcrumb bar.
func (a *App) handleBreadcrumbAction(action string) (tea.Model, tea.Cmd) {
	// No action links in the new views
	return a, nil
}

// navigateTo handles navigation to a target view state, loading data as needed.
func (a *App) navigateTo(target viewState) (tea.Model, tea.Cmd) {
	switch target {
	case viewSessions:
		a.state = viewSessions
		return a, nil

	case viewConversation:
		if len(a.conv.items) > 0 {
			a.state = viewConversation
			return a, nil
		}
		return a, a.openConversation(a.currentSess)
	}
	return a, nil
}

// clampPaginator prevents stale page bounds that cause panics in bubbles list.View().
func clampPaginator(l *list.Model) {
	if items := l.VisibleItems(); len(items) > 0 {
		maxPage := max((len(items)-1)/max(l.Paginator.PerPage, 1), 0)
		if l.Paginator.Page > maxPage {
			l.Paginator.Page = maxPage
		}
	} else {
		l.Paginator.Page = 0
	}
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
