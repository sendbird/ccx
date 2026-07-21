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
	"github.com/sendbird/ccx/internal/claudecmd"
	"github.com/sendbird/ccx/internal/clauderegistry"
	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/kitty"
	"github.com/sendbird/ccx/internal/opener"
	"github.com/sendbird/ccx/internal/remote"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
)

type tickMsg time.Time
type kittyTickMsg time.Time
type liveTickMsg time.Time // slow live capture (2s, unfocused)
type spinnerTickMsg time.Time
type globalStatsMsg session.GlobalStats

// previewDebounceMsg fires after a short delay to trigger preview updates.
// The id field is compared against the App's current debounce counter;
// if they mismatch, a newer navigation happened and this msg is stale.
type previewDebounceMsg struct{ id uint64 }

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

// Conversation right-pane detail levels.
const (
	previewText = 0 // compact — text only, no tool blocks
	previewTool = 1 // standard — text + tool blocks (hooks hidden)
	previewHook = 2 // verbose — text + tool blocks + hook details
)

var previewModeLabels = [3]string{"compact", "standard", "verbose"}

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

// kittyTickCmd fires a fast tick used only to watch for tmux window-switch
// transitions. tmux does not forward focus-out events on window switch, so
// we have to poll `PaneVisible()` ourselves. 250ms feels responsive without
// thrashing the tmux control channel.
func kittyTickCmd() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg {
		return kittyTickMsg(t)
	})
}

func liveTickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return liveTickMsg(t)
	})
}

// previewDebounce is the delay before firing a preview update after navigation.
const previewDebounce = 30 * time.Millisecond

// schedulePreviewUpdate increments the debounce counter and returns a Cmd
// that fires after previewDebounce. The returned msg carries the counter
// snapshot; if it still matches when received, the preview update runs.
func (a *App) schedulePreviewUpdate() tea.Cmd {
	a.previewDebounceID++
	id := a.previewDebounceID
	return tea.Tick(previewDebounce, func(time.Time) tea.Msg {
		return previewDebounceMsg{id: id}
	})
}

