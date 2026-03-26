package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/remote"
)

// remoteSetupMsg carries progress updates during remote pod setup.
type remoteSetupMsg struct {
	progress string // status message
	done     bool   // setup complete
	session  *remote.Session
	err      error
}

// remoteExecDoneMsg is sent when the interactive Claude session ends.
type remoteExecDoneMsg struct {
	podName string
	err     error
}

// startRemoteSession begins async pod setup with progress reporting.
func (a *App) startRemoteSession(cfg remote.Config) (tea.Model, tea.Cmd) {
	if a.remoteSession != nil {
		a.copiedMsg = "Remote session already active — :remote:stop first"
		return a, nil
	}

	a.remoteContent = dimStyle.Render("Starting remote session...")
	claudeDir := a.config.ClaudeDir
	var projectPath string
	if sess, ok := a.selectedSession(); ok {
		projectPath = sess.ProjectPath
	}

	return a, func() tea.Msg {
		var lastProgress string
		progress := func(msg string) {
			lastProgress = msg
		}

		sess, err := remote.Start(cfg, claudeDir, projectPath, progress)
		if err != nil {
			return remoteSetupMsg{err: err, progress: lastProgress}
		}
		return remoteSetupMsg{done: true, session: sess, progress: lastProgress}
	}
}

// handleRemoteSetup processes setup progress and completion.
func (a *App) handleRemoteSetup(msg remoteSetupMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		a.copiedMsg = "Remote failed: " + msg.err.Error()
		a.remoteContent = ""
		return a, nil
	}

	if msg.done {
		a.remoteSession = msg.session
		a.copiedMsg = fmt.Sprintf("Remote ready — %s", msg.session.PodName)

		// Exec interactive Claude — takes over the terminal
		cmd := msg.session.ClaudeCmd()
		podName := msg.session.PodName
		return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
			return remoteExecDoneMsg{podName: podName, err: err}
		})
	}

	// Progress update
	a.remoteContent = dimStyle.Render(msg.progress)
	return a, nil
}

// handleRemoteExecDone is called when the interactive Claude exits.
func (a *App) handleRemoteExecDone(msg remoteExecDoneMsg) (tea.Model, tea.Cmd) {
	a.copiedMsg = fmt.Sprintf("Remote session ended — pod %s still running", msg.podName)
	// Don't auto-delete — user may want to reconnect or inspect
	return a, nil
}

// stopRemoteSession stops the active remote session and deletes the pod.
func (a *App) stopRemoteSession() (tea.Model, tea.Cmd) {
	if a.remoteSession == nil {
		a.copiedMsg = "No active remote session"
		return a, nil
	}

	podName := a.remoteSession.PodName
	a.remoteSession.Stop()
	a.remoteSession = nil
	a.remoteContent = ""
	a.copiedMsg = fmt.Sprintf("Stopped and deleted pod %s", podName)
	return a, nil
}

// reconnectRemoteSession reattaches to the pod's Claude.
func (a *App) reconnectRemoteSession() (tea.Model, tea.Cmd) {
	if a.remoteSession == nil {
		a.copiedMsg = "No active remote session"
		return a, nil
	}

	cmd := a.remoteSession.ClaudeCmd()
	podName := a.remoteSession.PodName
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return remoteExecDoneMsg{podName: podName, err: err}
	})
}

// executeCmdRemoteStart handles "remote:start [prompt...]".
func (a *App) executeCmdRemoteStart(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)

	var cfg remote.Config

	// Use selected session for workdir and resume
	if sess, ok := a.selectedSession(); ok {
		if sess.ProjectPath != "" {
			cfg.LocalDir = sess.ProjectPath
		}
		cfg.SessionID = sess.ID
		cfg.SessionFile = sess.FilePath
	}

	// Everything after command name is the prompt
	if len(parts) >= 2 {
		cfg.Prompt = strings.Join(parts[1:], " ")
	}

	return a.startRemoteSession(cfg)
}
