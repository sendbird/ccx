package tmux

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	tk "github.com/keyolk/tuikit/tmux"

	"github.com/sendbird/ccx/internal/claudecmd"
	"github.com/sendbird/ccx/internal/clauderegistry"
)

// Pane represents a tmux pane with its metadata. Session, Window and Pane are
// the human-facing name and indexes ("main", "3", "0"); every tmux call
// targets PaneID / WindowID instead, since an index can be renumbered under
// us and tuikit/tmux refuses anything but an ID.
type Pane struct {
	PaneID     string
	WindowID   string
	Command    string
	Session    string
	Window     string
	WindowName string
	Pane       string
	PID        int
	Path       string
}

// tmux calls go through a tuikit/tmux Client and process walks through a
// tuikit/tmux Procs snapshot. Both are package state so the many call sites
// keep their signatures; tests swap them for a Fake and a canned ps table,
// so no test can reach the real tmux server or process table.
var (
	clientMu sync.Mutex
	client   = tk.New(nil) // outside tmux every call fails with ErrNotInTmux
	psSource tk.PSSource   // nil is the real ps
)

// SetClient replaces the tmux client (tests: tk.Client{Runner: tk.NewFake()}).
func SetClient(c tk.Client) {
	clientMu.Lock()
	client = c
	clientMu.Unlock()
}

// SetProcSource replaces where process snapshots come from and drops the
// cached one.
func SetProcSource(src tk.PSSource) {
	clientMu.Lock()
	psSource = src
	clientMu.Unlock()
	procTreeMu.Lock()
	procTreeVal = nil
	procTreeMu.Unlock()
}

// Client returns the tmux client in use.
func Client() tk.Client {
	clientMu.Lock()
	defer clientMu.Unlock()
	return client
}

func procSource() tk.PSSource {
	clientMu.Lock()
	defer clientMu.Unlock()
	return psSource
}

func bg() context.Context { return context.Background() }

// InTmux returns true if the process is running inside a tmux session.
func InTmux() bool {
	return os.Getenv("TMUX") != ""
}

// FindPane finds the tmux pane whose cwd matches projectPath
// and (optionally) has a claude process running in it.
// If sessionID is provided, prefer panes running that specific session.
func FindPane(projectPath string, sessionID ...string) (Pane, bool) {
	if !InTmux() || projectPath == "" {
		return Pane{}, false
	}

	panes, err := ListPanes()
	if err != nil || len(panes) == 0 {
		return Pane{}, false
	}

	absProject, _ := filepath.Abs(projectPath)
	if absProject == "" {
		absProject = projectPath
	}

	sid := ""
	if len(sessionID) > 0 {
		sid = sessionID[0]
	}

	// First pass: match by path AND specific session ID
	if sid != "" {
		for _, p := range panes {
			absPane, _ := filepath.Abs(p.Path)
			if absPane == "" {
				absPane = p.Path
			}
			if absPane == absProject && HasClaudeSession(p.PID, sid) {
				return p, true
			}
		}
	}

	// Second pass: match by path AND has claude child process
	for _, p := range panes {
		absPane, _ := filepath.Abs(p.Path)
		if absPane == "" {
			absPane = p.Path
		}
		if absPane == absProject && HasClaude(p.PID) {
			return p, true
		}
	}

	// Third pass: match by path only
	for _, p := range panes {
		absPane, _ := filepath.Abs(p.Path)
		if absPane == "" {
			absPane = p.Path
		}
		if absPane == absProject {
			return p, true
		}
	}

	return Pane{}, false
}

// fromKit converts a tuikit pane. tuikit parses every free-text field by its
// length, so a cwd or window name containing "|" (the old format separator),
// a tab or Hangul can no longer shift the columns after it.
func fromKit(p tk.Pane) Pane {
	return Pane{
		PaneID:     p.ID,
		WindowID:   p.WindowID,
		Command:    p.Command,
		Session:    p.SessionName,
		Window:     strconv.Itoa(p.WindowIndex),
		WindowName: p.WindowName,
		Pane:       strconv.Itoa(p.PaneIndex),
		PID:        p.PID,
		Path:       p.Cwd,
	}
}

