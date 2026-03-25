package tui

import (
	"context"
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
	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
)

type tickMsg time.Time
type liveTickMsg time.Time // slow live capture (2s, unfocused)
type spinnerTickMsg time.Time
type globalStatsMsg session.GlobalStats

// sessionsScannedMsg carries the result of async full session scanning.
type sessionsScannedMsg struct {
	sessions []session.Session
	err      error
}

// liveCaptureMsg carries async tmux capture-pane result.
type liveCaptureMsg struct {
	content string
	failed  bool
}

// Conversation preview detail levels (cycled with tab).
const (
	previewText = 0 // text only — no tool blocks
	previewTool = 1 // text + tool blocks (hooks hidden)
	previewHook = 2 // text + tool blocks + hook details
)

var previewModeLabels = [3]string{"text", "tool", "hook"}

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
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return liveTickMsg(t)
	})
}

// captureAfterKeyCmd sends a key to the tmux pane, waits briefly for tmux to
// process it, then captures the pane content. This gives responsive feedback
// on keypress without constant polling.
func captureAfterKeyCmd(p tmux.Pane, key string) tea.Cmd {
	return func() tea.Msg {
		tmux.SendSingleKey(p, key)
		time.Sleep(30 * time.Millisecond)
		content, err := tmux.CapturePane(p)
		if err != nil || !tmux.HasClaude(p.PID) {
			return liveCaptureMsg{failed: true}
		}
		return liveCaptureMsg{content: content}
	}
}

// capturePaneCmd returns a Cmd that captures tmux pane content asynchronously.
func capturePaneCmd(p tmux.Pane) tea.Cmd {
	return func() tea.Msg {
		content, err := tmux.CapturePane(p)
		if err != nil || !tmux.HasClaude(p.PID) {
			return liveCaptureMsg{failed: true}
		}
		return liveCaptureMsg{content: content}
	}
}

// paneProxyState holds state for both live preview and shell-in-preview.
type paneProxyState struct {
	pane    tmux.Pane
	sessID  string // non-empty for live Claude preview, empty for shell
	isShell bool   // true = we spawned this pane, must kill on close
}

type viewState int

const (
	viewSessions viewState = iota
	viewConversation
	viewMessageFull
	viewGlobalStats
	viewConfig
	viewPlugins
)

type App struct {
	state  viewState
	width  int
	height int
	config Config
	keymap Keymap

	// Data
	sessions       []session.Session
	currentSess    session.Session
	selectedSet    map[string]bool // multi-select: session ID → selected
	liveInputPanes []tmux.Pane     // bulk input: multiple target panes

	// List models
	sessionList list.Model

	// Split panes
	sessSplit SplitPane

	// Session-specific: pinned scroll state
	sessPreviewPinned bool

	// Split pane ratio (list width as % of terminal width)
	splitRatio int

	// Number key shortcuts (view + focus scoped)
	shortcuts Shortcuts

	// Badge visibility
	hiddenBadges map[string]bool

	// Live input: prefer $EDITOR mode
	editorInput bool

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
	statsDetail        statsDetailMode // drill-down detail category
	statsDetailVP      viewport.Model
	statsPageMenu      bool // "p" page jump popup

	// Session preview mode
	sessPreviewMode    sessPreview
	sessStatsCache     *session.SessionStats
	sessStatsCacheKey  string
	sessMemoryCache    string // rendered memory content
	sessMemoryCacheKey string
	sessTasksCache     string
	sessTasksCacheKey  string

	// Conversation preview state
	sessConvEntries     []mergedMsg     // merged conversation messages
	sessConvCursor      int             // current message cursor
	sessConvCacheID     string          // session ID for which convEntries are loaded
	sessConvExpanded    map[int]bool    // which messages are expanded
	sessConvSearching   bool            // typing in preview search
	sessConvSearchInput textinput.Model // search input for preview
	sessConvFiltered    []int           // indices into sessConvEntries matching search
	sessConvFilterTerm  string          // applied filter term

	// Group mode: groupFlat=0, groupProject=1, groupTree=2
	sessGroupMode   int
	sessionsLoading bool // true while initial async scan is in progress
	liveUpdate      bool // auto-refresh disabled by default

	// Edit file menu
	editMenu bool
	editSess session.Session

	// Actions menu (x key)
	actionsMenu bool
	actionsSess session.Session
	editChoices []editChoice // available files to edit

	// Tag menu (t key in actions)
	tagMenu     bool
	tagSessID   string   // single session ID
	tagSessIDs  []string // multi-select session IDs
	tagCursor   int
	tagInput    textinput.Model
	tagList     []string
	badgeStore  *session.BadgeStore

	// URL menu (u key in actions)
	urlMenu        bool
	urlAllItems    []extract.Item // unfiltered full list
	urlItems       []extract.Item // filtered (displayed) list
	urlCursor      int
	urlSelected    map[string]bool // selected URLs for multi-open/copy
	urlSearching   bool            // typing in search input
	urlSearchInput textinput.Model
	urlSearchTerm  string
	urlScope       string // context label: "session", "message", "block"

	// Conversation/message-full actions menu (x key)
	convActionsMenu bool

	// Views menu (V key)
	viewsMenu bool

	// Help overlay (? key)
	showHelp bool

	// Full text modal (c key in session conv preview)
	sessConvFullText   string // non-empty = show modal
	sessConvFullScroll int    // scroll offset in full text modal

	// Move project
	moveMode  bool
	moveInput textinput.Model
	moveSess  session.Session

	// Worktree creation
	worktreeMode  bool
	worktreeInput textinput.Model
	worktreeSess  session.Session

	// Memory import from worktree
	memImportActive bool
	memImportSrc    string // worktree project path
	memImportDst    string // main project path

	// Memory removal
	memRemoveActive bool
	memRemoveSrc    string // project path to remove from

	// Live input modal (I key)
	liveInputActive  bool
	liveInputPane    tmux.Pane
	liveInputModal   inputModal
	liveInputProjDir string // project path for $EDITOR cwd

	// Pane proxy: unified live preview + shell-in-preview
	paneProxy *paneProxyState

	// Conversation split view (viewConversation)
	conv struct {
		sess     session.Session
		messages []session.Entry
		merged   []mergedMsg
		agents   []session.Subagent
		items    []convItem
		split    SplitPane
		agent    session.Subagent // non-zero when viewing agent conversation
		task     session.TaskItem // non-zero when viewing task conversation
		// Preview detail level: text → tool → hook (cycled with tab)
		previewMode int // 0=text, 1=tool (no hooks), 2=hook (with hooks)

		// Block filter for preview pane
		blockFiltering bool            // true when filter input is active
		blockFilterTI  textinput.Model // filter text input
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
		searchTerm  string // committed search term
		searchLines []int  // line numbers that match
		searchIdx   int    // current match index in searchLines

		// Block filter for single-message mode
		blockFiltering bool
		blockFilterTI  textinput.Model
	}

	// Navigation stack for agent drill-down
	navStack []navFrame

	// Config explorer (viewConfig)
	cfgTree           *session.ConfigTree
	cfgList           list.Model
	cfgSplit          SplitPane
	cfgSearching      bool
	cfgSearchInput    textinput.Model
	cfgSearchTerm     string
	cfgSearchMatch    []int           // indices of matching items
	cfgSearchIdx      int             // current match index
	cfgSearchHist     []string        // search history (most recent last)
	cfgSearchHistI    int             // -1 = new input, 0..N = browsing history
	cfgSelectedSet    map[string]bool // config file path → selected
	cfgFilterCat      int             // -1 = all, 0..N = ConfigCategory value
	cfgNaming         bool            // naming input active for new config
	cfgNamingInput    textinput.Model
	cfgNamingCat      session.ConfigCategory
	cfgProjectPicker  bool              // project picker overlay active
	cfgProjectEntries []cfgProjectEntry // all projects
	cfgProjectInput   textinput.Model   // fuzzy search input
	cfgProjectCursor  int               // selected index in filtered list
	cfgTrash          []cfgTrashEntry   // undo stack for deleted items
	cfgDeleteConfirm  bool              // waiting for second x press
	cfgActionsMenu    bool              // config actions menu open
	cfgPageMenu       bool              // config page jump popup

	// Plugin explorer (viewPlugins)
	plgTree        *session.PluginTree
	plgList        list.Model
	plgSplit       SplitPane
	plgSearching   bool
	plgSearchInput textinput.Model
	plgSearchTerm  string

	// Plugin selection & actions
	plgSelectedSet      map[string]bool // plugin ID → selected
	plgActionsMenu      bool            // actions menu open
	plgUninstallConfirm bool            // waiting for second x press

	// Plugin detail drill-down
	plgDetailActive    bool            // true = showing component list for a plugin
	plgDetailPlugin    session.Plugin  // the plugin being inspected
	plgDetailList      list.Model      // component list
	plgDetailSplit     SplitPane       // component list + file preview
	plgCompSelectedSet map[string]bool // selected component paths
	plgCompActionsMenu bool            // actions menu active

	// Hooks view (legacy, kept for viewport reuse)
	hooksVP viewport.Model

	// Command mode (: key)
	cmdMode        bool
	cmdInput       textinput.Model
	cmdRegistry    []cmdEntry
	cmdSuggestions []cmdEntry
	cmdSuggIdx     int // -1 = none selected

	// Cross-session search (Ctrl+F)
	searchActive     bool
	searchInput      textinput.Model
	searchQuery      string
	searchResults    []session.SearchResult
	searchResultList list.Model
	searchLoading    bool
	searchCancel     context.CancelFunc
}

// selectedSession returns the currently selected session from the session list.
func (a *App) selectedSession() (session.Session, bool) {
	item, ok := a.sessionList.SelectedItem().(sessionItem)
	if !ok {
		return session.Session{}, false
	}
	return item.sess, true
}

func (a *App) hasMultiSelection() bool {
	return len(a.selectedSet) > 0
}

func (a *App) clearMultiSelection() {
	clear(a.selectedSet)
}

func (a *App) selectedSessions() []session.Session {
	var out []session.Session
	for _, s := range a.sessions {
		if a.selectedSet[s.ID] {
			out = append(out, s)
		}
	}
	return out
}

type sessPreview int

const (
	sessPreviewConversation sessPreview = iota // text-only, expandable
	sessPreviewStats
	sessPreviewMemory
	sessPreviewTasksPlan
	sessPreviewLive     // tmux pane capture
	numSessPreviewModes = 5
)

// Config holds application configuration from CLI flags.
type Config struct {
	ClaudeDir    string  // path to Claude data directory (empty = ~/.claude)
	TmuxEnabled  bool    // enable tmux integration (I, J, live modal)
	TmuxAutoLive bool    // auto-enter live session in same tmux window on startup
	WorktreeDir  string  // subdirectory name for worktrees (default ".worktree")
	SearchQuery  string  // initial search filter for session list
	Keymap       *Keymap // nil = use defaults
	GroupMode    string  // initial group mode (flat|proj|tree|chain|fork)
	PreviewMode  string  // initial preview mode (conv|stats|mem|tasks)
	ViewMode     string  // initial view (sessions|config|plugins|stats)
}