// runDebouncedPreview executes the preview update for the current view.
// Called when a previewDebounceMsg fires and its id is still current.
func (a *App) runDebouncedPreview() tea.Cmd {
	switch a.state {
	case viewConversation:
		a.updateConvPreview()
	case viewSessions:
		// Resolve refs for rows scrolled into view so their PR/Jira badge fills
		// in, alongside the selected session's preview update.
		return tea.Batch(a.updateSessionPreview(), a.resolveVisibleRefsCmd())
	case viewConfig:
		a.updateConfigPreview()
	case viewPlugins:
		a.updatePluginPreview()
	}
	return nil
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

// captureAfterLiteralCmd sends a block of literal text (for paste / multi-rune
// input) to the tmux pane, then captures the pane content.
func captureAfterLiteralCmd(p tmux.Pane, text string) tea.Cmd {
	return func() tea.Msg {
		tmux.SendLiteralKeys(p, text)
		time.Sleep(30 * time.Millisecond)
		content, err := tmux.CapturePane(p)
		if err != nil || !tmux.HasClaude(p.PID) {
			return liveCaptureMsg{failed: true}
		}
		return liveCaptureMsg{content: content}
	}
}

// paneProxyLiteralInput extracts literal text that should be forwarded as raw
// bytes to a proxied tmux pane instead of as a named key. This is primarily
// for bracketed paste and IME/multi-rune commits.
func paneProxyLiteralInput(msg tea.KeyMsg) (string, bool) {
	if msg.Type != tea.KeyRunes || len(msg.Runes) == 0 {
		return "", false
	}
	if msg.Paste || len(msg.Runes) > 1 {
		return string(msg.Runes), true
	}
	return "", false
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

	// Pick mode result — non-nil after user confirms a pick.
	pickResult PickResult

	// List models
	sessionList         list.Model
	sessionRowCache     *sessionRowCache
	convPreviewRowCache *sessionRowCache

	// Split panes
	sessSplit SplitPane

	// Session-specific: pinned scroll state
	sessPreviewPinned bool

	// Split pane ratio (list width as % of terminal width)
	splitRatio int

	// Preview debounce: rapid list navigation skips expensive preview renders
	// until the user pauses. Counter increments on each navigation; the delayed
	// msg only fires the update if its id matches the current counter.
	previewDebounceID uint64

	// Number key shortcuts (view + focus scoped)
	shortcuts Shortcuts

	// Badge visibility
	hiddenBadges map[string]bool

	// Fleet notifications: lifecycle transitions across live sessions.
	notifyPrev  map[string]session.LifecycleState // last-seen lifecycle per session ID
	notifyInbox []session.NotifyEvent             // unread notable transitions (newest last)

	// Live input: prefer $EDITOR mode
	editorInput bool

	// Content for clipboard/pager
	copiedMsg string

	// Copy mode for the focused conversation inspector
	copyModeActive bool
	copyLines      []string
	copyCursor     int
	copyAnchor     int

	// Live tracking
	lastMsgLoadTime time.Time
	liveTail        bool // auto-scroll to latest message on tick
	termFocused     bool // terminal has focus (for Kitty image cleanup)
	sessPendingG    bool // sessions view: first 'g' of a possible gg top-jump

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
	inspectorMenu      bool // conversation inspector facet picker
	sessPageMenu       bool // sessions preview page jump popup
	stateMenu          bool // sessions state-filter toggle popup ("l" prefix)

	// Session preview mode
	sessPreviewMode       sessPreview
	sessStatsCache        *session.SessionStats
	sessStatsCacheKey     string
	sessMemoryCache       string // rendered memory content
	sessMemoryCacheKey    string
	sessTasksCache        string
	sessTasksCacheKey     string
	sessShellsCache       string
	sessShellsCacheKey    string
	sessContextsCache     string
	sessContextsCacheKey  string
	sessCtxCursor         int                   // cursor over the flattened context node list
	sessCtxNodes          []session.ContextNode // flattened drill-targetable nodes, cursor order
	sessCtxCacheID        string                // session ID the context cursor currently tracks
	sessRefsCache         string
	sessRefsCacheKey      string
	sessRefsCacheID       string // session ID the refs cursor currently tracks
	sessWorkflowsCache    string
	sessWorkflowsCacheKey string
	sessWfRuns            []session.WorkflowRun // parsed runs for the selected session
	sessWfAgents          []session.Subagent    // workflow-nested agents (label-joined), drill-down targets
	sessWfCursor          int                   // cursor within the workflow agent list
	sessPreviewAgents     []session.Subagent    // agents shown in Tasks/Plan preview
	sessAgentCursor       int                   // cursor within agents list
	sessPreviewRefs       []session.SessionRef  // ordered refs shown in the References preview (open PRs first)
	sessRefsCursor        int                   // cursor within the References preview list
	sessRefsSelected      map[string]bool       // selected ref URLs for multi-open/copy (keyed by SessionRef.URL)
	sessRefsResolved      bool                  // whether the currently-previewed session's refs have been resolved
	refsInFlight          map[string]bool       // session IDs with a resolve pass currently running (prevents re-targeting every tick)
	openURL               func(string) error    // opens a URL in the browser; overridable in tests (defaults to `open`)

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
	sessGroupMode int
	// sessFolded tracks which session groups are collapsed.
	// Keys are group identities produced by the builders (e.g. project path,
	// team name, base repo path). When a key is present and true, the
	// group's children are hidden in the list.
	sessFolded      map[string]bool
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
	tagMenu    bool
	tagSessID  string   // single session ID
	tagSessIDs []string // multi-select session IDs
	tagCursor  int
	tagInput   textinput.Model
	tagList    []string
	badgeStore *session.BadgeStore

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

	// PR/Jira status for URL-menu rows, keyed by URL. Filled asynchronously
	// when the menu opens (same gh/Jira resolve + TTL cache the References
	// preview uses) so PR/Jira links show OPEN/MERGED/review/checks inline.
	urlRefStatus map[string]session.SessionRef

	// Changes-specific state for diff preview
	urlChangeMap map[string]extract.ChangeItem // file path → ChangeItem (for diff rendering)
	urlDiffVP    viewport.Model                // scrollable diff viewport
	urlDiffReady bool                          // whether diff viewport is initialized

	// Conversation inspector and execution-context action menus (x key)
	convActionsMenu      bool
	executionContextMenu bool

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

	// Worktree creation (w = move session to worktree, n = new worktree + session)
	worktreeMode    bool
	worktreeInput   textinput.Model
	worktreeSess    session.Session
	worktreeNewMode bool // true = create new session, false = move existing

	// Memory import from worktree
	memImportActive bool
	memImportSrc    string // worktree project path
	memImportDst    string // main project path

	// Memory removal
	memRemoveActive bool
	memRemoveSrc    string // project path to remove from

	// Remote execution
	remoteSession       *remote.Session
	remoteContent       string                  // progress view content (during setup)
	remoteSetupSteps    <-chan remote.SetupStep // setup progress channel (nil after setup)
	remoteProgressSteps []string                // completed setup step messages
	remoteConfirmCfg    *remote.Config          // pending confirmation (nil = no pending)
	remoteDefaults      remote.Config           // defaults from config.yaml
	remoteJSONLFile     *os.File                // temp file accumulating streamed JSONL
	remoteStreaming     bool                    // true once Claude output is streaming
	remoteLastPoll      time.Time               // last time saved-pod phases were polled
	// Generic confirm modal
	confirmMsg string // message to show (empty = no modal)

	// Conversation tooltip
	convTooltipScroll int                         // scroll offset in tooltip
	convTooltipOn     bool                        // tooltip visible (toggle with t)
	confirmAction     func() (tea.Model, tea.Cmd) // action to run on "y"

	// Worktree alignment
	worktreeAlignActive bool
	worktreeAlignRepo   string

	// Live input modal (I key)
	liveInputActive  bool
	liveInputPane    tmux.Pane
	liveInputModal   inputModal
	liveInputProjDir string // project path for $EDITOR cwd

	// Pane proxy: unified live preview + shell-in-preview
	paneProxy *paneProxyState

	// Conversation split view (viewConversation)
	conv struct {
		sess           session.Session
		messages       []session.Entry
		merged         []mergedMsg
		agents         []session.Subagent
		contextItems   []convItem
		contextIndex   int
		contextActive  bool
		items          []convItem
		flow           *session.FlowIndex
		inspector      conversationInspector
		execution      executionRailState
		toolUseToAgent map[string]string // tool_use_id → subagent ID (from toolUseResult.agentId)
		split          SplitPane
		agent          session.Subagent // non-zero when viewing agent conversation
		task           session.TaskItem // non-zero when viewing task conversation
		cron           session.CronItem // non-zero when viewing cron conversation
		// Right pane detail level: compact → standard → verbose.
		rightPaneMode int // 0=text, 1=tool (no hooks), 2=hook (with hooks)

		// Block filter for preview pane
		blockFiltering bool            // true when filter input is active
		blockFilterTI  textinput.Model // filter text input
	}
	convList list.Model

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
// In the project-centric view, the cursor can land on a `projectItem`
// (a folder-style row that is not itself a session). To keep preview/
// actions sensible we fall back to that project's most-recent session.
func (a *App) selectedSession() (session.Session, bool) {
	sel := a.sessionList.SelectedItem()
	if item, ok := sel.(sessionItem); ok {
		return item.sess, true
	}
	if pi, ok := sel.(projectItem); ok && len(pi.sessions) > 0 {
		return pi.sessions[0], true
	}
	return session.Session{}, false
}

// selectedProject returns the project row at the cursor, if any.
func (a *App) selectedProject() (projectItem, bool) {
	pi, ok := a.sessionList.SelectedItem().(projectItem)
	return pi, ok
}

func (a *App) selectedSessionListItemKey() string {
	if pi, ok := a.selectedProject(); ok {
		return "project:" + pi.basePath
	}
	if sess, ok := a.selectedSession(); ok {
		return "session:" + sess.ID
	}
	return ""
}

func (a *App) restoreSessionListSelection(key string) {
	if key == "" {
		a.bumpPastHeader(0, +1)
		return
	}
	for i, item := range a.sessionList.VisibleItems() {
		switch v := item.(type) {
		case projectItem:
			if key == "project:"+v.basePath {
				a.sessionList.Select(i)
				return
			}
		case sessionItem:
			if key == "session:"+v.sess.ID {
				a.sessionList.Select(i)
				return
			}
		}
	}
	a.bumpPastHeader(0, +1)
}

func (a *App) setSessionListFilter(query string) {
	selectionKey := a.selectedSessionListItemKey()
	a.sessionList.ResetFilter()
	a.config.SearchQuery = ""
	if strings.TrimSpace(query) != "" {
		applyListFilter(&a.sessionList, query)
		a.config.SearchQuery = query
	}
	a.restoreSessionListSelection(selectionKey)
}

func (a *App) visibleProjectBrowserItems() int {
	n := 0
	for _, item := range a.sessionList.VisibleItems() {
		switch item.(type) {
		case projectItem, sessionItem:
			n++
		}
	}
	return n
}

func (a *App) toggleCompletedProjectsFilter() {
	current := strings.TrimSpace(a.activeFilterValue())
	if current == "is:done" {
		a.setSessionListFilter("")
		a.copiedMsg = "Completed filter cleared"
		return
	}
	a.setSessionListFilter("is:done")
	if a.visibleProjectBrowserItems() == 0 {
		// Do not strand the user on a confusing blank browser; fall back to the
		// normal projects view and explain what happened.
		a.setSessionListFilter("")
		a.copiedMsg = "No completed projects found"
		return
	}
	a.copiedMsg = "Showing completed projects"
}

// stateFilterTokens are the mutually-orthogonal session-state tokens the state
// toggle menu ("l" prefix) manages. They are combined with comma-OR so several
// states can be shown at once, e.g. "is:live,is:input,is:mon".
var stateFilterTokens = map[string]string{
	"l": "is:live",
	"i": "is:input",
	"m": "is:mon",
	"d": "is:done",
	"w": "is:wait",
	"b": "is:bg",
	"s": "is:stuck",
}

// defaultActiveStateFilter is applied on startup so the project view shows only
// sessions that are doing something now: running, waiting for input, or with an
// in-flight Monitor. Empty string means "show everything".
const defaultActiveStateFilter = "is:live,is:input,is:mon"

// currentStateFilterSet parses the single comma-OR state term out of the active
// filter (if the whole filter is exactly one is:* OR-term) into a set of tokens.
// Returns nil when the active filter is empty or is not a pure state filter
// (e.g. the user typed a free-text search), so toggling starts fresh instead of
// clobbering an unrelated query.
func (a *App) currentStateFilterSet() map[string]bool {
	cur := strings.TrimSpace(a.activeFilterValue())
	if cur == "" {
		return map[string]bool{}
	}
	if strings.ContainsAny(cur, " \t") {
		return nil // multi-term / free-text filter — not a pure state filter
	}
	set := map[string]bool{}
	for _, alt := range strings.Split(cur, ",") {
		if !strings.HasPrefix(alt, "is:") {
			return nil
		}
		set[alt] = true
	}
	return set
}

// applyStateFilterSet rebuilds the comma-OR filter string from a token set and
// applies it, guarding against stranding the user on a blank browser.
func (a *App) applyStateFilterSet(set map[string]bool) {
	if len(set) == 0 {
		a.setSessionListFilter("")
		a.copiedMsg = "State filter cleared — showing all"
		return
	}
	// Deterministic order for a stable filter string / badge.
	order := []string{"is:live", "is:input", "is:mon", "is:done", "is:wait", "is:bg", "is:stuck"}
	var alts []string
	for _, tok := range order {
		if set[tok] {
			alts = append(alts, tok)
		}
	}
	query := strings.Join(alts, ",")
	a.setSessionListFilter(query)
	if a.visibleProjectBrowserItems() == 0 {
		a.setSessionListFilter("")
		a.copiedMsg = "No sessions match " + query + " — showing all"
		return
	}
	a.copiedMsg = "State filter: " + query
}

// toggleStateFilter flips one state token in the active state filter. A
// free-text or multi-term filter is replaced with just this token (starting a
// fresh state filter) rather than being silently merged.
func (a *App) toggleStateFilter(subkey string) {
	tok, ok := stateFilterTokens[subkey]
	if !ok {
		return
	}
	set := a.currentStateFilterSet()
	if set == nil {
		set = map[string]bool{}
	}
	if set[tok] {
		delete(set, tok)
	} else {
		set[tok] = true
	}
	a.applyStateFilterSet(set)
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

// PickResult returns the result captured during pick mode, or nil if the
// user cancelled or pick mode is disabled.
func (a *App) PickResult() PickResult { return a.pickResult }

type sessPreview int

const (
	sessPreviewConversation sessPreview = iota // text-only, expandable
	sessPreviewStats
	sessPreviewMemory
	sessPreviewTasksPlan
	sessPreviewAgents
	sessPreviewWorkflows
	sessPreviewShells
	sessPreviewContexts
	sessPreviewRefs     // PR / Jira references with resolved status
	sessPreviewLive     // tmux pane capture
	sessPreviewRemote   // remote session status/stream
	numSessPreviewModes = 11
)

// Config holds application configuration from CLI flags.
type Config struct {
	ClaudeDir    string           // path to Claude data directory (empty = ~/.claude)
	TmuxEnabled  bool             // enable tmux integration (I, J, live modal)
	TmuxAutoLive bool             // auto-enter live session in same tmux window on startup
	WorktreeDir  string           // subdirectory name for worktrees (default ".worktree")
	SearchQuery  string           // initial search filter for session list
	Keymap       *Keymap          // nil = use defaults
	GroupMode    string           // initial group mode (flat|proj|tree|chain|fork)
	PreviewMode  string           // initial preview mode (conv|stats|mem|tasks|agents|shells|contexts|live)
	ViewMode     string           // initial view (sessions|config|plugins|stats)
	JumpSession  string           // session ID to open and navigate to on launch
	JumpUUID     string           // entry UUID to navigate to within the session
	PickMode     bool             // true = running under `ccx pick session`: show "Pick" action, skip prefs save
	Claude       claudecmd.Config // command template for local Claude launches
	Open         opener.Config    // command template for opening URLs
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
		state:               viewSessions,
		sessions:            sessions,
		sessionsLoading:     true, // always true — full scan happens async
		config:              cfg,
		keymap:              km,
		splitRatio:          35,
		selectedSet:         make(map[string]bool),
		hiddenBadges:        make(map[string]bool),
		refsInFlight:        make(map[string]bool),
		sessRefsSelected:    make(map[string]bool),
		notifyPrev:          make(map[string]session.LifecycleState),
		sessionRowCache:     newSessionRowCache(1024),
		convPreviewRowCache: newSessionRowCache(4096),
		termFocused:         true,
		// Default to a true project-centric browser: ccx now opens with one
		// row per project (folder-like), and sessions of the same repo (and
		// its worktrees) appear as expandable children beneath the project
		// header. CLI flags or persisted preferences below can still
		// override this.
		sessGroupMode: groupProjectCentric,
	}

	// Restore persisted view state (CLI flags override in the apply block below)
	_, prefs, sc, rc, cc, oc := LoadCCXConfig(configPath())
	if a.config.Claude.CommandTemplate == "" {
		a.config.Claude = cc
	}
	if a.config.Open.CommandTemplate == "" {
		a.config.Open = oc
	}
	cliSearch := a.config.SearchQuery
	a.applyPreferences(prefs)
	a.shortcuts = sc
	a.remoteDefaults = rc

	// Default the project view to "active" sessions (running / awaiting input /
	// with an in-flight monitor) unless the user asked for something specific via
	// --search or a persisted filter. The `S` state menu toggles this.
	if cliSearch == "" && a.config.SearchQuery == "" {
		a.config.SearchQuery = defaultActiveStateFilter
	}

	// Cleanup stale remote sessions, then restore remaining as virtual items
	cleanupStaleRemoteSessions()
	a.sessions = append(loadSavedRemoteSessions(), a.sessions...)
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
		modeMap := map[string]int{"flat": groupFlat, "proj": groupProject, "tree": groupTree, "chain": groupChain, "fork": groupFork, "repo": groupBaseProject, "projects": groupProjectCentric}
		if m, ok := modeMap[a.config.GroupMode]; ok {
			a.sessGroupMode = m
		}
	}
	// The main browser is canonically project-centric now. Preserve legacy
	// group-mode parsing for explicit in-session commands/tests, but do not
	// let persisted historical values (flat/repo/...) drag startup back out
	// of the projects view.
	a.sessGroupMode = groupProjectCentric
	if a.config.PreviewMode != "" {
		modeMap := map[string]sessPreview{"conv": sessPreviewConversation, "stats": sessPreviewStats, "mem": sessPreviewMemory, "tasks": sessPreviewTasksPlan, "agents": sessPreviewAgents, "wf": sessPreviewWorkflows, "workflows": sessPreviewWorkflows, "shells": sessPreviewShells, "contexts": sessPreviewContexts, "ctx": sessPreviewContexts, "refs": sessPreviewRefs, "pr": sessPreviewRefs, "live": sessPreviewLive}
		if m, ok := modeMap[a.config.PreviewMode]; ok {
			a.sessPreviewMode = m
			a.sessSplit.Show = true
		}
	}
	if a.config.ViewMode != "" {
		modeMap := map[string]viewState{
			"sessions": viewSessions, "projects": viewSessions, "config": viewConfig,
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
		kitty.InvalidatePaneOffset()
		cmd := a.resizeAll()
		// On first size, trigger deferred view init for -view flag or picker jump
		if first && (a.state != viewSessions || a.config.JumpSession != "") {
			cmd = tea.Batch(cmd, func() tea.Msg { return initViewMsg{} })
		}
		// Enable focus reporting so we get BlurMsg on tmux window switch
		if first && kitty.Supported() {
			cmd = tea.Batch(cmd, tea.EnableReportFocus, kittyTickCmd())
		}
		return a, cmd

	case tea.BlurMsg:
		// BlurMsg fires on BOTH same-window pane focus changes and on
		// tmux window switches. We only want to clear Kitty graphics
		// when the user actually switches windows — moving focus between
		// panes in the same window keeps our pane on screen, and the
		// image should stay visible there.
		if kitty.Supported() && !kitty.PaneVisible() {
			a.termFocused = false
			// tmux stops forwarding our pane's passthrough output once
			// the window switches away, so a clear emitted on the next
			// render would be dropped. Writing directly to stdout here
			// fires before tmux finishes the switch.
			fmt.Fprint(os.Stdout, kitty.ClearImages())
			_ = os.Stdout.Sync()
		}
		return a, nil

	case tea.FocusMsg:
		a.termFocused = true
		kitty.InvalidatePaneOffset()
		return a, nil

	case initViewMsg:
		// Jump to a specific session+message (from picker subcommand)
		if a.config.JumpSession != "" {
			return a.handleJumpFromPicker()
		}
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

	case remoteSetupMsg:
		return a.handleRemoteSetup(msg)

	case remoteFetchMsg:
		return a.handleRemoteFetch(msg)

	case remoteExecDoneMsg:
		return a.handleRemoteExecDone(msg)

	case remotePhaseMsg:
		return a.handleRemotePhase(msg)

	case remoteExecOutputMsg:
		return a.handleRemoteExecOutput(msg)

	case remoteSnapshotMsg:
		return a.handleRemoteSnapshot(msg)

	case remotePullMsg:
		return a.handleRemotePull(msg)

	case remoteForkReadyMsg:
		return a.handleRemoteForkReady(msg)

	case delayedRefreshMsg:
		// Auto-refresh after spawning a new session; retry if session not found yet
		oldCount := len(a.sessions)
		cmd := a.doRefresh()
		if len(a.sessions) == oldCount && msg.remaining > 0 {
			// No new session found — retry after another delay
			return a, tea.Batch(cmd, func() tea.Msg {
				time.Sleep(3 * time.Second)
				return delayedRefreshMsg{remaining: msg.remaining - 1}
			})
		}
		return a, cmd

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

	case previewDebounceMsg:
		if msg.id != a.previewDebounceID {
			return a, nil // stale: a newer navigation happened
		}
		return a, a.runDebouncedPreview()

	case tickMsg:
		cmd := a.handleTick()
		return a, tea.Batch(cmd, tickCmd())

	case kittyTickMsg:
		// Fast visibility poll: detect tmux window-switch transitions that
		// don't surface as BlurMsg. When our pane becomes invisible, clear
		// any lingering Kitty graphics so they don't bleed through to the
		// window the user switched to.
		if kitty.Supported() {
			visible := kitty.PaneVisible()
			if !visible && a.termFocused {
				a.termFocused = false
				fmt.Fprint(os.Stdout, kitty.ClearImages())
				_ = os.Stdout.Sync()
			} else if visible && !a.termFocused {
				a.termFocused = true
				kitty.InvalidatePaneOffset()
			}
		}
		return a, kittyTickCmd()

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
		session.EnrichLiveSessions(msg.sessions)

		// Remember cursor position from phase 1
		selectedID := ""
		if sess, ok := a.selectedSession(); ok {
			selectedID = sess.ID
		}

		// Carry lazily-resolved ref state from the phase-1 slice into the freshly
		// scanned one BEFORE replacing a.sessions. Phase-2 scan sets HasRefs but
		// never resolves status, so without this it wipes refs the user already
		// extracted (e.g. by opening a preview during phase-1) and the preview
		// reverts to a permanent "Resolving…".
		a.carryOverRefState(msg.sessions)
		a.sessions = a.injectRemoteSessions(msg.sessions)
		// PR/Jira ref resolution (network-bound: gh + Jira REST) is deferred to
		// the first background refresh tick rather than fired here, so the initial
		// session list renders without waiting on a fan-out of gh subprocesses.
		// Build/rebuild session list
		if a.width > 0 && a.height > 0 {
			contentH := a.height - 3
			sessW := a.sessSplit.ListWidth(a.width, a.splitRatio)
			a.sessionList = newSessionList(a.sessions, sessW, contentH, a.sessGroupMode, a.selectedSet, a.hiddenBadges, a.sessFolded, a.sessionRowCache, a.config.WorktreeDir)
			a.sessSplit.CacheKey = ""
			if a.config.SearchQuery != "" {
				applyListFilter(&a.sessionList, a.config.SearchQuery)
			}
			// Restore cursor to previously selected session.
			// Use VisibleItems() because Select() operates on the visible (filtered) index space.
			if selectedID != "" {
				for i, item := range a.sessionList.VisibleItems() {
					if si, ok := item.(sessionItem); ok && si.sess.ID == selectedID {
						a.sessionList.Select(i)
						return a, nil
					}
				}
			}
			a.bumpPastHeader(0, +1)
			return a, a.autoSelectSession()
		}
		return a, nil

	case globalStatsMsg:
		stats := session.GlobalStats(msg)
		a.globalStatsCache = &stats
		a.globalStatsLoading = false
		// Only render/switch into the stats view if the user is still there.
		// If they navigated away while stats were loading, just cache the
		// result and stay where they are.
		if a.state != viewGlobalStats {
			return a, nil
		}
		contentH := a.height - 3
		a.globalStatsVP = viewport.New(a.width, contentH)
		a.globalStatsVP.SetContent(renderGlobalStats(stats, a.width))
		return a, nil

	case searchBatchMsg:
		a.updateSearchResults(msg.results)
		return a, nil

	case refsExtractedMsg:
		// Offline extract landed: store the link list (status still unresolved)
		// and, if this session's refs preview is open, render it immediately so
		// URLs/labels/timestamps appear without waiting on the network. Then kick
		// off the status resolve, which streams back as refStatusMsg per ref.
		var statusCmd tea.Cmd
		for i := range a.sessions {
			if a.sessions[i].ID != msg.id {
				continue
			}
			a.sessions[i].Refs = msg.refs
			if len(msg.refs) == 0 {
				// Nothing to resolve — mark done and clear in-flight so we stop
				// re-targeting it.
				a.sessions[i].RefsResolved = true
				delete(a.refsInFlight, msg.id)
			} else {
				statusCmd = a.resolveRefsStatusCmd(msg.id, msg.refs)
			}
			break
		}
		if a.state == viewSessions && a.sessSplit.Show && a.sessPreviewMode == sessPreviewRefs {
			if sess, ok := a.selectedSession(); ok && sess.ID == msg.id {
				a.sessRefsCacheKey = ""
				previewCmd := a.updateSessionRefsPreview(sess)
				return a, tea.Batch(previewCmd, statusCmd)
			}
		}
		return a, statusCmd

	case refStatusMsg:
		// One ref's status landed: merge it into the session (matched by URL) and,
		// if that session's refs preview is open, re-render so it fills in live.
		// Mark RefsResolved once every ref in the session has a status.
		for i := range a.sessions {
			if a.sessions[i].ID != msg.id {
				continue
			}
			allResolved := true
			for j := range a.sessions[i].Refs {
				if a.sessions[i].Refs[j].URL == msg.ref.URL {
					a.sessions[i].Refs[j] = msg.ref
				}
				if !a.sessions[i].Refs[j].Resolved {
					allResolved = false
				}
			}
			if allResolved {
				a.sessions[i].RefsResolved = true
				delete(a.refsInFlight, msg.id)
			}
			break
		}
		// Reflect the newly-resolved status onto the list row so the open-PR/Jira
		// badge fills in live, whether or not the References preview is open.
		a.syncSessionRefsToList(msg.id)
		if a.state == viewSessions && a.sessSplit.Show && a.sessPreviewMode == sessPreviewRefs {
			if sess, ok := a.selectedSession(); ok && sess.ID == msg.id {
				a.sessRefsCacheKey = "" // force re-render with the newly-resolved ref
				return a, a.updateSessionRefsPreview(sess)
			}
		}
		return a, nil

	case urlRefStatusMsg:
		// One PR/Jira URL's status landed; store it so the URL menu row renders
		// the state inline. Ignore if the menu was closed in the meantime.
		if a.urlRefStatus != nil {
			a.urlRefStatus[msg.ref.URL] = msg.ref
		}
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

		// Confirm modal (y/n)
		if a.confirmMsg != "" {
			action := a.confirmAction
			a.confirmMsg = ""
			a.confirmAction = nil
			if msg.String() == "y" || msg.String() == "Y" {
				if action != nil {
					return action()
				}
			}
			return a, nil
		}

		if a.isFiltering() {
			m, cmd := a.updateActiveList(msg)
			a.syncAllFilterVisibility()
			return m, cmd
		}

		// Nested conversation filters unwind from the focused preview outward:
		// block filter first, then the chronological list filter on the next Esc.
		if msg.String() == "esc" && a.state == viewConversation && a.conv.split.Folds != nil && a.conv.split.Folds.BlockFilter != "" {
			a.clearBlockFilter()
			return a, nil
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

		// Help overlay: available from any view. Any key closes it; otherwise
		// the help key opens it (unless a text input or another overlay owns keys).
		if a.showHelp {
			a.showHelp = false
			return a, nil
		}
		if msg.String() == a.keymap.Session.Help && !a.isInTextInput() && !a.isInOverlay() {
			a.showHelp = true
			return a, nil
		}

		// Number key shortcuts (0-9): view + focus scoped
		if key := msg.String(); !a.isInTextInput() && !a.isInOverlay() &&
			len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
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
		case viewConfig:
			return a.handleConfigKeys(msg)
		case viewPlugins:
			return a.handlePluginKeys(msg)
		}
	}

	return a.updateActiveComponent(msg)
}

// activeDividerCol returns the cell-width column of the active split's divider.
// Returns 0 when no split is visible (overlay fills full width).
func (a *App) activeDividerCol() int {
	switch a.state {
	case viewSessions:
		if a.sessSplit.Show {
			return a.sessSplit.ListWidth(a.width, a.splitRatio)
		}
	case viewConversation:
		if a.conv.split.Show {
			return a.conv.split.ListWidth(a.width, a.splitRatio)
		}
	case viewConfig:
		if a.cfgSplit.Show {
			return a.cfgSplit.ListWidth(a.width, a.splitRatio)
		}
	case viewPlugins:
		if a.plgDetailActive && a.plgDetailSplit.Show {
			return a.plgDetailSplit.ListWidth(a.width, a.splitRatio)
		}
		if a.plgSplit.Show {
			return a.plgSplit.ListWidth(a.width, a.splitRatio)
		}
	}
	return 0
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
		if a.confirmMsg != "" {
			content = renderConfirmModal(content, a.confirmMsg, a.width, ContentHeight(a.height))
		} else if a.sessConvFullText != "" {
			content = renderFullTextModal(content, a.sessConvFullText, a.sessConvFullScroll, a.width, ContentHeight(a.height))
		}
		help = a.sessHelpLine()

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
			help = formatHelp("p:page ↑↓:scroll esc:back " + a.helpSuffix())
		} else {
			content = a.globalStatsVP.View()
			help = formatHelp("p:page ↑↓:scroll " + a.keymap.Session.Views + ":views " + a.helpSuffix())
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
		help = a.convHelpLine(badges)

	case viewConfig:
		title = a.renderBreadcrumb()
		content = a.renderConfigSplit()
		if a.cfgProjectPicker {
			content = a.renderProjectPickerOverlay(content)
		}
		help = a.configHelpLine()

	case viewPlugins:
		title = a.renderBreadcrumb()
		if a.plgDetailActive {
			content = a.renderPluginDetailSplit()
		} else {
			content = a.renderPluginSplit()
		}
		help = a.pluginsHelpLine()
	}

	// Context-aware help overlay — available in every view.
	if a.showHelp {
		content = a.renderHelpModal(content, a.width, ContentHeight(a.height))
	}

	// Command mode help — overrides view-specific help in any view
	if a.cmdMode {
		help = "  " + a.cmdInput.View() + helpStyle.Render("  tab:complete ↵:run esc:cancel")
	}

	// URL menu centered modal
	if a.urlMenu {
		hintBox := a.renderURLMenu()
		if hintBox != "" {
			content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 24), maxHeight: max(ContentHeight(a.height)-4, 8)})
		}
		if a.urlSearching {
			help = "  " + a.urlSearchInput.View() + helpStyle.Render("  enter:apply esc:cancel")
		} else {
			help = formatHelp("↑↓:nav ↵:open y:copy /:search esc:close")
		}
	}

	// Execution-context action menu hint box
	if a.executionContextMenu && a.state == viewConversation {
		hintBox := a.renderExecutionContextMenu()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 28), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp("↵:jump esc:close")
	}

	// Conversation inspector actions menu hint box
	if a.convActionsMenu && a.state == viewConversation {
		hintBox := a.renderConvActionsHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 28), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp(fmtKey(a.keymap.Conversation.Actions, "actions") + " — pick an action")
	}

	// Actions menu hint box floating above help line
	if a.actionsMenu && a.state == viewSessions {
		hintBox := a.renderActionsHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 28), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp("x:actions — pick an action")
	}

	if a.sessPageMenu && a.state == viewSessions {
		hintBox := a.renderSessPageHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 20), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp("p:page — pick a preview")
	}

	if a.stateMenu && a.state == viewSessions {
		hintBox := a.renderStateHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 20), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp("S:states — toggle which session states show")
	}

	// Tag menu centered modal
	if a.tagMenu {
		modal := a.renderTagMenu()
		if modal != "" {
			content = overlayCenteredModal(content, modal, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 24), maxHeight: max(ContentHeight(a.height)-4, 8)})
		}
	}

	// Config actions menu hint box
	if a.cfgActionsMenu && a.state == viewConfig {
		hintBox := a.renderCfgActionsHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 28), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp("x:actions — pick an action")
	}

	// Plugin actions menu hint box
	if a.plgActionsMenu && a.state == viewPlugins {
		hintBox := a.renderPlgActionsHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 28), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp("x:actions — pick an action")
	}

	// Plugin detail actions menu hint box
	if a.plgCompActionsMenu && a.state == viewPlugins && a.plgDetailActive {
		hintBox := a.renderPlgCompActionsHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 28), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp("x:actions — pick an action")
	}

	// Views menu centered modal
	if a.viewsMenu {
		hintBox := a.renderViewsHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 20), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp("v:views — pick a view")
	}

	// Edit menu centered modal
	if a.editMenu {
		hintBox := a.renderEditHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 20), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp("e:edit — pick a file")
	}

	// Stats page jump centered modal
	if a.statsPageMenu && a.state == viewGlobalStats {
		hintBox := a.renderStatsPageHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 20), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp("p:page — pick a page")
	}

	// Config page jump centered modal
	if a.cfgPageMenu && a.state == viewConfig {
		hintBox := a.renderCfgPageHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 20), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp("p:page — pick a section")
	}

	// Conversation inspector facet picker.
	if a.inspectorMenu && a.state == viewConversation {
		hintBox := a.renderInspectorMenuHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-8, 20), maxHeight: max(ContentHeight(a.height)-4, 8)})
		help = formatHelp("p:facets — pick a Session-scope inspector facet")
	}

	// Filter/search hint boxes as constrained centered modals
	if a.conv.blockFiltering && a.state == viewConversation {
		hintBox := renderBlockFilterHintBox()
		content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-10, 28), maxHeight: max(ContentHeight(a.height)-6, 8)})
	}

	// Command mode hint box as constrained centered modal
	if a.cmdMode {
		hintBox := a.renderCmdHintBox()
		if hintBox != "" {
			content = overlayCenteredModal(content, hintBox, a.width, ContentHeight(a.height), modalOptions{paddingX: 2, paddingY: 1, maxWidth: max(a.width-10, 40), maxHeight: max(ContentHeight(a.height)-6, 10)})
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
			divCol := a.activeDividerCol()
			for i, bl := range boxLines {
				y := startY + i
				if y < len(contentLines) {
					limit := a.width
					if divCol > 0 {
						limit = divCol
					}
					contentLines[y] = overlayLine(contentLines[y], bl, 1, limit)
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

	help = a.truncateFooter(help)

	screen := title + "\n" + content + "\n" + help

	// Live input modal overlays everything
	if a.liveInputActive {
		screen = a.liveInputModal.render(screen, a.width, a.height)
	}

	// Cross-session search overlays everything as a centered modal
	if a.searchActive {
		screen = a.renderSearchModal(screen)
	}

	// Kitty inline image layer: draw or clear images each frame.
	if kitty.Supported() {
		screen += a.kittyImageLayer()
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

	if key != "g" {
		a.sessPendingG = false
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

	// State-filter toggle menu ("l" prefix)
	if a.stateMenu {
		return a.handleStateMenu(key)
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
		if literal, ok := paneProxyLiteralInput(msg); ok {
			return a, captureAfterLiteralCmd(a.paneProxy.pane, literal)
		}
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
		case "left":
			return a, captureAfterKeyCmd(a.paneProxy.pane, "left")
		}
		return a.handlePaneProxyKey(key)
	}

	// View-specific keys
	km := a.keymap
	if a.config.PickMode && key == km.Session.Pick {
		var items []session.Session
		if a.hasMultiSelection() {
			items = a.selectedSessions()
		} else if pi, ok := a.selectedProject(); ok {
			items = append(items, pi.sessions...)
		} else if sess, ok := a.selectedSession(); ok {
			items = []session.Session{sess}
		}
		if len(items) == 0 {
			return a, nil
		}
		a.pickResult = SessionsResult{Items: items}
		return a, tea.Quit
	}
	switch key {
	case km.Session.Quit:
		return a.quit()
	case km.Session.Escape:
		if a.hasMultiSelection() {
			a.clearMultiSelection()
			return a, nil
		}
		if sp.Show && a.sessPreviewMode != sessPreviewConversation {
			a.closePaneProxy()
			if a.sessPreviewMode == sessPreviewRefs {
				clear(a.sessRefsSelected)
			}
			a.sessPreviewMode = sessPreviewConversation
			sp.CacheKey = ""
			sp.Focus = false
			return a, nil
		}
		if sp.Show && sp.Focus {
			sp.Focus = false
			return a, nil
		}
		if sp.Show {
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
		// Project rows: Enter toggles expansion instead of opening anything.
		if pi, ok := a.sessionList.SelectedItem().(projectItem); ok {
			a.toggleProjectFold(pi)
			return a, nil
		}
		// Remote sessions: attach interactively
		if sess, ok := a.selectedSession(); ok && sess.IsRemote {
			return a.attachToRemoteSession(sess)
		}
		// If conversation preview is focused, jump to the selected message
		if sp.Focus && sp.Show && a.sessPreviewMode == sessPreviewConversation && len(a.sessConvEntries) > 0 {
			return a.jumpToConvMessage()
		}
		// If Agents preview is focused, jump to selected agent
		if sp.Focus && sp.Show && a.sessPreviewMode == sessPreviewAgents && len(a.sessPreviewAgents) > 0 {
			m, cmd, _ := a.jumpToAgentConversation()
			return m, cmd
		}
		// If Workflows preview is focused, drill into the selected agent transcript
		if sp.Focus && sp.Show && a.sessPreviewMode == sessPreviewWorkflows && len(a.sessWfAgents) > 0 {
			m, cmd, _ := a.drillIntoWorkflowAgent()
			return m, cmd
		}
		// If References preview is focused, open the selected PR/Jira URL(s)
		// under the cursor in the browser (mirrors `o`; without this Enter
		// would fall through to openConversation).
		if sp.Focus && sp.Show && a.sessPreviewMode == sessPreviewRefs && len(a.sessPreviewRefs) > 0 {
			m, cmd, _ := a.openSelectedRefs()
			return m, cmd
		}
		// If the Session Context tree is focused, drill into the node under the
		// cursor (config / plugin explorer) instead of opening the conversation.
		if sp.Focus && sp.Show && a.sessPreviewMode == sessPreviewContexts && len(a.sessCtxNodes) > 0 {
			m, cmd, _ := a.openSelectedContextNode()
			return m, cmd
		}
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		a.currentSess = sess
		return a, a.openConversation(sess)
	case km.Session.Select:
		if sp.Focus && sp.Show {
			// References preview: space multi-selects the ref under the
			// cursor instead of a no-op (mirrors the URL menu's space-select).
			if a.sessPreviewMode == sessPreviewRefs && len(a.sessPreviewRefs) > 0 {
				return a, a.toggleRefSelection()
			}
			return a, nil
		}
		if pi, ok := a.selectedProject(); ok {
			for _, s := range pi.sessions {
				if a.selectedSet[s.ID] {
					delete(a.selectedSet, s.ID)
				} else {
					a.selectedSet[s.ID] = true
				}
			}
			return a, nil
		}
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
		if pi, ok := a.selectedProject(); ok {
			for _, s := range pi.sessions {
				a.selectedSet[s.ID] = true
			}
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
		// Remote sessions: spawn kubectl exec in hidden tmux pane, use as live preview
		if sess, ok := a.selectedSession(); ok && sess.IsRemote {
			return a.openRemoteLivePreview(sess)
		}
		if !a.config.TmuxEnabled {
			return a, nil
		}
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		return a.openLivePreview(sess)
	case km.Session.Switch:
		// Jump straight to the session's live tmux window (client switch).
		if !a.config.TmuxEnabled || !tmux.InTmux() {
			a.copiedMsg = "Requires tmux"
			return a, nil
		}
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		if !sess.IsLive {
			a.copiedMsg = "Not a live session"
			return a, nil
		}
		return a.jumpToTmuxPane(sess.ProjectPath, sess.ID)
	case km.Session.Edit:
		sess, ok := a.selectedSession()
		if !ok {
			return a, nil
		}
		return a.openEditMenu(sess)
	case km.Session.Refresh:
		// Rescan the session list first (its carryOverRefState may re-populate
		// ref state), then force the open preview to re-read: clear the process
		// -wide ref TTL cache and invalidate the current mode's caches so
		// updateSessionPreview re-fetches instead of short-circuiting.
		cmd := a.doRefresh()
		// Live/remote previews are driven by their own streams (doRefresh already
		// calls refreshSessionPreviewLive); re-running updateSessionPreview for
		// them would flash "(connecting…)" and re-find the pane, so skip it.
		if a.sessPreviewMode == sessPreviewLive || a.sessPreviewMode == sessPreviewRemote {
			a.copiedMsg = "Refreshed"
			return a, cmd
		}
		session.ClearRefCache()
		a.invalidateOpenPreviewCaches()
		previewCmd := a.updateSessionPreview()
		a.copiedMsg = "Refreshed"
		return a, tea.Batch(cmd, previewCmd)
	case "n":
		// Jump to the most recent fleet notification's session.
		if a.notifyUnreadCount() == 0 {
			return a, nil
		}
		if a.jumpToNotification() {
			a.copiedMsg = "Jumped to notified session"
		} else {
			a.copiedMsg = "Notified session not visible"
		}
		return a, nil
	case km.Session.Views:
		a.viewsMenu = true
		return a, nil
	case km.Session.GlobalSearch:
		a.enterSearchMode()
		return a, nil
	// Tab/shift+tab now only control preview behavior. The main browser is
	// project-centric by default, so users should not need Tab to rotate
	// through alternate grouping models just to get the right mental model.
	case km.Session.Preview:
		if !sp.Show {
			sp.Show = true
			sp.Focus = false
			return a, a.updateSessionPreview()
		}
		a.cycleSessionPreviewMode()
		return a, a.updateSessionPreview()
	case km.Session.PreviewBack:
		if !sp.Show {
			sp.Show = true
			sp.Focus = false
			return a, a.updateSessionPreview()
		}
		a.cycleSessionPreviewModeReverse()
		return a, a.updateSessionPreview()
	}

	// Fold/unfold groups: only available when the list (not the preview)
	// is focused, so the same keys can still be used as text input
	// elsewhere. `o` toggles the group at the cursor; `f`/`F` fold/expand
	// everything.
	if !sp.Focus {
		switch key {
		case "o":
			a.toggleSessGroupFoldAtCursor()
			return a, nil
		case "f":
			a.setAllSessGroupsFolded(true)
			return a, nil
		case "F":
			a.setAllSessGroupsFolded(false)
			return a, nil
		case "D":
			a.toggleCompletedProjectsFilter()
			return a, a.updateSessionPreview()
		case "S":
			a.stateMenu = true
			return a, nil
		}
	}

	// Sessions list vim-style jumps: gg = top, G = end.
	// Handle these before TranslateNav so user-configured navigation aliases
	// cannot reinterpret the first `g` as Home.
	if !sp.Focus {
		switch key {
		case "g":
			if a.sessPendingG {
				a.sessPendingG = false
				a.sessionList.Select(0)
				a.bumpPastHeader(0, +1)
				return a, a.schedulePreviewUpdate()
			}
			a.sessPendingG = true
			return a, nil
		case "G", "end":
			a.sessPendingG = false
			items := a.sessionList.VisibleItems()
			if len(items) > 0 {
				a.sessionList.Select(len(items) - 1)
				a.bumpPastHeader(len(items)-1, -1)
			}
			return a, a.schedulePreviewUpdate()
		case "home":
			a.sessPendingG = false
			a.sessionList.Select(0)
			a.bumpPastHeader(0, +1)
			return a, a.schedulePreviewUpdate()
		}
	}

	// Translate navigation aliases (e.g. vim j→down, emacs ctrl+n→down)
	if nav, navMsg := a.keymap.TranslateNav(key, msg); nav != "" {
		key = nav
		msg = navMsg
	}

	// Shared split-pane semantics for left/right/tab/esc/resize.
	result := sp.HandleSplitKey(key, a.width, a.height, a.splitRatio, a.adjustSplitRatio)
	switch result {
	case splitKeyClosed:
		return a, nil
	case splitKeyFocused, splitKeyOpened:
		if a.paneProxy != nil {
			return a, capturePaneCmd(a.paneProxy.pane)
		}
		return a, a.updateSessionPreview()
	case splitKeyUnfocused:
		return a, nil
	case splitKeyHandled:
		return a, nil
	}

	// Focused preview: custom conversation nav or simple scroll
	if sp.Focus && sp.Show {
		if a.sessPageMenu {
			return a.handleSessPageMenu(key)
		}
		if key == "p" {
			a.sessPageMenu = true
			return a, nil
		}
		if m, cmd, handled := a.handleFocusedPreviewKeys(sp, key); handled {
			return m, cmd
		}
	}

	// List boundary (up/down always navigate list, scroll preview at edges)
	if !sp.Focus && sp.HandleListBoundary(key) {
		return a, a.schedulePreviewUpdate()
	}

	// Default list update
	oldIdx := a.sessionList.Index()
	m, cmd := a.updateSessionList(msg)
	newIdx := a.sessionList.Index()
	// If the cursor landed on a section header (e.g. after Up/Down jumped
	// across the "Sessions" divider), skip to the next session item.
	if newIdx != oldIdx {
		a.skipHeaderInDirection(oldIdx, newIdx)
		newIdx = a.sessionList.Index()
	} else {
		a.skipHeaderInDirection(oldIdx, newIdx)
	}
	if sp.Show && oldIdx == newIdx {
		switch key {
		case "down", "up", "pgdown", "pgup":
			scrollPreview(&sp.Preview, key)
			return a, nil
		}
	}
	debounceCmd := a.schedulePreviewUpdate()
	return m, tea.Batch(cmd, debounceCmd)
}

// skipHeaderInDirection moves the list cursor past header items in the same
// direction the user was navigating (down if newIdx >= oldIdx, otherwise up).
// At the boundaries it bumps the other way so we never get stuck on a header.
func (a *App) skipHeaderInDirection(oldIdx, newIdx int) {
	visible := a.sessionList.VisibleItems()
	if len(visible) == 0 {
		return
	}
	cur := a.sessionList.Index()
	if cur < 0 || cur >= len(visible) {
		return
	}
	if _, ok := visible[cur].(sessionItem); ok {
		return
	}
	// projectItem rows are selectable (cursor can land on a project header).
	if _, ok := visible[cur].(projectItem); ok {
		return
	}
	dir := 1
	if newIdx < oldIdx {
		dir = -1
	}
	idx := cur + dir
	for idx >= 0 && idx < len(visible) {
		switch visible[idx].(type) {
		case sessionItem, projectItem:
			a.sessionList.Select(idx)
			return
		}
		idx += dir
	}
	// Reverse direction if we hit a boundary on a header.
	idx = cur - dir
	for idx >= 0 && idx < len(visible) {
		switch visible[idx].(type) {
		case sessionItem, projectItem:
			a.sessionList.Select(idx)
			return
		}
		idx -= dir
	}
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
	if a.sessPreviewMode == sessPreviewAgents && len(a.sessPreviewAgents) > 0 {
		return a.handleTasksPreviewKeys(sp, key)
	}
	if a.sessPreviewMode == sessPreviewWorkflows && len(a.sessWfAgents) > 0 {
		return a.handleWorkflowsPreviewKeys(sp, key)
	}
	if a.sessPreviewMode == sessPreviewRefs && len(a.sessPreviewRefs) > 0 {
		return a.handleRefsPreviewKeys(sp, key)
	}
	if a.sessPreviewMode == sessPreviewContexts && len(a.sessCtxNodes) > 0 {
		return a.handleContextsPreviewKeys(sp, key)
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

// handleTasksPreviewKeys handles keys for navigating agents in the Tasks/Plan preview.
func (a *App) handleTasksPreviewKeys(sp *SplitPane, key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "enter":
		return a.jumpToAgentConversation()
	case "/":
		sp.Focus = false
		return a, startListSearch(&a.sessionList), true
	}
	// Flat cursor navigation over agents
	switch HandleFlatCursorNav(&a.sessAgentCursor, len(a.sessPreviewAgents), key) {
	case NavCursorMoved:
		// Rebuild content so the "> " cursor highlight tracks the new position,
		// then nudge the viewport to keep it in view.
		if sess, ok := a.selectedSession(); ok {
			a.sessSplit.Preview.SetContent(a.buildAgentsPreviewContent(sess))
		}
		if key == "up" || key == "k" {
			sp.Preview.LineUp(1)
		} else if key == "down" || key == "j" {
			sp.Preview.LineDown(1)
		}
		return a, nil, true
	case NavBoundaryDown:
		// boundary crossing disabled in sessions preview
		return a, nil, true
	case NavBoundaryUp:
		// boundary crossing disabled in sessions preview
		return a, nil, true
	}
	// pgup/pgdown/home/end: scroll viewport
	if scrollViewport(&sp.Preview, key) {
		return a, nil, true
	}
	return a, nil, false
}

// jumpToAgentConversation opens the conversation view and jumps to the selected agent.
func (a *App) jumpToAgentConversation() (tea.Model, tea.Cmd, bool) {
	if a.sessAgentCursor < 0 || a.sessAgentCursor >= len(a.sessPreviewAgents) {
		return a, nil, true
	}
	agent := a.sessPreviewAgents[a.sessAgentCursor]
	sess, ok := a.selectedSession()
	if !ok {
		return a, nil, true
	}
	a.currentSess = sess
	cmd := a.openConversation(sess)

	for i, item := range a.convList.VisibleItems() {
		ci, ok := item.(convItem)
		if !ok {
			continue
		}
		if ci.kind == convAgent && (ci.agent.ID == agent.ID || ci.agent.ShortID == agent.ShortID) {
			a.selectConvBody(i)
			break
		}
	}
	a.updateConvPreview()
	return a, cmd, true
}

// handleWorkflowsPreviewKeys handles cursor navigation + drill-down for the
// workflow preview. Enter opens the selected workflow agent's full transcript.
func (a *App) handleWorkflowsPreviewKeys(sp *SplitPane, key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "enter":
		return a.drillIntoWorkflowAgent()
	case "/":
		sp.Focus = false
		return a, startListSearch(&a.sessionList), true
	}
	switch HandleFlatCursorNav(&a.sessWfCursor, len(a.sessWfAgents), key) {
	case NavCursorMoved:
		if sess, ok := a.selectedSession(); ok {
			previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
			sp.Preview.SetContent(a.buildWorkflowsPreviewContent(sess, previewW))
		}
		if key == "up" || key == "k" {
			sp.Preview.LineUp(1)
		} else if key == "down" || key == "j" {
			sp.Preview.LineDown(1)
		}
		return a, nil, true
	case NavBoundaryDown, NavBoundaryUp:
		return a, nil, true
	}
	if scrollViewport(&sp.Preview, key) {
		return a, nil, true
	}
	return a, nil, false
}

// handleRefsPreviewKeys handles cursor navigation + open for the References
// preview. Enter opens the selected references (or the one under the cursor,
// if nothing is multi-selected) in the browser. Space toggles multi-selection
// on the current item; y copies the selected (or current) reference URL(s).
func (a *App) handleRefsPreviewKeys(sp *SplitPane, key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "enter", "o":
		return a.openSelectedRefs()
	case " ":
		return a, a.toggleRefSelection(), true
	case "y":
		return a.copySelectedRefs()
	case "/":
		sp.Focus = false
		return a, startListSearch(&a.sessionList), true
	}
	switch HandleFlatCursorNav(&a.sessRefsCursor, len(a.sessPreviewRefs), key) {
	case NavCursorMoved:
		a.sessRefsCacheKey = "" // cursor moved → force re-render with new highlight
		if sess, ok := a.selectedSession(); ok {
			return a, a.updateSessionRefsPreview(sess), true
		}
		return a, nil, true
	case NavBoundaryDown, NavBoundaryUp:
		return a, nil, true
	}
	if scrollViewport(&sp.Preview, key) {
		return a, nil, true
	}
	return a, nil, false
}

// handleContextsPreviewKeys drives the interactive Session Context tree: up/down
// move a cursor over the drill-targetable nodes, Enter opens the related config
// / plugin explorer via openRelatedContextNode.
func (a *App) handleContextsPreviewKeys(sp *SplitPane, key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "enter", "o", "right":
		return a.openSelectedContextNode()
	case "/":
		sp.Focus = false
		return a, startListSearch(&a.sessionList), true
	}
	switch HandleFlatCursorNav(&a.sessCtxCursor, len(a.sessCtxNodes), key) {
	case NavCursorMoved:
		a.sessContextsCacheKey = "" // cursor moved → re-render with new highlight
		if sess, ok := a.selectedSession(); ok {
			a.updateSessionContextsPreview(sess)
		}
		return a, nil, true
	case NavBoundaryDown, NavBoundaryUp:
		return a, nil, true
	}
	if scrollViewport(&sp.Preview, key) {
		return a, nil, true
	}
	return a, nil, false
}

// openSelectedContextNode drills into the config/plugin destination of the
// context node under the cursor.
func (a *App) openSelectedContextNode() (tea.Model, tea.Cmd, bool) {
	if a.sessCtxCursor < 0 || a.sessCtxCursor >= len(a.sessCtxNodes) {
		return a, nil, true
	}
	m, cmd := a.openRelatedContextNode(a.sessCtxNodes[a.sessCtxCursor])
	return m, cmd, true
}

// Returns the cmd that must be dispatched by the caller to actually
// re-render the preview viewport (clearing sessRefsCacheKey alone only
// invalidates the cache — updateSessionRefsPreview is what repopulates
// sessSplit.Preview's content).
func (a *App) toggleRefSelection() tea.Cmd {
	if a.sessRefsCursor < 0 || a.sessRefsCursor >= len(a.sessPreviewRefs) {
		return nil
	}
	u := a.sessPreviewRefs[a.sessRefsCursor].URL
	if u == "" {
		return nil
	}
	if a.sessRefsSelected[u] {
		delete(a.sessRefsSelected, u)
	} else {
		a.sessRefsSelected[u] = true
	}
	a.sessRefsCacheKey = "" // selection changed → force re-render
	if a.sessRefsCursor < len(a.sessPreviewRefs)-1 {
		a.sessRefsCursor++
	}
	if sess, ok := a.selectedSession(); ok {
		return a.updateSessionRefsPreview(sess)
	}
	return nil
}

// selectedRefs returns the refs to act on: the multi-selected set (in display
// order) if any, otherwise just the one under the cursor.
func (a *App) selectedRefs() []session.SessionRef {
	if len(a.sessRefsSelected) > 0 {
		var refs []session.SessionRef
		for _, r := range a.sessPreviewRefs {
			if a.sessRefsSelected[r.URL] {
				refs = append(refs, r)
			}
		}
		return refs
	}
	if a.sessRefsCursor >= 0 && a.sessRefsCursor < len(a.sessPreviewRefs) {
		return []session.SessionRef{a.sessPreviewRefs[a.sessRefsCursor]}
	}
	return nil
}

// openSelectedRefs opens every selected reference's URL (or just the one
// under the cursor when nothing is multi-selected) in the default browser.
func (a *App) openSelectedRefs() (tea.Model, tea.Cmd, bool) {
	refs := a.selectedRefs()
	if len(refs) == 0 {
		return a, nil, true
	}
	opened := 0
	for _, ref := range refs {
		if ref.URL == "" {
			continue
		}
		if err := a.openInBrowser(ref.URL); err == nil {
			opened++
		}
	}
	if opened == 0 {
		a.copiedMsg = "No URL for this reference"
	} else if len(refs) == 1 {
		a.copiedMsg = "Opened " + refs[0].Label
	} else {
		a.copiedMsg = fmt.Sprintf("Opened %d reference(s)", opened)
	}
	return a, nil, true
}

// copySelectedRefs copies every selected reference's URL (or just the one
// under the cursor) to the clipboard, newline-joined.
func (a *App) copySelectedRefs() (tea.Model, tea.Cmd, bool) {
	refs := a.selectedRefs()
	if len(refs) == 0 {
		return a, nil, true
	}
	var urls []string
	for _, ref := range refs {
		if ref.URL != "" {
			urls = append(urls, ref.URL)
		}
	}
	if len(urls) == 0 {
		a.copiedMsg = "No URL for this reference"
		return a, nil, true
	}
	copyToClipboard(strings.Join(urls, "\n"))
	if len(urls) == 1 {
		a.copiedMsg = "Copied " + refs[0].Label
	} else {
		a.copiedMsg = fmt.Sprintf("Copied %d reference(s)", len(urls))
	}
	return a, nil, true
}

// openInBrowser opens a URL using the configured opener (open.command_template,
// falling back to the OS default). a.openURL overrides it so tests can intercept
// the call instead of spawning a real process.
func (a *App) openInBrowser(url string) error {
	if a.openURL != nil {
		return a.openURL(url)
	}
	return opener.Open(a.config.Open, url)
}

// drillIntoWorkflowAgent opens the conversation view for the current session and
// navigates into the workflow agent under the cursor — its full transcript,
// which the workflow run summary only previews.
func (a *App) drillIntoWorkflowAgent() (tea.Model, tea.Cmd, bool) {
	if a.sessWfCursor < 0 || a.sessWfCursor >= len(a.sessWfAgents) {
		return a, nil, true
	}
	agent := a.sessWfAgents[a.sessWfCursor]
	sess, ok := a.selectedSession()
	if !ok {
		return a, nil, true
	}
	a.currentSess = sess
	cmd := a.openConversation(sess)

	for i, item := range a.convList.VisibleItems() {
		ci, ok := item.(convItem)
		if !ok {
			continue
		}
		if ci.kind == convAgent && (ci.agent.ID == agent.ID || ci.agent.ShortID == agent.ShortID) {
			a.selectConvBody(i)
			break
		}
	}
	a.updateConvPreview()
	return a, cmd, true
}

// rebuildTasksPreviewContent re-renders the Tasks/Plan content with the current agent cursor.
func (a *App) rebuildTasksPreviewContent() {
	sess, ok := a.selectedSession()
	if !ok {
		return
	}
	a.sessTasksCacheKey = "" // force rebuild
	a.sessTasksCache = a.buildTasksPlanContent(sess)
	a.sessSplit.Preview.SetContent(a.sessTasksCache)
}

// sessPreviewBoundaryCross moves to the next/prev session when the preview
// cursor hits the boundary, and reloads the preview for the new session.
func (a *App) sessPreviewBoundaryCross(dir string) {
	idx := a.sessionList.Index()
	n := len(a.sessionList.Items())
	switch dir {
	case "down":
		if idx < n-1 {
			a.sessionList.Select(idx + 1)
			a.sessSplit.CacheKey = ""
			a.updateSessionPreview()
			// Position cursor at first item in new preview
			a.sessConvCursor = 0
		}
	case "up":
		if idx > 0 {
			a.sessionList.Select(idx - 1)
			a.sessSplit.CacheKey = ""
			a.updateSessionPreview()
			// Position cursor at last item in new preview
			visible := a.convVisibleEntries()
			if len(visible) > 0 {
				a.sessConvCursor = len(visible) - 1
			}
		}
	}
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
				if a.sessConvCursor > 0 {
					a.sessConvCursor--
				}
			} else {
				a.sessConvCursor--
			}
			a.sessPreviewPinned = true
			a.refreshConvPreview()
		} else {
			// At top boundary, stay within the current session preview.
			// boundary crossing disabled in sessions preview
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
				if a.sessConvCursor < len(visible)-1 {
					a.sessConvCursor++
				}
			} else {
				a.sessConvCursor++
			}
			a.refreshConvPreview()
		} else {
			// At bottom boundary, stay within the current session preview.
			// boundary crossing disabled in sessions preview
		}
		a.sessPreviewPinned = a.sessConvCursor < len(visible)-1
		return a, nil, true
	case "c":
		return a.openSessionPreviewFullText(visible), nil, true
	case a.keymap.Session.Actions:
		if sess, ok := a.selectedSession(); ok {
			a.actionsSess = sess
		}
		a.actionsMenu = true
		return a, nil, true
	case a.keymap.Actions.Copy:
		a.copySessionPreviewSelection()
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
			return globalStatsMsg(session.AggregateStats(sessions, a.config.WorktreeDir))
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
		case a.keymap.Session.Refresh:
			return a.openGlobalStats()
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
	case a.keymap.Session.Refresh:
		return a.openGlobalStats()
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
	case "r":
		return a.openStatsDetail(statsDetailRepos)
	case "p":
		return a.openStatsDetail(statsDetailProjects)
	case "o":
		// back to overview
		a.statsDetail = statsDetailNone
		return a, nil
	}
	return a, nil
}

