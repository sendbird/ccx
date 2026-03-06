package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

type tmuxPane struct {
	PaneID  string
	Command string
	Session string
	Window  string
	Pane    string
	PID     int
	Path    string
}

func inTmux() bool {
	return os.Getenv("TMUX") != ""
}

// findTmuxPane finds the tmux pane whose cwd matches projectPath
// and (optionally) has a claude process running in it.
// If sessionID is provided, prefer panes running that specific session.
func findTmuxPane(projectPath string, sessionID ...string) (tmuxPane, bool) {
	if !inTmux() || projectPath == "" {
		return tmuxPane{}, false
	}

	panes, err := listTmuxPanes()
	if err != nil || len(panes) == 0 {
		return tmuxPane{}, false
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
			if absPane == absProject && hasClaudeSession(p.PID, sid) {
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
		if absPane == absProject && hasClaude(p.PID) {
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

	return tmuxPane{}, false
}

func listTmuxPanes() ([]tmuxPane, error) {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{pane_id}|#{pane_current_command}|#{session_name}|#{window_index}|#{pane_index}|#{pane_pid}|#{pane_current_path}",
	).Output()
	if err != nil {
		return nil, err
	}

	var panes []tmuxPane
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 7)
		if len(parts) < 7 {
			continue
		}
		pid, _ := strconv.Atoi(parts[5])
		panes = append(panes, tmuxPane{
			PaneID:  parts[0],
			Command: parts[1],
			Session: parts[2],
			Window:  parts[3],
			Pane:    parts[4],
			PID:     pid,
			Path:    parts[6],
		})
	}
	return panes, nil
}

// hasClaudeSession checks if a pane's shell has a claude child process
// running the specific session ID (matches --resume <id> in args).
func hasClaudeSession(shellPID int, sessionID string) bool {
	if shellPID == 0 || sessionID == "" {
		return false
	}
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(shellPID), "-af", "claude").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), sessionID)
}

// hasClaude checks if a pane's shell has a claude child process.
func hasClaude(shellPID int) bool {
	if shellPID == 0 {
		return false
	}
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(shellPID), "-f", "claude").Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// claudeArgsForShell returns the full command line of claude child processes
// under the given shell PID, or "" if none found.
func claudeArgsForShell(shellPID int) string {
	if shellPID == 0 {
		return ""
	}
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(shellPID), "-af", "claude").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// claudePane represents a tmux pane running a Claude process.
type claudePane struct {
	path string // absolute pane CWD
	args string // claude process command line
}

// findClaudePanes returns tmux panes that have an active Claude child process,
// along with the claude process command line for session-ID matching.
func findClaudePanes() []claudePane {
	if !inTmux() {
		return nil
	}
	panes, err := listTmuxPanes()
	if err != nil {
		return nil
	}
	var result []claudePane
	for _, p := range panes {
		args := claudeArgsForShell(p.PID)
		if args == "" {
			continue
		}
		absPath, _ := filepath.Abs(p.Path)
		if absPath != "" {
			result = append(result, claudePane{path: absPath, args: args})
		}
	}
	return result
}

// findLiveProjectPaths returns project paths that have an active Claude process.
// Used as fallback for non-tmux environments.
func findLiveProjectPaths() map[string]bool {
	live := make(map[string]bool)
	out, err := exec.Command("pgrep", "-x", "claude").Output()
	if err != nil {
		return live
	}
	pids := strings.Fields(strings.TrimSpace(string(out)))
	if len(pids) == 0 {
		return live
	}
	pidArg := strings.Join(pids, ",")
	out, err = exec.Command("lsof", "-a", "-d", "cwd", "-Fn", "-p", pidArg).Output()
	if err != nil {
		return live
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			path := strings.TrimSpace(line[1:])
			if path != "" {
				live[path] = true
			}
		}
	}
	return live
}

// markLiveSessions sets IsLive and IsResponding on sessions by matching
// running Claude processes. In tmux, matches by session ID in process args
// with fallback to most-recent-for-path. Outside tmux, matches by path only.
func markLiveSessions(sessions []session.Session) {
	if inTmux() {
		markLiveSessionsTmux(sessions)
	} else {
		markLiveSessionsNonTmux(sessions)
	}
	// Set IsResponding for live sessions
	for i := range sessions {
		if sessions[i].IsLive {
			info, err := os.Stat(sessions[i].FilePath)
			if err == nil {
				sessions[i].IsResponding = time.Since(info.ModTime()) < 10*time.Second
			}
		}
	}
}

