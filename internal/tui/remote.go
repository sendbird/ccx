package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/remote"
	"github.com/sendbird/ccx/internal/session"
)

// injectRemoteSessions prepends virtual remote sessions into a session list,
// preserving any that exist in memory (with live status) and adding saved ones.
func (a *App) injectRemoteSessions(sessions []session.Session) []session.Session {
	// Collect current in-memory remote sessions (may have live status)
	remoteMap := make(map[string]session.Session)
	for _, s := range a.sessions {
		if s.IsRemote {
			remoteMap[s.RemotePodName] = s
		}
	}
	// Also load from disk for any not currently in memory
	for _, s := range loadSavedRemoteSessions() {
		if _, exists := remoteMap[s.RemotePodName]; !exists {
			remoteMap[s.RemotePodName] = s
		}
	}
	// Prepend remote sessions
	var result []session.Session
	for _, s := range remoteMap {
		result = append(result, s)
	}
	return append(result, sessions...)
}

// loadSavedRemoteSessions restores persisted remote sessions as virtual items.
func loadSavedRemoteSessions() []session.Session {
	saved := remote.LoadSavedSessions()
	var sessions []session.Session
	for _, s := range saved {
		sessions = append(sessions, session.Session{
			ID:            "remote-" + s.PodName,
			ShortID:       s.PodName,
			ProjectPath:   s.LocalDir,
			ProjectName:   "remote:" + s.PodName,
			ModTime:       time.Now(),
			IsRemote:      true,
			RemotePodName: s.PodName,
			RemoteStatus:  s.Status,
			FirstPrompt:   "Remote: " + s.Status,
		})
	}
	return sessions
}

// buildRemoteProgressView renders the progress panel for a remote session.
func (a *App) buildRemoteProgressView(sess *remote.Session, currentStep string) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	labelStyle := lipgloss.NewStyle().Foreground(colorDim)
	valStyle := lipgloss.NewStyle().Foreground(colorAccent)

	var sb strings.Builder

	sb.WriteString(titleStyle.Render("Remote Session") + "\n\n")

	// Cluster info
	sb.WriteString(labelStyle.Render("  Context:   ") + valStyle.Render(sess.Config.Context) + "\n")
	sb.WriteString(labelStyle.Render("  Namespace: ") + valStyle.Render(sess.Config.Namespace) + "\n")
	sb.WriteString(labelStyle.Render("  Pod:       ") + valStyle.Render(sess.PodName) + "\n")
	sb.WriteString(labelStyle.Render("  Image:     ") + valStyle.Render(sess.Config.Image) + "\n")
	if sess.Config.LocalDir != "" {
		sb.WriteString(labelStyle.Render("  Workdir:   ") + valStyle.Render(sess.Config.LocalDir) + "\n")
	}
	if sess.Config.SessionID != "" {
		sb.WriteString(labelStyle.Render("  Session:   ") + valStyle.Render(sess.Config.SessionID[:min(12, len(sess.Config.SessionID))]) + "\n")
	}
	sb.WriteString("\n")

	// Progress steps
	sb.WriteString(titleStyle.Render("Progress") + "\n\n")
	for _, step := range a.remoteProgressSteps {
		sb.WriteString("  " + lipgloss.NewStyle().Foreground(colorAccent).Render("✓") + " " + step + "\n")
	}
	if currentStep != "" {
		sb.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Render("◉") + " " + currentStep + "\n")
	}

	return sb.String()
}

// remoteSetupMsg carries a setup progress step.
type remoteSetupMsg struct {
	podName string
	step    remote.SetupStep
}

// remoteStreamMsg carries a line from the running Claude.
type remoteStreamMsg struct {
	podName string
	line    []byte
	err     error
	done    bool
}

// remoteExecDoneMsg is sent when interactive attach ends.
type remoteExecDoneMsg struct {
	podName string
	err     error
}