func (a *App) handleSessPageMenu(key string) (tea.Model, tea.Cmd) {
	a.sessPageMenu = false
	switch key {
	case "v":
		return a, a.setSessPreviewMode(sessPreviewConversation)
	case "s":
		return a, a.setSessPreviewMode(sessPreviewStats)
	case "m":
		return a, a.setSessPreviewMode(sessPreviewMemory)
	case "t":
		return a, a.setSessPreviewMode(sessPreviewTasksPlan)
	case "a":
		return a, a.setSessPreviewMode(sessPreviewAgents)
	case "w":
		return a, a.setSessPreviewMode(sessPreviewWorkflows)
	case "c":
		return a, a.setSessPreviewMode(sessPreviewContexts)
	case "r":
		return a, a.setSessPreviewMode(sessPreviewRefs)
	case "l":
		if sess, ok := a.selectedSession(); ok {
			if sess.IsRemote {
				return a.openRemoteLivePreview(sess)
			}
			return a.openLivePreview(sess)
		}
	}
	return a, nil
}

func (a *App) renderSessPageHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "
	line1 := hl.Render("v") + d.Render(":conv") + sp + hl.Render("s") + d.Render(":stats")
	line2 := hl.Render("m") + d.Render(":mem") + sp + hl.Render("t") + d.Render(":tasks")
	line3 := hl.Render("a") + d.Render(":agents") + sp + hl.Render("l") + d.Render(":live")
	line4 := hl.Render("w") + d.Render(":workflows") + sp + hl.Render("c") + d.Render(":contexts")
	body := strings.Join([]string{line1, line2, line3, line4, d.Render("esc:cancel")}, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

// handleStateMenu maps a state-toggle sub-key to a filter toggle. Each key flips
// one session-state token in the active comma-OR filter; "a" clears the filter
// (show all). The menu closes after one keypress.
func (a *App) handleStateMenu(key string) (tea.Model, tea.Cmd) {
	a.stateMenu = false
	switch key {
	case "a":
		a.setSessionListFilter("")
		a.copiedMsg = "Showing all sessions"
		return a, a.updateSessionPreview()
	case "esc":
		return a, nil
	}
	if _, ok := stateFilterTokens[key]; ok {
		a.toggleStateFilter(key)
		return a, a.updateSessionPreview()
	}
	return a, nil
}

// renderStateHintBox draws the state-toggle popup, marking currently-active
// states with a filled dot so multi-state toggles are visible at a glance.
func (a *App) renderStateHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	on := lipgloss.NewStyle().Foreground(colorAccent)
	set := a.currentStateFilterSet()
	mark := func(sub, label string) string {
		glyph := "○ "
		if set != nil && set[stateFilterTokens[sub]] {
			glyph = on.Render("● ")
		}
		return glyph + hl.Render(sub) + d.Render(":"+label)
	}
	sp := "  "
	line1 := mark("l", "live") + sp + mark("i", "input") + sp + mark("m", "mon")
	line2 := mark("d", "done") + sp + mark("w", "wait") + sp + mark("b", "bg") + sp + mark("s", "stuck")
	line3 := hl.Render("a") + d.Render(":all (clear)") + sp + d.Render("esc:cancel")
	body := strings.Join([]string{d.Render("Toggle session states (OR):"), line1, line2, line3}, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

func (a *App) renderStatsPageHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "

	line1 := hl.Render("t") + d.Render(":tools") + sp + hl.Render("m") + d.Render(":mcp") + sp + hl.Render("a") + d.Render(":agents")
	line2 := hl.Render("s") + d.Render(":skills") + sp + hl.Render("c") + d.Render(":cmds") + sp + hl.Render("e") + d.Render(":errors")
	line3 := hl.Render("r") + d.Render(":repos") + sp + hl.Render("p") + d.Render(":projects") + sp + hl.Render("o") + d.Render(":overview")

	body := strings.Join([]string{line1, line2, line3, d.Render("esc:cancel")}, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

func (a *App) handleInspectorMenu(key string) (tea.Model, tea.Cmd) {
	// The facet picker surveys session-wide urls/changes/files. That is a
	// whole-session view, so it targets the pinned "Session Flow" (RootID) row
	// rather than layering Session scope onto the currently-selected turn — an
	// ordinary node no longer offers Session scope at all.
	a.selectSessionFlowContext()
	switch key {
	case "u":
		a.openInspector(inspectorRefs, session.ScopeSession, false)
	case "i":
		a.openInspector(inspectorImages, session.ScopeSession, false)
	case "g":
		a.openInspector(inspectorChanges, session.ScopeSession, false)
	case "f":
		a.openInspector(inspectorFiles, session.ScopeSession, false)
	case "c":
		a.openInspector(inspectorOverview, session.ScopeSession, false)
	}
	return a, nil
}

func (a *App) renderInspectorMenuHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "

	line1 := hl.Render("u") + d.Render(":urls") + sp + hl.Render("i") + d.Render(":images")
	line2 := hl.Render("g") + d.Render(":changes") + sp + hl.Render("f") + d.Render(":files")
	line3 := hl.Render("c") + d.Render(":contexts")

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

// paneProxyIndicator returns a styled [LIVE <state>]/[SHELL <state>] badge for the help line.
func (a *App) paneProxyIndicator() string {
	if a.paneProxy == nil {
		return ""
	}
	label := "LIVE"
	if a.paneProxy.isShell {
		label = "SHELL"
	}
	dot := iconIdle
	style := dimStyle
	if a.sessSplit.Focus {
		dot = iconFocused
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

func (a *App) claudeCmd(dir string, args ...string) (*exec.Cmd, error) {
	return claudecmd.Command(a.config.Claude, dir, args...)
}

func (a *App) openConfigExplorerAtPath(path string) (tea.Model, tea.Cmd) {
	m, cmd := a.openConfigExplorer()
	if path == "" {
		return m, cmd
	}
	clean := filepath.Clean(path)
	for i, item := range a.cfgList.Items() {
		ci, ok := item.(cfgItem)
		if !ok || ci.isHeader {
			continue
		}
		if filepath.Clean(ci.item.Path) == clean {
			a.cfgList.Select(i)
			a.updateConfigPreview()
			return m, cmd
		}
	}
	a.copiedMsg = "Related config item not found"
	return m, cmd
}

func (a *App) openPluginExplorerAt(pluginID string) (tea.Model, tea.Cmd) {
	m, cmd := a.openPluginExplorer()
	if pluginID == "" {
		return m, cmd
	}
	for i, item := range a.plgList.Items() {
		pi, ok := item.(plgItem)
		if !ok || pi.isHeader {
			continue
		}
		if pi.plugin.ID == pluginID || filepath.Clean(pi.plugin.Install.InstallPath) == filepath.Clean(pluginID) {
			a.plgList.Select(i)
			a.updatePluginPreview()
			return m, cmd
		}
	}
	a.copiedMsg = "Related plugin not found"
	return m, cmd
}

func (a *App) openPluginComponentAt(pluginID, componentPath, componentType string) (tea.Model, tea.Cmd) {
	m, cmd := a.openPluginExplorerAt(pluginID)
	if componentPath == "" {
		return m, cmd
	}
	selected, ok := a.plgList.SelectedItem().(plgItem)
	if !ok || selected.isHeader {
		return m, cmd
	}
	m, cmd = a.openPluginDetail(selected.plugin)
	clean := filepath.Clean(componentPath)
	for i, item := range a.plgDetailList.Items() {
		ci, ok := item.(plgCompItem)
		if !ok || ci.isHeader {
			continue
		}
		if filepath.Clean(ci.comp.Path) == clean && (componentType == "" || ci.comp.Type == componentType) {
			a.plgDetailList.Select(i)
			a.updatePluginDetailPreview()
			return m, cmd
		}
	}
	a.copiedMsg = "Related plugin component not found"
	return m, cmd
}

func (a *App) openRelatedContextNode(node session.ContextNode) (tea.Model, tea.Cmd) {
	switch node.RelatedView {
	case "config":
		return a.openConfigExplorerAtPath(node.RelatedPath)
	case "plugin-component":
		return a.openPluginComponentAt(node.RelatedPluginID, node.RelatedPluginComponentPath, node.RelatedPluginComponentType)
	case "plugin":
		return a.openPluginExplorerAt(node.RelatedPluginID)
	default:
		a.copiedMsg = "No related destination"
		return a, nil
	}
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
		cmd, err := a.claudeCmd(dir, "--resume", sess.ID)
		if err != nil {
			a.copiedMsg = "Claude command failed: " + err.Error()
			return a, nil
		}
		return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
			return tea.QuitMsg{}
		})
	}

	// Non-live session in tmux: spawn a new tmux window
	if tmux.InTmux() {
		windowName := sess.ProjectName
		if windowName == "" {
			windowName = "claude"
		}
		if err := tmux.NewWindowClaudeWithConfig(windowName, dir, sess.ID, a.config.Claude); err != nil {
			a.copiedMsg = "Spawn failed"
		} else {
			a.copiedMsg = "Resumed in new window"
		}
		return a, nil
	}

	// Non-tmux: take over CSB
	cmd, err := a.claudeCmd(dir, "--resume", sess.ID)
	if err != nil {
		a.copiedMsg = "Claude command failed: " + err.Error()
		return a, nil
	}
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
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

func (a *App) openSessionPreviewFullText(visible []mergedMsg) *App {
	if a.sessConvCursor < 0 || a.sessConvCursor >= len(visible) {
		return a
	}
	text := entryFullText(visible[a.sessConvCursor].entry)
	if text == "" {
		return a
	}
	a.sessConvFullText = text
	a.sessConvFullScroll = 0
	return a
}

func (a *App) copySessionPreviewSelection() {
	visible := a.convVisibleEntries()
	if a.sessPreviewMode != sessPreviewConversation || a.sessConvCursor < 0 || a.sessConvCursor >= len(visible) {
		a.copiedMsg = "Nothing to copy"
		return
	}
	text := entryFullText(visible[a.sessConvCursor].entry)
	if text == "" {
		a.copiedMsg = "Nothing to copy"
		return
	}
	if err := copyToClipboard(text); err != nil {
		a.copiedMsg = "Copy failed"
		return
	}
	a.copiedMsg = "Copied message!"
}

func (a *App) copySessionAction() (tea.Model, tea.Cmd) {
	if a.sessSplit.Focus && a.sessSplit.Show && a.sessPreviewMode == sessPreviewConversation {
		a.copySessionPreviewSelection()
		return a, nil
	}
	return a.copySelectedSessionPath()
}

func (a *App) copySelectedSessionPaths(selected []session.Session) (tea.Model, tea.Cmd) {
	var paths []string
	for _, sess := range selected {
		if sess.FilePath != "" {
			paths = append(paths, sess.FilePath)
		}
	}
	if len(paths) == 0 {
		a.copiedMsg = "No session files"
		return a, nil
	}
	if err := copyToClipboard(strings.Join(paths, "\n")); err != nil {
		a.copiedMsg = "Copy failed"
		return a, nil
	}
	a.copiedMsg = fmt.Sprintf("Copied %d session path", len(paths))
	if len(paths) != 1 {
		a.copiedMsg += "s"
	}
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
		if item, ok := a.selectedConversationItem(); ok && item.kind == convAgent && item.groupTag == "" {
			a.editChoices = append(a.editChoices,
				editChoice{"a", "agent:" + item.agent.ShortID, item.agent.FilePath},
			)
		}
	}

	// Offer images from the current inspector message.
	if a.state == viewConversation {
		var entry session.Entry
		if a.conv.split.Folds != nil {
			entry = a.conv.split.Folds.Entry
		}
		if len(entry.Content) == 0 {
			if item, ok := a.selectedConversationItem(); ok && item.kind == convMsg {
				entry = item.merged.entry
			}
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
	parts = append(parts, viewLabel("↵", "projects", a.state == viewSessions))
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

func (a *App) sessionPreviewActionsActive() bool {
	return a.sessSplit.Focus && a.sessSplit.Show && a.sessPreviewMode == sessPreviewConversation
}

func (a *App) handleActionsMenu(key string) (tea.Model, tea.Cmd) {
	a.actionsMenu = false
	a.copiedMsg = ""
	if a.sessionPreviewActionsActive() {
		switch key {
		case "c":
			return a, a.setSessPreviewMode(sessPreviewContexts)
		case "f":
			visible := a.convVisibleEntries()
			return a.openSessionPreviewFullText(visible), nil
		case "y", a.keymap.Actions.Copy:
			a.copySessionPreviewSelection()
			return a, nil
		}
	}
	if !a.sessionPreviewActionsActive() && a.hasMultiSelection() {
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
		sessCopy := sess
		a.confirmMsg = fmt.Sprintf("Delete session %s?", sess.ShortID)
		a.confirmAction = func() (tea.Model, tea.Cmd) { return a.deleteSession(sessCopy) }
		return a, nil
	case akm.Resume:
		return a.resumeSession(sess)
	case akm.CopyPath:
		return a.copySelectedSessionPath()
	case akm.Copy:
		return a.copySessionAction()
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
	case akm.Changes:
		changes := extract.SessionChanges(sess.FilePath)
		items, cmap := changeItemsFromSlice(changes)
		a.urlChangeMap = cmap
		a.initDiffViewport()
		return a.openURLMenuFromItems(items, "session changes")
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
	case akm.New:
		return a.startNewSessionInProject(sess)
	case akm.Remote:
		cfg := remote.Config{
			LocalDir:    sess.ProjectPath,
			SessionID:   sess.ID,
			SessionFile: sess.FilePath,
		}
		return a.startRemoteSession(cfg)
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
		if err := tmux.NewWindowClaudeWithConfig(windowName, dir, sess.ID, a.config.Claude); err != nil {
			a.copiedMsg = "Fork failed: " + err.Error()
		} else {
			a.copiedMsg = "Forked → " + windowName
		}
		return a, nil
	}

	// Non-tmux: take over terminal
	cmd, err := a.claudeCmd(dir, "--resume", sess.ID)
	if err != nil {
		a.copiedMsg = "Claude command failed: " + err.Error()
		return a, nil
	}
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
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
	case akm.Copy, akm.CopyPath:
		return a.copySelectedSessionPaths(selected)
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
	case akm.URLs:
		return a.openBulkURLMenu(selected, false)
	case akm.Files:
		return a.openBulkURLMenu(selected, true)
	case akm.Changes:
		return a.openBulkChangesMenu(selected)
	}
	return a, nil
}

// openBulkURLMenu merges URLs or file paths from multiple sessions into the URL menu.
func (a *App) openBulkChangesMenu(selected []session.Session) (tea.Model, tea.Cmd) {
	seen := make(map[string]bool)
	var merged []extract.ChangeItem
	for _, s := range selected {
		for _, ch := range extract.SessionChanges(s.FilePath) {
			url := ch.Item.URL
			if seen[url] {
				continue
			}
			seen[url] = true
			merged = append(merged, ch)
		}
	}
	items, cmap := changeItemsFromSlice(merged)
	a.urlChangeMap = cmap
	a.initDiffViewport()
	return a.openURLMenuFromItems(items, fmt.Sprintf("%d sessions changes", len(selected)))
}

func (a *App) openBulkURLMenu(selected []session.Session, files bool) (tea.Model, tea.Cmd) {
	seen := make(map[string]bool)
	var merged []extract.Item
	for _, s := range selected {
		var items []extract.Item
		if files {
			items = extract.SessionFilePaths(s.FilePath)
		} else {
			items = extract.SessionURLs(s.FilePath)
		}
		for _, item := range items {
			if !seen[item.URL] {
				seen[item.URL] = true
				merged = append(merged, item)
			}
		}
	}
	scope := fmt.Sprintf("%d sessions", len(selected))
	if files {
		scope += " files"
	}
	return a.openURLMenuFromItems(merged, scope)
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
		a.config.SearchQuery = ""
	}
	items := buildGroupedItems(remaining, a.sessGroupMode, a.sessFolded)
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
		if err := tmux.NewWindowClaudeWithConfig(name, dir, s.ID, a.config.Claude); err == nil {
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

// openMessageImage finds the first image in the current inspector message and opens it.
func (a *App) openMessageImage() (tea.Model, tea.Cmd) {
	var entry session.Entry
	if a.conv.split.Folds != nil {
		entry = a.conv.split.Folds.Entry
	}
	if len(entry.Content) == 0 {
		if item, ok := a.selectedConversationItem(); ok && item.kind == convMsg {
			entry = item.merged.entry
		}
	}

	// If block cursor is on an image, open that one.
	var folds *FoldState
	if a.conv.split.Folds != nil {
		folds = a.conv.split.Folds
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
	// Remove from UI immediately
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

	items := buildGroupedItems(remaining, a.sessGroupMode, a.sessFolded)
	a.sessionList.SetItems(items)
	if idx >= len(items) {
		idx = len(items) - 1
	}
	if idx >= 0 {
		a.sessionList.Select(idx)
	}
	a.sessSplit.CacheKey = ""
	a.copiedMsg = "Deleted"

	// Async cleanup — file/pod deletion happens in background
	if sess.IsRemote {
		rs := a.remoteSession
		podName := sess.RemotePodName
		if rs != nil && rs.PodName == podName {
			a.remoteSession = nil
			a.remoteContent = ""
			a.remoteProgressSteps = nil
		}
		return a, func() tea.Msg {
			if rs != nil && rs.PodName == podName {
				rs.Stop()
			} else {
				for _, saved := range remote.LoadSavedSessions() {
					if saved.PodName == podName {
						cfg := remote.Config{Context: saved.Context, Namespace: saved.Namespace}
						remote.DeletePod(context.Background(), cfg, podName)
						break
					}
				}
			}
			remote.RemoveSavedSession(podName)
			return nil
		}
	}

	claudeDir := a.config.ClaudeDir
	filePath := sess.FilePath
	sessID := sess.ID
	return a, func() tea.Msg {
		os.Remove(filePath)
		os.RemoveAll(filepath.Join(filepath.Dir(filePath), sessID))
		os.RemoveAll(filepath.Join(claudeDir, "file-history", sessID))
		os.RemoveAll(filepath.Join(claudeDir, "tasks", sessID))
		return nil
	}
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

// startNewSessionInProject opens a new session in the same project.
// For git repos: prompts for branch name (worktree-first workflow).
// For non-git dirs: creates session directly.
func (a *App) startNewSessionInProject(sess session.Session) (tea.Model, tea.Cmd) {
	// Check if this is a git repo
	dir := sess.ProjectPath
	if dir == "" {
		return a.newSessionInDir("", "")
	}
	gitPath := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitPath); err != nil {
		// Not a git repo — create session directly
		return a.newSessionInDir(dir, sess.ProjectName)
	}

	// Git repo — prompt for branch (worktree-first)
	a.worktreeSess = sess
	a.worktreeMode = true
	a.worktreeNewMode = true
	ti := textinput.New()
	ti.Prompt = "Branch (empty=main): "
	ti.Width = a.width - 20
	ti.Focus()
	a.worktreeInput = ti
	return a, ti.Cursor.BlinkCmd()
}

func (a *App) handleWorktreeInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		name := strings.TrimSpace(a.worktreeInput.Value())
		a.worktreeMode = false
		isNew := a.worktreeNewMode
		a.worktreeNewMode = false

		if isNew {
			// New session mode
			if name == "" {
				// No branch = new session in main project dir
				return a.newSessionInDir(a.worktreeSess.ProjectPath, a.worktreeSess.ProjectName)
			}
			// Create worktree + new session
			return a.executeNewWorktreeSession(a.worktreeSess, name)
		}
		// Move mode (original behavior)
		if name == "" {
			return a, nil
		}
		return a.executeWorktree(a.worktreeSess, name)
	case "esc":
		a.worktreeMode = false
		a.worktreeNewMode = false
		return a, nil
	}
	var cmd tea.Cmd
	a.worktreeInput, cmd = a.worktreeInput.Update(msg)
	return a, cmd
}

// newSessionInDir spawns a new Claude session in the given directory.
func (a *App) newSessionInDir(dir, name string) (tea.Model, tea.Cmd) {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if name == "" {
		name = filepath.Base(dir)
	}
	if tmux.InTmux() {
		if err := tmux.NewWindowClaudeNewWithConfig(name, dir, a.config.Claude); err != nil {
			a.copiedMsg = "Spawn failed"
			return a, nil
		}
		a.copiedMsg = "New session → " + name
		return a, a.delayedRefreshCmd()
	}
	cmd, err := a.claudeCmd(dir)
	if err != nil {
		a.copiedMsg = "Claude command failed: " + err.Error()
		return a, nil
	}
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return tea.QuitMsg{}
	})
}

// delayedRefreshCmd returns a command that waits then triggers a refresh.
// Retries a few times since Claude takes a moment to create its session file.
func (a *App) delayedRefreshCmd() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(2 * time.Second)
		return delayedRefreshMsg{remaining: 3}
	}
}

