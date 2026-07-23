package tmux

import (
	"os/exec"
	"strings"

	"github.com/sendbird/ccx/internal/claudecmd"
)

// NormalizeWindowName strips status-indicator characters from a tmux window
// name, keeping only [a-zA-Z0-9_-]. tmux decorates window names with markers
// ("*", "-", ":", …) that carry status, not identity; stripping them yields a
// stable key for matching the window a session originally used across resumes.
// Returns "" when the input has no kept characters.
func NormalizeWindowName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CurrentSessionName returns the tmux session the client is attached to, or ""
// when not inside tmux / on error.
func CurrentSessionName() string {
	out, err := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// isShell reports whether cmd is an interactive shell (pane at a prompt) rather
// than a foreground program. tmux's pane_current_command is the foreground
// process, so "bash"/"zsh"/"fish" means the pane is sitting at a prompt.
func isShell(cmd string) bool {
	switch cmd {
	case "bash", "zsh", "fish", "sh", "dash", "ksh":
		return true
	}
	return false
}

// ResumeInNamedWindow resumes sessionID in a tmux window whose name normalizes
// to normalizedName.
//
// Reuse: if a window in the current tmux session has a matching normalized name
// AND its active pane is an idle shell (no claude running), the resume command
// is sent to that pane — the session picks back up in its original window.
//
// Otherwise a new window named normalizedName is created with the resume
// command. dir is the working directory for the resumed Claude.
func ResumeInNamedWindow(normalizedName, dir, sessionID string, cfg claudecmd.Config) error {
	if normalizedName == "" {
		normalizedName = "claude"
	}
	if !InTmux() {
		// Caller should not reach here for the non-tmux path, but stay safe.
		return NewWindowClaudeWithConfig(normalizedName, dir, sessionID, cfg)
	}

	curSession := CurrentSessionName()
	panes, err := ListPanes()
	if err == nil && curSession != "" {
		for _, p := range panes {
			if p.Session != curSession {
				continue
			}
			if NormalizeWindowName(p.WindowName) != normalizedName {
				continue
			}
			// Reuse the window only when its active pane is an idle shell —
			// never clobber a pane running claude or another program.
			if !isShell(p.Command) || HasClaude(p.PID) {
				continue
			}
			if err := SwitchToPane(p); err != nil {
				continue
			}
			shellCmd, err := ClaudeWindowShellCommand(dir, cfg, "--resume", sessionID)
			if err != nil {
				return err
			}
			return SendKeys(p, shellCmd)
		}
	}

	// No reusable window: create a new one named with the normalized name.
	return NewWindowClaudeWithConfig(normalizedName, dir, sessionID, cfg)
}