func markLiveSessionsTmux(sessions []session.Session) {
	cps := findClaudePanes()
	if len(cps) == 0 {
		return
	}

	// Group session indices by ProjectPath
	pathIdx := map[string][]int{}
	for i, s := range sessions {
		pathIdx[s.ProjectPath] = append(pathIdx[s.ProjectPath], i)
	}

	matched := make([]bool, len(cps)) // track which panes found a session-ID match
	for ci, cp := range cps {
		for _, si := range pathIdx[cp.path] {
			if sessions[si].IsLive {
				continue
			}
			if strings.Contains(cp.args, sessions[si].ID) {
				sessions[si].IsLive = true
				matched[ci] = true
				break
			}
		}
	}

	// Fallback: unmatched panes → most recently modified session for that path
	for ci, cp := range cps {
		if matched[ci] {
			continue
		}
		bestIdx := -1
		for _, si := range pathIdx[cp.path] {
			if sessions[si].IsLive {
				continue
			}
			if bestIdx == -1 || sessions[si].ModTime.After(sessions[bestIdx].ModTime) {
				bestIdx = si
			}
		}
		if bestIdx >= 0 {
			sessions[bestIdx].IsLive = true
		}
	}
}

func markLiveSessionsNonTmux(sessions []session.Session) {
	livePaths := findLiveProjectPaths()
	// Match most recent session per live path
	bestForPath := map[string]int{}
	for i, s := range sessions {
		if !livePaths[s.ProjectPath] {
			continue
		}
		if prev, ok := bestForPath[s.ProjectPath]; !ok || s.ModTime.After(sessions[prev].ModTime) {
			bestForPath[s.ProjectPath] = i
		}
	}
	for _, idx := range bestForPath {
		sessions[idx].IsLive = true
	}
}

// currentTmuxWindowClaudes returns project paths of Claude processes in the current tmux window.
func currentTmuxWindowClaudes() []string {
	if !inTmux() {
		return nil
	}

	out, err := exec.Command("tmux", "display-message", "-p",
		"#{session_name}|#{window_index}").Output()
	if err != nil {
		return nil
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 2)
	if len(parts) < 2 {
		return nil
	}
	mySession, myWindow := parts[0], parts[1]

	panes, err := listTmuxPanes()
	if err != nil {
		return nil
	}

	var paths []string
	for _, p := range panes {
		if p.Session != mySession || p.Window != myWindow {
			continue
		}
		if !hasClaude(p.PID) {
			continue
		}
		absPath, _ := filepath.Abs(p.Path)
		if absPath != "" {
			paths = append(paths, absPath)
		}
	}
	return paths
}

func switchToTmuxPane(p tmuxPane) error {
	target := p.Session + ":" + p.Window + "." + p.Pane
	// Select the window first (in case it's in a different tmux window)
	exec.Command("tmux", "select-window", "-t", p.Session+":"+p.Window).Run()
	return exec.Command("tmux", "select-pane", "-t", target).Run()
}

// tmuxNewWindowClaude creates a new tmux window with the given name,
// cd's to dir, and runs "claude --resume <sessionID>".
func tmuxNewWindowClaude(windowName, dir, sessionID string) error {
	cmd := "cd " + shellQuote(dir) + " && claude --resume " + sessionID
	return exec.Command("tmux", "new-window", "-d",
		"-n", windowName, cmd).Run()
}

// tmuxSendKeys sends text input to a tmux pane followed by Enter to submit.
// Uses -l for the text (literal, no key-name interpretation) then a separate
// send-keys for Enter so it's treated as a keypress.
func tmuxSendKeys(p tmuxPane, keys string) error {
	target := p.Session + ":" + p.Window + "." + p.Pane
	// Send text literally (no special key interpretation)
	if err := exec.Command("tmux", "send-keys", "-l", "-t", target, keys).Run(); err != nil {
		return err
	}
	// Send Enter as a key to submit
	return exec.Command("tmux", "send-keys", "-t", target, "Enter").Run()
}

// tmuxPromptAndSend opens a tmux command-prompt that sends the typed text
// to the target pane followed by Enter so Claude submits. This works while CSB keeps running.
func tmuxPromptAndSend(p tmuxPane, promptText string) error {
	target := p.Session + ":" + p.Window + "." + p.Pane
	// Use -l for literal text, then a separate send-keys for Enter.
	// tmux command-prompt runs the command string in tmux's command mode.
	sendCmd := "send-keys -l -t " + target + " '%1' \\; send-keys -t " + target + " Enter"
	return exec.Command("tmux", "command-prompt", "-p", promptText, sendCmd).Run()
}