// startRemoteSession creates a remote pod and inserts a virtual session.
func (a *App) startRemoteSession(cfg remote.Config) (tea.Model, tea.Cmd) {
	if a.remoteSession != nil {
		a.copiedMsg = "Remote session already active — :remote:stop first"
		return a, nil
	}

	claudeDir := a.config.ClaudeDir
	var projectPath string
	if sess, ok := a.selectedSession(); ok {
		projectPath = sess.ProjectPath
	}

	// Start async setup
	sess, steps := remote.Start(cfg, claudeDir, projectPath)
	a.remoteSession = sess
	a.remoteSetupSteps = steps

	// Persist remote session to disk
	remote.AddSavedSession(remote.SavedSession{
		PodName:   sess.PodName,
		Context:   sess.Config.Context,
		Namespace: sess.Config.Namespace,
		Image:     sess.Config.Image,
		LocalDir:  cfg.LocalDir,
		SessionID: cfg.SessionID,
		WorkDir:   sess.Config.WorkDir,
		Status:    "starting",
	})

	// Insert virtual session into the list
	virtualSess := session.Session{
		ID:           "remote-" + sess.PodName,
		ShortID:      sess.PodName,
		ProjectPath:  cfg.LocalDir,
		ProjectName:  "remote:" + sess.PodName,
		ModTime:      time.Now(),
		IsLive:       true,
		IsRemote:     true,
		RemotePodName: sess.PodName,
		RemoteStatus: "starting...",
		FirstPrompt:  "Remote session",
	}
	a.sessions = append([]session.Session{virtualSess}, a.sessions...)
	a.rebuildSessionList()
	a.sessionList.Select(0) // select the new virtual session

	// Initialize progress tracking
	a.remoteProgressSteps = nil
	a.remoteContent = a.buildRemoteProgressView(sess, "Initializing...")
	a.copiedMsg = fmt.Sprintf("Remote → %s/%s", cfg.Namespace, sess.PodName)

	// Open preview showing progress
	if !a.sessSplit.Show {
		a.sessSplit.Show = true
		contentH := max(a.height-3, 1)
		a.sessionList.SetSize(a.sessSplit.ListWidth(a.width, a.splitRatio), contentH)
	}
	a.sessSplit.CacheKey = ""
	a.sessSplit.Preview.SetContent(a.remoteContent)

	// Start reading setup steps
	podName := sess.PodName
	return a, readSetupStep(podName, steps)
}

// readSetupStep reads the next setup progress step.
func readSetupStep(podName string, steps <-chan remote.SetupStep) tea.Cmd {
	return func() tea.Msg {
		step, ok := <-steps
		if !ok {
			return remoteSetupMsg{podName: podName, step: remote.SetupStep{Done: true}}
		}
		return remoteSetupMsg{podName: podName, step: step}
	}
}

// handleRemoteSetup processes setup progress.
func (a *App) handleRemoteSetup(msg remoteSetupMsg) (tea.Model, tea.Cmd) {
	if msg.step.Err != nil {
		a.copiedMsg = "Remote failed: " + msg.step.Err.Error()
		a.updateRemoteSessionStatus(msg.podName, "failed: "+msg.step.Err.Error())
		return a, nil
	}

	if msg.step.Done {
		a.updateRemoteSessionStatus(msg.podName, "running")
		a.remoteSetupSteps = nil // setup finished
		a.remoteProgressSteps = append(a.remoteProgressSteps, "Claude started")
		if a.remoteSession != nil {
			a.remoteContent = a.buildRemoteProgressView(a.remoteSession, "")
		}
		a.copiedMsg = "Remote Claude running"
		// Update preview
		if a.sessSplit.Show {
			if sess, ok := a.selectedSession(); ok && sess.IsRemote {
				a.sessSplit.Preview.SetContent(a.remoteContent)
			}
		}
		// Start streaming output for live preview
		if a.remoteSession != nil && a.remoteSession.Stream != nil {
			return a, a.readRemoteStream(msg.podName)
		}
		return a, nil
	}

	// Progress update — accumulate completed steps
	a.updateRemoteSessionStatus(msg.podName, msg.step.Message)
	if len(a.remoteProgressSteps) > 0 {
		// Previous step completed, add it to done list
	}
	a.remoteProgressSteps = append(a.remoteProgressSteps, msg.step.Message)
	// Rebuild the progress view with all steps
	if a.remoteSession != nil {
		// Show last step as "current" (in progress), rest as completed
		completed := a.remoteProgressSteps[:len(a.remoteProgressSteps)-1]
		current := a.remoteProgressSteps[len(a.remoteProgressSteps)-1]
		a.remoteProgressSteps = completed
		a.remoteContent = a.buildRemoteProgressView(a.remoteSession, current)
		a.remoteProgressSteps = append(completed, current) // restore for next iteration
	}

	// Update preview if this remote session is selected
	if a.sessSplit.Show {
		if sess, ok := a.selectedSession(); ok && sess.IsRemote && sess.RemotePodName == msg.podName {
			a.sessSplit.Preview.SetContent(a.remoteContent)
			a.sessSplit.CacheKey = "remote-progress" // prevent conversation loading
		}
	}

	// Continue reading steps
	if a.remoteSetupSteps != nil {
		return a, readSetupStep(msg.podName, a.remoteSetupSteps)
	}
	return a, nil
}