// ListPanes returns all tmux panes across all sessions and windows.
func ListPanes() ([]Pane, error) {
	kp, err := Client().Panes(bg(), tk.PaneFilter{})
	if err != nil {
		return nil, err
	}
	panes := make([]Pane, 0, len(kp))
	for _, p := range kp {
		panes = append(panes, fromKit(p))
	}
	return panes, nil
}

// HasClaudeSession checks if a pane's shell has a claude process running the
// specific session ID (its args carry the ID) anywhere under it — like
// HasClaude, not only as a direct child, so a wrapped claude counts.
func HasClaudeSession(shellPID int, sessionID string) bool {
	if shellPID == 0 || sessionID == "" {
		return false
	}
	procs, ok := loadProcs()
	if !ok {
		return false
	}
	return procs.HasDescendant(shellPID, func(p tk.Proc) bool {
		return strings.Contains(p.Command, "claude") && strings.Contains(p.Command, sessionID)
	})
}

// HasClaude checks if a pane's shell has a claude process anywhere in its
// descendant tree — not just as a direct child. Claude is frequently launched
// behind a wrapper (ccproxy, tee, sudo, a shell function), so checking only the
// immediate children misses it. This must stay consistent with
// MarkLiveSessions, which attributes live claudes to panes via a PPID walk;
// otherwise a session shows [LIVE] but its live preview capture is rejected.
// The snapshot below makes this answer up to windowClaudesTTL stale relative to
// MarkLiveSessions' own walk, so the disagreement window is bounded by that
// TTL rather than being open-ended.
func HasClaude(shellPID int) bool {
	if shellPID == 0 {
		return false
	}
	procs, ok := loadProcs()
	if !ok {
		return false
	}
	return procs.HasDescendant(shellPID, isClaude)
}

func isClaude(p tk.Proc) bool { return strings.Contains(p.Command, "claude") }

// loadProcs reads the process table once (tuikit/tmux Procs). Building it
// costs ~100ms on a busy machine, and the old code paid it separately for
// every pane examined — scanning a window with two Claude panes read the
// whole process table twice. The snapshot is memoized for the same short
// window as the pane scan that drives it, so one startup pays for one `ps`.
func loadProcs() (tk.Procs, bool) {
	procTreeMu.Lock()
	defer procTreeMu.Unlock()
	if procTreeVal != nil && time.Since(procTreeAt) <= windowClaudesTTL {
		return procTreeVal, true
	}
	procs, err := tk.ReadProcs(bg(), procSource())
	if err != nil {
		return nil, false
	}
	procTreeVal, procTreeAt = procs, time.Now()
	return procs, true
}

var (
	procTreeMu  sync.Mutex
	procTreeVal tk.Procs
	procTreeAt  time.Time
)

// SwitchToPane switches the tmux client to the given pane: switch-client
// only when it is in another session, then select-window, then select-pane
// (tuikit/tmux Jump).
func SwitchToPane(p Pane) error {
	return Client().Jump(bg(), p.PaneID)
}

// SendKeyMsg forwards one key press to a pane. Typed text — one rune or a
// batch, Korean included — goes with -l; named keys and ctrl chords by
// their tmux names (tuikit/tmux TeaKeyToTmux). The old string mapper took
// any one-byte key as text and dropped the rest, so a Hangul syllable
// typed into a live preview never reached the pane.
func SendKeyMsg(p Pane, msg tea.KeyMsg) error {
	return Client().SendKeyMsg(bg(), p.PaneID, msg)
}

// SendLiteralKeys sends raw text literally to a tmux pane without appending
// Enter. Useful for bracketed paste / multi-rune input forwarded through a
// proxied preview pane.
func SendLiteralKeys(p Pane, text string) error {
	return Client().SendLiteral(bg(), p.PaneID, text)
}

// SendKeys sends text input to a tmux pane followed by Enter to submit:
// the text with -l (no key-name interpretation), then Enter as a key.
func SendKeys(p Pane, keys string) error {
	return Client().SendLine(bg(), p.PaneID, keys)
}