type delayedRefreshMsg struct{ remaining int }

// executeNewWorktreeSession creates a git worktree and spawns a new Claude session in it.
func (a *App) executeNewWorktreeSession(sess session.Session, branch string) (tea.Model, tea.Cmd) {
	out, err := exec.Command("git", "-C", sess.ProjectPath, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		a.copiedMsg = "Not a git repo"
		return a, nil
	}
	repoRoot := strings.TrimSpace(string(out))
	wtPath := filepath.Join(repoRoot, a.config.WorktreeDir, branch)

	// Try adding worktree
	if err := exec.Command("git", "-C", repoRoot, "worktree", "add", wtPath, branch).Run(); err != nil {
		if err2 := exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", branch, wtPath).Run(); err2 != nil {
			a.copiedMsg = "Worktree failed: " + err2.Error()
			return a, nil
		}
	}

	// Spawn new Claude session in the worktree
	name := filepath.Base(repoRoot) + "/" + branch
	if tmux.InTmux() {
		if err := tmux.NewWindowClaudeNewWithConfig(name, wtPath, a.config.Claude); err != nil {
			a.copiedMsg = "Spawn failed"
			return a, nil
		}
		a.copiedMsg = fmt.Sprintf("New session → %s/%s", a.config.WorktreeDir, branch)
		return a, a.delayedRefreshCmd()
	}
	cmd, err := a.claudeCmd(wtPath)
	if err != nil {
		a.copiedMsg = "Claude command failed: " + err.Error()
		return a, nil
	}
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return tea.QuitMsg{}
	})
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