// readRemoteStream reads the next line from running Claude.
func (a *App) readRemoteStream(podName string) tea.Cmd {
	if a.remoteSession == nil || a.remoteSession.Stream == nil {
		return nil
	}
	stream := a.remoteSession.Stream
	return func() tea.Msg {
		line, ok := <-stream
		if !ok {
			return remoteStreamMsg{podName: podName, done: true}
		}
		return remoteStreamMsg{
			podName: podName,
			line:    line.Line,
			err:     line.Err,
			done:    line.Done,
		}
	}
}

// handleRemoteStream processes a line from running Claude.
func (a *App) handleRemoteStream(msg remoteStreamMsg) (tea.Model, tea.Cmd) {
	if msg.done || msg.err != nil {
		a.updateRemoteSessionStatus(msg.podName, "stopped")
		if msg.err != nil {
			a.copiedMsg = "Remote stream error"
		}
		return a, nil
	}

	// Append to content
	line := strings.TrimSpace(string(msg.line))
	if line != "" {
		if a.remoteContent == "" || strings.HasPrefix(a.remoteContent, "\x1b") {
			a.remoteContent = line
		} else {
			a.remoteContent += "\n" + line
		}
	}

	// Update preview if remote session is selected
	if a.sessSplit.Show {
		if sess, ok := a.selectedSession(); ok && sess.IsRemote && sess.RemotePodName == msg.podName {
			a.sessSplit.Preview.SetContent(a.remoteContent)
			a.sessSplit.Preview.GotoBottom()
		}
	}

	return a, a.readRemoteStream(msg.podName)
}

// updateRemoteSessionStatus updates the virtual session's status in the list and on disk.
func (a *App) updateRemoteSessionStatus(podName, status string) {
	for i := range a.sessions {
		if a.sessions[i].IsRemote && a.sessions[i].RemotePodName == podName {
			a.sessions[i].RemoteStatus = status
			a.sessions[i].FirstPrompt = "Remote: " + status
			break
		}
	}
	remote.UpdateSavedSessionStatus(podName, status)
}

// stopRemoteSession stops the remote and removes the virtual session.
func (a *App) stopRemoteSession() (tea.Model, tea.Cmd) {
	if a.remoteSession == nil {
		a.copiedMsg = "No active remote session"
		return a, nil
	}

	podName := a.remoteSession.PodName
	a.remoteSession.Stop()
	a.remoteSession = nil
	a.remoteContent = ""
	a.remoteProgressSteps = nil
	remote.RemoveSavedSession(podName)

	// Remove virtual session from list
	var filtered []session.Session
	for _, s := range a.sessions {
		if !(s.IsRemote && s.RemotePodName == podName) {
			filtered = append(filtered, s)
		}
	}
	a.sessions = filtered
	a.rebuildSessionList()

	a.copiedMsg = fmt.Sprintf("Stopped pod %s", podName)
	return a, nil
}

// reconnectRemoteSession attaches interactively to the remote Claude.
func (a *App) reconnectRemoteSession() (tea.Model, tea.Cmd) {
	if a.remoteSession == nil {
		a.copiedMsg = "No active remote session"
		return a, nil
	}

	cmd := a.remoteSession.AttachCmd()
	podName := a.remoteSession.PodName
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return remoteExecDoneMsg{podName: podName, err: err}
	})
}

// handleRemoteExecDone is called when interactive attach ends.
func (a *App) handleRemoteExecDone(msg remoteExecDoneMsg) (tea.Model, tea.Cmd) {
	a.copiedMsg = "Detached from remote — pod still running"
	return a, nil
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

	if len(parts) >= 2 {
		cfg.Prompt = strings.Join(parts[1:], " ")
	}

	return a.startRemoteSession(cfg)
}