// tmuxCapturePane captures the visible content of a tmux pane.
// Does NOT include scrollback — use tmux copy mode to scroll back.
func tmuxCapturePane(p tmuxPane) (string, error) {
	target := p.Session + ":" + p.Window + "." + p.Pane
	out, err := exec.Command("tmux", "capture-pane", "-e", "-p", "-t", target).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// tmuxCopyModeScroll enters tmux copy mode (if not already in it) and scrolls.
// direction: "page-up", "page-down", "halfpage-up", "halfpage-down",
// "scroll-up", "scroll-down", "cancel" (exit copy mode).
func tmuxCopyModeScroll(p tmuxPane, direction string) {
	target := p.Session + ":" + p.Window + "." + p.Pane
	if direction == "cancel" {
		exec.Command("tmux", "send-keys", "-t", target, "-X", "cancel").Run()
		return
	}
	// Enter copy mode (idempotent if already in it)
	exec.Command("tmux", "copy-mode", "-t", target).Run()
	exec.Command("tmux", "send-keys", "-t", target, "-X", direction).Run()
}



// teaKeyToTmux maps a Bubble Tea key string to tmux send-keys argument(s).
// Returns (tmuxKey, literal). If literal is true, use send-keys -l.
func teaKeyToTmux(key string) (string, bool) {
	switch key {
	case "enter":
		return "Enter", false
	case "backspace":
		return "BSpace", false
	case "tab":
		return "Tab", false
	case "space", " ":
		return "Space", false
	case "up":
		return "Up", false
	case "down":
		return "Down", false
	case "left":
		return "Left", false
	case "right":
		return "Right", false
	case "home":
		return "Home", false
	case "end":
		return "End", false
	case "pgup":
		return "PageUp", false
	case "pgdown":
		return "PageDown", false
	case "delete":
		return "DC", false
	case "esc":
		return "Escape", false
	case "ctrl+c":
		return "C-c", false
	case "ctrl+d":
		return "C-d", false
	case "ctrl+z":
		return "C-z", false
	case "ctrl+l":
		return "C-l", false
	case "ctrl+a":
		return "C-a", false
	case "ctrl+e":
		return "C-e", false
	case "ctrl+r":
		return "C-r", false
	case "ctrl+w":
		return "C-w", false
	case "ctrl+u":
		return "C-u", false
	case "ctrl+k":
		return "C-k", false
	case "ctrl+p":
		return "C-p", false
	case "ctrl+n":
		return "C-n", false
	default:
		// Single printable rune → send literally
		if len(key) == 1 {
			return key, true
		}
		return "", false
	}
}

// tmuxSendSingleKey sends a single key event to a tmux pane.
func tmuxSendSingleKey(p tmuxPane, key string) error {
	tmuxKey, literal := teaKeyToTmux(key)
	if tmuxKey == "" {
		return nil // unsupported key, ignore
	}
	target := p.Session + ":" + p.Window + "." + p.Pane
	if literal {
		return exec.Command("tmux", "send-keys", "-l", "-t", target, tmuxKey).Run()
	}
	return exec.Command("tmux", "send-keys", "-t", target, tmuxKey).Run()
}

// tmuxKillWindow kills the tmux window containing the given pane.
func tmuxKillWindow(p tmuxPane) error {
	target := p.Session + ":" + p.Window
	return exec.Command("tmux", "kill-window", "-t", target).Run()
}

// shellQuote wraps a string in single quotes for safe shell embedding.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// moveWithAndSwitchPane moves the current pane (CSB) to the target's tmux window
// as a side-by-side split, then focuses the target pane.
func moveWithAndSwitchPane(target tmuxPane) error {
	out, err := exec.Command("tmux", "display-message", "-p",
		"#{pane_id}|#{session_name}:#{window_index}").Output()
	if err != nil {
		return switchToTmuxPane(target)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 2)
	if len(parts) < 2 {
		return switchToTmuxPane(target)
	}
	myPaneID := parts[0]
	myWindow := parts[1]
	targetWindow := target.Session + ":" + target.Window

	// Select target window first
	exec.Command("tmux", "select-window", "-t", targetWindow).Run()

	// Move CSB pane to target window if in a different window
	if myWindow != targetWindow {
		exec.Command("tmux", "join-pane",
			"-s", myPaneID, "-t", target.PaneID,
			"-h", "-l", "30%").Run()
	}

	// Focus the target pane
	return exec.Command("tmux", "select-pane", "-t", target.PaneID).Run()
}