// newSession opens a new Claude session (without --resume) in the selected
// session's project directory, or CWD if no session is selected.
func (a *App) newSession() (tea.Model, tea.Cmd) {
	dir, _ := os.Getwd()
	windowName := "claude"
	if sess, ok := a.selectedSession(); ok {
		if sess.ProjectPath != "" {
			dir = sess.ProjectPath
		}
		if sess.ProjectName != "" {
			windowName = sess.ProjectName
		}
	}

	if tmux.InTmux() {
		if err := tmux.NewWindowClaudeNewWithConfig(windowName, dir, a.config.Claude); err != nil {
			a.copiedMsg = "Spawn failed"
		} else {
			a.copiedMsg = "New session in new window"
		}
		return a, nil
	}

	// Non-tmux: take over terminal
	cmd, err := a.claudeCmd(dir)
	if err != nil {
		a.copiedMsg = "Claude command failed: " + err.Error()
		return a, nil
	}
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return tea.QuitMsg{}
	})
}

// executeCmdNewSession handles "new <path>" — opens a new Claude session in the given directory.
func (a *App) executeCmdNewSession(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		return a.newSession() // no path, use selected session's project
	}
	dir := parts[len(parts)-1]
	// Expand ~ to home
	if strings.HasPrefix(dir, "~") {
		home, _ := os.UserHomeDir()
		dir = home + dir[1:]
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		a.copiedMsg = "Not a directory: " + dir
		return a, nil
	}
	windowName := filepath.Base(dir)
	if tmux.InTmux() {
		if err := tmux.NewWindowClaudeNewWithConfig(windowName, dir, a.config.Claude); err != nil {
			a.copiedMsg = "Spawn failed"
		} else {
			a.copiedMsg = "New session → " + windowName
		}
		return a, nil
	}
	cmd, err := a.claudeCmd(dir)
	if err != nil {
		a.copiedMsg = "Claude command failed: " + err.Error()
		return a, nil
	}
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return tea.QuitMsg{}
	})
}

// executeCmdWorktreeNew handles "worktree:new <branch>" / "wt:new <branch>".
// It creates a git worktree for the given branch and opens a new Claude session in it.
func (a *App) executeCmdWorktreeNew(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		a.copiedMsg = "Usage: worktree:new <branch>"
		return a, nil
	}
	branch := parts[len(parts)-1]

	sess, ok := a.selectedSession()
	if !ok || sess.ProjectPath == "" {
		a.copiedMsg = "No session with project selected"
		return a, nil
	}

	// Get repo root
	out, err := exec.Command("git", "-C", sess.ProjectPath, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		a.copiedMsg = "Not a git repo"
		return a, nil
	}
	repoRoot := strings.TrimSpace(string(out))

	wtPath := filepath.Join(repoRoot, a.config.WorktreeDir, branch)

	// Try creating worktree: first on existing branch, then create new branch
	if err := exec.Command("git", "-C", repoRoot, "worktree", "add", wtPath, branch).Run(); err != nil {
		if err2 := exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", branch, wtPath).Run(); err2 != nil {
			a.copiedMsg = "Worktree failed: " + err2.Error()
			return a, nil
		}
	}

	// Open new Claude session in the worktree
	windowName := branch
	if tmux.InTmux() {
		if err := tmux.NewWindowClaudeNewWithConfig(windowName, wtPath, a.config.Claude); err != nil {
			a.copiedMsg = "Worktree created but spawn failed"
		} else {
			a.copiedMsg = fmt.Sprintf("Worktree + session → %s/%s", a.config.WorktreeDir, branch)
		}
		return a, nil
	}

	// Non-tmux: take over terminal
	cmd, err := a.claudeCmd(wtPath)
	if err != nil {
		a.copiedMsg = "Claude command failed: " + err.Error()
		return a, nil
	}
	a.copiedMsg = fmt.Sprintf("Worktree created → %s/%s", a.config.WorktreeDir, branch)
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return tea.QuitMsg{}
	})
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

