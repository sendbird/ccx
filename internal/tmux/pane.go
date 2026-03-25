package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Pane represents a tmux pane with its metadata.
type Pane struct {
	PaneID     string
	Command    string
	Session    string
	Window     string
	WindowName string
	Pane       string
	PID        int
	Path       string
}

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

// ListPanes returns all tmux panes across all sessions and windows.
func ListPanes() ([]Pane, error) {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{pane_id}|#{pane_current_command}|#{session_name}|#{window_index}|#{pane_index}|#{pane_pid}|#{pane_current_path}|#{window_name}",
	).Output()
	if err != nil {
		return nil, err
	}

	var panes []Pane
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 8)
		if len(parts) < 7 {
			continue
		}
		pid, _ := strconv.Atoi(parts[5])
		p := Pane{
			PaneID:  parts[0],
			Command: parts[1],
			Session: parts[2],
			Window:  parts[3],
			Pane:    parts[4],
			PID:     pid,
			Path:    parts[6],
		}
		if len(parts) >= 8 {
			p.WindowName = parts[7]
		}
		panes = append(panes, p)
	}
	return panes, nil
}

// HasClaudeSession checks if a pane's shell has a claude child process
// running the specific session ID (matches --resume <id> in args).
func HasClaudeSession(shellPID int, sessionID string) bool {
	if shellPID == 0 || sessionID == "" {
		return false
	}
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(shellPID), "-af", "claude").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), sessionID)
}

// HasClaude checks if a pane's shell has a claude child process.
func HasClaude(shellPID int) bool {
	if shellPID == 0 {
		return false
	}
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(shellPID), "-f", "claude").Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// SwitchToPane switches the tmux client to the given pane.
func SwitchToPane(p Pane) error {
	target := p.Session + ":" + p.Window + "." + p.Pane

	// If the pane is in a different tmux session, switch the client first.
	out, _ := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output()
	currentSession := strings.TrimSpace(string(out))
	if currentSession != "" && currentSession != p.Session {
		exec.Command("tmux", "switch-client", "-t", p.Session).Run()
	}

	// Select the window (in case it's in a different tmux window)
	exec.Command("tmux", "select-window", "-t", p.Session+":"+p.Window).Run()
	return exec.Command("tmux", "select-pane", "-t", target).Run()
}

// TeaKeyToTmux maps a Bubble Tea key string to tmux send-keys argument(s).
// Returns (tmuxKey, literal). If literal is true, use send-keys -l.
func TeaKeyToTmux(key string) (string, bool) {
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

// SendSingleKey sends a single key event to a tmux pane.
func SendSingleKey(p Pane, key string) error {
	tmuxKey, literal := TeaKeyToTmux(key)
	if tmuxKey == "" {
		return nil // unsupported key, ignore
	}
	target := p.Session + ":" + p.Window + "." + p.Pane
	if literal {
		return exec.Command("tmux", "send-keys", "-l", "-t", target, tmuxKey).Run()
	}
	return exec.Command("tmux", "send-keys", "-t", target, tmuxKey).Run()
}

// SendKeys sends text input to a tmux pane followed by Enter to submit.
// Uses -l for the text (literal, no key-name interpretation) then a separate
// send-keys for Enter so it's treated as a keypress.
func SendKeys(p Pane, keys string) error {
	target := p.Session + ":" + p.Window + "." + p.Pane
	// Send text literally (no special key interpretation)
	if err := exec.Command("tmux", "send-keys", "-l", "-t", target, keys).Run(); err != nil {
		return err
	}
	// Send Enter as a key to submit
	return exec.Command("tmux", "send-keys", "-t", target, "Enter").Run()
}

// CapturePane captures the visible content of a tmux pane.
// Does NOT include scrollback.
func CapturePane(p Pane) (string, error) {
	target := p.Session + ":" + p.Window + "." + p.Pane
	out, err := exec.Command("tmux", "capture-pane", "-e", "-p", "-t", target).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// NewWindowClaude creates a new tmux window with the given name,
// cd's to dir, and runs "claude --resume <sessionID>".
func NewWindowClaude(windowName, dir, sessionID string) error {
	cmd := "cd " + ShellQuote(dir) + " && claude --resume " + sessionID
	return exec.Command("tmux", "new-window", "-d",
		"-n", windowName, cmd).Run()
}

// NewWindowClaudeNew creates a new tmux window with the given name,
// cd's to dir, and runs "claude" (without --resume, starting a fresh session).
func NewWindowClaudeNew(windowName, dir string) error {
	cmd := "cd " + ShellQuote(dir) + " && claude"
	return exec.Command("tmux", "new-window", "-d",
		"-n", windowName, cmd).Run()
}

// PaneCursorCol returns the cursor column position in the pane.
func PaneCursorCol(p Pane) (int, error) {
	target := p.Session + ":" + p.Window + "." + p.Pane
	out, err := exec.Command("tmux", "display-message", "-t", target, "-p", "#{cursor_x}").Output()
	if err != nil {
		return -1, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	return n, err
}

// KillWindow kills the tmux window containing the given pane.
func KillWindow(p Pane) error {
	target := p.Session + ":" + p.Window
	return exec.Command("tmux", "kill-window", "-t", target).Run()
}

// CurrentWindowClaudes returns project paths of Claude processes in the current tmux window.
func CurrentWindowClaudes() []string {
	if !InTmux() {
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

	panes, err := ListPanes()
	if err != nil {
		return nil
	}

	var paths []string
	for _, p := range panes {
		if p.Session != mySession || p.Window != myWindow {
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

// MoveWithAndSwitchPane moves the current pane (CSB) to the target's tmux window
// as a side-by-side split, then focuses the target pane.
func MoveWithAndSwitchPane(target Pane) error {
	out, err := exec.Command("tmux", "display-message", "-p",
		"#{pane_id}|#{session_name}:#{window_index}").Output()
	if err != nil {
		return SwitchToPane(target)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 2)
	if len(parts) < 2 {
		return SwitchToPane(target)
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

// PromptAndSend opens a tmux command-prompt that sends the typed text
// to the target pane followed by Enter so Claude submits.
func PromptAndSend(p Pane, promptText string) error {
	target := p.Session + ":" + p.Window + "." + p.Pane
	// Use -l for literal text, then a separate send-keys for Enter.
	// tmux command-prompt runs the command string in tmux's command mode.
	sendCmd := "send-keys -l -t " + target + " '%1' \\; send-keys -t " + target + " Enter"
	return exec.Command("tmux", "command-prompt", "-p", promptText, sendCmd).Run()
}

// ShellQuote wraps a string in single quotes for safe shell embedding.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