func NewApp(sessions []session.Session, cfg Config) *App {
	if len(sessions) > 0 {
		// Set IsLive by matching running Claude processes to sessions.
		tmux.MarkLiveSessions(sessions)
	}

	if cfg.WorktreeDir == "" {
		cfg.WorktreeDir = ".worktree"
	}

	km := DefaultKeymap()
	if cfg.Keymap != nil {
		km = *cfg.Keymap
	}

	a := &App{
		state:           viewSessions,
		sessions:        sessions,
		sessionsLoading: true, // always true — full scan happens async
		config:          cfg,
		keymap:          km,
		splitRatio:      35,
		selectedSet:     make(map[string]bool),
		hiddenBadges:    make(map[string]bool),
	}

	// Restore persisted view state (CLI flags override in the apply block below)
	_, prefs, sc := LoadCCXConfig(configPath())
	a.applyPreferences(prefs)
	a.shortcuts = sc
	a.sessSplit = SplitPane{List: &a.sessionList, ItemHeight: 2}
	a.conv.split = SplitPane{List: &a.convList, Show: true, Folds: &FoldState{}, ItemHeight: 1}
	a.cfgSplit = SplitPane{List: &a.cfgList, ItemHeight: 1}
	a.plgSplit = SplitPane{List: &a.plgList, ItemHeight: 1}
	a.plgDetailSplit = SplitPane{List: &a.plgDetailList, ItemHeight: 1}
	a.plgSelectedSet = make(map[string]bool)
	a.plgCompSelectedSet = make(map[string]bool)
	a.cmdRegistry = buildCmdRegistry()

	// Initialize tag menu
	a.badgeStore = session.LoadBadges(cfg.ClaudeDir)
	a.tagInput = textinput.New()
	a.tagInput.Placeholder = "badge-name"
	a.tagInput.CharLimit = 20

	// Apply group/preview/view mode from CLI flags or restored preferences
	if a.config.GroupMode != "" {
		modeMap := map[string]int{"flat": groupFlat, "proj": groupProject, "tree": groupTree, "chain": groupChain, "fork": groupFork, "repo": groupBaseProject}
		if m, ok := modeMap[a.config.GroupMode]; ok {
			a.sessGroupMode = m
		}
	}
	if a.config.PreviewMode != "" {
		modeMap := map[string]sessPreview{"conv": sessPreviewConversation, "stats": sessPreviewStats, "mem": sessPreviewMemory, "tasks": sessPreviewTasksPlan, "live": sessPreviewLive}
		if m, ok := modeMap[a.config.PreviewMode]; ok {
			a.sessPreviewMode = m
			a.sessSplit.Show = true
		}
	}
	if a.config.ViewMode != "" {
		modeMap := map[string]viewState{
			"sessions": viewSessions, "config": viewConfig,
			"plugins": viewPlugins, "stats": viewGlobalStats,
		}
		if m, ok := modeMap[a.config.ViewMode]; ok {
			a.state = m
		}
	}

	return a
}

// initViewMsg is sent after the first WindowSizeMsg to initialize the
// starting view when launched with -view config/plugins/stats.
type initViewMsg struct{}

