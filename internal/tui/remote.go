package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/remote"
)

// remoteStartedMsg is sent when a remote session has been created.
type remoteStartedMsg struct {
	session *remote.Session
	err     error
}

// remoteStreamMsg carries a line from the remote session stream.
type remoteStreamMsg struct {
	line []byte
	err  error
	done bool
}

// startRemoteSession begins the async remote session creation.
func (a *App) startRemoteSession(cfg remote.Config) (tea.Model, tea.Cmd) {
	if a.remoteSession != nil {
		a.copiedMsg = "Remote session already active"
		return a, nil
	}

	a.copiedMsg = "Starting remote session..."
	claudeDir := a.config.ClaudeDir
	var projectPath string
	if sess, ok := a.selectedSession(); ok {
		projectPath = sess.ProjectPath
	}

	return a, func() tea.Msg {
		sess, err := remote.Start(cfg, claudeDir, projectPath)
		return remoteStartedMsg{session: sess, err: err}
	}
}

// handleRemoteStarted processes the result of starting a remote session.
func (a *App) handleRemoteStarted(msg remoteStartedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		a.copiedMsg = "Remote failed: " + msg.err.Error()
		return a, nil
	}

	a.remoteSession = msg.session
	a.remoteContent = dimStyle.Render(fmt.Sprintf("Connected to pod %s", msg.session.PodName))
	a.remoteLines = 0
	a.copiedMsg = fmt.Sprintf("Remote → %s", msg.session.PodName)

	// Start reading the stream
	return a, a.readRemoteStream()
}

// readRemoteStream returns a command that reads the next line from the stream.
func (a *App) readRemoteStream() tea.Cmd {
	if a.remoteSession == nil {
		return nil
	}
	stream := a.remoteSession.Stream
	return func() tea.Msg {
		line, ok := <-stream
		if !ok {
			return remoteStreamMsg{done: true}
		}
		return remoteStreamMsg{
			line: line.Line,
			err:  line.Err,
			done: line.Done,
		}
	}
}

// handleRemoteStream processes a line from the remote session stream.
func (a *App) handleRemoteStream(msg remoteStreamMsg) (tea.Model, tea.Cmd) {
	if msg.done || msg.err != nil {
		label := "Remote session ended"
		if msg.err != nil {
			label = "Remote error: " + msg.err.Error()
		}
		a.copiedMsg = label
		if a.remoteSession != nil {
			a.remoteSession.Stop()
			a.remoteSession = nil
		}
		return a, nil
	}

	// Append line to rendered content
	a.remoteLines++
	line := strings.TrimSpace(string(msg.line))
	if line != "" {
		if a.remoteContent == "" {
			a.remoteContent = line
		} else {
			a.remoteContent += "\n" + line
		}
	}

	// Update preview if showing remote
	if a.sessSplit.Show {
		a.sessSplit.Preview.SetContent(a.remoteContent)
		a.sessSplit.Preview.GotoBottom()
	}

	// Continue reading
	return a, a.readRemoteStream()
}

// stopRemoteSession stops the active remote session and cleans up the pod.
func (a *App) stopRemoteSession() (tea.Model, tea.Cmd) {
	if a.remoteSession == nil {
		a.copiedMsg = "No active remote session"
		return a, nil
	}

	podName := a.remoteSession.PodName
	a.remoteSession.Stop()
	a.remoteSession = nil
	a.remoteContent = ""
	a.remoteLines = 0
	a.copiedMsg = fmt.Sprintf("Stopped remote → %s", podName)
	return a, nil
}

// executeCmdRemoteStart handles "remote:start <context> <repo> [branch]".
func (a *App) executeCmdRemoteStart(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	// remote:start <context> <repo> [branch] [prompt...]
	if len(parts) < 3 {
		a.copiedMsg = "Usage: remote:start <context> <repo> [branch]"
		return a, nil
	}

	cfg := remote.Config{
		Context:   parts[1],
		Namespace: "default",
		GitRepo:   parts[2],
	}
	if len(parts) >= 4 {
		cfg.GitBranch = parts[3]
	}
	if len(parts) >= 5 {
		cfg.Prompt = strings.Join(parts[4:], " ")
	}

	return a.startRemoteSession(cfg)
}