// SendInterrupt sends Ctrl-C to a pane.
func SendInterrupt(p Pane) error {
	return Client().SendKeys(bg(), p.PaneID, "C-c")
}

// CapturePane captures the visible content of a tmux pane, with escapes.
// Does NOT include scrollback.
func CapturePane(p Pane) (string, error) {
	out, err := Client().CapturePane(bg(), p.PaneID, tk.CaptureOpts{Escapes: true})
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// NewWindowClaude creates a new tmux window with the given name,
// cd's to dir, and resumes the session with the default Claude command.
func NewWindowClaude(windowName, dir, sessionID string) error {
	return NewWindowClaudeWithConfig(windowName, dir, sessionID, claudecmd.Config{})
}

func NewWindowClaudeWithConfig(windowName, dir, sessionID string, cfg claudecmd.Config) error {
	cmd, err := ClaudeWindowShellCommand(dir, cfg, "--resume", sessionID)
	if err != nil {
		return err
	}
	_, err = Client().NewWindow(bg(), "", tk.WindowOpts{Name: windowName, Command: tk.Command{Shell: cmd}})
	return err
}

// NewWindowClaudeNew creates a new tmux window with the given name,
// cd's to dir, and starts a fresh session with the default Claude command.
func NewWindowClaudeNew(windowName, dir string) error {
	return NewWindowClaudeNewWithConfig(windowName, dir, claudecmd.Config{})
}

func NewWindowClaudeNewWithConfig(windowName, dir string, cfg claudecmd.Config) error {
	cmd, err := ClaudeWindowShellCommand(dir, cfg)
	if err != nil {
		return err
	}
	_, err = Client().NewWindow(bg(), "", tk.WindowOpts{Name: windowName, Command: tk.Command{Shell: cmd}})
	return err
}

func ClaudeWindowShellCommand(dir string, cfg claudecmd.Config, args ...string) (string, error) {
	return claudecmd.ShellCommand(cfg, dir, args...)
}

// SpawnHiddenWindow creates a detached tmux window running the given command
// and returns its pane for capture-pane. Only PaneID and WindowName are set;
// every later call targets the ID.
func SpawnHiddenWindow(windowName, shellCmd string) (Pane, error) {
	id, err := Client().NewWindow(bg(), "", tk.WindowOpts{Name: windowName, Command: tk.Command{Shell: shellCmd}})
	if err != nil {
		return Pane{}, err
	}
	return Pane{PaneID: id, WindowName: windowName}, nil
}

// PaneCursorCol returns the cursor column position in the pane.
func PaneCursorCol(p Pane) (int, error) {
	out, err := Client().DisplayMessage(bg(), p.PaneID, "#{cursor_x}")
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// KillWindow kills the tmux window containing the given pane.
func KillWindow(p Pane) error {
	target := p.WindowID
	if target == "" {
		target = p.PaneID // tmux resolves a pane to its window
	}
	return Client().KillWindow(bg(), target)
}

// DisplayPopup runs shellCmd in a tmux popup of the given size and waits
// for it to close. It waits on a person (an editor), so the call has no
// timeout.
func DisplayPopup(width, height, shellCmd string) error {
	return Client().DisplayPopup(tk.Interactive(bg()), "", tk.PopupOpts{Width: width, Height: height, Command: tk.Command{Shell: shellCmd}})
}

// CurrentWindowClaudes returns project paths of Claude processes running in
// the tmux window this process actually lives in — not the tmux client's
// active window, which may differ when several windows share one client.
//
// The earlier implementation asked tmux for "#{session_name}|#{window_index}",
// i.e. the window the *client* is looking at. A Claude session running in a
// background window of the same client (common during a multi-window build)
// then resolved refs against the foreground window's panes instead of its
// own, surfacing unrelated PRs. We now walk this process's own PPID chain to
// the tmux pane that hosts it and use that pane's window. The client-active
// window is the fallback when the pane can't be determined (e.g. a wrapper
// has re-parented the process beyond tmux's view).
func CurrentWindowClaudes() []string {
	if !InTmux() {
		return nil
	}
	if paths, ok := cachedWindowClaudes(); ok {
		return paths
	}

	panes, err := ListPanes()
	if err != nil {
		return nil
	}

	// Pin "our" window by walking this process's own PPID chain to the tmux
	// pane that hosts it. That pane's session|window is the window whose
	// Claude panes we want — independent of which window the client shows.
	mySession, myWindow := clientActiveWindow(panes)
	if own := findPaneByPID(panes, ownPanePID(panes)); own != nil {
		mySession, myWindow = own.Session, own.Window
	}

	paths := claudesInWindow(panes, mySession, myWindow)
	storeWindowClaudes(paths)
	return paths
}

// The window's Claude panes are resolved by shelling out to tmux and then
// pgrep-ing every pane's process subtree — ~210ms on a busy window, measured.
// Startup calls this several times (auto-select, then its filter-cleared
// retry), and it is on the path to the first frame, so the result is memoized
// briefly. The TTL is short because the answer is genuinely live state: a pane
// the user opens in another window should show up on the next tick, not
// minutes later.
const windowClaudesTTL = 2 * time.Second

var (
	windowClaudesMu   sync.Mutex
	windowClaudesAt   time.Time
	windowClaudesVal  []string
	windowClaudesOnce bool
)

func cachedWindowClaudes() ([]string, bool) {
	windowClaudesMu.Lock()
	defer windowClaudesMu.Unlock()
	if !windowClaudesOnce || time.Since(windowClaudesAt) > windowClaudesTTL {
		return nil, false
	}
	return windowClaudesVal, true
}

func storeWindowClaudes(paths []string) {
	windowClaudesMu.Lock()
	windowClaudesVal = paths
	windowClaudesAt = time.Now()
	windowClaudesOnce = true
	windowClaudesMu.Unlock()
}

// InvalidateWindowClaudes drops the memoized window scan so the next call
// re-reads live tmux state. Used by the refresh path, where the user is
// explicitly asking for current reality.
func InvalidateWindowClaudes() {
	windowClaudesMu.Lock()
	windowClaudesOnce = false
	windowClaudesMu.Unlock()
	procTreeMu.Lock()
	procTreeVal = nil
	procTreeMu.Unlock()
}

// claudesInWindow returns the absolute cwd of every pane in the named tmux
// window that has a Claude process (direct child or deeper descendant) running
// in it. Pure function over the pane list — extracted so the window-selection
// logic is testable without a live tmux.
func claudesInWindow(panes []Pane, session, window string) []string {
	var paths []string
	for _, p := range panes {
		if p.Session != session || p.Window != window {
			continue
		}
		if !HasClaude(p.PID) {
			continue
		}
		absPath, _ := filepath.Abs(p.Path)
		if absPath != "" {
			paths = append(paths, absPath)
		}
	}
	return paths
}

// findPaneByPID returns the pane with the given PID, or nil when panePID is 0
// or no pane matches (e.g. this process lives outside any tmux pane).
func findPaneByPID(panes []Pane, panePID int) *Pane {
	if panePID == 0 {
		return nil
	}
	for i := range panes {
		if panes[i].PID == panePID {
			return &panes[i]
		}
	}
	return nil
}

// clientActiveWindow returns the session|window the tmux client currently
// shows, as a fallback when ownPanePID can't attribute this process to a pane.
func clientActiveWindow(panes []Pane) (session, window string) {
	loc, err := Client().Current(bg())
	if err != nil {
		return "", ""
	}
	for _, p := range panes {
		if p.PaneID == loc.PaneID {
			return p.Session, p.Window
		}
	}
	return "", ""
}

// CurrentWindowClaudeSessionIDs returns the session IDs of live Claude
// processes running in the tmux window this process lives in.
//
// Panes are matched by process ancestry, not by cwd. Several panes in one
// window frequently report the same pane_current_path (the shell never left
// the directory the agent was launched from) while their Claude sessions live
// in different projects — worktrees especially. Collapsing those panes to one
// path made the refs lookup pick a single arbitrary session and miss the rest,
// so we attribute each live registry entry to its owning pane instead.
func CurrentWindowClaudeSessionIDs(live []clauderegistry.LiveSession) []string {
	if !InTmux() || len(live) == 0 {
		return nil
	}
	panes, err := ListPanes()
	if err != nil {
		return nil
	}

	mySession, myWindow := clientActiveWindow(panes)
	if own := findPaneByPID(panes, ownPanePID(panes)); own != nil {
		mySession, myWindow = own.Session, own.Window
	}

	procs, _ := loadProcs()
	return claudeSessionsInWindow(panes, live, mySession, myWindow, procs)
}

// claudeSessionsInWindow attributes each live registry entry to the pane that
// owns its process and returns the session IDs landing in the named window,
// in registry order. Pure function over its inputs so the attribution logic is
// testable without a live tmux or process tree.
func claudeSessionsInWindow(panes []Pane, live []clauderegistry.LiveSession, session, window string, procs tk.Procs) []string {
	inWindow := make(map[int]bool, len(panes))
	panePIDs := make(map[int]bool, len(panes))
	for _, p := range panes {
		panePIDs[p.PID] = true
		if p.Session == session && p.Window == window {
			inWindow[p.PID] = true
		}
	}

	var ids []string
	seen := make(map[string]bool, len(live))
	for _, l := range live {
		panePID := walkToPane(l.PID, panePIDs, procs)
		if panePID == 0 || !inWindow[panePID] || l.SessionID == "" || seen[l.SessionID] {
			continue
		}
		seen[l.SessionID] = true
		ids = append(ids, l.SessionID)
	}
	return ids
}

// ownPanePID walks this process's PPID chain up to the tmux pane shell that
// hosts it. Returns 0 when the chain doesn't reach a pane PID (the process
// is outside tmux, or a wrapper has re-parented it beyond the visible tree).
func ownPanePID(panes []Pane) int {
	panePIDs := make(map[int]bool, len(panes))
	for _, p := range panes {
		panePIDs[p.PID] = true
	}
	procs, _ := loadProcs()
	return walkToPane(os.Getpid(), panePIDs, procs)
}

// CurrentWindowKey returns "session_name|window_index" for the tmux window
// this process lives in, or "" when not running inside tmux.
//
// Resolved the same way as CurrentWindowClaudes — via this process's own pane —
// rather than by asking tmux for the default target, which answers about the
// window the *client* is currently looking at. Callers use this key to mark
// which sessions are "in my window"; against the client-active window that
// marking follows the user's focus instead of staying pinned to ccx.
func CurrentWindowKey() string {
	if !InTmux() {
		return ""
	}
	if panes, err := ListPanes(); err == nil {
		if own := findPaneByPID(panes, ownPanePID(panes)); own != nil {
			return own.Session + "|" + own.Window
		}
	}
	// Fall back to the client-active window when our pane can't be resolved.
	panes, _ := ListPanes()
	session, window := clientActiveWindow(panes)
	if session == "" {
		return ""
	}
	return session + "|" + window
}

// MoveWithAndSwitchPane moves the current pane (CSB) to the target's tmux window
// as a side-by-side split, then focuses the target pane.
func MoveWithAndSwitchPane(target Pane) error {
	c := Client()
	// Target our own pane explicitly: without -t, tmux reports the pane the
	// client is looking at, and we would join *that* pane into the target
	// window instead of ours.
	self := tk.PaneFromEnv(os.Getenv)
	if self == "" {
		return SwitchToPane(target)
	}
	myWindow, err := c.DisplayMessage(bg(), self, "#{window_id}")
	if err != nil {
		return SwitchToPane(target)
	}
	if err := c.SelectWindow(bg(), target.WindowID); err != nil {
		return SwitchToPane(target)
	}
	// Move CSB pane to target window if in a different window. join-pane has
	// no typed call; both targets are IDs.
	if strings.TrimSpace(myWindow) != target.WindowID {
		if _, err := c.Run("join-pane", "-s", self, "-t", target.PaneID, "-h", "-l", "30%"); err != nil {
			return err
		}
	}
	return c.SelectPane(bg(), target.PaneID)
}

// ShellQuote wraps a string in single quotes for safe shell embedding.
func ShellQuote(s string) string {
	return claudecmd.ShellQuote(s)
}