func (a *App) Init() tea.Cmd {
	cmds := []tea.Cmd{tickCmd()}
	if a.sessionsLoading {
		// Phase 1 (live sessions) was done synchronously in main.
		// Fire phase 2: full async scan for all remaining sessions.
		cmds = append(cmds, spinnerTickCmd())
		claudeDir := a.config.ClaudeDir
		cmds = append(cmds, func() tea.Msg {
			sessions, err := session.ScanSessions(claudeDir)
			return sessionsScannedMsg{sessions: sessions, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		first := a.width == 0 && a.height == 0
		a.width = msg.Width
		a.height = msg.Height
		cmd := a.resizeAll()
		// On first size, trigger deferred view init for -view flag
		if first && a.state != viewSessions {
			cmd = tea.Batch(cmd, func() tea.Msg { return initViewMsg{} })
		}
		return a, cmd

	case initViewMsg:
		switch a.state {
		case viewConfig:
			return a.openConfigExplorer()
		case viewPlugins:
			return a.openPluginExplorer()
		case viewGlobalStats:
			return a.openGlobalStats()
		}
		return a, nil

	case openStatsPageMsg:
		if a.state == viewGlobalStats && !a.globalStatsLoading {
			return a.openStatsDetail(msg.page)
		}
		return a, nil

	case editorDoneMsg:
		if a.state == viewConfig {
			// Re-scan config after editor closes
			a.refreshConfigExplorer()
		}
		return a, nil

	case configTestDoneMsg:
		os.RemoveAll(msg.tmpDir)
		a.clearCfgSelection()
		if a.state == viewConfig {
			a.refreshConfigExplorer()
		}
		return a, nil

	case pluginTestDoneMsg:
		os.RemoveAll(msg.tmpDir)
		a.clearPlgSelection()
		return a, nil

	case pluginCmdDoneMsg:
		if msg.err != nil {
			a.copiedMsg = "Error: " + msg.err.Error()
		} else {
			label := msg.action
			if len(label) > 0 {
				label = strings.ToUpper(label[:1]) + label[1:]
			}
			a.copiedMsg = label + " done"
		}
		a.clearPlgSelection()
		// Re-scan plugins to reflect changes
		return a.openPluginExplorer()

	case liveInputSentMsg:
		if msg.err != nil {
			a.copiedMsg = "Send failed"
		} else {
			a.copiedMsg = "Sent!"
		}
		return a, nil

	case tickMsg:
		cmd := a.handleTick()
		return a, tea.Batch(cmd, tickCmd())

	case liveTickMsg:
		// 2s tick: async capture for passive updates (process output, unfocused view)
		if a.state == viewSessions && a.sessPreviewMode == sessPreviewLive && a.paneProxy != nil {
			return a, tea.Batch(capturePaneCmd(a.paneProxy.pane), liveTickCmd())
		}
		if a.liveTail {
			a.handleLiveTail()
			return a, liveTickCmd()
		}
		return a, nil

	case liveFindMsg:
		// Async result of findTmuxPane — only apply if still on the same session in live mode
		if a.sessPreviewMode != sessPreviewLive {
			return a, nil
		}
		sess, ok := a.selectedSession()
		if !ok || sess.ID != msg.sessID {
			return a, nil // user navigated away, discard stale result
		}
		if msg.found {
			a.paneProxy = &paneProxyState{pane: msg.pane, sessID: msg.sessID}
			return a, tea.Batch(capturePaneCmd(a.paneProxy.pane), liveTickCmd())
		}
		a.closePaneProxy()
		a.sessSplit.Preview.SetContent(dimStyle.Render("(tmux pane not found)"))
		return a, nil

	case liveCaptureMsg:
		if a.paneProxy == nil || a.sessPreviewMode != sessPreviewLive {
			return a, nil
		}
		if msg.failed {
			// Pane gone (session ended) — close proxy, revert to conversation preview
			a.closePaneProxy()
			a.sessSplit.Focus = false
			a.sessPreviewMode = sessPreviewConversation
			a.sessSplit.CacheKey = ""
			return a, nil
		} else {
			a.sessSplit.Preview.SetContent(msg.content)
			a.sessSplit.Preview.GotoBottom()
		}
		return a, nil

	case spinnerTickMsg:
		if a.globalStatsLoading || a.sessionsLoading {
			a.spinnerFrame = (a.spinnerFrame + 1) % len(spinnerFrames)
			return a, spinnerTickCmd()
		}
		return a, nil

	case sessionsScannedMsg:
		// Full scan complete — replace partial live sessions with full list
		a.sessionsLoading = false
		if msg.err != nil || len(msg.sessions) == 0 {
			if len(a.sessions) == 0 {
				a.sessions = nil
			}
			return a, nil
		}
		tmux.MarkLiveSessions(msg.sessions)

		// Remember cursor position from phase 1
		selectedID := ""
		if sess, ok := a.selectedSession(); ok {
			selectedID = sess.ID
		}

		a.sessions = msg.sessions
		// Build/rebuild session list
		if a.width > 0 && a.height > 0 {
			contentH := a.height - 3
			sessW := a.sessSplit.ListWidth(a.width, a.splitRatio)
			a.sessionList = newSessionList(a.sessions, sessW, contentH, a.sessGroupMode, a.selectedSet, a.hiddenBadges, a.config.WorktreeDir)
			a.sessSplit.CacheKey = ""
			if a.config.SearchQuery != "" {
				applyListFilter(&a.sessionList, a.config.SearchQuery)
			}
			// Restore cursor to previously selected session
			if selectedID != "" {
				for i, item := range a.sessionList.Items() {
					if si, ok := item.(sessionItem); ok && si.sess.ID == selectedID {
						a.sessionList.Select(i)
						return a, nil
					}
				}
			}
			return a, a.autoSelectSession()
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

	case searchBatchMsg:
		a.updateSearchResults(msg.results)
		return a, nil

	case tea.MouseMsg:
		return a.handleMouse(msg)

	case tea.KeyMsg:
		a.copiedMsg = ""
		// Pane proxy focused: forward ctrl+c to tmux pane instead of quitting
		if msg.String() == "ctrl+c" && a.isPaneProxyFocused() {
			return a, captureAfterKeyCmd(a.paneProxy.pane, "ctrl+c")
		}
		if msg.String() == "ctrl+c" {
			return a.quit()
		}

		// Live input modal intercepts all keys
		if a.liveInputActive {
			return a.handleLiveInputKey(msg.String())
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

		// Cross-session search: overlay from any view
		if a.searchActive {
			return a.handleSearchKey(msg)
		}

		// URL menu: available from any view
		if a.urlMenu {
			return a.handleURLMenu(msg)
		}

		// Command mode: available from any view
		if a.cmdMode {
			return a.handleCmdMode(msg)
		}
		if msg.String() == a.keymap.Session.Command {
			// Don't enter command mode when typing in text inputs
			if !a.isInTextInput() {
				a.startCmdMode()
				return a, nil
			}
		}

		// Number key shortcuts (1-9): view + focus scoped
		if key := msg.String(); !a.isInTextInput() && !a.isInOverlay() &&
			len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			if m, cmd, handled := a.handleShortcutKey(key); handled {
				return m, cmd
			}
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
		case viewConfig:
			return a.handleConfigKeys(msg)
		case viewPlugins:
			return a.handlePluginKeys(msg)
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
		if a.sessionsLoading && len(a.sessions) == 0 {
			idx := a.spinnerFrame % len(spinnerFrames)
			frame := spinnerFrames[idx]
			spinnerColors := []lipgloss.Color{"#10B981", "#3B82F6", "#F59E0B", "#7C3AED", "#EC4899"}
			c := spinnerColors[a.spinnerFrame/len(spinnerFrames)%len(spinnerColors)]
			s := lipgloss.NewStyle().Foreground(c).Bold(true)
			content = "\n  " + s.Render(fmt.Sprintf("%s Scanning sessions…", frame))
			help = formatHelp("loading… q:quit")
			break
		}
		if len(a.sessions) == 0 {
			dir := a.config.ClaudeDir
			if dir == "" {
				dir = "~/.claude/projects/"
			}
			content = "\n  " + dimStyle.Render(fmt.Sprintf("No sessions found in %s", dir))
			help = formatHelp("q:quit")
			break
		}
		content = a.renderSessionSplit()
		if a.sessConvFullText != "" {
			content = renderFullTextModal(content, a.sessConvFullText, a.sessConvFullScroll, a.width, ContentHeight(a.height))
			help = formatHelp("↑↓:scroll pgup/pgdn:page esc/c:close")
		} else if a.showHelp {
			content = renderHelpModal(content, a.width, ContentHeight(a.height), a.keymap, a.shortcutHint())
			help = formatHelp("press any key to close")
		} else if a.tagMenu {
			help = "" // Tag menu has its own help text inside the modal
		} else if a.moveMode {
			help = "  " + a.moveInput.View() + helpStyle.Render("  enter:move esc:cancel")
		} else if a.worktreeMode {
			help = "  " + a.worktreeInput.View() + helpStyle.Render("  enter:create esc:cancel")
		} else if a.sessConvSearching {
			help = "  " + a.sessConvSearchInput.View() + helpStyle.Render("  enter:apply esc:cancel")
		} else {
			// Pane proxy focused: show proxy-specific help with indicator
			if a.sessSplit.Focus && a.paneProxy != nil && a.sessPreviewMode == sessPreviewLive {
				indicator := a.paneProxyIndicator()
				h := "keys→pane ^G:jump ^N:newline ^Q:unfocus"
				help = "  " + indicator + " " + formatHelp(h)
			} else if a.paneProxy != nil && a.sessPreviewMode == sessPreviewLive && !a.sessSplit.Focus {
				indicator := a.paneProxyIndicator()
				h := "→:focus esc:close []:resize"
				help = "  " + indicator + " " + formatHelp(h)
			} else {
				sk := a.keymap.Session
				h := fmtKey(sk.Open, "open") + " " + fmtKey(sk.Edit, "edit") + " " + fmtKey(sk.Actions, "actions") + " " + fmtKey(sk.Views, "views") + " " + fmtKey(sk.Refresh, "refresh")
				if !a.sessSplit.Show {
					h += " →:preview tab:group"
				} else if a.sessSplit.Focus && a.sessPreviewMode == sessPreviewConversation {
					h += " ↑↓:nav c:full " + fmtKey(sk.Open, "jump") + " →←:fold f/F:all " + fmtKey(sk.Search, "search") + " tab:mode"
				} else if a.sessSplit.Focus {
					h += " tab:mode ←:unfocus " + displayKey(sk.ResizeShrink) + displayKey(sk.ResizeGrow) + ":resize"
				} else {
					h += " tab:group →:focus ←:close " + displayKey(sk.ResizeShrink) + displayKey(sk.ResizeGrow) + ":resize"
				}
				if a.config.TmuxEnabled && tmux.InTmux() {
					h += " " + fmtKey(sk.Live, "live")
				}
				help = formatHelp(h + " " + fmtKey(sk.Search, "search") + " " + fmtKey(sk.Help, "help") + " " + fmtKey(sk.Quit, "quit"))
			}
		}

	case viewGlobalStats:
		title = a.renderBreadcrumb()
		if a.globalStatsLoading {
			idx := a.spinnerFrame % len(spinnerFrames)
			frame := spinnerFrames[idx]
			spinnerColors := []lipgloss.Color{"#10B981", "#3B82F6", "#F59E0B", "#7C3AED", "#EC4899"}
			c := spinnerColors[a.spinnerFrame/len(spinnerFrames)%len(spinnerColors)]
			s := lipgloss.NewStyle().Foreground(c).Bold(true)
			content = "\n  " + s.Render(fmt.Sprintf("%s Scanning %d sessions…", frame, len(a.sessions)))
			help = formatHelp("loading… v:views q:quit")
		} else if a.statsDetail != statsDetailNone {
			content = a.statsDetailVP.View()
			help = formatHelp("p:page tab/S-tab:cycle ↑↓:scroll esc:back")
		} else {
			content = a.globalStatsVP.View()
			help = formatHelp("p:page tab:first ↑↓:scroll v:views q:quit")
		}

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
		if a.conv.blockFiltering {
			help = "  " + a.conv.blockFilterTI.View() + helpStyle.Render("  enter:apply esc:cancel")
		} else {
			h := "↵:open e:edit x:actions L:live R:refresh"
			if a.config.TmuxEnabled && tmux.InTmux() && a.currentSess.IsLive {
				h += " I:input J:jump"
			}
			if sp.Show {
				if sp.Focus {
					if a.conv.previewMode == previewText {
						h += " ↑↓:scroll"
					} else {
						h += " ↑↓:blocks ←→:fold f/F:all /:filter"
					}
				} else {
					// Show next mode label for tab hint
					next := previewModeLabels[(a.conv.previewMode+1)%3]
					h += " tab:" + next + " →:focus"
				}
				h += " esc:close []:resize"
			} else {
				h += " tab:preview →:preview"
			}
			// Show active block filter badge
			if sp.Folds != nil && sp.Folds.BlockFilter != "" {
				vis := countVisibleBlocks(sp.Folds.BlockVisible)
				total := len(sp.Folds.Entry.Content)
				filterInfo := filterBadge.Render(fmt.Sprintf(" [%d/%d] %s", vis, total, sp.Folds.BlockFilter))
				help = filterInfo + " " + badges + formatHelp(h+" /:search esc:back q:quit")
			} else {
				help = badges + formatHelp(h+" /:search esc:back q:quit")
			}
		}

	case viewMessageFull:
		title = a.renderBreadcrumb()
		content = a.renderMessageFull()
		if a.msgFull.blockFiltering {
			help = "  " + a.msgFull.blockFilterTI.View() + helpStyle.Render("  enter:apply esc:cancel")
		} else if a.msgFull.searching {
			help = "  " + a.msgFull.searchInput.View() + helpStyle.Render("  enter:search esc:cancel")
		} else if a.msgFull.allMessages {
			if a.copyModeActive {
				help = formatHelp("all messages  ↑↓:move v/sp:sel y/↵:copy home/end esc:cancel")
			} else {
				sh := "all messages  ↑↓:scroll v:copy y:all x:actions /:search"
				if a.msgFull.searchTerm != "" {
					sh += fmt.Sprintf(" [%d/%d] n/N:match", a.msgFull.searchIdx+1, len(a.msgFull.searchLines))
				}
				help = formatHelp(sh + " esc:back q:quit")
			}
		} else {
			pos := fmt.Sprintf("#%d/%d", a.msgFull.idx+1, len(a.msgFull.merged))
			if a.copyModeActive {
				help = formatHelp(pos + "  ↑↓:move v/sp:sel y/↵:copy home/end esc:cancel")
			} else {
				selCount := len(a.msgFull.folds.Selected)
				sh := pos + "  ↑↓:blocks ←→:fold sp:select n/N:msg f/F:all v:copy y:all x:actions /:filter"
				if selCount > 0 {
					sh = pos + fmt.Sprintf("  [%d sel] ↑↓:blocks sp:select y:copy esc:clear", selCount)
				} else if a.msgFull.searchTerm != "" {
					sh = pos + fmt.Sprintf("  [%d/%d] n/N:match ↑↓:blocks ←→:fold sp:select f/F:all v:copy y:all", a.msgFull.searchIdx+1, len(a.msgFull.searchLines))
				}
				// Show active block filter badge
				if a.msgFull.folds.BlockFilter != "" {
					vis := countVisibleBlocks(a.msgFull.folds.BlockVisible)
					total := len(a.msgFull.folds.Entry.Content)
					filterInfo := filterBadge.Render(fmt.Sprintf(" [%d/%d] %s", vis, total, a.msgFull.folds.BlockFilter))
					help = filterInfo + " " + formatHelp(sh+" esc:back q:quit")
				} else {
					help = formatHelp(sh + " esc:back q:quit")
				}
			}
		}

	case viewConfig:
		title = a.renderBreadcrumb()
		content = a.renderConfigSplit()
		if a.cfgProjectPicker {
			content = a.renderProjectPickerOverlay(content)
			help = formatHelp("/:filter ↵:select esc:cancel")
		} else if a.cfgNaming {
			help = "  " + a.cfgNamingInput.View() + helpStyle.Render("  enter:create esc:cancel")
		} else if a.cfgSearching {
			help = "  " + a.cfgSearchInput.View() + helpStyle.Render("  enter:apply esc:cancel")
		} else if a.cfgSearchTerm != "" {
			badge := fmt.Sprintf("[%d/%d]", a.cfgSearchIdx+1, len(a.cfgSearchMatch))
			if len(a.cfgSearchMatch) == 0 {
				badge = "[0/0]"
			}
			help = "  " + filterBadge.Render(badge) + formatHelp(" n/N:next/prev esc:clear")
		} else {
			h := "sp:sel x:actions p:page tab:filter P:project a:new /:search R:refresh v:views q:quit"
			if a.cfgHasSelection() {
				h = "sp:sel x:actions p:page tab:filter esc:clear q:quit"
			}
			if a.cfgSplit.Show {
				if a.cfgSplit.Focus {
					h = "↑↓:scroll esc:unfocus q:quit"
				} else if a.cfgHasSelection() {
					h = "↑↓:nav →:focus sp:sel x:actions p:page esc:clear q:quit"
				} else {
					h = "↑↓:nav →:focus sp:sel x:actions p:page tab:filter P:project a:new v:views q:quit"
				}
			}
			// Badges: filter + selection count
			var badges string
			if fl := a.cfgFilterLabel(); fl != "" {
				badges += filterBadge.Render(fl) + " "
			}
			if a.cfgHasSelection() {
				badges += filterBadge.Render(fmt.Sprintf("%d selected", len(a.cfgSelectedSet))) + " "
			}
			help = "  " + badges + formatHelp(h)
		}

	case viewPlugins:
		title = a.renderBreadcrumb()
		if a.plgDetailActive {
			content = a.renderPluginDetailSplit()
			h := "↑↓:nav →:preview sp:sel x:actions e:edit c:copy-path o:shell esc:back q:quit"
			if a.plgDetailSplit.Show && a.plgDetailSplit.Focus {
				h = "↑↓:scroll ←:unfocus q:quit"
			}
			help = "  " + formatHelp(h)
		} else {
			content = a.renderPluginSplit()
			if a.plgSearching {
				help = "  " + a.plgSearchInput.View() + helpStyle.Render("  enter:apply esc:cancel")
			} else if a.plgSearchTerm != "" {
				help = "  " + filterBadge.Render(a.plgSearchTerm) + formatHelp(" n/N:next/prev esc:clear")
			} else {
				h := "↑↓:nav ↵:open →:preview sp:select x:actions /:search R:refresh v:views esc:back q:quit"
				if a.plgSplit.Show && a.plgSplit.Focus {
					h = "↑↓:scroll ←:unfocus q:quit"
				}
				if a.plgHasSelection() {
					badges := filterBadge.Render(fmt.Sprintf("%d sel", len(a.plgSelectedSet)))
					help = "  " + badges + formatHelp(" "+h)
				} else {
					help = "  " + formatHelp(h)
				}
			}
		}
	}

	// Command mode help — overrides view-specific help in any view
	if a.cmdMode {
		help = "  " + a.cmdInput.View() + helpStyle.Render("  tab:complete ↵:run esc:cancel")
	}

	// URL menu hint box
	if a.urlMenu {
		hintBox := a.renderURLMenu()
		if hintBox != "" {
			content = placeHintBox(content, hintBox)
		}
		if a.urlSearching {
			help = "  " + a.urlSearchInput.View() + helpStyle.Render("  enter:apply esc:cancel")
		} else {
			help = formatHelp("↑↓:nav ↵:open y:copy /:search esc:close")
		}
	}

	// Conversation/message-full actions menu hint box
	if a.convActionsMenu && (a.state == viewConversation || a.state == viewMessageFull) {
		hintBox := renderConvActionsHintBox()
		content = placeHintBox(content, hintBox)
		help = formatHelp("x:actions — pick an action")
	}

	// Actions menu hint box floating above help line
	if a.actionsMenu && a.state == viewSessions {
		hintBox := a.renderActionsHintBox()
		content = placeHintBox(content, hintBox)
		help = formatHelp("x:actions — pick an action")
	}

	// Tag menu floating modal
	if a.tagMenu {
		modal := a.renderTagMenu()
		content = placeHintBox(content, modal)
	}

	// Config actions menu hint box
	if a.cfgActionsMenu && a.state == viewConfig {
		hintBox := a.renderCfgActionsHintBox()
		content = placeHintBox(content, hintBox)
		help = formatHelp("x:actions — pick an action")
	}

	// Plugin actions menu hint box
	if a.plgActionsMenu && a.state == viewPlugins {
		hintBox := a.renderPlgActionsHintBox()
		content = placeHintBox(content, hintBox)
		help = formatHelp("x:actions — pick an action")
	}

	// Plugin detail actions menu hint box
	if a.plgCompActionsMenu && a.state == viewPlugins && a.plgDetailActive {
		hintBox := a.renderPlgCompActionsHintBox()
		content = placeHintBox(content, hintBox)
		help = formatHelp("x:actions — pick an action")
	}

	// Views menu hint box floating above help line
	if a.viewsMenu {
		hintBox := a.renderViewsHintBox()
		content = placeHintBox(content, hintBox)
		help = formatHelp("v:views — pick a view")
	}

	// Edit menu hint box floating above help line
	if a.editMenu {
		hintBox := a.renderEditHintBox()
		content = placeHintBox(content, hintBox)
		help = formatHelp("e:edit — pick a file")
	}

	// Stats page jump hint box
	if a.statsPageMenu && a.state == viewGlobalStats {
		hintBox := a.renderStatsPageHintBox()
		content = placeHintBox(content, hintBox)
		help = formatHelp("p:page — pick a page")
	}

	// Config page jump hint box
	if a.cfgPageMenu && a.state == viewConfig {
		hintBox := a.renderCfgPageHintBox()
		content = placeHintBox(content, hintBox)
		help = formatHelp("p:page — pick a section")
	}

	// Block filter hint box floating above help line (conversation preview and full-screen message)
	if a.conv.blockFiltering && a.state == viewConversation {
		hintBox := renderBlockFilterHintBox()
		content = placeHintBox(content, hintBox)
	}
	if a.state == viewMessageFull {
		if a.msgFull.blockFiltering {
			hintBox := renderBlockFilterHintBox()
			content = placeHintBox(content, hintBox)
		} else if a.msgFull.searching {
			hintBox := renderMsgFullSearchHintBox()
			content = placeHintBox(content, hintBox)
		}
	}

	// Command mode hint box floating above help line
	if a.cmdMode {
		hintBox := a.renderCmdHintBox()
		if hintBox != "" {
			content = placeHintBox(content, hintBox)
		}
	}

	// Override help with filter input when filtering; hints float above
	if a.isFiltering() {
		val := a.activeFilterValue()
		prompt := helpKeyStyle.Render("Search: ") + val + blockCursorStyle.Render("▏")
		help = "  " + prompt + helpStyle.Render("  (space=AND) enter:apply esc:cancel")
		// Float hint box above the help line
		hintBox := a.renderSearchHintBox()
		if hintBox != "" {
			contentLines := strings.Split(content, "\n")
			boxLines := strings.Split(hintBox, "\n")
			boxH := len(boxLines)
			boxW := 0
			for _, l := range boxLines {
				if w := lipgloss.Width(l); w > boxW {
					boxW = w
				}
			}
			// Place hint box at bottom-left of content area
			startY := len(contentLines) - boxH
			if startY < 0 {
				startY = 0
			}
			for i, bl := range boxLines {
				y := startY + i
				if y < len(contentLines) {
					contentLines[y] = overlayLine(contentLines[y], bl, 1, a.width)
				}
			}
			content = strings.Join(contentLines, "\n")
		}
	} else if a.hasFilterApplied() {
		help = "  " + filterBadge.Render("[filtered]") + " " + help
	}

	if a.copiedMsg != "" {
		help += "  " + lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(a.copiedMsg)
	}

	screen := title + "\n" + content + "\n" + help

	// Live input modal overlays everything
	if a.liveInputActive {
		screen = a.liveInputModal.render(screen, a.width, a.height)
	}

	// Cross-session search overlays everything
	if a.searchActive {
		screen = a.renderSearchView()
	}

	return screen
}

// --- Key handlers ---

func (a *App) handleSessionKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While loading with no sessions yet, only allow quit
	if a.sessionsLoading && len(a.sessions) == 0 {
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return a.quit()
		}
		return a, nil
	}

	sp := &a.sessSplit
	key := msg.String()

	// Help overlay: any key closes it
	if a.showHelp {
		a.showHelp = false
		return a, nil
	}

	// Full text modal: scroll or dismiss
	if a.sessConvFullText != "" {
		switch key {
		case "esc", "q", "c":
			a.sessConvFullText = ""
			a.sessConvFullScroll = 0
		case "up", "k":
			if a.sessConvFullScroll > 0 {
				a.sessConvFullScroll--
			}
		case "down", "j":
			a.sessConvFullScroll++
		case "pgup":
			a.sessConvFullScroll = max(a.sessConvFullScroll-10, 0)
		case "pgdown":
			a.sessConvFullScroll += 10
		}
		return a, nil
	}

	// Tag menu: manage custom badges
	if a.tagMenu {
		return a.handleTagMenuKey(msg)
	}

	// Clear actions menu on any unrelated key
	if a.actionsMenu {
		return a.handleActionsMenu(key)
	}

	// Views menu: pick a view
	if a.viewsMenu {
		return a.handleViewsMenu(key)
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

	// Pane proxy focused: keys forwarded to tmux pane
	if a.isPaneProxyFocused() {
		switch key {
		case "ctrl+q":
			sp.Focus = false
			return a, tea.Batch(capturePaneCmd(a.paneProxy.pane), liveTickCmd())
		case "ctrl+g":
			// Jump to the actual tmux pane
			if err := tmux.SwitchToPane(a.paneProxy.pane); err != nil {
				a.copiedMsg = "Switch failed"
			}
			return a, nil
		case "ctrl+n":
			// Send backslash + enter for multi-line input in Claude
			return a, a.liveNewlineCmd()
		}
		return a.handlePaneProxyKey(key)
	}

	// View-specific keys
	km := a.keymap
	switch key {
	case km.Session.Quit:
		return a.quit()
	case km.Session.Escape:
		if a.hasMultiSelection() {
			a.clearMultiSelection()
			return a, nil
		}
		if sp.Show {
			// If in a non-default preview mode, go back to messages first
			if a.sessPreviewMode != sessPreviewConversation {
				a.closePaneProxy()
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
	case km.Session.Open:
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
	case km.Session.Select:
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		if a.selectedSet[sess.ID] {
			delete(a.selectedSet, sess.ID)
		} else {
			a.selectedSet[sess.ID] = true
		}
		idx := a.sessionList.Index()
		total := len(a.sessionList.VisibleItems())
		if idx < total-1 {
			a.sessionList.Select(idx + 1)
		}
		return a, nil
	case km.Session.Actions:
		if a.hasMultiSelection() {
			a.actionsMenu = true
			return a, nil
		}
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		a.actionsMenu = true
		a.actionsSess = sess
		return a, nil
	case km.Session.Live:
		if !a.config.TmuxEnabled {
			return a, nil
		}
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		return a.openLivePreview(sess)
	case km.Session.Edit:
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		return a.openEditMenu(sess)
	case km.Session.Group:
		a.sessGroupMode = (a.sessGroupMode + 1) % numGroupModes
		a.rebuildSessionList()
		return a, nil
	case km.Session.Refresh:
		cmd := a.doRefresh()
		a.copiedMsg = "Refreshed"
		return a, cmd
	case km.Session.Views:
		a.viewsMenu = true
		return a, nil
	case km.Session.GlobalSearch:
		a.enterSearchMode()
		return a, nil
	case km.Session.Help:
		a.showHelp = true
		return a, nil
	// Tab/shift+tab: context-aware cycling
	// List focused → cycle group mode; Preview focused → cycle preview mode
	case km.Session.Preview:
		if sp.Focus && sp.Show {
			a.cycleSessionPreviewMode()
		} else {
			a.sessGroupMode = (a.sessGroupMode + 1) % numGroupModes
			a.rebuildSessionList()
		}
		return a, nil
	case km.Session.PreviewBack:
		if sp.Focus && sp.Show {
			a.cycleSessionPreviewModeReverse()
		} else {
			a.sessGroupMode = (a.sessGroupMode - 1 + numGroupModes) % numGroupModes
			a.rebuildSessionList()
		}
		return a, nil
	case km.Session.Left:
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
	case km.Session.Right:
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
			if a.paneProxy != nil {
				return a, capturePaneCmd(a.paneProxy.pane) // immediate capture on focus
			}
			return a, nil
		}
	case km.Session.ResizeShrink:
		if sp.Show {
			a.adjustSplitRatio(-5) // preview larger
		}
		return a, nil
	case km.Session.ResizeGrow:
		if sp.Show {
			a.adjustSplitRatio(5) // preview smaller
		}
		return a, nil
	}

	// Translate navigation aliases (e.g. vim j→down, emacs ctrl+n→down)
	if nav, navMsg := a.keymap.TranslateNav(key, msg); nav != "" {
		key = nav
		msg = navMsg
	}

	// Focused preview: custom conversation nav or simple scroll
	if sp.Focus && sp.Show {
		if m, cmd, handled := a.handleFocusedPreviewKeys(sp, key); handled {
			return m, cmd
		}
	}

	// List boundary (up/down always navigate list, scroll preview at edges)
	if !sp.Focus && sp.HandleListBoundary(key) {
		return a, a.updateSessionPreview()
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
	previewCmd := a.updateSessionPreview()
	if cmd == nil {
		return m, previewCmd
	}
	if previewCmd != nil {
		return m, tea.Batch(cmd, previewCmd)
	}
	return m, cmd
}

// handlePaneProxyKey forwards a key to the tmux pane and captures the result.
// Uses captureAfterKeyCmd to send key + capture in one Cmd (no polling needed).
func (a *App) handlePaneProxyKey(key string) (tea.Model, tea.Cmd) {
	return a, captureAfterKeyCmd(a.paneProxy.pane, key)
}

// liveNewlineCmd sends backslash + Enter to the tmux pane for multi-line input.
func (a *App) liveNewlineCmd() tea.Cmd {
	pane := a.paneProxy.pane
	return func() tea.Msg {
		target := pane.Session + ":" + pane.Window + "." + pane.Pane
		exec.Command("tmux", "send-keys", "-l", "-t", target, "\\").Run()
		exec.Command("tmux", "send-keys", "-t", target, "Enter").Run()
		time.Sleep(30 * time.Millisecond)
		content, err := tmux.CapturePane(pane)
		if err != nil || !tmux.HasClaude(pane.PID) {
			return liveCaptureMsg{failed: true}
		}
		return liveCaptureMsg{content: content}
	}
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
	case "c":
		if a.sessConvCursor < len(visible) {
			text := entryFullText(visible[a.sessConvCursor].entry)
			if text != "" {
				a.sessConvFullText = text
				a.sessConvFullScroll = 0
			}
		}
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
	// Switch to stats view immediately, show spinner in viewport
	contentH := a.height - 3
	a.globalStatsVP = viewport.New(a.width, contentH)
	a.globalStatsVP.SetContent("")
	a.state = viewGlobalStats

	sessions := a.sessions
	return a, tea.Batch(
		spinnerTickCmd(),
		func() tea.Msg {
			return globalStatsMsg(session.AggregateStats(sessions))
		},
	)
}

func (a *App) handleGlobalStatsKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Views menu: pick a view
	if a.viewsMenu {
		return a.handleViewsMenu(key)
	}

	// Page jump menu: second key picks the page
	if a.statsPageMenu {
		a.statsPageMenu = false
		return a.handleStatsPageMenu(key)
	}

	// In detail view: tab cycles, esc goes back
	if a.statsDetail != statsDetailNone {
		switch key {
		case "q":
			return a.quit()
		case "esc":
			a.statsDetail = statsDetailNone
			return a, nil
		case a.keymap.Session.Views:
			a.viewsMenu = true
			return a, nil
		case "tab":
			return a.openStatsDetail(a.statsDetail.next())
		case "shift+tab":
			return a.openStatsDetail(a.statsDetail.prev())
		case "p":
			a.statsPageMenu = true
			return a, nil
		}
		var cmd tea.Cmd
		a.statsDetailVP, cmd = a.statsDetailVP.Update(msg)
		return a, cmd
	}

	switch key {
	case "q":
		return a.quit()
	case "esc":
		return a, nil
	case a.keymap.Session.Views:
		a.viewsMenu = true
		return a, nil
	case "p":
		a.statsPageMenu = true
		return a, nil
	case "tab":
		return a.openStatsDetail(statsDetailTools)
	case "shift+tab":
		return a.openStatsDetail(statsDetailLast)
	}
	var cmd tea.Cmd
	a.globalStatsVP, cmd = a.globalStatsVP.Update(msg)
	return a, cmd
}

func (a *App) handleStatsPageMenu(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "t":
		return a.openStatsDetail(statsDetailTools)
	case "m":
		return a.openStatsDetail(statsDetailMCP)
	case "a":
		return a.openStatsDetail(statsDetailAgents)
	case "s":
		return a.openStatsDetail(statsDetailSkills)
	case "c":
		return a.openStatsDetail(statsDetailCommands)
	case "e":
		return a.openStatsDetail(statsDetailErrors)
	case "o":
		// back to overview
		a.statsDetail = statsDetailNone
		return a, nil
	}
	return a, nil
}

func (a *App) renderStatsPageHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "

	line1 := hl.Render("t") + d.Render(":tools") + sp + hl.Render("m") + d.Render(":mcp") + sp + hl.Render("a") + d.Render(":agents")
	line2 := hl.Render("s") + d.Render(":skills") + sp + hl.Render("c") + d.Render(":cmds") + sp + hl.Render("e") + d.Render(":errors")
	line3 := hl.Render("o") + d.Render(":overview")

	body := strings.Join([]string{line1, line2, line3, d.Render("esc:cancel")}, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

func (a *App) openStatsDetail(mode statsDetailMode) (tea.Model, tea.Cmd) {
	if a.globalStatsCache == nil {
		return a, nil
	}
	a.statsDetail = mode
	contentH := a.height - 3
	a.statsDetailVP = viewport.New(a.width, contentH)
	a.statsDetailVP.SetContent(renderStatsDetail(mode, *a.globalStatsCache, a.width))
	return a, nil
}

// --- Live Preview (tmux capture in split pane) ---

func (a *App) openLivePreview(sess session.Session) (tea.Model, tea.Cmd) {
	if !sess.IsLive {
		a.copiedMsg = "not a live session"
		return a, nil
	}
	pane, found := tmux.FindPane(sess.ProjectPath, sess.ID)
	if !found {
		a.copiedMsg = "tmux pane not found"
		return a, nil
	}
	a.paneProxy = &paneProxyState{pane: pane, sessID: sess.ID}
	a.toggleSessionPreviewMode(sessPreviewLive)
	a.refreshLivePreview()
	return a, liveTickCmd()
}

func (a *App) refreshLivePreview() {
	if a.paneProxy == nil {
		a.sessSplit.Preview.SetContent(dimStyle.Render("(no pane)"))
		return
	}
	content, err := tmux.CapturePane(a.paneProxy.pane)
	if err != nil {
		a.sessSplit.Preview.SetContent(dimStyle.Render("(capture failed)"))
		return
	}
	a.sessSplit.Preview.SetContent(content)
	a.sessSplit.Preview.GotoBottom()
}

// isPaneProxyFocused returns true when pane proxy is active and preview is focused.
func (a *App) isPaneProxyFocused() bool {
	return a.paneProxy != nil && a.sessSplit.Focus && a.sessPreviewMode == sessPreviewLive
}

// paneProxyIndicator returns a styled [LIVE ●]/[SHELL ●] badge for the help line.
func (a *App) paneProxyIndicator() string {
	if a.paneProxy == nil {
		return ""
	}
	label := "LIVE"
	if a.paneProxy.isShell {
		label = "SHELL"
	}
	dot := "○"
	style := dimStyle
	if a.sessSplit.Focus {
		dot = "●"
		style = liveBadge
	}
	return style.Render("[" + label + " " + dot + "]")
}

// closePaneProxy cleans up the pane proxy, killing spawned shell windows.
func (a *App) closePaneProxy() {
	if a.paneProxy == nil {
		return
	}
	if a.paneProxy.isShell {
		tmux.KillWindow(a.paneProxy.pane)
	}
	a.paneProxy = nil
}

func (a *App) resumeSession(sess session.Session) (tea.Model, tea.Cmd) {
	dir := sess.ProjectPath
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}

	if sess.IsLive {
		// Live session: jump to existing pane
		pane, found := tmux.FindPane(sess.ProjectPath, sess.ID)
		if found {
			if err := tmux.SwitchToPane(pane); err != nil {
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
	if tmux.InTmux() {
		windowName := sess.ProjectName
		if windowName == "" {
			windowName = "claude"
		}
		if err := tmux.NewWindowClaude(windowName, dir, sess.ID); err != nil {
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

func (a *App) copySelectedSessionPath() (tea.Model, tea.Cmd) {
	sess, ok := a.selectedSession()
	if !ok {
		return a, nil
	}
	if sess.FilePath == "" {
		a.copiedMsg = "No session file"
		return a, nil
	}
	if err := copyToClipboard(sess.FilePath); err != nil {
		a.copiedMsg = "Copy failed"
		return a, nil
	}
	a.copiedMsg = "Session path copied"
	return a, nil
}

// --- Edit file with $EDITOR ---

type editChoice struct {
	key   string // "m", "t", "k", "p"
	label string // "memory", "todos", "tasks", "plan"
	path  string // file path to open
}

func editableFiles(sess session.Session) []editChoice {
	return []editChoice{
		{"s", "session", sess.FilePath},
	}
}

func (a *App) openEditMenu(sess session.Session) (tea.Model, tea.Cmd) {
	a.editMenu = true
	a.editSess = sess
	a.editChoices = nil

	// When inside a subagent, the primary file is the agent's JSONL
	if a.conv.agent.FilePath != "" {
		a.editChoices = append(a.editChoices,
			editChoice{"s", "agent", a.conv.agent.FilePath},
			editChoice{"p", "parent", sess.FilePath},
		)
	} else {
		a.editChoices = append(a.editChoices,
			editChoice{"s", "session", sess.FilePath},
		)
	}

	// If cursor is on a subagent item, offer its file
	if a.state == viewConversation {
		if item, ok := a.convList.SelectedItem().(convItem); ok && item.kind == convAgent && item.groupTag == "" {
			a.editChoices = append(a.editChoices,
				editChoice{"a", "agent:" + item.agent.ShortID, item.agent.FilePath},
			)
		}
	}

	// Offer images from the current message (extracted from cache or JSONL base64)
	if a.state == viewConversation || a.state == viewMessageFull {
		var entry session.Entry
		if a.state == viewConversation {
			// Try folds first, then fall back to selected list item
			if a.conv.split.Folds != nil {
				entry = a.conv.split.Folds.Entry
			}
			if len(entry.Content) == 0 {
				if item, ok := a.convList.SelectedItem().(convItem); ok && item.kind == convMsg {
					entry = item.merged.entry
				}
			}
		} else {
			entry = a.msgFull.folds.Entry
		}
		imgCount := 0
		for _, block := range entry.Content {
			if block.Type == "image" && block.ImagePasteID > 0 {
				if p := a.resolveImagePath(block.ImagePasteID); p != "" {
					key := "i"
					if imgCount > 0 {
						key = fmt.Sprintf("%d", imgCount)
					}
					a.editChoices = append(a.editChoices,
						editChoice{key, fmt.Sprintf("image #%d", block.ImagePasteID), p},
					)
					imgCount++
				}
			}
		}
	}

	a.editChoices = append(a.editChoices, editChoice{"t", "text", ""})
	return a, nil
}

func (a *App) handleEditMenu(key string) (tea.Model, tea.Cmd) {
	a.editMenu = false
	for _, c := range a.editChoices {
		if c.key == key && c.path != "" {
			return a.openInEditor(c.path)
		}
	}
	if key == "t" {
		return a.openConvAsText()
	}
	a.copiedMsg = ""
	return a, nil
}

func (a *App) handleViewsMenu(key string) (tea.Model, tea.Cmd) {
	a.viewsMenu = false
	a.copiedMsg = ""
	switch key {
	case a.keymap.Views.Stats:
		return a.openGlobalStats()
	case a.keymap.Views.Config:
		return a.openConfigExplorer()
	case a.keymap.Views.Plugins:
		return a.openPluginExplorer()
	case "enter", " ":
		// Sessions (default)
		a.state = viewSessions
		return a, nil
	}
	return a, nil
}

func (a *App) renderViewsHintBox() string {
	h := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "
	km := a.keymap.Views
	// Highlight current view
	var parts []string
	viewLabel := func(k, label string, active bool) string {
		if active {
			return lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Bold(true).Render("[" + label + "]")
		}
		return h.Render(displayKey(k)) + d.Render(":"+label)
	}
	parts = append(parts, viewLabel("↵", "sessions", a.state == viewSessions))
	parts = append(parts, viewLabel(km.Stats, "stats", a.state == viewGlobalStats))
	parts = append(parts, viewLabel(km.Config, "config", a.state == viewConfig))
	parts = append(parts, viewLabel(km.Plugins, "plugins", a.state == viewPlugins))
	line := strings.Join(parts, sp)
	body := line + "\n" + d.Render("esc:cancel")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

func (a *App) renderEditHintBox() string {
	h := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "
	var parts []string
	for _, c := range a.editChoices {
		parts = append(parts, h.Render(c.key)+d.Render(":"+c.label))
	}
	line := strings.Join(parts, sp)
	body := line + "\n" + d.Render("esc:cancel")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

func (a *App) handleActionsMenu(key string) (tea.Model, tea.Cmd) {
	a.actionsMenu = false
	a.copiedMsg = ""
	if a.hasMultiSelection() {
		return a.handleBulkActionsMenu(key)
	}
	akm := a.keymap.Actions
	sess := a.actionsSess
	switch key {
	case akm.Delete:
		if sess.IsLive {
			a.copiedMsg = "Cannot delete live session"
			return a, nil
		}
		return a.deleteSession(sess)
	case akm.Resume:
		return a.resumeSession(sess)
	case akm.CopyPath:
		return a.copySelectedSessionPath()
	case akm.Move:
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
	case akm.Kill:
		if !sess.IsLive {
			a.copiedMsg = "Not a live session"
			return a, nil
		}
		return a.killLiveSession(sess)
	case akm.Input:
		if !a.config.TmuxEnabled {
			return a, nil
		}
		return a.openLiveInput(sess.ProjectPath, sess.ID)
	case akm.Jump:
		if !a.config.TmuxEnabled {
			return a, nil
		}
		return a.jumpToTmuxPane(sess.ProjectPath, sess.ID)
	case akm.Worktree:
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
	case akm.URLs:
		return a.openURLMenuFromItems(extract.SessionURLs(sess.FilePath), "session")
	case akm.Files:
		return a.openURLMenuFromItems(extract.SessionFilePaths(sess.FilePath), "session files")
	case akm.Tags:
		a.tagMenu = true
		a.tagSessID = sess.ID
		a.tagList = a.badgeStore.AllBadges()
		a.tagCursor = 0
		a.tagInput.SetValue("")
		a.tagInput.Focus()
		return a, a.tagInput.Cursor.BlinkCmd()
	case akm.ImportMem:
		return a.importWorktreeMemory(sess)
	case akm.RemoveMem:
		return a.removeSessionMemory(sess)
	case akm.Fork:
		return a.forkSession(sess)
	}
	return a, nil
}

// forkSession resumes a session in a new tmux window, creating a fork.
func (a *App) forkSession(sess session.Session) (tea.Model, tea.Cmd) {
	dir := sess.ProjectPath
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}

	if tmux.InTmux() {
		windowName := sess.ProjectName
		if windowName == "" {
			windowName = "claude"
		}
		windowName += "-fork"
		if err := tmux.NewWindowClaude(windowName, dir, sess.ID); err != nil {
			a.copiedMsg = "Fork failed: " + err.Error()
		} else {
			a.copiedMsg = "Forked → " + windowName
		}
		return a, nil
	}

	// Non-tmux: take over terminal
	c := exec.Command("claude", "--resume", sess.ID)
	c.Dir = dir
	return a, tea.ExecProcess(c, func(err error) tea.Msg {
		return editorDoneMsg{}
	})
}

func (a *App) handleBulkActionsMenu(key string) (tea.Model, tea.Cmd) {
	akm := a.keymap.Actions
	selected := a.selectedSessions()
	switch key {
	case akm.Delete:
		return a.bulkDelete(selected)
	case akm.Resume:
		return a.bulkResume(selected)
	case akm.Kill:
		return a.bulkKill(selected)
	case akm.Input:
		return a.bulkInput(selected)
	case akm.Tags:
		// Collect all selected session IDs
		var sessIDs []string
		for _, s := range selected {
			sessIDs = append(sessIDs, s.ID)
		}
		a.tagMenu = true
		a.tagSessIDs = sessIDs // Use plural for multi-select
		a.tagSessID = ""       // Clear single session
		a.tagList = a.badgeStore.AllBadges()
		a.tagCursor = 0
		a.tagInput.SetValue("")
		a.tagInput.Focus()
		return a, a.tagInput.Cursor.BlinkCmd()
	}
	return a, nil
}

func (a *App) bulkDelete(selected []session.Session) (tea.Model, tea.Cmd) {
	deleted, skipped := 0, 0
	deletedIDs := make(map[string]bool)
	for _, s := range selected {
		if s.IsLive {
			skipped++
			continue
		}
		if err := os.Remove(s.FilePath); err != nil && !os.IsNotExist(err) {
			skipped++
			continue
		}
		deleted++
		deletedIDs[s.ID] = true
		delete(a.selectedSet, s.ID)
	}
	// Rebuild session list without actually deleted sessions
	var remaining []session.Session
	for _, s := range a.sessions {
		if !deletedIDs[s.ID] {
			remaining = append(remaining, s)
		}
	}
	a.sessions = remaining
	if a.hasFilterApplied() {
		a.sessionList.ResetFilter()
	}
	items := buildGroupedItems(remaining, a.sessGroupMode)
	a.sessionList.SetItems(items)
	idx := a.sessionList.Index()
	if idx >= len(items) {
		idx = len(items) - 1
	}
	if idx >= 0 {
		a.sessionList.Select(idx)
	}
	a.sessSplit.CacheKey = ""
	a.clearMultiSelection()
	if skipped > 0 {
		a.copiedMsg = fmt.Sprintf("Deleted %d (skipped %d live)", deleted, skipped)
	} else {
		a.copiedMsg = fmt.Sprintf("Deleted %d", deleted)
	}
	return a, nil
}

func (a *App) bulkResume(selected []session.Session) (tea.Model, tea.Cmd) {
	if !tmux.InTmux() {
		a.copiedMsg = "Requires tmux"
		return a, nil
	}
	count := 0
	for _, s := range selected {
		if s.IsLive {
			continue
		}
		dir := s.ProjectPath
		if dir == "" {
			dir, _ = os.UserHomeDir()
		}
		name := s.ProjectName
		if name == "" {
			name = s.ShortID
		}
		if err := tmux.NewWindowClaude(name, dir, s.ID); err == nil {
			count++
		}
	}
	a.clearMultiSelection()
	a.copiedMsg = fmt.Sprintf("Resumed %d", count)
	return a, nil
}

func (a *App) bulkKill(selected []session.Session) (tea.Model, tea.Cmd) {
	count := 0
	for _, s := range selected {
		if !s.IsLive {
			continue
		}
		pane, found := tmux.FindPane(s.ProjectPath, s.ID)
		if !found {
			continue
		}
		target := pane.Session + ":" + pane.Window + "." + pane.Pane
		exec.Command("tmux", "send-keys", "-t", target, "C-c").Run()
		exec.Command("tmux", "send-keys", "-t", target, "C-c").Run()
		if a.paneProxy != nil && a.paneProxy.sessID == s.ID {
			a.closePaneProxy()
			a.sessPreviewMode = sessPreviewConversation
			a.sessSplit.CacheKey = ""
			a.sessSplit.Focus = false
		}
		count++
	}
	a.clearMultiSelection()
	a.copiedMsg = fmt.Sprintf("Killed %d", count)
	return a, nil
}

func (a *App) bulkInput(selected []session.Session) (tea.Model, tea.Cmd) {
	if !tmux.InTmux() {
		a.copiedMsg = "Requires tmux"
		return a, nil
	}
	var panes []tmux.Pane
	for _, s := range selected {
		if !s.IsLive {
			continue
		}
		pane, found := tmux.FindPane(s.ProjectPath, s.ID)
		if !found || !tmux.HasClaude(pane.PID) {
			continue
		}
		panes = append(panes, pane)
	}
	if len(panes) == 0 {
		a.copiedMsg = "No live Claude panes"
		return a, nil
	}
	a.liveInputPanes = panes
	a.liveInputModal = newInputModal()
	a.liveInputModal.title = fmt.Sprintf("Send to %d panes", len(panes))
	a.liveInputActive = true
	a.liveInputProjDir = selected[0].ProjectPath
	return a, nil
}

// killLiveSession sends SIGHUP to the Claude process in the session's tmux pane.
func (a *App) killLiveSession(sess session.Session) (tea.Model, tea.Cmd) {
	pane, found := tmux.FindPane(sess.ProjectPath, sess.ID)
	if !found {
		// Fallback: try to find any Claude process for this path
		pane, found = tmux.FindPane(sess.ProjectPath)
		if !found {
			a.copiedMsg = "No tmux pane found"
			return a, nil
		}
	}
	// Send ctrl+c then ctrl+d to gracefully stop Claude
	target := pane.Session + ":" + pane.Window + "." + pane.Pane
	exec.Command("tmux", "send-keys", "-t", target, "C-c").Run()
	exec.Command("tmux", "send-keys", "-t", target, "C-c").Run()
	// Close any live preview for this session
	if a.paneProxy != nil && a.paneProxy.sessID == sess.ID {
		a.closePaneProxy()
		a.sessPreviewMode = sessPreviewConversation
		a.sessSplit.CacheKey = ""
		a.sessSplit.Focus = false
	}
	a.copiedMsg = "Killed"
	return a, nil
}

func (a *App) resolveImagePath(pasteID int) string {
	home, _ := os.UserHomeDir()
	p, err := session.ExtractImageToTemp(home, a.currentSess.FilePath, a.currentSess.ID, pasteID)
	if err != nil {
		return ""
	}
	return p
}

// openMessageImage finds the first image in the current message and opens it.
// Works from conversation view (split preview) and detail view.
func (a *App) openMessageImage() (tea.Model, tea.Cmd) {
	var entry session.Entry
	switch a.state {
	case viewConversation:
		if a.conv.split.Folds != nil {
			entry = a.conv.split.Folds.Entry
		}
		if len(entry.Content) == 0 {
			if item, ok := a.convList.SelectedItem().(convItem); ok && item.kind == convMsg {
				entry = item.merged.entry
			}
		}
	case viewMessageFull:
		entry = a.msgFull.folds.Entry
	}

	// If block cursor is on an image, open that one
	var folds *FoldState
	if a.state == viewConversation && a.conv.split.Folds != nil {
		folds = a.conv.split.Folds
	} else if a.state == viewMessageFull {
		folds = &a.msgFull.folds
	}
	if folds != nil {
		bc := folds.BlockCursor
		if bc >= 0 && bc < len(entry.Content) && entry.Content[bc].Type == "image" && entry.Content[bc].ImagePasteID > 0 {
			return a.openCachedImage(entry.Content[bc].ImagePasteID)
		}
	}

	// Otherwise open the first image in the message
	for _, block := range entry.Content {
		if block.Type == "image" && block.ImagePasteID > 0 {
			return a.openCachedImage(block.ImagePasteID)
		}
	}

	a.copiedMsg = "No image in this message"
	return a, nil
}

func (a *App) openCachedImage(pasteID int) (tea.Model, tea.Cmd) {
	p := a.resolveImagePath(pasteID)
	if p == "" {
		a.copiedMsg = "Image not found"
		return a, nil
	}
	c := exec.Command("open", p)
	if err := c.Start(); err != nil {
		a.copiedMsg = "Error: " + err.Error()
		return a, nil
	}
	a.copiedMsg = "Opened image #" + fmt.Sprintf("%d", pasteID)
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
	os.RemoveAll(filepath.Join(filepath.Dir(sess.FilePath), sess.ID))
	os.RemoveAll(filepath.Join(a.config.ClaudeDir, "file-history", sess.ID))
	os.RemoveAll(filepath.Join(a.config.ClaudeDir, "tasks", sess.ID))

	delete(a.selectedSet, sess.ID)

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
	pane, found := tmux.FindPane(projectPath, sessionID...)
	if !found {
		a.copiedMsg = "No tmux pane found"
		return a, nil
	}
	if err := tmux.SwitchToPane(pane); err != nil {
		a.copiedMsg = "Switch failed"
		return a, nil
	}
	return a, nil
}

// liveInputSentMsg is sent after async tmuxSendKeys completes.
type liveInputSentMsg struct{ err error }

func (a *App) openLiveInput(projectPath string, sessionID ...string) (tea.Model, tea.Cmd) {
	if !tmux.InTmux() {
		a.copiedMsg = "Requires tmux"
		return a, nil
	}
	pane, found := tmux.FindPane(projectPath, sessionID...)
	if !found || !tmux.HasClaude(pane.PID) {
		a.copiedMsg = "No live Claude pane"
		return a, nil
	}
	a.liveInputPane = pane
	a.liveInputProjDir = projectPath

	// If user prefers $EDITOR, skip inline modal and open editor directly
	if a.editorInput {
		return a, func() tea.Msg {
			tmpFile, err := os.CreateTemp("", "ccx-input-*.md")
			if err != nil {
				return liveInputSentMsg{err: err}
			}
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vim"
			}
			cmd := fmt.Sprintf("cd %s && %s %s", shellescape(projectPath), editor, tmpFile.Name())
			exec.Command("tmux", "display-popup", "-E", "-w", "80%", "-h", "70%", cmd).Run()

			content, readErr := os.ReadFile(tmpFile.Name())
			if readErr != nil || len(strings.TrimSpace(string(content))) == 0 {
				return liveInputSentMsg{err: fmt.Errorf("empty")}
			}
			sendText := strings.TrimRight(string(content), "\n")
			return liveInputSentMsg{err: tmux.SendKeys(pane, sendText)}
		}
	}

	a.liveInputModal = newInputModal()
	a.liveInputActive = true
	return a, nil
}

func (a *App) handleLiveInputKey(key string) (tea.Model, tea.Cmd) {
	action := a.liveInputModal.handleKey(key)

	switch action {
	case "send":
		// Remember inline preference
		a.editorInput = false
		text := strings.TrimRight(a.liveInputModal.Text(), "\n")
		if strings.TrimSpace(text) == "" {
			a.liveInputActive = false
			a.liveInputPanes = nil
			return a, nil
		}
		a.liveInputActive = false
		if len(a.liveInputPanes) > 0 {
			panes := a.liveInputPanes
			a.liveInputPanes = nil
			return a, func() tea.Msg {
				var lastErr error
				for _, p := range panes {
					if err := tmux.SendKeys(p, text); err != nil {
						lastErr = err
					}
				}
				return liveInputSentMsg{err: lastErr}
			}
		}
		pane := a.liveInputPane
		return a, func() tea.Msg {
			err := tmux.SendKeys(pane, text)
			return liveInputSentMsg{err: err}
		}
	case "editor":
		// Remember editor preference for next time
		a.editorInput = true
		// Write current text to temp file, open $EDITOR in tmux popup
		a.liveInputActive = false
		panes := a.liveInputPanes
		a.liveInputPanes = nil
		pane := a.liveInputPane
		text := a.liveInputModal.Text()
		projDir := a.liveInputProjDir
		return a, func() tea.Msg {
			tmpFile, err := os.CreateTemp("", "ccx-input-*.md")
			if err != nil {
				return liveInputSentMsg{err: err}
			}
			tmpFile.WriteString(text)
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vim"
			}
			cmd := fmt.Sprintf("cd %s && %s %s", shellescape(projDir), editor, tmpFile.Name())
			exec.Command("tmux", "display-popup", "-E", "-w", "80%", "-h", "70%", cmd).Run()

			content, readErr := os.ReadFile(tmpFile.Name())
			if readErr != nil || len(strings.TrimSpace(string(content))) == 0 {
				return liveInputSentMsg{err: fmt.Errorf("empty")}
			}
			sendText := strings.TrimRight(string(content), "\n")
			if len(panes) > 0 {
				var lastErr error
				for _, p := range panes {
					if err := tmux.SendKeys(p, sendText); err != nil {
						lastErr = err
					}
				}
				return liveInputSentMsg{err: lastErr}
			}
			return liveInputSentMsg{err: tmux.SendKeys(pane, sendText)}
		}
	case "cancel":
		a.liveInputActive = false
		a.liveInputPanes = nil
		return a, nil
	}
	return a, nil
}

// shellescape wraps a string in single quotes for safe shell usage.
func shellescape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// --- Live refresh ---

// refreshRespondingState re-checks IsResponding for live sessions by stat-ing
// their JSONL files. Updates the list if any badge changed.
func (a *App) refreshRespondingState() {
	changed := false
	for i := range a.sessions {
		if !a.sessions[i].IsLive {
			if a.sessions[i].IsResponding {
				a.sessions[i].IsResponding = false
				changed = true
			}
			continue
		}
		info, err := os.Stat(a.sessions[i].FilePath)
		if err != nil {
			continue
		}
		wasResponding := a.sessions[i].IsResponding
		a.sessions[i].IsResponding = time.Since(info.ModTime()) < 10*time.Second
		if a.sessions[i].IsResponding != wasResponding {
			changed = true
		}
	}
	if changed && !a.isFiltering() && !a.hasFilterApplied() {
		a.rebuildSessionList()
	}
}

func (a *App) handleTick() tea.Cmd {
	// Always refresh conversation preview for live sessions (regardless of liveUpdate)
	if a.state == viewSessions && a.sessSplit.Show && a.sessPreviewMode == sessPreviewConversation {
		if sess, ok := a.selectedSession(); ok && sess.IsLive {
			a.sessSplit.CacheKey = ""    // invalidate to force re-fetch
			_ = a.updateSessionPreview() // conversation mode returns nil cmd
		}
	}
	// Always re-check IsResponding for live sessions (cheap os.Stat check).
	// Without this, BUSY badges go stale when liveUpdate is off.
	if a.state == viewSessions {
		a.refreshRespondingState()
	}

	if !a.liveUpdate {
		return nil
	}
	return a.doRefresh()
}

func (a *App) doRefresh() tea.Cmd {
	switch a.state {
	case viewSessions:
		// Full rescan to discover new/deleted sessions
		fresh, err := session.ScanSessions(a.config.ClaudeDir)
		if err == nil && len(fresh) > 0 {
			// Preserve live state detection
			tmux.MarkLiveSessions(fresh)

			// Remember cursor position
			selectedID := ""
			if sess, ok := a.selectedSession(); ok {
				selectedID = sess.ID
			}

			a.sessions = fresh
			a.globalStatsCache = nil // invalidate cached stats

			if !a.isFiltering() && !a.hasFilterApplied() {
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
			}
		} else {
			// Fallback: lightweight stat-only refresh
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
			type liveState struct{ live, responding bool }
			oldLive := make([]liveState, len(a.sessions))
			for i := range a.sessions {
				oldLive[i] = liveState{a.sessions[i].IsLive, a.sessions[i].IsResponding}
				a.sessions[i].IsLive = false
				a.sessions[i].IsResponding = false
			}
			tmux.MarkLiveSessions(a.sessions)
			for i := range a.sessions {
				if a.sessions[i].IsLive != oldLive[i].live {
					needsSort = true
				}
				if a.sessions[i].IsResponding != oldLive[i].responding {
					needsRefresh = true
				}
			}
			if (needsSort || needsRefresh) && !a.isFiltering() && !a.hasFilterApplied() {
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
			}
		}

		// Refresh preview for live sessions (auto-scroll to bottom)
		a.refreshSessionPreviewLive()

		// Prune stale selectedSet entries
		if a.hasMultiSelection() {
			valid := make(map[string]bool, len(a.sessions))
			for _, s := range a.sessions {
				valid[s.ID] = true
			}
			for id := range a.selectedSet {
				if !valid[id] {
					delete(a.selectedSet, id)
				}
			}
		}
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
			func() int {
				if sp.Folds != nil {
					return sp.Folds.BlockCursor
				}
				return -1
			}(),
			sp.Preview.TotalLineCount(), sp.Preview.Height)

		a.scrollConvPreviewToTail()

		debugLog.Printf("handleLiveTail: after scrollToTail YOffset=%d blockCursor=%d",
			sp.Preview.YOffset,
			func() int {
				if sp.Folds != nil {
					return sp.Folds.BlockCursor
				}
				return -1
			}())

	case viewMessageFull:
		a.handleLiveTailMsgFull()
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
// Returns a tea.Cmd if async work is needed (e.g., live pane lookup).
func (a *App) refreshActivePreview() tea.Cmd {
	switch a.state {
	case viewSessions:
		return a.updateSessionPreview()
	case viewConversation:
		if a.conv.previewMode == previewText {
			a.conv.split.CacheKey = "" // force re-render
			a.updateConvPreview()
		} else {
			a.conv.split.RefreshFoldPreview(a.width, a.splitRatio)
		}
	}
	return nil
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

	// Don't call updateSessionPreview for live mode from render path — the
	// returned async cmd would be discarded. Live mode is initialized from
	// Update paths (resizeAll, key handlers) where cmds are dispatched.
	if a.sessPreviewMode != sessPreviewLive {
		_ = a.updateSessionPreview()
	}

	if a.sessSplit.Preview.Width != previewW || a.sessSplit.Preview.Height != contentH {
		oldOffset := a.sessSplit.Preview.YOffset
		oldTotal := a.sessSplit.Preview.TotalLineCount()
		a.sessSplit.Preview.Width = previewW
		a.sessSplit.Preview.Height = max(contentH, 1)
		// Re-render at new size without reloading data or resetting cursor
		if a.sessPreviewMode == sessPreviewConversation && len(a.sessConvEntries) > 0 {
			a.refreshConvPreview()
		} else if a.sessPreviewMode != sessPreviewLive {
			a.sessSplit.CacheKey = ""
			_ = a.updateSessionPreview()
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
	if a.sessPreviewMode == sessPreviewLive && a.paneProxy != nil {
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
	a.closePaneProxy()
	a.sessSplit.CacheKey = ""
}

// cycleSessionPreviewModeReverse goes to the previous preview tab.
// Skips sessPreviewLive — it's only entered via the L key.
func (a *App) cycleSessionPreviewModeReverse() {
	a.sessPreviewMode = (a.sessPreviewMode + numSessPreviewModes - 1) % numSessPreviewModes
	if a.sessPreviewMode == sessPreviewLive {
		a.sessPreviewMode = (a.sessPreviewMode + numSessPreviewModes - 1) % numSessPreviewModes
	}
	a.closePaneProxy()
	a.sessSplit.CacheKey = ""
}

// liveFindMsg carries the result of an async findTmuxPane lookup.
type liveFindMsg struct {
	pane   tmux.Pane
	found  bool
	sessID string
}

func (a *App) updateSessionPreview() tea.Cmd {
	if !a.sessSplit.Show {
		return nil
	}
	sess, ok := a.selectedSession()
	if !ok {
		return nil
	}

	cacheKey := fmt.Sprintf("%d:%s", a.sessPreviewMode, sess.ID)
	if cacheKey == a.sessSplit.CacheKey {
		return nil
	}

	// If conversation data is already loaded for this session, just re-render
	// at the new size without reloading data or resetting the cursor.
	if a.sessPreviewMode == sessPreviewConversation && len(a.sessConvEntries) > 0 && a.sessConvCacheID == sess.ID {
		a.sessSplit.CacheKey = cacheKey
		a.refreshConvPreview()
		return nil
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
			a.sessSplit.Preview.SetContent(dimStyle.Render("(connecting…)"))
			// Async: find pane + capture without blocking navigation
			projectPath := sess.ProjectPath
			sessID := sess.ID
			return func() tea.Msg {
				pane, found := tmux.FindPane(projectPath, sessID)
				return liveFindMsg{pane: pane, found: found, sessID: sessID}
			}
		}
		a.closePaneProxy()
		a.sessSplit.Preview.SetContent(dimStyle.Render("(not a live session)"))
	default:
		a.updateSessionConvPreview(sess)
	}
	return nil
}

// prependConvHeaders prepends fork-origin and todo headers to conversation preview content.
func (a *App) prependConvHeaders(sess session.Session, content string, previewW int) string {
	// Fork origin header
	if sess.ParentSessionID != "" {
		parentLabel := sess.ParentSessionID[:min(8, len(sess.ParentSessionID))]
		for i := range a.sessions {
			if a.sessions[i].ID == sess.ParentSessionID {
				prompt := a.sessions[i].FirstPrompt
				maxPromptW := previewW - 20
				if maxPromptW > 0 && len(prompt) > maxPromptW {
					prompt = prompt[:maxPromptW-3] + "..."
				}
				parentLabel += " " + prompt
				break
			}
		}
		header := dimStyle.Render("── Forked from: "+parentLabel+" ──") + "\n\n"
		content = header + content
	}

	// Todo progress header
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

	return content
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

	content := a.prependConvHeaders(sess, rendered, previewW)

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
	if sess, ok2 := a.selectedSession(); ok2 {
		content = a.prependConvHeaders(sess, content, previewW)
	}
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

	// Find the target message in the visible list items by UUID or timestamp.
	// Must search the list's items (visible only), not a.conv.items (includes folded).
	bestIdx := 0
	items := a.convList.Items()
	found := false

	if target.entry.UUID != "" {
		for i, li := range items {
			ci, ok := li.(convItem)
			if ok && ci.kind == convMsg && ci.merged.entry.UUID == target.entry.UUID {
				bestIdx = i
				found = true
				break
			}
		}
	}
	if !found && !target.entry.Timestamp.IsZero() {
		bestDist := time.Duration(math.MaxInt64)
		for i, li := range items {
			ci, ok := li.(convItem)
			if !ok || ci.kind != convMsg || ci.merged.entry.Role != target.entry.Role {
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

// renderActionsHintBox renders a compact bordered hint box for the actions menu.
func (a *App) renderActionsHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "
	akm := a.keymap.Actions

	var lines []string
	if a.hasMultiSelection() {
		header := fmt.Sprintf("%d selected", len(a.selectedSet))
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(header))
		lines = append(lines, hl.Render(displayKey(akm.Delete))+d.Render(":delete")+sp+hl.Render(displayKey(akm.Resume))+d.Render(":resume")+sp+hl.Render(displayKey(akm.Kill))+d.Render(":kill")+sp+hl.Render(displayKey(akm.Input))+d.Render(":input"))
		lines = append(lines, hl.Render(displayKey(akm.Tags))+d.Render(":tags"))
	} else {
		sess := a.actionsSess
		lines = append(lines, hl.Render(displayKey(akm.Delete))+d.Render(":delete")+sp+hl.Render(displayKey(akm.Move))+d.Render(":move")+sp+hl.Render(displayKey(akm.Resume))+d.Render(":resume")+sp+hl.Render(displayKey(akm.CopyPath))+d.Render(":copy-path"))
		line2 := hl.Render(displayKey(akm.Worktree)) + d.Render(":worktree") + sp + hl.Render(displayKey(akm.URLs)) + d.Render(":urls") + sp + hl.Render(displayKey(akm.Files)) + d.Render(":files") + sp + hl.Render(displayKey(akm.Tags)) + d.Render(":tags")
		if sess.HasMemory {
			line2 += sp + hl.Render(displayKey(akm.RemoveMem)) + d.Render(":rm-mem")
		}
		if sess.IsWorktree {
			line2 += sp + hl.Render(displayKey(akm.ImportMem)) + d.Render(":import-mem")
		}
		line2 += sp + hl.Render(displayKey(akm.Fork)) + d.Render(":fork")
		lines = append(lines, line2)
		if sess.IsLive && a.config.TmuxEnabled {
			lines = append(lines, hl.Render(displayKey(akm.Kill))+d.Render(":kill")+sp+hl.Render(displayKey(akm.Input))+d.Render(":input")+sp+hl.Render(displayKey(akm.Jump))+d.Render(":jump"))
		}
	}
	lines = append(lines, d.Render("esc:cancel"))

	body := strings.Join(lines, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

// renderSearchHintBox renders a compact bordered hint box for search filters.
func (a *App) renderSearchHintBox() string {
	h := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8"))
	d := dimStyle
	sp := " "

	var lines []string
	switch a.state {
	case viewSessions:
		lines = []string{
			h.Render("is:") + d.Render("live wt team"),
			h.Render("has:") + d.Render("mem todo task plan agent compact skill mcp"),
			h.Render("tag:") + d.Render("badge-name"),
			d.Render("text: project branch prompt"),
		}
	case viewConversation:
		lines = []string{
			h.Render("role:") + d.Render("user") + sp + h.Render("role:") + d.Render("asst"),
			h.Render("tool:") + d.Render("Bash Read Edit Write"),
		}
	case viewConfig:
		lines = []string{
			h.Render("is:") + d.Render("user project local"),
			h.Render("is:") + d.Render("memory skill agent command hook mcp"),
			d.Render("text: filename description"),
		}
	case viewPlugins:
		lines = []string{
			h.Render("is:") + d.Render("installed available enabled blocked"),
			h.Render("has:") + d.Render("agent skill command hook mcp lsp script setting memory"),
			d.Render("text: name marketplace description"),
		}
	default:
		return ""
	}

	body := strings.Join(lines, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

func (a *App) syncAllFilterVisibility() {
	syncFilterVisibility(&a.sessionList)
	syncFilterVisibility(&a.convList)
}

// isInTextInput returns true when the user is typing in any text input
// (search, move, worktree, live input, block filter, etc.) where ':' should be literal.
func (a *App) isInTextInput() bool {
	return a.isFiltering() || a.moveMode || a.worktreeMode ||
		a.sessConvSearching || a.liveInputActive || a.cfgSearching || a.cfgNaming ||
		a.urlSearching || a.conv.blockFiltering || a.msgFull.blockFiltering ||
		a.msgFull.searching
}

func (a *App) isFiltering() bool {
	switch a.state {
	case viewSessions:
		return a.sessionList.FilterState() == list.Filtering
	case viewConversation:
		return a.convList.FilterState() == list.Filtering
	case viewConfig:
		return a.cfgSearching || a.cfgNaming || a.cfgProjectPicker
	case viewPlugins:
		return a.plgSearching
	}
	return false
}

func (a *App) hasFilterApplied() bool {
	switch a.state {
	case viewSessions:
		return a.sessionList.FilterState() == list.FilterApplied
	case viewConversation:
		return a.convList.FilterState() == list.FilterApplied
	case viewConfig:
		return a.cfgSearchTerm != ""
	case viewPlugins:
		return a.plgSearchTerm != ""
	}
	return false
}

func (a *App) activeFilterValue() string {
	switch a.state {
	case viewSessions:
		return a.sessionList.FilterInput.Value()
	case viewConversation:
		return a.convList.FilterInput.Value()
	case viewConfig:
		if a.cfgSearching {
			return a.cfgSearchInput.Value()
		}
		return a.cfgSearchTerm
	case viewPlugins:
		if a.plgSearching {
			return a.plgSearchInput.Value()
		}
		return a.plgSearchTerm
	}
	return ""
}

func (a *App) resetActiveFilter() {
	switch a.state {
	case viewSessions:
		// Remember selected session before reset
		var selID string
		if sess, ok := a.selectedSession(); ok {
			selID = sess.ID
		}
		a.sessionList.ResetFilter()
		// Re-select the same session
		if selID != "" {
			for i, item := range a.sessionList.Items() {
				if si, ok := item.(sessionItem); ok && si.sess.ID == selID {
					a.sessionList.Select(i)
					break
				}
			}
		}
	case viewConversation:
		idx := a.convList.Index()
		a.convList.ResetFilter()
		// Re-select same index (clamped)
		total := len(a.convList.Items())
		if idx >= total {
			idx = total - 1
		}
		if idx >= 0 {
			a.convList.Select(idx)
		}
	case viewConfig:
		a.clearCfgSearch()
	case viewPlugins:
		a.plgSearchTerm = ""
		a.rebuildPlgList()
	}
}

func (a *App) updateActiveList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch a.state {
	case viewSessions:
		m, cmd := a.updateSessionList(msg)
		if previewCmd := a.updateSessionPreview(); previewCmd != nil {
			if cmd != nil {
				return m, tea.Batch(cmd, previewCmd)
			}
			return m, previewCmd
		}
		return m, cmd
	case viewConversation:
		if a.listReady(&a.convList) {
			var cmd tea.Cmd
			a.convList, cmd = a.convList.Update(msg)
			return a, cmd
		}
		return a, nil
	case viewConfig:
		if a.cfgProjectPicker {
			if km, ok := msg.(tea.KeyMsg); ok {
				return a.handleCfgProjectPicker(km)
			}
			var cmd tea.Cmd
			a.cfgProjectInput, cmd = a.cfgProjectInput.Update(msg)
			return a, cmd
		}
		if a.cfgSearching {
			if km, ok := msg.(tea.KeyMsg); ok {
				return a.handleCfgSearch(km)
			}
			var cmd tea.Cmd
			a.cfgSearchInput, cmd = a.cfgSearchInput.Update(msg)
			return a, cmd
		}
		if a.cfgNaming {
			if km, ok := msg.(tea.KeyMsg); ok {
				return a.handleCfgNaming(km)
			}
			var cmd tea.Cmd
			a.cfgNamingInput, cmd = a.cfgNamingInput.Update(msg)
			return a, cmd
		}
		if a.listReady(&a.cfgList) {
			var cmd tea.Cmd
			a.cfgList, cmd = a.cfgList.Update(msg)
			a.updateConfigPreview()
			return a, cmd
		}
		return a, nil
	case viewPlugins:
		if a.plgDetailActive {
			if a.listReady(&a.plgDetailList) {
				var cmd tea.Cmd
				a.plgDetailList, cmd = a.plgDetailList.Update(msg)
				a.updatePluginDetailPreview()
				return a, cmd
			}
			return a, nil
		}
		if a.plgSearching {
			if km, ok := msg.(tea.KeyMsg); ok {
				return a.handlePlgSearch(km)
			}
			var cmd tea.Cmd
			a.plgSearchInput, cmd = a.plgSearchInput.Update(msg)
			return a, cmd
		}
		if a.listReady(&a.plgList) {
			var cmd tea.Cmd
			a.plgList, cmd = a.plgList.Update(msg)
			a.updatePluginPreview()
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
		if previewCmd := a.updateSessionPreview(); previewCmd != nil {
			if cmd != nil {
				return m, tea.Batch(cmd, previewCmd)
			}
			return m, previewCmd
		}
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
		if a.statsDetail != statsDetailNone {
			a.statsDetailVP, cmd = a.statsDetailVP.Update(msg)
		} else {
			a.globalStatsVP, cmd = a.globalStatsVP.Update(msg)
		}
		return a, cmd
	case viewConfig:
		if a.listReady(&a.cfgList) {
			var cmd tea.Cmd
			a.cfgList, cmd = a.cfgList.Update(msg)
			a.updateConfigPreview()
			return a, cmd
		}
		return a, nil
	case viewPlugins:
		if a.plgDetailActive {
			if a.listReady(&a.plgDetailList) {
				var cmd tea.Cmd
				a.plgDetailList, cmd = a.plgDetailList.Update(msg)
				a.updatePluginDetailPreview()
				return a, cmd
			}
			return a, nil
		}
		if a.listReady(&a.plgList) {
			var cmd tea.Cmd
			a.plgList, cmd = a.plgList.Update(msg)
			a.updatePluginPreview()
			return a, cmd
		}
		return a, nil
	}
	return a, nil
}

// autoSelectSession selects the session matching a Claude process in the same tmux window.
// When multiple sessions share the same project path, prefer the most recently modified
// one (sessions are sorted by ModTime descending, so first match wins).
// If the matched session is live, auto-enters it with live tail enabled.
func (a *App) autoSelectSession() tea.Cmd {
	for _, projPath := range tmux.CurrentWindowClaudes() {
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
		if len(a.sessions) > 0 {
			a.sessionList = newSessionList(a.sessions, sessW, contentH, a.sessGroupMode, a.selectedSet, a.hiddenBadges, a.config.WorktreeDir)
			a.sessSplit.CacheKey = ""
			if a.config.SearchQuery != "" {
				applyListFilter(&a.sessionList, a.config.SearchQuery)
			}
			cmd = a.autoSelectSession()
			// Trigger live preview lookup if restored from preferences
			if a.sessSplit.Show && a.sessPreviewMode == sessPreviewLive {
				if liveCmd := a.updateSessionPreview(); liveCmd != nil {
					cmd = tea.Batch(cmd, liveCmd)
				}
			}
		}
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
	if a.statsDetail != statsDetailNone && a.statsDetailVP.Width > 0 {
		a.statsDetailVP.Width = a.width
		a.statsDetailVP.Height = contentH
		if a.globalStatsCache != nil {
			a.statsDetailVP.SetContent(renderStatsDetail(a.statsDetail, *a.globalStatsCache, a.width))
		}
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
	// Config explorer view
	if a.cfgList.Width() > 0 {
		idx := a.cfgList.Index()
		a.cfgList.SetSize(a.cfgSplit.ListWidth(a.width, a.splitRatio), contentH)
		a.cfgList.Select(idx)
		a.cfgSplit.CacheKey = "" // force preview re-render at new size
	}
	// Plugin explorer view
	if a.plgList.Width() > 0 {
		idx := a.plgList.Index()
		a.plgList.SetSize(a.plgSplit.ListWidth(a.width, a.splitRatio), contentH)
		a.plgList.Select(idx)
		a.plgSplit.CacheKey = ""
	}
	if a.plgDetailList.Width() > 0 {
		idx := a.plgDetailList.Index()
		a.plgDetailList.SetSize(a.plgDetailSplit.ListWidth(a.width, a.splitRatio), contentH)
		a.plgDetailList.Select(idx)
		a.plgDetailSplit.CacheKey = ""
	}
	// Hooks view
	if a.hooksVP.Width > 0 {
		a.hooksVP.Width = a.width
		a.hooksVP.Height = contentH
		a.hooksVP.SetContent(renderHooksView(a.width))
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
	a.sessionList = newSessionList(a.sessions, sessW, contentH, a.sessGroupMode, a.selectedSet, a.hiddenBadges, a.config.WorktreeDir)
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
			{" Stats", viewGlobalStats},
		}
		if a.statsDetail != statsDetailNone {
			crumbs = append(crumbs, crumb{statsDetailTitle(a.statsDetail), viewGlobalStats})
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
	case viewConfig:
		label := " Config"
		if fl := a.cfgFilterLabel(); fl != "" {
			label += " [" + fl + "]"
		}
		crumbs = []crumb{
			{label, viewConfig},
		}
		if a.cfgTree != nil && a.cfgTree.ProjectName != "" {
			crumbs = append(crumbs, crumb{a.cfgTree.ProjectName, viewConfig})
		}
	case viewPlugins:
		label := " Plugins"
		if a.plgSearchTerm != "" {
			label += " [" + a.plgSearchTerm + "]"
		}
		crumbs = []crumb{
			{label, viewPlugins},
		}
		if a.plgDetailActive {
			crumbs = append(crumbs, crumb{a.plgDetailPlugin.Name, viewPlugins})
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
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Background(colorTitleBg)
	parentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Background(colorTitleBg)
	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E2E8F0")).Background(colorTitleBg)

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
		actionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Background(colorTitleBg)
		sepAction := lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563")).Background(colorTitleBg).Render("  ")
		text += sepAction
		x += lipgloss.Width(sepAction)
		for i, act := range actions {
			if i > 0 {
				divider := lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563")).Background(colorTitleBg).Render(" ")
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
		countStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A1A1AA")).Background(colorTitleBg)
		rightStr := countStyle.Render(rightParts + " ")
		rightW := lipgloss.Width(rightStr)
		gap := max(a.width-x-rightW, 1)
		text += lipgloss.NewStyle().Background(colorTitleBg).Render(strings.Repeat(" ", gap)) + rightStr
	}

	// Fill remaining width
	titleW := lipgloss.Width(text)
	if titleW < a.width {
		text += lipgloss.NewStyle().Background(colorTitleBg).Render(strings.Repeat(" ", a.width-titleW))
	}

	return text
}

// breadcrumbRightStatus returns the right-aligned status text for the title bar.
// Shows: item count, scroll %, and loading indicators.
func (a *App) breadcrumbRightStatus() string {
	var parts []string

	// Session group mode badge (styled)
	if a.state == viewSessions {
		modeLabels := []string{"FLAT", "PROJ", "TREE", "CHAIN", "FORK", "REPO"}
		modeColors := []lipgloss.Color{"#9CA3AF", "#3B82F6", "#10B981", "#F59E0B", "#EC4899"}
		ml := modeLabels[a.sessGroupMode]
		mc := modeColors[a.sessGroupMode]
		modeStyle := lipgloss.NewStyle().Foreground(mc).Bold(true)
		parts = append(parts, modeStyle.Render(ml))
		if a.hasMultiSelection() {
			parts = append(parts, fmt.Sprintf("%d selected", len(a.selectedSet)))
		}
	}

	// Preview mode badge for conversation/message views
	if a.state == viewConversation || a.state == viewMessageFull {
		modeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
		parts = append(parts, modeStyle.Render(strings.ToUpper(previewModeLabels[a.conv.previewMode])))
	}

	// Loading indicator
	if a.globalStatsLoading || a.sessionsLoading {
		idx := a.spinnerFrame % len(spinnerFrames)
		frame := spinnerFrames[idx]
		spinnerColors := []lipgloss.Color{"#10B981", "#3B82F6", "#F59E0B", "#7C3AED", "#EC4899"}
		c := spinnerColors[a.spinnerFrame/len(spinnerFrames)%len(spinnerColors)]
		s := lipgloss.NewStyle().Foreground(c).Bold(true)
		if a.globalStatsLoading {
			parts = append(parts, s.Render(fmt.Sprintf("%s scanning %d sessions", frame, len(a.sessions))))
		} else {
			parts = append(parts, s.Render(fmt.Sprintf("%s loading…", frame)))
		}
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
		if a.statsDetail != statsDetailNone {
			pct = int(a.statsDetailVP.ScrollPercent() * 100)
		} else {
			pct = int(a.globalStatsVP.ScrollPercent() * 100)
		}
	case viewConfig:
		if a.cfgSplit.Show {
			pct = int(a.cfgSplit.Preview.ScrollPercent() * 100)
		}
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
	case viewConfig:
		if a.cfgList.Width() > 0 {
			return &a.cfgList
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

	case viewConfig:
		return a.openConfigExplorer()

	case viewPlugins:
		return a.openPluginExplorer()
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