// refreshRespondingState re-checks IsResponding for live sessions from
// the Claude registry. Updates the list if any badge changed.
func (a *App) refreshRespondingState() {
	live, _ := clauderegistry.Read()
	busy := make(map[string]bool, len(live))
	for _, l := range live {
		if l.IsBusy() {
			busy[l.SessionID] = true
		}
	}
	changed := false
	for i := range a.sessions {
		if !a.sessions[i].IsLive {
			if a.sessions[i].IsResponding {
				a.sessions[i].IsResponding = false
				changed = true
			}
			continue
		}
		wasResponding := a.sessions[i].IsResponding
		a.sessions[i].IsResponding = busy[a.sessions[i].ID]
		if a.sessions[i].IsResponding != wasResponding {
			changed = true
		}
	}
	if changed && !a.isFiltering() {
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

	// Poll remote pod phases at most every 30s. Off the main goroutine.
	var pollCmd tea.Cmd
	if time.Since(a.remoteLastPoll) >= 30*time.Second {
		a.remoteLastPoll = time.Now()
		pollCmd = pollRemotePhasesCmd()
	}

	// Fill in open-PR/Jira badges for the sessions currently on screen. Scoped to
	// the visible page (not the fleet) so this cannot regress into the CPU sweep
	// removed in #60; refsInFlight dedups so a row is only worked once.
	refsCmd := a.resolveVisibleRefsCmd()

	if !a.liveUpdate {
		return tea.Batch(pollCmd, refsCmd)
	}
	return tea.Batch(pollCmd, refsCmd, a.doRefresh())
}

// invalidateOpenPreviewCaches clears the cache keys of whichever session
// preview mode is currently open, so a subsequent updateSessionPreview()
// re-reads the underlying data instead of short-circuiting. Follows the
// refreshSessionPreviewLive template (clear the mode's own key plus the shared
// split CacheKey). For refs it also resets the per-session resolution state and
// the in-flight latch; the process-wide TTL cache is cleared separately via
// session.ClearRefCache so the re-resolve actually hits the network. Live and
// remote modes are left untouched (their content is pushed by a live stream).
func (a *App) invalidateOpenPreviewCaches() {
	switch a.sessPreviewMode {
	case sessPreviewConversation:
		a.sessConvCacheID = ""
		a.sessConvEntries = nil
	case sessPreviewStats:
		a.sessStatsCache = nil
		a.sessStatsCacheKey = ""
	case sessPreviewMemory:
		a.sessMemoryCacheKey = ""
	case sessPreviewTasksPlan:
		a.sessTasksCacheKey = ""
	case sessPreviewShells:
		a.sessShellsCacheKey = ""
	case sessPreviewContexts:
		a.sessContextsCacheKey = ""
	case sessPreviewWorkflows:
		a.sessWorkflowsCacheKey = ""
	case sessPreviewRefs:
		a.invalidateSelectedSessionRefs()
	case sessPreviewAgents:
		// No per-session memo; the shared CacheKey reset below suffices.
	}
	a.sessSplit.CacheKey = ""
}

// invalidateSelectedSessionRefs resets the currently-selected session's ref
// resolution state so the refs preview re-extracts and re-resolves. The store
// copy is the authoritative one (updateSessionRefsPreview reads it via
// sessionByIDFromStore), so reset it there.
func (a *App) invalidateSelectedSessionRefs() {
	sess, ok := a.selectedSession()
	if !ok {
		return
	}
	for i := range a.sessions {
		if a.sessions[i].ID == sess.ID {
			a.sessions[i].RefsResolved = false
			a.sessions[i].Refs = nil
		}
	}
	delete(a.refsInFlight, sess.ID)
	a.sessRefsCacheKey = ""
}

func (a *App) doRefresh() tea.Cmd {
	switch a.state {
	case viewSessions:
		// Full rescan to discover new/deleted sessions
		fresh, err := session.ScanSessions(a.config.ClaudeDir)
		if err == nil && len(fresh) > 0 {
			// Preserve live state detection
			tmux.MarkLiveSessions(fresh)
			session.EnrichLiveSessions(fresh)

			// ScanSessions only sets HasRefs; it does not re-resolve PR/Jira
			// status (that is network-bound and done on demand). Carry the
			// already-resolved Refs/RefsResolved from the current a.sessions
			// into the fresh slice, or a manual refresh / new-session rescan
			// would wipe them and flip an open preview back to "Resolving…".
			a.carryOverRefState(fresh)

			a.sessions = a.injectRemoteSessions(fresh)
			a.globalStatsCache = nil // invalidate cached stats

			if !a.isFiltering() {
				a.rebuildSessionList()
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
					// New activity may have introduced fresh PR/Jira links, so
					// allow one more resolve pass (the URL→status cache is still
					// TTL-guarded, so this stays cheap when nothing changed). Skip
					// if a resolve is already in flight — a live session's mtime
					// changes every few seconds, and resetting mid-pass made
					// enrichRefsCmd re-parse the (large) transcript and re-run gh
					// every tick, spiking CPU and stalling navigation.
					if !a.refsInFlight[a.sessions[i].ID] {
						a.sessions[i].RefsResolved = false
					}
				}
			}
			type liveState struct{ live, responding bool }
			oldLive := make([]liveState, len(a.sessions))
			for i := range a.sessions {
				oldLive[i] = liveState{a.sessions[i].IsLive, a.sessions[i].IsResponding}
				a.sessions[i].IsLive = false
				a.sessions[i].IsResponding = false
				a.sessions[i].IsCurrentWindow = false
			}
			tmux.MarkLiveSessions(a.sessions)
			session.EnrichLiveSessions(a.sessions)
			for i := range a.sessions {
				if a.sessions[i].IsLive != oldLive[i].live {
					needsSort = true
				}
				if a.sessions[i].IsResponding != oldLive[i].responding {
					needsRefresh = true
				}
			}
			if (needsSort || needsRefresh) && !a.isFiltering() {
				if needsSort {
					sort.Slice(a.sessions, func(i, j int) bool {
						return a.sessions[i].ModTime.After(a.sessions[j].ModTime)
					})
				}
				a.rebuildSessionList()
			}
		}

		// Detect notable lifecycle transitions across the fleet and queue them.
		a.collectNotifications()

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
		// PR/Jira status is resolved on demand for the session whose References
		// preview is open (updateSessionRefsPreview → resolveRefsStatusCmd), NOT
		// swept across the whole fleet here. A background sweep meant resolving
		// every HasRefs session's refs via `gh pr view` (~1.6s each) — hundreds of
		// subprocesses that spiked CPU and froze the UI for minutes on large
		// session dirs, while resolving statuses the user never looked at.
		return nil
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
		visItems := a.convList.VisibleItems()
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
		a.selectConvBody(lastMsg)

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
		if a.conv.rightPaneMode == previewText {
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

	// Don't call updateSessionPreview from the render path for modes whose
	// update returns an async cmd (live, refs) — View() cannot dispatch a cmd, so
	// it would be lost. Those modes are initialized and their cmds dispatched from
	// Update paths (setSessPreviewMode, the navigation debounce, resizeAll). View
	// only re-renders their already-populated content (the resize block below).
	if a.sessPreviewMode != sessPreviewLive && a.sessPreviewMode != sessPreviewRefs {
		_ = a.updateSessionPreview()
	}

	if a.sessSplit.Preview.Width != previewW || a.sessSplit.Preview.Height != contentH {
		oldOffset := a.sessSplit.Preview.YOffset
		oldTotal := a.sessSplit.Preview.TotalLineCount()
		a.sessSplit.Preview.Width = previewW
		a.sessSplit.Preview.Height = max(contentH, 1)

		// Re-render at new size without reloading data or resetting cursor
		// Skip for live and non-streaming remote setup
		isRemoteSetup := false
		if selSess, selOk := a.selectedSession(); selOk && selSess.IsRemote && !a.remoteStreaming {
			isRemoteSetup = true
		}
		if a.sessPreviewMode == sessPreviewConversation && len(a.sessConvEntries) > 0 && !isRemoteSetup {
			a.refreshConvPreview()
		} else if a.sessPreviewMode != sessPreviewLive && !isRemoteSetup {
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

	clampPaginator(&a.sessionList)

	left := a.sessionList.View()
	right := a.sessSplit.Preview.View()

	return renderFixedSplit(left, right, listW, previewW, contentH, borderColor)
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
		if mode == sessPreviewRefs {
			clear(a.sessRefsSelected)
		}
		a.sessPreviewMode = sessPreviewConversation
	} else {
		if a.sessPreviewMode == sessPreviewRefs {
			clear(a.sessRefsSelected)
		}
		a.sessPreviewMode = mode
	}
	a.sessSplit.CacheKey = "" // force re-render
}

// cycleSessionPreviewMode advances to the next preview tab.
// Skips sessPreviewLive — it's only entered via the L key.
func (a *App) cycleSessionPreviewMode() {
	if a.sessPreviewMode == sessPreviewRefs {
		clear(a.sessRefsSelected)
	}
	a.sessPreviewMode = (a.sessPreviewMode + 1) % numSessPreviewModes
	// Skip live and remote — they're entered via dedicated keys
	for a.sessPreviewMode == sessPreviewLive || a.sessPreviewMode == sessPreviewRemote {
		a.sessPreviewMode = (a.sessPreviewMode + 1) % numSessPreviewModes
	}
	a.closePaneProxy()
	a.sessSplit.CacheKey = ""
}

// cycleSessionPreviewModeReverse goes to the previous preview tab.
// Skips sessPreviewLive and sessPreviewRemote.
func (a *App) cycleSessionPreviewModeReverse() {
	if a.sessPreviewMode == sessPreviewRefs {
		clear(a.sessRefsSelected)
	}
	a.sessPreviewMode = (a.sessPreviewMode + numSessPreviewModes - 1) % numSessPreviewModes
	for a.sessPreviewMode == sessPreviewLive || a.sessPreviewMode == sessPreviewRemote {
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
	if pi, ok := a.selectedProject(); ok {
		// In refs mode a project head row previews its representative session's
		// refs (selectedSession returns pi.sessions[0] for a projectItem). The
		// project-summary preview has no refs, so route refs mode through the
		// session path instead — otherwise the extract is never dispatched and
		// the preview sticks on "Resolving…" (projectCentric is the default group
		// mode, so this is the common case, not an edge case).
		if a.sessPreviewMode == sessPreviewRefs && len(pi.sessions) > 0 {
			cacheKey := fmt.Sprintf("%d:%s", a.sessPreviewMode, pi.sessions[0].ID)
			if cacheKey == a.sessSplit.CacheKey {
				return nil
			}
			a.sessSplit.CacheKey = cacheKey
			a.sessPreviewPinned = false
			return a.updateSessionRefsPreview(pi.sessions[0])
		}
		cacheKey := fmt.Sprintf("project:%d:%s", a.sessPreviewMode, pi.basePath)
		if cacheKey == a.sessSplit.CacheKey {
			return nil
		}
		a.sessSplit.CacheKey = cacheKey
		a.sessPreviewPinned = false
		a.updateProjectPreview(pi)
		return nil
	}
	sess, ok := a.selectedSession()
	if !ok {
		return nil
	}

	// Force remote preview mode during setup (not yet streaming)
	previewMode := a.sessPreviewMode
	if sess.IsRemote && !a.remoteStreaming {
		previewMode = sessPreviewRemote
	}

	cacheKey := fmt.Sprintf("%d:%s", previewMode, sess.ID)
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

	switch previewMode {
	case sessPreviewStats:
		a.updateSessionStatsPreview(sess)
	case sessPreviewMemory:
		a.updateSessionMemoryPreview(sess)
	case sessPreviewTasksPlan:
		a.updateSessionTasksPlanPreview(sess)
	case sessPreviewAgents:
		a.updateSessionAgentsPreview(sess)
	case sessPreviewWorkflows:
		a.updateSessionWorkflowsPreview(sess)
	case sessPreviewShells:
		a.updateSessionShellsPreview(sess)
	case sessPreviewContexts:
		a.updateSessionContextsPreview(sess)
	case sessPreviewRefs:
		return a.updateSessionRefsPreview(sess)
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
	case sessPreviewRemote:
		content := a.remoteContent
		if content == "" {
			content = dimStyle.Render(fmt.Sprintf("Remote session: %s [%s]", sess.RemotePodName, sess.RemoteStatus))
		}
		a.sessSplit.Preview.SetContent(content)
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
	// Default to all messages expanded (unfolded)
	a.sessConvExpanded = make(map[int]bool)
	for i := range a.sessConvEntries {
		a.sessConvExpanded[i] = true
	}
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
	rendered, _, _, _, _ := renderConversationPreviewWindowed(visible, previewW, a.sessConvCursor, a.sessConvExpanded, a.sessConvFilterTerm, a.convPreviewRowCache, sess.IsLive)

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
	content, renderedVisible, localCursor, localExpanded, windowed := renderConversationPreviewWindowed(visible, previewW, a.sessConvCursor, a.sessConvExpanded, a.sessConvFilterTerm, a.convPreviewRowCache, isLive)
	if sess, ok2 := a.selectedSession(); ok2 {
		content = a.prependConvHeaders(sess, content, previewW)
	}
	oldOffset := a.sessSplit.Preview.YOffset
	a.sessSplit.Preview.SetContent(content)

	// Scroll to keep cursor visible: estimate cursor line position. In
	// windowed mode the content is already centered around the cursor, so
	// line math should use the rendered window's local cursor instead of the
	// full conversation index.
	cursorLine := convCursorLine(renderedVisible, localCursor, localExpanded, previewW)
	if windowed {
		cursorLine += 1 // top "earlier messages" marker
	}
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
	a.sessConvExpanded = make(map[int]bool)
	for i := range visible {
		a.sessConvExpanded[i] = true
	}
	a.refreshConvPreview()
}

// clearConvFilter removes the conversation preview filter.
func (a *App) clearConvFilter() {
	// Preserve the currently-highlighted entry: when filter is active the
	// cursor indexes into the filtered (subset) slice, but after clearing
	// it must index into the full sessConvEntries. Map back via the
	// stored sessConvFiltered indices.
	targetIdx := -1
	if a.sessConvFilterTerm != "" && a.sessConvCursor >= 0 && a.sessConvCursor < len(a.sessConvFiltered) {
		targetIdx = a.sessConvFiltered[a.sessConvCursor]
	}
	a.sessConvFilterTerm = ""
	a.sessConvFiltered = nil
	if targetIdx >= 0 && targetIdx < len(a.sessConvEntries) {
		a.sessConvCursor = targetIdx
	} else {
		a.sessConvCursor = 0
	}
	a.sessConvExpanded = make(map[int]bool)
	for i := range a.sessConvEntries {
		a.sessConvExpanded[i] = true
	}
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
	items := a.convList.VisibleItems()
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
		a.selectConvBody(bestIdx)
	}
	// Don't auto-snap for targeted jumps
	a.liveTail = false
	a.conv.split.BottomAlign = false
	a.updateConvPreview()

	return a, cmd
}

// handleJumpFromPicker finds a session by ID and opens its conversation,
// navigating to the entry matching JumpUUID.
func (a *App) handleJumpFromPicker() (tea.Model, tea.Cmd) {
	targetSessID := a.config.JumpSession
	targetUUID := a.config.JumpUUID
	a.config.JumpSession = "" // consume

	// Find the session
	for i, s := range a.sessions {
		if s.ID != targetSessID {
			continue
		}
		a.sessionList.Select(i)
		a.currentSess = s
		cmd := a.openConversation(s)

		// Navigate to the target entry UUID
		if targetUUID != "" {
			items := a.convList.VisibleItems()
			for j, li := range items {
				ci, ok := li.(convItem)
				if !ok || ci.kind != convMsg {
					continue
				}
				for idx := ci.merged.startIdx; idx <= ci.merged.endIdx && idx < len(a.conv.messages); idx++ {
					if a.conv.messages[idx].UUID == targetUUID {
						a.selectConvBody(j)
						a.liveTail = false
						a.conv.split.BottomAlign = false
						a.updateConvPreview()
						return a, cmd
					}
				}
			}
		}
		return a, cmd
	}
	return a, nil
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

func (a *App) updateProjectPreview(pi projectItem) {
	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)
	var sb strings.Builder

	title := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	section := lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	muted := dimStyle
	okStyle := doneBadgeStyle
	warnStyle := waitBadgeStyle
	errStyle := stuckBadgeStyle
	liveStyle := liveBadge
	bgStyle := bgBadgeStyle
	monStyle := monBadgeStyle

	sb.WriteString(title.Render(pi.displayName))
	if pi.branch != "" {
		sb.WriteString(muted.Render(" (" + pi.branch + ")"))
	}
	sb.WriteString("\n")
	sb.WriteString(muted.Render(pi.basePath))
	sb.WriteString("\n\n")

	sb.WriteString(section.Render("Summary") + "\n")
	sb.WriteString(fmt.Sprintf("Sessions: %d", len(pi.sessions)))
	if pi.worktrees > 0 {
		sb.WriteString(muted.Render(fmt.Sprintf("  •  Worktrees: %d", pi.worktrees)))
	}
	sb.WriteString(muted.Render(fmt.Sprintf("  •  Messages: %d", pi.totalMsgs)))
	sb.WriteString("\n")

	statusParts := []string{}
	if pi.liveSessions > 0 {
		statusParts = append(statusParts, liveStyle.Render(fmt.Sprintf("LIVE×%d", pi.liveSessions)))
	}
	if pi.busyCount > 0 {
		statusParts = append(statusParts, busyBadge.Render(fmt.Sprintf("BUSY×%d", pi.busyCount)))
	}
	if pi.bgSessions > 0 {
		statusParts = append(statusParts, bgStyle.Render(fmt.Sprintf("BG×%d", pi.bgSessions)))
	}
	if pi.monSessions > 0 {
		statusParts = append(statusParts, monStyle.Render(fmt.Sprintf("MON×%d", pi.monSessions)))
	}
	if pi.waitCount > 0 {
		statusParts = append(statusParts, warnStyle.Render(fmt.Sprintf("WAIT×%d", pi.waitCount)))
	}
	if pi.stuckCount > 0 {
		statusParts = append(statusParts, errStyle.Render(fmt.Sprintf("STUCK×%d", pi.stuckCount)))
	}
	if pi.doneCount > 0 {
		statusParts = append(statusParts, okStyle.Render(fmt.Sprintf("DONE×%d", pi.doneCount)))
	}
	if len(statusParts) > 0 {
		sb.WriteString(strings.Join(statusParts, "  "))
		sb.WriteString("\n")
	}
	if pi.hereCount > 0 {
		sb.WriteString(hereBadge.Render(fmt.Sprintf("HERE×%d", pi.hereCount)))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString(section.Render("Sessions in project") + "\n")
	for i, s := range pi.sessions {
		badgeParts := []string{}
		if s.IsCurrentWindow {
			badgeParts = append(badgeParts, hereBadge.Render("HERE"))
		}
		if s.IsLive {
			badgeParts = append(badgeParts, liveStyle.Render("LIVE"))
		}
		if s.HasMonitorJobs && s.IsLive {
			badgeParts = append(badgeParts, monStyle.Render("MON"))
		}
		switch s.Lifecycle() {
		case session.LifecycleBusy:
			badgeParts = append(badgeParts, busyBadge.Render("BUSY"))
		case session.LifecycleBG:
			badgeParts = append(badgeParts, bgStyle.Render("BG"))
		case session.LifecycleWait:
			badgeParts = append(badgeParts, warnStyle.Render("WAIT"))
		case session.LifecycleStuck:
			badgeParts = append(badgeParts, errStyle.Render("STUCK"))
		case session.LifecycleDone:
			badgeParts = append(badgeParts, okStyle.Render("DONE"))
		}
		name := s.ProjectName
		if s.IsWorktree {
			name = "wt: " + name
		}
		line := fmt.Sprintf("%2d. %s  %s  %dm  %s", i+1, timeAgo(s.ModTime), s.ShortID, s.MsgCount, name)
		sb.WriteString(line)
		if len(badgeParts) > 0 {
			sb.WriteString("  ")
			sb.WriteString(strings.Join(badgeParts, " "))
		}
		sb.WriteString("\n")
		if s.FirstPrompt != "" {
			prompt := s.FirstPrompt
			maxPromptW := max(previewW-6, 10)
			if len(prompt) > maxPromptW {
				prompt = prompt[:maxPromptW-3] + "..."
			}
			sb.WriteString(muted.Render("    " + prompt))
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\n")
	sb.WriteString(muted.Render("Enter/o: expand or collapse project  •  move to a child session to open detailed previews"))

	a.sessSplit.Preview = viewport.New(previewW, contentH)
	a.sessSplit.Preview.SetContent(sb.String())
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

func (a *App) updateSessionAgentsPreview(sess session.Session) {
	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)
	a.sessSplit.Preview = viewport.New(previewW, contentH)
	a.sessSplit.Preview.SetContent(a.buildAgentsPreviewContent(sess))
}

func (a *App) updateSessionWorkflowsPreview(sess session.Session) {
	// Load + join workflow runs and their nested agents once per session, so the
	// cursor can drill into any agent's transcript.
	if a.sessWorkflowsCacheKey != sess.ID {
		a.sessWfRuns, _ = session.FindWorkflows(sess.FilePath)
		allAgents, _ := session.FindSubagents(sess.FilePath)
		a.sessWfAgents = session.JoinWorkflowAgents(a.sessWfRuns, allAgents)
		a.sessWfCursor = 0
		a.sessWorkflowsCacheKey = sess.ID
	}
	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)
	a.sessSplit.Preview = viewport.New(previewW, contentH)
	a.sessSplit.Preview.SetContent(a.buildWorkflowsPreviewContent(sess, previewW))
}

// buildWorkflowsPreviewContent renders the workflow runs recorded for a session
// as an interactive list: per-run header (name/status/metrics), the phase list,
// and each agent's label/state/tokens/tool-calls with a result preview. The
// agent under sessWfCursor is highlighted; Enter drills into that agent's full
// transcript (something Claude's own workflow view can't do — it only shows the
// result). Agent rows are indexed to match a.sessWfAgents so the cursor maps to
// a real drill-down target.
func (a *App) buildWorkflowsPreviewContent(sess session.Session, width int) string {
	runs := a.sessWfRuns
	if len(runs) == 0 {
		runs, _ = session.FindWorkflows(sess.FilePath)
	}
	if len(runs) == 0 {
		return dimStyle.Render("No workflow runs found.")
	}

	focused := a.sessSplit.Focus
	// Map agentID → index in a.sessWfAgents (drill-down order / cursor space).
	cursorIdx := make(map[string]int, len(a.sessWfAgents))
	for i, ag := range a.sessWfAgents {
		cursorIdx[ag.ID] = i
	}
	hasDrill := len(a.sessWfAgents) > 0

	var sb strings.Builder
	if hasDrill {
		hint := "↑↓:agent  enter:open transcript"
		if !focused {
			hint = "→:focus, then ↑↓/enter to drill into agents"
		}
		sb.WriteString(dimStyle.Render(hint) + "\n\n")
	}

	for ri, r := range runs {
		if ri > 0 {
			sb.WriteString("\n")
		}
		name := r.Name
		if name == "" {
			name = r.RunID
		}
		sb.WriteString(dimStyle.Render(fmt.Sprintf("── %s ──", name)) + "\n")

		// Status + metrics line.
		statusStyle := dimStyle
		switch r.Status {
		case "completed":
			statusStyle = lipgloss.NewStyle().Foreground(colorAccent)
		case "error", "failed":
			statusStyle = lipgloss.NewStyle().Foreground(colorError)
		case "running":
			statusStyle = lipgloss.NewStyle().Foreground(colorAssistant)
		}
		meta := fmt.Sprintf("  %s", statusStyle.Render(orDash(r.Status)))
		if r.AgentCount > 0 {
			meta += dimStyle.Render(fmt.Sprintf("  ·  %d agents", r.AgentCount))
		}
		if r.TotalTokens > 0 {
			meta += dimStyle.Render(fmt.Sprintf("  ·  %s tok", fmtNum(r.TotalTokens)))
		}
		if r.TotalToolCalls > 0 {
			meta += dimStyle.Render(fmt.Sprintf("  ·  %d tools", r.TotalToolCalls))
		}
		if r.DurationMS > 0 {
			meta += dimStyle.Render("  ·  " + formatDurationMS(r.DurationMS))
		}
		sb.WriteString(meta + "\n")
		if r.Summary != "" {
			sb.WriteString(dimStyle.Render("  "+r.Summary) + "\n")
		}
		sb.WriteString("\n")

		// Phases.
		if len(r.Phases) > 0 {
			var titles []string
			for _, p := range r.Phases {
				titles = append(titles, p.Title)
			}
			sb.WriteString(dimStyle.Render("  phases: "+strings.Join(titles, " → ")) + "\n\n")
		}

		// Agents.
		for _, ag := range r.Agents {
			icon := iconIdle
			style := dimStyle
			switch ag.State {
			case "done":
				icon = iconDone
				style = lipgloss.NewStyle().Foreground(colorAccent)
			case "error", "failed":
				icon = iconIdle
				style = lipgloss.NewStyle().Foreground(colorError)
			case "running":
				icon = iconActive
				style = lipgloss.NewStyle().Foreground(colorAssistant)
			}
			label := ag.Label
			if label == "" {
				label = ag.AgentID
			}

			// Cursor marker when this agent is the drill-down target.
			marker := "  "
			ci, drillable := cursorIdx[ag.AgentID]
			if drillable && focused && ci == a.sessWfCursor {
				marker = selectMarkStyle.Render("> ")
			}

			line := fmt.Sprintf("%s%s %s", marker, icon, label)
			if ag.PhaseTitle != "" {
				line += dimStyle.Render("  [" + ag.PhaseTitle + "]")
			}
			sb.WriteString(style.Render(line))
			var stats []string
			if ag.Tokens > 0 {
				stats = append(stats, fmtNum(ag.Tokens)+" tok")
			}
			if ag.ToolCalls > 0 {
				stats = append(stats, fmt.Sprintf("%d tools", ag.ToolCalls))
			}
			if ag.DurationMS > 0 {
				stats = append(stats, formatDurationMS(ag.DurationMS))
			}
			if drillable {
				stats = append(stats, "↵ transcript")
			}
			if len(stats) > 0 {
				sb.WriteString(dimStyle.Render("  " + strings.Join(stats, " · ")))
			}
			sb.WriteString("\n")
			if ag.ResultPreview != "" {
				preview := ag.ResultPreview
				if idx := strings.IndexByte(preview, '\n'); idx > 0 {
					preview = preview[:idx]
				}
				sb.WriteString(dimStyle.Render("      "+truncate(preview, max(width-8, 20))) + "\n")
			}
		}

		// Final result.
		if strings.TrimSpace(r.Result) != "" {
			sb.WriteString("\n" + dimStyle.Render("  result:") + "\n")
			result := renderMarkdownText(r.Result, max(width-4, 20))
			for _, ln := range strings.Split(result, "\n") {
				sb.WriteString("  " + ln + "\n")
			}
		}
	}
	return sb.String()
}

// orDash returns s, or "—" when empty.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// formatDurationMS renders a millisecond duration compactly (e.g. "4m55s").
func formatDurationMS(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func (a *App) updateSessionShellsPreview(sess session.Session) {
	if a.sessShellsCacheKey != sess.ID {
		a.sessShellsCache = a.buildShellsPreviewContent(sess)
		a.sessShellsCacheKey = sess.ID
	}
	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)
	a.sessSplit.Preview = viewport.New(previewW, contentH)
	a.sessSplit.Preview.SetContent(a.sessShellsCache)
}

func (a *App) updateSessionContextsPreview(sess session.Session) {
	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)
	// Reset the cursor when the selected session changes so we don't carry a
	// stale index into a different tree.
	if a.sessCtxCacheID != sess.ID {
		a.sessCtxCursor = 0
		a.sessCtxCacheID = sess.ID
	}
	// Cursor/focus are part of the key so highlight moves re-render.
	cacheKey := fmt.Sprintf("%s:%d:%d:%d:%t", sess.ID, sess.ModTime.UnixNano(), previewW, a.sessCtxCursor, a.sessSplit.Focus)
	if a.sessContextsCacheKey != cacheKey {
		tree, err := session.BuildSessionContextTree(a.config.ClaudeDir, sess)
		if err != nil {
			a.sessContextsCache = dimStyle.Render("Failed to build context tree: " + err.Error())
			a.sessCtxNodes = nil
		} else {
			a.sessCtxNodes = flattenContextNodes(tree)
			if a.sessCtxCursor >= len(a.sessCtxNodes) {
				a.sessCtxCursor = max(len(a.sessCtxNodes)-1, 0)
			}
			a.sessContextsCache = renderSessionContextTreeCursor(tree, previewW, a.sessCtxCursor, a.sessSplit.Focus)
		}
		a.sessContextsCacheKey = cacheKey
	}
	a.sessSplit.Preview = viewport.New(previewW, contentH)
	a.sessSplit.Preview.SetContent(a.sessContextsCache)
}

// updateSessionRefsPreview renders the PR/Jira references for a session with
// their resolved status. When a session has links that have not been through a
// resolve pass yet, we extract them offline (fast), render a "Resolving…"
// placeholder, and kick off an async status resolve (returned as a tea.Cmd)
// instead of blocking navigation — each ref's status streams back via
// refStatusMsg and re-renders this preview if it is still open. Status is only
// ever resolved for the session shown here, never swept across the fleet.
// sessionByIDFromStore returns the authoritative session (by ID) from
// a.sessions, the source of truth for lazily-resolved state like Refs. Unlike
// selectedSession()/sessionByID(), which read the list widget's snapshot copies,
// this reflects updates made by async message handlers that mutate a.sessions
// without rebuilding the list.
func (a *App) sessionByIDFromStore(id string) (session.Session, bool) {
	for i := range a.sessions {
		if a.sessions[i].ID == id {
			return a.sessions[i], true
		}
	}
	return session.Session{}, false
}

// carryOverRefState copies lazily-resolved PR/Jira ref state (the extracted Refs
// list + RefsResolved) from the current a.sessions into a freshly-scanned slice,
// in place. ScanSessions detects HasRefs but never resolves status (network-
// bound, on-demand only), so without this a full rescan would blank every
// already-extracted ref and flip an open References preview back to
// "Resolving…" — or, if a resolve was mid-flight, strand it on a bogus
// "No resolvable references".
//
// We carry the Refs list whenever the old session had one (even if status was
// still resolving), so the preview keeps showing links across a rescan. When
// the transcript changed since resolution we keep the cached refs but clear
// RefsResolved so the next preview open re-resolves (picking up any new links);
// the URL→status cache is TTL-guarded, so that re-check stays cheap. A session
// with a resolve currently in flight is left for that pass to finish.
func (a *App) carryOverRefState(fresh []session.Session) {
	if len(a.sessions) == 0 {
		return
	}
	prev := make(map[string]*session.Session, len(a.sessions))
	for i := range a.sessions {
		prev[a.sessions[i].ID] = &a.sessions[i]
	}
	for i := range fresh {
		old, ok := prev[fresh[i].ID]
		if !ok {
			continue
		}
		if len(old.Refs) == 0 && !old.RefsResolved {
			continue // nothing extracted yet — let on-demand extract handle it
		}
		fresh[i].Refs = old.Refs
		fresh[i].RefsResolved = old.RefsResolved
		// Transcript grew since we resolved: keep the cached refs on screen but
		// allow one re-resolve so newly-added links surface. Skip while a
		// resolve is already in flight (resetting mid-pass re-triggers extract
		// every tick — the CPU-spike footgun the earlier fixes fought).
		if old.RefsResolved && !a.refsInFlight[fresh[i].ID] &&
			!fresh[i].ModTime.Equal(old.ModTime) {
			fresh[i].RefsResolved = false
		}
	}
}

// syncSessionRefsToList copies a session's resolved ref state from a.sessions
// (source of truth) into the list widget's sessionItem.sess copy, in place, so
// the open-PR/Jira badge reflects it. The async extract/resolve handlers mutate
// a.sessions but the list holds snapshot copies from the last rebuild, so the
// badge (which reads si.sess.OpenRefCounts) would otherwise never update. This
// avoids a full rebuildSessionList (which resets scroll/cursor) on every ref
// that lands. Returns true if the row was found and updated.
func (a *App) syncSessionRefsToList(id string) bool {
	fresh, ok := a.sessionByIDFromStore(id)
	if !ok {
		return false
	}
	items := a.sessionList.Items()
	for i, item := range items {
		switch v := item.(type) {
		case sessionItem:
			if v.sess.ID != id {
				continue
			}
			v.sess.Refs = fresh.Refs
			v.sess.RefsResolved = fresh.RefsResolved
			items[i] = v
			a.sessionList.SetItems(items)
			return true
		case projectItem:
			// In projectCentric mode the badge is on the project head row, which
			// carries a pre-summed openPRs. Update the embedded session and
			// re-sum so the row's PR badge reflects the resolve.
			found := false
			for j := range v.sessions {
				if v.sessions[j].ID == id {
					v.sessions[j].Refs = fresh.Refs
					v.sessions[j].RefsResolved = fresh.RefsResolved
					found = true
				}
			}
			if !found {
				continue
			}
			openPRs := 0
			for j := range v.sessions {
				if n, _ := v.sessions[j].OpenRefCounts(); n > 0 {
					openPRs += n
				}
			}
			v.openPRs = openPRs
			items[i] = v
			a.sessionList.SetItems(items)
			return true
		}
	}
	return false
}

// resolveVisibleRefsCmd kicks off offline ref extraction for the sessions
// currently ON SCREEN (the visible page slice) that have links but have not
// been extracted/resolved yet. This is what makes the open-PR/Jira badge fill
// in asynchronously without the user opening each References preview.
//
// It is deliberately scoped to the visible page — NOT the whole fleet. A
// tick-driven sweep across every HasRefs session is what pegged CPU for minutes
// (each `gh pr view` ~1.6s; hundreds of them) and was ripped out in #60. Bounding
// work to what the user can actually see keeps the fan-out tiny: extract is a
// ~10ms offline line scan, and the follow-on status resolve is already capped by
// resolveSem (4 concurrent). refsInFlight dedups so a row in view across many
// ticks is only worked once.
func (a *App) resolveVisibleRefsCmd() tea.Cmd {
	if a.state != viewSessions {
		return nil
	}
	items := a.sessionList.VisibleItems()
	if len(items) == 0 {
		return nil
	}
	start, end := a.sessionList.Paginator.GetSliceBounds(len(items))
	var cmds []tea.Cmd
	for _, item := range items[start:end] {
		si, ok := item.(sessionItem)
		if !ok {
			continue
		}
		s, ok := a.sessionByIDFromStore(si.sess.ID)
		if !ok || !s.HasRefs || s.RefsResolved || a.refsInFlight[s.ID] || len(s.Refs) > 0 {
			continue
		}
		// Arm the dedup guard only if an extract is actually dispatched (empty
		// FilePath → nil cmd); latching it otherwise strands the row forever.
		if cmd := a.extractSessionRefsCmd(s.ID, s.FilePath); cmd != nil {
			a.refsInFlight[s.ID] = true
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (a *App) updateSessionRefsPreview(sess session.Session) tea.Cmd {
	// Read the authoritative session from a.sessions, not the list-widget copy
	// the caller passed in. selectedSession() returns sessionItem.sess, a copy
	// snapshotted at the last rebuildSessionList — the async extract/resolve
	// handlers update a.sessions[i].Refs but do NOT rebuild the list, so that
	// copy's Refs/RefsResolved stay stale and the preview would render
	// "Resolving…" forever. a.sessions is the source of truth for ref status.
	if fresh, ok := a.sessionByIDFromStore(sess.ID); ok {
		sess = fresh
	}

	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)

	// Reset the cursor when the selected session changes so it never points past
	// a different session's (possibly shorter) ref list.
	if a.sessRefsCacheID != sess.ID {
		a.sessRefsCursor = 0
		clear(a.sessRefsSelected)
		a.sessRefsCacheID = sess.ID
	}

	var resolveCmd tea.Cmd
	refs := sess.Refs
	// Live (in-progress) sessions grow after the last scan set HasRefs, so a PR
	// URL written moments ago (e.g. the PR we just created) won't have flipped
	// HasRefs yet — the flag is a stale snapshot. Treat live sessions as
	// possibly-having-refs and run the cheap offline extract straight off the
	// file so freshly-added links surface without waiting for a rescan/refresh.
	mayHaveRefs := sess.HasRefs || sess.IsLive
	if len(refs) == 0 && mayHaveRefs && !sess.RefsResolved && !a.refsInFlight[sess.ID] {
		// Not extracted yet: render the placeholder now and kick off the offline
		// extract (no network) so URLs/labels appear fast; status resolves in a
		// second step. refsInFlight dedups so repeated navigation/renders don't
		// re-parse the same session. The returned cmd is dispatched by the Update
		// path that opened/navigated the preview (setSessPreviewMode and the
		// navigation debounce both return it); this method is gated behind
		// updateSessionPreview's session/mode cache key, so it fires once per
		// (session, mode) rather than on every frame.
		// Arm the dedup guard ONLY if we actually dispatched an extract. If the
		// session has no FilePath, extractSessionRefsCmd returns nil — arming
		// anyway would latch refsInFlight with nothing to clear it, stranding the
		// preview on "Resolving…" forever.
		if resolveCmd = a.extractSessionRefsCmd(sess.ID, sess.FilePath); resolveCmd != nil {
			a.refsInFlight[sess.ID] = true
		}
	}
	// Order refs (open PRs first) and stash them so the focused-pane key handler
	// can map the cursor to a concrete URL to open.
	a.sessPreviewRefs = orderRefs(refs)
	if a.sessRefsCursor >= len(a.sessPreviewRefs) {
		a.sessRefsCursor = 0
	}
	cacheKey := fmt.Sprintf("%s:%d:%d:%d:%t:%t:%s", sess.ID, len(refs), previewW, a.sessRefsCursor, a.sessSplit.Focus, sess.RefsResolved, refsSelectionSignature(a.sessPreviewRefs, a.sessRefsSelected))
	if a.sessRefsCacheKey != cacheKey {
		// Pass mayHaveRefs (not just HasRefs) so a live session with no extracted
		// refs yet shows "Resolving…" rather than the misleading "No PR or Jira
		// links" while the offline extract above is still in flight.
		a.sessRefsCache = a.renderSessionRefs(a.sessPreviewRefs, previewW, mayHaveRefs, sess.RefsResolved)
		a.sessRefsCacheKey = cacheKey
	}
	a.sessRefsResolved = sess.RefsResolved
	if a.sessSplit.Preview.Width != previewW || a.sessSplit.Preview.Height != contentH {
		a.sessSplit.Preview = viewport.New(previewW, contentH)
	}
	a.sessSplit.Preview.SetContent(a.sessRefsCache)
	return resolveCmd
}

// extractSessionRefsCmd reads a session's transcript and extracts its PR/Jira
// refs (offline — no status resolution). Fast enough to run on demand; the
// result comes back as refsExtractedMsg so the preview can render URLs/labels
// immediately, then a status resolve is kicked off separately.
func (a *App) extractSessionRefsCmd(id, filePath string) tea.Cmd {
	if id == "" || filePath == "" {
		return nil
	}
	return func() tea.Msg {
		refs := session.ExtractSessionRefsFromFile(filePath)
		return refsExtractedMsg{id: id, refs: refs}
	}
}

// resolveRefsStatusCmd resolves status (gh + Jira REST, TTL-cached) for an
// already-extracted set of refs. Each ref resolves in its own command so
// results stream back one at a time (refStatusMsg) as they land — a slow gh or
// a timing-out Jira call no longer blocks the whole list. refs are expected in
// display order (most-recent first) so the newest statuses fill in first.
func (a *App) resolveRefsStatusCmd(id string, refs []session.SessionRef) tea.Cmd {
	if id == "" || len(refs) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(refs))
	for _, r := range refs {
		ref := r
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			return refStatusMsg{id: id, ref: session.ResolveRef(ctx, ref)}
		})
	}
	return tea.Batch(cmds...)
}

// orderRefs returns refs sorted PRs-first, then most-recently-seen first within
// each kind so the newest work is at the top of the preview.
func orderRefs(refs []session.SessionRef) []session.SessionRef {
	ordered := make([]session.SessionRef, len(refs))
	copy(ordered, refs)
	sort.SliceStable(ordered, func(i, j int) bool {
		ki, kj := ordered[i].Kind, ordered[j].Kind
		if ki != kj {
			return ki == session.RefPR
		}
		return ordered[i].FirstSeen.After(ordered[j].FirstSeen)
	})
	return ordered
}

// refsSelectionSignature builds a stable, order-preserving string of which refs
// (by index into ordered) are currently selected, so the preview render cache
// invalidates whenever the selection set changes shape (not just its size —
// toggling one ref off and a different one on leaves the count unchanged).
func refsSelectionSignature(ordered []session.SessionRef, selected map[string]bool) string {
	if len(selected) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, r := range ordered {
		if selected[r.URL] {
			fmt.Fprintf(&sb, "%d,", i)
		}
	}
	return sb.String()
}

// renderSessionRefs draws the PR/Jira reference list with status indicators.
// The item at sessRefsCursor is highlighted when the preview pane is focused so
// the user can pick a reference and open it in the browser. resolved reports
// whether a resolve pass has completed — when true but the list is empty, the
// session's only links were unresolvable (e.g. compare-page URLs), so we say so
// instead of leaving a permanent "Resolving…" spinner.
func (a *App) renderSessionRefs(ordered []session.SessionRef, width int, hasRefs, resolved bool) string {
	var sb strings.Builder
	title := "── References ──"
	if len(ordered) > 0 && a.sessSplit.Focus {
		title = "── References  ↵:open  sp:select  y:copy ──"
	}
	sb.WriteString(statTitleStyle.Render(title) + "\n\n")
	if len(ordered) == 0 {
		switch {
		case !hasRefs:
			sb.WriteString(dimStyle.Render("No PR or Jira links in this session."))
		case resolved:
			sb.WriteString(dimStyle.Render("No resolvable PR/Jira references."))
		default:
			sb.WriteString(dimStyle.Render("Resolving PR/Jira status…"))
		}
		return sb.String()
	}

	lastKind := session.RefKind("")
	for i, r := range ordered {
		if r.Kind != lastKind {
			title := "Pull Requests"
			if r.Kind == session.RefJira {
				title = "Jira Issues"
			}
			sb.WriteString("\n" + dimStyle.Bold(true).Render(title) + "\n")
			lastKind = r.Kind
		}
		selected := i == a.sessRefsCursor && a.sessSplit.Focus
		checked := a.sessRefsSelected[r.URL]
		sb.WriteString(refLine(r, width, selected, checked) + "\n")
	}
	if selCount := len(a.sessRefsSelected); selCount > 0 {
		sb.WriteString("\n" + dimStyle.Render(fmt.Sprintf("%d selected", selCount)))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// refLine renders a single reference: cursor + selection mark + state dot +
// label + status detail.
func refLine(r session.SessionRef, width int, selected, checked bool) string {
	dot, label := refStateBadge(r)
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true).Render("> ")
	}
	mark := "  "
	if checked {
		mark = lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true).Render("* ")
	}
	line := cursor + mark + dot + " " + label

	var detail string
	switch r.Kind {
	case session.RefPR:
		parts := []string{}
		if r.ReviewDecision != "" {
			parts = append(parts, prettyReview(r.ReviewDecision))
		}
		if r.ChecksState != "" {
			parts = append(parts, prettyChecks(r.ChecksState))
		}
		detail = strings.Join(parts, "  ")
	case session.RefJira:
		if r.JiraStatus != "" {
			detail = r.JiraStatus
		} else if !r.Resolved {
			detail = "…"
		}
	}
	if detail != "" {
		line += "  " + dimStyle.Render(detail)
	}
	// Timestamp of first appearance, so the list reads newest-first with context.
	if !r.FirstSeen.IsZero() {
		line += "  " + dimStyle.Render("· "+timeAgo(r.FirstSeen))
	}
	return line
}

// refStateBadge returns a colored state token and the styled label for a ref.
func refStateBadge(r session.SessionRef) (dot, label string) {
	labelStyle := lipgloss.NewStyle().Bold(true)
	switch r.Kind {
	case session.RefPR:
		switch r.State {
		case session.RefStateOpen:
			return liveDotStyle.Render(iconStatusDot), prBadgeStyle.Render(r.Label) + dimStyle.Render(" OPEN")
		case session.RefStateDraft:
			return dimStyle.Render(iconStatusDot), labelStyle.Render(r.Label) + dimStyle.Render(" DRAFT")
		case session.RefStateMerged:
			return compactBadgeStyle.Render(iconStatusDot), dimStyle.Render(r.Label + " MERGED")
		case session.RefStateClosed:
			return stuckBadgeStyle.Render(iconStatusDot), dimStyle.Render(r.Label + " CLOSED")
		default:
			return dimStyle.Render("○"), labelStyle.Render(r.Label)
		}
	case session.RefJira:
		if r.Resolved && r.JiraStatusDone {
			return doneBadgeStyle.Render(iconStatusDot), dimStyle.Render(r.Label)
		}
		return waitDotIfOpen(r), memoryBadge.Render(r.Label)
	}
	return dimStyle.Render("○"), r.Label
}

func waitDotIfOpen(r session.SessionRef) string {
	if r.Resolved && r.JiraStatus != "" {
		return liveDotStyle.Render(iconStatusDot)
	}
	return dimStyle.Render("○") // unknown status
}

func prettyReview(d string) string {
	switch d {
	case "APPROVED":
		return doneBadgeStyle.Render("✓ approved")
	case "CHANGES_REQUESTED":
		return stuckBadgeStyle.Render("✗ changes")
	case "REVIEW_REQUIRED":
		return waitBadgeStyle.Render("review needed")
	}
	return d
}

func prettyChecks(s string) string {
	switch s {
	case "SUCCESS":
		return doneBadgeStyle.Render("✓ CI")
	case "FAILURE":
		return stuckBadgeStyle.Render("✗ CI")
	case "PENDING":
		return waitBadgeStyle.Render("• CI")
	}
	return s
}

func renderSessionContextTree(tree *session.SessionContextTree, width int) string {
	return renderSessionContextTreeCursor(tree, width, -1, false)
}

// contextNodeDrillable reports whether a node has a drill-in destination
// (config explorer / plugin explorer). Only drillable nodes are cursor stops.
func contextNodeDrillable(node session.ContextNode) bool {
	return node.RelatedView != ""
}

// flattenContextNodes returns the drill-targetable nodes in the same pre-order
// the tree renders, so a flat cursor index lines up with the highlighted row.
func flattenContextNodes(tree *session.SessionContextTree) []session.ContextNode {
	if tree == nil {
		return nil
	}
	var out []session.ContextNode
	var walk func(n session.ContextNode)
	walk = func(n session.ContextNode) {
		if contextNodeDrillable(n) {
			out = append(out, n)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, root := range tree.Roots {
		walk(root)
	}
	return out
}

// renderSessionContextTreeCursor renders the tree; when focused, the drillable
// node at cursorDrillIdx (index into flattenContextNodes) is highlighted.
func renderSessionContextTreeCursor(tree *session.SessionContextTree, width, cursorDrillIdx int, focused bool) string {
	if tree == nil {
		return dimStyle.Render("No context tree available.")
	}
	var sb strings.Builder
	header := "── Session Context"
	if tree.SessionID != "" {
		header += ": " + tree.SessionID[:min(8, len(tree.SessionID))]
	}
	header += " ──"
	sb.WriteString(dimStyle.Render(header) + "\n")
	if focused {
		sb.WriteString(dimStyle.Render("↑↓:node ↵:open ←:unfocus") + "\n")
	}
	sb.WriteString("\n")
	if tree.ProjectPath != "" {
		home, _ := os.UserHomeDir()
		sb.WriteString(dimStyle.Render("project: "+session.ShortenPath(tree.ProjectPath, home)) + "\n\n")
	}
	drillIdx := 0
	for i, node := range tree.Roots {
		renderContextNode(&sb, node, "", i == len(tree.Roots)-1, width, cursorDrillIdx, focused, &drillIdx)
	}
	if len(tree.Warnings) > 0 {
		sb.WriteString("\n" + dimStyle.Render("Warnings") + "\n")
		for _, warning := range tree.Warnings {
			sb.WriteString(dimStyle.Render("  - "+truncateContextText(warning, width-4)) + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderContextNode(sb *strings.Builder, node session.ContextNode, prefix string, last bool, width, cursorDrillIdx int, focused bool, drillIdx *int) {
	connector := "├─ "
	nextPrefix := prefix + "│  "
	if last {
		connector = "└─ "
		nextPrefix = prefix + "   "
	}
	// A drillable node participates in cursor navigation; capture its flat index.
	isCursor := false
	if contextNodeDrillable(node) {
		if focused && *drillIdx == cursorDrillIdx {
			isCursor = true
		}
		*drillIdx++
	}
	line := contextNodeLine(node)
	if width > 0 {
		line = truncateContextText(line, width-len(prefix)-3)
	}
	style := dimStyle
	if node.Used {
		style = lipgloss.NewStyle().Foreground(colorAccent)
	} else if node.Status == "missing" {
		style = lipgloss.NewStyle().Foreground(colorDim)
	}
	if isCursor {
		cur := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
		sb.WriteString(dimStyle.Render(prefix) + cur.Render("> ") + cur.Render(line) + "\n")
	} else {
		sb.WriteString(dimStyle.Render(prefix+connector) + style.Render(line) + "\n")
	}
	for i, child := range node.Children {
		renderContextNode(sb, child, nextPrefix, i == len(node.Children)-1, width, cursorDrillIdx, focused, drillIdx)
	}
}

func contextNodeLine(node session.ContextNode) string {
	line := node.Label
	if node.Count > 0 {
		line += fmt.Sprintf(" [%d]", node.Count)
	}
	if node.Status != "" {
		line += " (" + node.Status + ")"
	}
	if node.Detail != "" {
		line += " — " + oneLine(node.Detail)
	}
	if node.Path != "" {
		home, _ := os.UserHomeDir()
		line += "  " + session.ShortenPath(node.Path, home)
	}
	return line
}

func truncateContextText(s string, maxLen int) string {
	if maxLen <= 3 || len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "\n"); idx >= 0 {
		s = s[:idx]
	}
	return s
}

func (a *App) buildShellsPreviewContent(sess session.Session) string {
	if !sess.HasShellJobs {
		return dimStyle.Render("No background shells or monitors found for this session.")
	}
	jobs := sess.ShellJobs
	if len(jobs) == 0 {
		entries, err := session.LoadMessages(sess.FilePath)
		if err != nil {
			return dimStyle.Render("Failed to load session: " + err.Error())
		}
		jobs = session.LoadShellJobsFromEntries(entries)
	}
	if len(jobs) == 0 {
		return dimStyle.Render("No background shells or monitors found for this session.")
	}

	bashCount, monCount, killed, polled := 0, 0, 0, 0
	for _, j := range jobs {
		switch j.ToolName {
		case "Bash":
			bashCount++
		case "Monitor":
			monCount++
		}
		switch j.Status {
		case "killed", "stopped":
			killed++
		case "polled":
			polled++
		}
	}

	var sb strings.Builder
	header := fmt.Sprintf("── Background shells [%d Bash, %d Monitor", bashCount, monCount)
	if polled > 0 {
		header += fmt.Sprintf(", %d polled", polled)
	}
	if killed > 0 {
		header += fmt.Sprintf(", %d killed", killed)
	}
	header += "] ──"
	sb.WriteString(dimStyle.Render(header) + "\n\n")

	bashStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBF24")).Bold(true)
	monStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")).Bold(true)

	for _, j := range jobs {
		icon, statusColor := iconActive, colorAssistant
		switch j.Status {
		case "polled":
			icon = iconProgress
			statusColor = colorAccent
		case "killed", "stopped":
			icon = iconStopped
			statusColor = colorDim
		}
		statusStyle := lipgloss.NewStyle().Foreground(statusColor).Bold(true)

		toolLabel := bashStyle.Render("Bash")
		tag := ""
		if j.ToolName == "Monitor" {
			toolLabel = monStyle.Render("Monitor")
			if j.Persistent {
				tag = dimStyle.Render(" [persistent]")
			}
		} else {
			tag = dimStyle.Render(" [bg]")
		}

		// Status label: when the owning Claude process is live and the job hasn't
		// been killed, surface it as actively running now (the JSONL-derived
		// status alone can't tell "still watching" from "last event was a while
		// ago"). Registry liveness is authoritative for the process being up.
		statusLabel := j.Status
		if sess.IsLive && j.Status != "killed" && j.Status != "stopped" {
			statusLabel = j.Status + " · live"
		}
		headline := fmt.Sprintf("%s %s%s  %s", statusStyle.Render(icon), toolLabel, tag, statusStyle.Render(statusLabel))
		if j.PollCount > 0 {
			headline += dimStyle.Render(fmt.Sprintf("  (%d polls)", j.PollCount))
		}
		if j.TimeoutMS > 0 {
			headline += dimStyle.Render(fmt.Sprintf("  timeout=%dms", j.TimeoutMS))
		}
		sb.WriteString(headline + "\n")

		if j.Description != "" {
			sb.WriteString(dimStyle.Render("    # "+j.Description) + "\n")
		}
		cmd := j.Command
		if cmd == "" {
			cmd = "(empty command)"
		}
		for _, line := range splitLines(cmd) {
			if len(line) > 110 {
				line = line[:107] + "..."
			}
			sb.WriteString(bashCmdStyle.Render("    $ "+line) + "\n")
		}
		if !j.StartedAt.IsZero() {
			sb.WriteString(dimStyle.Render("    started: "+timeAgo(j.StartedAt)) + "\n")
		}
		if !j.LastEventAt.IsZero() && !j.LastEventAt.Equal(j.StartedAt) {
			sb.WriteString(dimStyle.Render("    last:    "+timeAgo(j.LastEventAt)) + "\n")
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (a *App) buildTasksPlanContent(sess session.Session) string {
	home, _ := os.UserHomeDir()
	var sb strings.Builder

	// Tasks — try file-based tasks first, fall back to JSONL parsing
	tasks := sess.Tasks
	crons := sess.Crons
	fromConv := false
	if (len(tasks) == 0 && sess.HasTasks) || (len(crons) == 0 && sess.HasCrons) {
		entries, err := session.LoadMessages(sess.FilePath)
		if err == nil {
			if len(tasks) == 0 && sess.HasTasks {
				tasks = session.LoadTasksFromEntries(entries)
				fromConv = true
			}
			if len(crons) == 0 && sess.HasCrons {
				crons = session.LoadCronsFromEntries(entries)
				fromConv = true
			}
		}
	}
	if len(tasks) > 0 {
		completed := 0
		for _, t := range tasks {
			if t.Status == "completed" {
				completed++
			}
		}
		label := fmt.Sprintf("── Tasks [%d/%d] ──", completed, len(tasks))
		if fromConv {
			label += " (from conversation)"
		}
		sb.WriteString(dimStyle.Render(label) + "\n\n")
		for _, t := range tasks {
			icon := iconIdle
			style := dimStyle
			switch t.Status {
			case "completed":
				icon = iconDone
				style = lipgloss.NewStyle().Foreground(colorAccent)
			case "in_progress":
				icon = iconActive
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

	if len(crons) > 0 {
		active := 0
		for _, c := range crons {
			if c.Status != "deleted" {
				active++
			}
		}
		label := fmt.Sprintf("── Cron Jobs [%d/%d active] ──", active, len(crons))
		if fromConv {
			label += " (from conversation)"
		}
		sb.WriteString(dimStyle.Render(label) + "\n\n")
		for _, c := range crons {
			icon := iconActive
			style := lipgloss.NewStyle().Foreground(colorAssistant)
			status := "active"
			if c.Status == "deleted" {
				icon = iconStopped
				style = dimStyle
				status = "deleted"
			}
			recurring := "once"
			if c.Recurring {
				recurring = "recurring"
			}
			headline := strings.TrimSpace(strings.Join([]string{c.ID, c.Cron}, "  "))
			if headline == "" {
				headline = "(unknown cron)"
			}
			sb.WriteString(style.Render(fmt.Sprintf("  %s %s", icon, headline)) + dimStyle.Render(fmt.Sprintf("  [%s, %s]", recurring, status)) + "\n")
			if c.Prompt != "" {
				prompt := c.Prompt
				if idx := strings.Index(prompt, "\n"); idx > 0 {
					prompt = prompt[:idx]
				}
				if len(prompt) > 120 {
					prompt = prompt[:117] + "..."
				}
				sb.WriteString(dimStyle.Render("    "+prompt) + "\n")
			}
			if !c.CreatedAt.IsZero() {
				sb.WriteString(dimStyle.Render("    created: "+c.CreatedAt.Format(time.RFC3339)) + "\n")
			}
			if c.Status == "deleted" && !c.DeletedAt.IsZero() {
				sb.WriteString(dimStyle.Render("    deleted: "+c.DeletedAt.Format(time.RFC3339)) + "\n")
			}
			sb.WriteString("\n")
		}
	}

	// Agents are shown in the dedicated agents preview mode.

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
		return dimStyle.Render("No tasks, agents, cron jobs, or plans found for this session.")
	}
	return sb.String()
}

func (a *App) buildAgentsPreviewContent(sess session.Session) string {
	a.sessPreviewAgents = nil
	if !sess.HasAgents {
		return dimStyle.Render("No agents found for this session.")
	}
	agents, err := session.FindSubagents(sess.FilePath)
	if err != nil || len(agents) == 0 {
		return dimStyle.Render("No agents found for this session.")
	}
	a.sessPreviewAgents = agents
	if a.sessAgentCursor >= len(agents) {
		a.sessAgentCursor = 0
	}
	running := 0
	for _, ag := range agents {
		if ag.MsgCount > 0 && ag.MsgCount%2 == 1 {
			running++
		}
	}
	var sb strings.Builder
	label := fmt.Sprintf("── Agents [%d] ↵:jump ──", len(agents))
	if running > 0 {
		label = fmt.Sprintf("── Agents [%d, %d active] ↵:jump ──", len(agents), running)
	}
	sb.WriteString(dimStyle.Render(label) + "\n\n")
	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
	for i, ag := range agents {
		icon := iconAgent
		style := dimStyle
		if ag.MsgCount > 0 && ag.MsgCount%2 == 1 {
			icon = iconActive
			style = lipgloss.NewStyle().Foreground(colorAssistant)
		} else if ag.MsgCount > 0 {
			icon = iconDone
			style = lipgloss.NewStyle().Foreground(colorAccent)
		}
		typeBadge := ag.AgentType
		if typeBadge == "" {
			typeBadge = "agent"
		}
		headline := fmt.Sprintf("%s %s", icon, typeBadge)
		if ag.ShortID != "" {
			headline += "  " + ag.ShortID
		}
		cursor := "  "
		if i == a.sessAgentCursor && a.sessSplit.Focus {
			cursor = sel.Render("> ")
			sb.WriteString(cursor + sel.Render(headline) + "\n")
		} else {
			sb.WriteString(cursor + style.Render(headline) + "\n")
		}
		if ag.FirstPrompt != "" {
			prompt := ag.FirstPrompt
			if len(prompt) > 100 {
				prompt = prompt[:97] + "..."
			}
			sb.WriteString(dimStyle.Render("    "+prompt) + "\n")
		}
		if !ag.Timestamp.IsZero() {
			sb.WriteString(dimStyle.Render("    "+timeAgo(ag.Timestamp)) + "\n")
		}
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
			icon := iconIdle
			style := dimStyle
			switch t.Status {
			case "completed":
				icon = iconDone
				style = lipgloss.NewStyle().Foreground(colorAccent)
			case "in_progress":
				icon = iconActive
				style = lipgloss.NewStyle().Foreground(colorAssistant)
			}
			sb.WriteString(style.Render(fmt.Sprintf("  %s %s", icon, t.Content)) + "\n")
		}
		sb.WriteString("\n")
	}

	// Memory notes — parsed with frontmatter (name/description/type), index first.
	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 20)
	notes := session.LoadMemoryNotes(sess.ProjectPath, home)
	for _, note := range notes {
		sb.WriteString(a.renderMemoryNote(note, previewW))
	}

	if sb.Len() == 0 {
		return dimStyle.Render("No memory or todos found.")
	}
	return sb.String()
}

// memTypeStyle colors a memory note's type tag.
func memTypeStyle(t string) lipgloss.Style {
	switch t {
	case "user":
		return lipgloss.NewStyle().Foreground(colorUser).Bold(true)
	case "feedback":
		return lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	case "project":
		return lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	case "reference":
		return dimStyle.Bold(true)
	}
	return dimStyle
}

// renderMemoryNote renders one parsed memory note as a titled card: a header
// line (name + type tag), the description as a subtitle, then the markdown body
// (tables/headings rendered). MEMORY.md (the index) gets a distinct label.
func (a *App) renderMemoryNote(note session.MemoryNote, width int) string {
	var sb strings.Builder

	title := note.Name
	if note.IsIndex {
		sb.WriteString(dimStyle.Render("══ Memory Index ══") + "\n\n")
	} else {
		header := dimStyle.Render("── ") + memTypeStyle(note.Type).Render(title) + dimStyle.Render(" ──")
		if note.Type != "" {
			header += "  " + memTypeStyle(note.Type).Render("["+note.Type+"]")
		}
		sb.WriteString(header + "\n")
		if note.Description != "" {
			sb.WriteString(dimStyle.Render("  "+note.Description) + "\n")
		}
		sb.WriteString("\n")
	}

	body := renderMarkdownText(note.Body, max(width-2, 20))
	sb.WriteString(strings.TrimRight(body, "\n") + "\n\n")
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
		// Re-render non-message preview for live session. Only modes whose
		// content actually changes as the session writes need refreshing;
		// crucially, sessPreviewLive/Remote must be left alone — their content is
		// the tmux pane proxy, refreshed separately, and re-rendering here would
		// clobber the live pane with another preview (memory) every tick.
		switch a.sessPreviewMode {
		case sessPreviewStats:
			a.sessSplit.CacheKey = ""
			a.sessStatsCache = nil
			a.sessStatsCacheKey = ""
			a.updateSessionStatsPreview(sess)
		case sessPreviewTasksPlan:
			a.sessSplit.CacheKey = ""
			a.sessTasksCacheKey = ""
			a.updateSessionTasksPlanPreview(sess)
		case sessPreviewMemory:
			a.sessSplit.CacheKey = ""
			a.sessMemoryCacheKey = ""
			a.updateSessionMemoryPreview(sess)
		case sessPreviewWorkflows:
			a.sessSplit.CacheKey = ""
			a.sessWorkflowsCacheKey = ""
			a.updateSessionWorkflowsPreview(sess)
		}
		// sessPreviewLive, sessPreviewRemote, sessPreviewAgents, sessPreviewShells,
		// sessPreviewContexts: leave as-is.
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

	// Expand new messages by default (only add entries beyond old count)
	if a.sessConvExpanded != nil {
		for i := oldCount; i < len(visible); i++ {
			a.sessConvExpanded[i] = true
		}
	}

	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	content, _, _, _, _ := renderConversationPreviewWindowed(visible, previewW, a.sessConvCursor, a.sessConvExpanded, a.sessConvFilterTerm, a.convPreviewRowCache, true)
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

// truncateFooter clips the (already-colorized) footer line to the terminal
// width so a long help string never overflows and pushes hints off-screen.
// ANSI+CJK aware via truncateExact.
func (a *App) truncateFooter(h string) string {
	if a.width <= 0 {
		return h
	}
	out, _ := truncateExact(h, a.width)
	return out
}

// renderActionsHintBox renders a compact bordered hint box for the actions menu.
func (a *App) renderActionsHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "
	akm := a.keymap.Actions

	var lines []string
	if a.hasMultiSelection() && !a.sessionPreviewActionsActive() {
		header := fmt.Sprintf("%d selected", len(a.selectedSet))
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(header))
		lines = append(lines, hl.Render(displayKey(akm.Delete))+d.Render(":delete")+sp+hl.Render(displayKey(akm.Resume))+d.Render(":resume")+sp+hl.Render(displayKey(akm.Copy))+d.Render(":copy")+sp+hl.Render(displayKey(akm.Kill))+d.Render(":kill")+sp+hl.Render(displayKey(akm.Input))+d.Render(":input"))
		lines = append(lines, hl.Render(displayKey(akm.URLs))+d.Render(":urls")+sp+hl.Render(displayKey(akm.Files))+d.Render(":files")+sp+hl.Render(displayKey(akm.Changes))+d.Render(":changes")+sp+hl.Render(displayKey(akm.Tags))+d.Render(":tags"))
	} else {
		sess := a.actionsSess
		if a.sessionPreviewActionsActive() {
			lines = append(lines, hl.Render("c")+d.Render(":contexts")+sp+hl.Render("f")+d.Render(":full")+sp+hl.Render("y")+d.Render(":copy"))
		} else {
			lines = append(lines, hl.Render(displayKey(akm.Delete))+d.Render(":delete")+sp+hl.Render(displayKey(akm.Move))+d.Render(":move")+sp+hl.Render(displayKey(akm.Resume))+d.Render(":resume")+sp+hl.Render(displayKey(akm.Copy))+d.Render(":copy")+sp+hl.Render(displayKey(akm.CopyPath))+d.Render(":copy-path"))
		}
		line2 := hl.Render(displayKey(akm.Worktree)) + d.Render(":worktree") + sp + hl.Render(displayKey(akm.URLs)) + d.Render(":urls") + sp + hl.Render(displayKey(akm.Files)) + d.Render(":files") + sp + hl.Render(displayKey(akm.Changes)) + d.Render(":changes") + sp + hl.Render(displayKey(akm.Tags)) + d.Render(":tags")
		if sess.HasMemory {
			line2 += sp + hl.Render(displayKey(akm.RemoveMem)) + d.Render(":rm-mem")
		}
		if sess.IsWorktree {
			line2 += sp + hl.Render(displayKey(akm.ImportMem)) + d.Render(":import-mem")
		}
		line2 += sp + hl.Render(displayKey(akm.Fork)) + d.Render(":fork")
		line2 += sp + hl.Render(displayKey(akm.New)) + d.Render(":new")
		line2 += sp + hl.Render(displayKey(akm.Remote)) + d.Render(":remote")
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
			h.Render("role:") + d.Render("user asst"),
			h.Render("tool:") + d.Render("Bash Read Edit Write Agent"),
			h.Render("has:") + d.Render("image task bg agent thinking"),
			h.Render("is:") + d.Render("error agent task bg"),
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
		a.urlSearching || a.conv.blockFiltering
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
		a.config.SearchQuery = ""
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
		// Capture stable identity of the selected item before reset so we
		// can re-select the same logical entry once the filter is cleared.
		// Falling back to the (filtered) index would land on an unrelated
		// item because the index space shifts when ResetFilter expands the
		// visible items back to the full set.
		selID := a.selectedConversationItemID()
		wasContext := a.conv.contextActive
		idx := a.convList.Index()
		a.convList.ResetFilter()
		if wasContext && a.restoreConvSelection(selID) {
			return
		}
		if selID != "" {
			for i, item := range a.convList.VisibleItems() {
				if ci, ok := item.(convItem); ok && convItemID(ci) == selID {
					a.selectConvBody(i)
					return
				}
			}
		}
		// Fallback: clamp the previous index into the now-larger list.
		total := len(a.convList.VisibleItems())
		if idx >= total {
			idx = total - 1
		}
		if idx >= 0 {
			a.selectConvBody(idx)
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
			if _, ok := msg.(list.FilterMatchesMsg); ok {
				a.conv.contextActive = false
				a.updateConvHeader()
				a.updateConvPreview()
			}
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
			if _, ok := msg.(list.FilterMatchesMsg); ok {
				a.conv.contextActive = false
				a.updateConvHeader()
				a.updateConvPreview()
			}
			return a, cmd
		}
		return a, nil
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
	visible := a.sessionList.VisibleItems()
	for _, projPath := range tmux.CurrentWindowClaudes() {
		absProj, _ := filepath.Abs(projPath)
		if absProj == "" {
			absProj = projPath
		}
		for i, item := range visible {
			si, ok := item.(sessionItem)
			if !ok {
				continue
			}
			sp := si.sess.ProjectPath
			absSP, _ := filepath.Abs(sp)
			if absSP == "" {
				absSP = sp
			}
			if absSP == absProj {
				a.sessionList.Select(i)
				// Auto-enter live sessions (only if TmuxAutoLive is enabled)
				if si.sess.IsLive && a.config.TmuxAutoLive {
					a.currentSess = si.sess
					return a.openConversation(si.sess)
				}
				return nil
			}
		}
	}
	// Fallback: ensure cursor isn't parked on a header.
	a.bumpPastHeader(0, +1)
	return nil
}

// bumpPastHeader moves the list cursor in `dir` until it lands on a session
// item (or hits the boundary). When called with start>=0 it Selects start
// first.
func (a *App) bumpPastHeader(start, dir int) {
	visible := a.sessionList.VisibleItems()
	if len(visible) == 0 {
		return
	}
	if start < 0 || start >= len(visible) {
		start = a.sessionList.Index()
	}
	idx := start
	for idx >= 0 && idx < len(visible) {
		switch visible[idx].(type) {
		case sessionItem, projectItem:
			a.sessionList.Select(idx)
			return
		}
		idx += dir
	}
}

func (a *App) resizeAll() tea.Cmd {
	contentH := a.height - 3
	var cmd tea.Cmd

	sessW := a.sessSplit.ListWidth(a.width, a.splitRatio)
	if a.sessionList.Width() == 0 {
		if len(a.sessions) > 0 {
			a.sessionList = newSessionList(a.sessions, sessW, contentH, a.sessGroupMode, a.selectedSet, a.hiddenBadges, a.sessFolded, a.sessionRowCache, a.config.WorktreeDir)
			a.sessSplit.CacheKey = ""
			if a.config.SearchQuery != "" {
				applyListFilter(&a.sessionList, a.config.SearchQuery)
			}
			cmd = a.autoSelectSession()
			// Trigger preview lookup for modes restored from preferences whose
			// update returns an async cmd. View() cannot dispatch a cmd, and this
			// startup branch is the first (and only) place the restored preview is
			// initialized, so both live and refs must fire here — otherwise a
			// persisted "refs" mode sticks on "Resolving…" forever (the extract is
			// never dispatched: setSessPreviewMode, the usual trigger, is not on
			// the startup-restore path).
			if a.sessSplit.Show &&
				(a.sessPreviewMode == sessPreviewLive || a.sessPreviewMode == sessPreviewRefs) {
				if previewCmd := a.updateSessionPreview(); previewCmd != nil {
					cmd = tea.Batch(cmd, previewCmd)
				}
			}
		}
	} else if a.state == viewSessions {
		idx := a.sessionList.Index()
		a.sessionList.SetSize(sessW, contentH)
		a.sessionList.Select(idx)
		a.sessSplit.CacheKey = ""
		// Refs preview is excluded from the View render path (it returns an async
		// cmd View can't dispatch), so re-render it here on resize where we can.
		if a.sessSplit.Show && a.sessPreviewMode == sessPreviewRefs {
			if refsCmd := a.updateSessionPreview(); refsCmd != nil {
				cmd = tea.Batch(cmd, refsCmd)
			}
		}
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
		selectedID := a.selectedConversationItemID()
		a.updateConvHeader()
		a.conv.split.Resize(a.width, a.conversationLayoutHeight(), a.splitRatio)
		a.restoreConvSelection(selectedID)
		// Re-render preview content at new dimensions (preserves folds/scroll)
		if a.conv.split.Show {
			a.conv.split.cachedRP = nil
			if a.conv.split.Folds != nil && len(a.conv.split.Folds.Entry.Content) > 0 {
				a.conv.split.RefreshFoldPreview(a.width, a.splitRatio)
			}
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

	// Preserve active filter
	var filterTerm string
	if a.sessionList.FilterState() == list.FilterApplied {
		filterTerm = a.sessionList.FilterInput.Value()
	}

	contentH := max(a.height-3, 1)
	sessW := a.sessSplit.ListWidth(a.width, a.splitRatio)
	a.sessionList = newSessionList(a.sessions, sessW, contentH, a.sessGroupMode, a.selectedSet, a.hiddenBadges, a.sessFolded, a.sessionRowCache, a.config.WorktreeDir)
	a.sessSplit.CacheKey = ""

	// Reapply filter
	if filterTerm != "" {
		applyListFilter(&a.sessionList, filterTerm)
	}

	// Also re-apply startup search query if no interactive filter was active
	if filterTerm == "" && a.config.SearchQuery != "" {
		applyListFilter(&a.sessionList, a.config.SearchQuery)
	}

	// Restore cursor to previously selected session.
	// Use VisibleItems() because Select() operates on the visible (filtered) index space.
	if selectedID != "" {
		for i, item := range a.sessionList.VisibleItems() {
			if si, ok := item.(sessionItem); ok && si.sess.ID == selectedID {
				a.sessionList.Select(i)
				return
			}
		}
	}
	// Default: ensure cursor isn't parked on a header.
	a.bumpPastHeader(0, +1)
}

// toggleSessGroupFoldAtCursor flips the fold state of the group at the
// current cursor row. Works for any row in the group (header or child).
// When called on a non-group row it's a no-op so the key is safe to press.
func (a *App) toggleSessGroupFoldAtCursor() {
	// Project rows (project-centric view) have their own toggle key.
	if pi, ok := a.sessionList.SelectedItem().(projectItem); ok {
		a.toggleProjectFold(pi)
		return
	}
	si, ok := a.sessionList.SelectedItem().(sessionItem)
	if !ok {
		return
	}
	key := si.groupKey
	if key == "" {
		// Child rows don't carry the groupKey; walk back to the header
		// that contains them.
		idx := a.sessionList.Index()
		items := a.sessionList.VisibleItems()
		for i := idx - 1; i >= 0; i-- {
			parent, ok := items[i].(sessionItem)
			if !ok {
				continue
			}
			if parent.groupKey != "" {
				key = parent.groupKey
				break
			}
			if parent.treeDepth == 0 {
				// Hit a non-group top-level row before finding a header.
				break
			}
		}
	}
	if key == "" {
		return
	}
	if a.sessFolded == nil {
		a.sessFolded = make(map[string]bool)
	}
	a.sessFolded[key] = !a.sessFolded[key]
	a.rebuildSessionList()
	// After rebuild, try to land the cursor on the now-toggled header so
	// the user can immediately re-expand or move on without searching for
	// their row again.
	for i, item := range a.sessionList.VisibleItems() {
		if s, ok := item.(sessionItem); ok && s.groupKey == key {
			a.sessionList.Select(i)
			break
		}
	}
}

// setAllSessGroupsFolded sets the fold flag for every visible group head.
// Used by `f` (fold all) and `F` (expand all).
func (a *App) setAllSessGroupsFolded(folded bool) {
	if a.sessFolded == nil {
		a.sessFolded = make(map[string]bool)
	}
	// Capture currently selected session ID so cursor stays anchored.
	var selID string
	if sess, ok := a.selectedSession(); ok {
		selID = sess.ID
	}
	for _, item := range a.sessionList.Items() {
		switch v := item.(type) {
		case sessionItem:
			if v.groupKey == "" {
				continue
			}
			if folded {
				a.sessFolded[v.groupKey] = true
			} else {
				delete(a.sessFolded, v.groupKey)
			}
		case projectItem:
			key := "repo:" + v.basePath
			if folded {
				a.sessFolded[key] = true
			} else {
				delete(a.sessFolded, key)
			}
		}
	}
	a.rebuildSessionList()
	if selID == "" {
		return
	}
	for i, item := range a.sessionList.VisibleItems() {
		if si, ok := item.(sessionItem); ok && si.sess.ID == selID {
			a.sessionList.Select(i)
			return
		}
	}
}

// toggleProjectFold flips the fold state of a project row. The cursor
// stays on the same project row after rebuild so the user can immediately
// fold/unfold again or move on.
func (a *App) toggleProjectFold(pi projectItem) {
	if a.sessFolded == nil {
		a.sessFolded = make(map[string]bool)
	}
	key := "repo:" + pi.basePath
	a.sessFolded[key] = !a.sessFolded[key]
	a.rebuildSessionList()
	for i, item := range a.sessionList.VisibleItems() {
		if p, ok := item.(projectItem); ok && p.basePath == pi.basePath {
			a.sessionList.Select(i)
			break
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
		crumbs = []crumb{{" Projects", viewSessions}}
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
			{" Projects", viewSessions},
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
		maxLeftW := max(a.width-rightW-1, 1)
		if lipgloss.Width(text) > maxLeftW {
			text = truncate(text, maxLeftW)
			x = lipgloss.Width(text)
		}
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
// stateFilterBadge returns a short title-bar badge describing the active
// session-state filter (e.g. "LIVE·INPUT·MON" or "DONE-ONLY"), or "" when no
// pure state filter is active.
func (a *App) stateFilterBadge() string {
	set := a.currentStateFilterSet()
	if len(set) == 0 {
		return ""
	}
	order := []struct{ tok, label string }{
		{"is:live", "LIVE"}, {"is:input", "INPUT"}, {"is:mon", "MON"},
		{"is:done", "DONE"}, {"is:wait", "WAIT"}, {"is:bg", "BG"}, {"is:stuck", "STUCK"},
	}
	var labels []string
	for _, o := range order {
		if set[o.tok] {
			labels = append(labels, o.label)
		}
	}
	if len(labels) == 0 {
		return ""
	}
	if len(labels) == 1 {
		return labels[0] + "-ONLY"
	}
	return strings.Join(labels, "·")
}

func (a *App) breadcrumbRightStatus() string {
	var parts []string

	// Main browser badge: always present it as PROJECTS in the UI even if
	// alternate legacy grouping modes still exist internally.
	if a.state == viewSessions {
		modeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
		parts = append(parts, modeStyle.Render("PROJECTS"))
		if badge := a.stateFilterBadge(); badge != "" {
			filterMode := lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981")).Bold(true)
			parts = append(parts, filterMode.Render(badge))
		}
		if a.hasMultiSelection() {
			parts = append(parts, fmt.Sprintf("%d selected", len(a.selectedSet)))
		}
	}

	// Preview mode badge for conversation/message views
	if a.state == viewConversation {
		modeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
		parts = append(parts, modeStyle.Render("FLOW"))
		parts = append(parts, modeStyle.Render(strings.ToUpper(a.conv.inspector.Tab.String())))
		parts = append(parts, modeStyle.Render(strings.ToUpper(inspectorScopeName(a.conv.inspector.Scope))))
		if a.conv.inspector.Zoom {
			parts = append(parts, modeStyle.Render("ZOOM"))
		}
		parts = append(parts, modeStyle.Render(strings.ToUpper(previewModeLabels[a.conv.rightPaneMode])))
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

	// Fleet notification indicator (sessions view only).
	if a.state == viewSessions {
		if ind := a.notifyIndicator(); ind != "" {
			parts = append(parts, ind)
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
		return roleChip("user")
	}
	return roleChip("assistant")
}

// refsExtractedMsg carries the offline-extracted refs (URLs, labels, timestamps
// — no status) for one session, so the preview can render instantly before the
// network-bound status resolve completes.
type refsExtractedMsg struct {
	id   string
	refs []session.SessionRef
}

// refStatusMsg carries the resolved status of a SINGLE ref (matched by URL)
// within a session, so status streams into the preview one ref at a time as
// each gh/Jira call returns instead of waiting for the whole batch.
type refStatusMsg struct {
	id  string
	ref session.SessionRef
}

// urlRefStatusMsg carries the resolved status of a single PR/Jira URL shown in
// the URL menu, keyed by URL rather than session so it streams into the menu
// (which is not scoped to a session) one ref at a time.
type urlRefStatusMsg struct {
	ref session.SessionRef
}

// PR/Jira status is resolved on demand only for the session whose References
// preview is open (updateSessionRefsPreview → resolveRefsStatusCmd, streaming
// back as refStatusMsg). An earlier fleet-wide background sweep ran `gh pr view`
// (~1.6s each) across every HasRefs session — hundreds of subprocesses that
// spiked CPU and froze the UI for minutes, resolving statuses no one viewed.
