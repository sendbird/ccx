package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/remote"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
)

// injectRemoteSessions prepends virtual remote sessions into a session list.
func (a *App) injectRemoteSessions(sessions []session.Session) []session.Session {
	remoteMap := make(map[string]session.Session)
	for _, s := range a.sessions {
		if s.IsRemote {
			remoteMap[s.RemotePodName] = s
		}
	}
	for _, s := range loadSavedRemoteSessions() {
		if _, exists := remoteMap[s.RemotePodName]; !exists {
			remoteMap[s.RemotePodName] = s
		}
	}
	var result []session.Session
	for _, s := range remoteMap {
		result = append(result, s)
	}
	return append(result, sessions...)
}

// cleanupStaleRemoteSessions removes saved sessions whose pods no longer exist.
func cleanupStaleRemoteSessions() {
	saved := remote.LoadSavedSessions()
	var kept []remote.SavedSession
	for _, s := range saved {
		cfg := remote.Config{Context: s.Context, Namespace: s.Namespace}
		phase, err := remote.PodPhase(context.Background(), cfg, s.PodName)
		if err != nil {
			kept = append(kept, s)
			continue
		}
		if phase == "Running" || phase == "Pending" {
			kept = append(kept, s)
		}
	}
	if len(kept) != len(saved) {
		remote.SaveSessions(kept)
	}
}

func loadSavedRemoteSessions() []session.Session {
	saved := remote.LoadSavedSessions()
	var sessions []session.Session
	for _, s := range saved {
		sessions = append(sessions, session.Session{
			ID:              "remote-" + s.PodName,
			ShortID:         s.PodName,
			ProjectPath:     s.LocalDir,
			ProjectName:     "remote:" + s.PodName,
			ModTime:         time.Now(),
			IsRemote:        true,
			RemotePodName:   s.PodName,
			RemoteContext:   s.Context,
			RemoteNamespace: s.Namespace,
			RemoteStatus:    s.Status,
			FirstPrompt:     fmt.Sprintf("%s/%s/%s [%s]", s.Context, s.Namespace, s.PodName, s.Status),
		})
	}
	return sessions
}

// buildRemoteProgressView renders the progress panel.
func (a *App) buildRemoteProgressView(sess *remote.Session, currentStep string) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	labelStyle := lipgloss.NewStyle().Foreground(colorDim)
	valStyle := lipgloss.NewStyle().Foreground(colorAccent)

	var sb strings.Builder
	expStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Italic(true)
	sb.WriteString(titleStyle.Render("Remote Session") + " " + expStyle.Render("(experimental)") + "\n\n")
	sb.WriteString(labelStyle.Render("  Context:   ") + valStyle.Render(sess.Config.Context) + "\n")
	sb.WriteString(labelStyle.Render("  Namespace: ") + valStyle.Render(sess.Config.Namespace) + "\n")
	sb.WriteString(labelStyle.Render("  Pod:       ") + valStyle.Render(sess.PodName) + "\n")
	sb.WriteString(labelStyle.Render("  Image:     ") + valStyle.Render(sess.Config.Image) + "\n")
	if sess.Config.LocalDir != "" {
		sb.WriteString(labelStyle.Render("  Workdir:   ") + valStyle.Render(sess.Config.LocalDir) + "\n")
	}
	if sess.Config.SessionID != "" {
		sid := sess.Config.SessionID
		if len(sid) > 12 {
			sid = sid[:12]
		}
		sb.WriteString(labelStyle.Render("  Session:   ") + valStyle.Render(sid) + "\n")
	}
	sb.WriteString("\n" + titleStyle.Render("Progress") + "\n\n")
	for _, step := range a.remoteProgressSteps {
		sb.WriteString("  " + lipgloss.NewStyle().Foreground(colorAccent).Render("✓") + " " + step + "\n")
	}
	if currentStep != "" {
		sb.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Render("◉") + " " + currentStep + "\n")
	}
	return sb.String()
}

// --- Message types ---

type remoteSetupMsg struct {
	podName string
	step    remote.SetupStep
}

type remoteExecDoneMsg struct {
	podName string
	err     error
}

// mergeRemoteConfig applies defaults from config.yaml onto a runtime config.
// Runtime values take precedence over defaults.
func mergeRemoteConfig(defaults, cfg remote.Config) remote.Config {
	if cfg.Context == "" { cfg.Context = defaults.Context }
	if cfg.Namespace == "" { cfg.Namespace = defaults.Namespace }
	if cfg.Image == "" { cfg.Image = defaults.Image }
	if cfg.WorkDir == "" { cfg.WorkDir = defaults.WorkDir }
	if cfg.CPULimit == "" { cfg.CPULimit = defaults.CPULimit }
	if cfg.MemoryLimit == "" { cfg.MemoryLimit = defaults.MemoryLimit }
	if len(cfg.EnvVars) == 0 { cfg.EnvVars = defaults.EnvVars }
	if len(cfg.MirrorEnv) == 0 { cfg.MirrorEnv = defaults.MirrorEnv }
	if len(cfg.Labels) == 0 { cfg.Labels = defaults.Labels }
	if len(cfg.Tolerations) == 0 { cfg.Tolerations = defaults.Tolerations }
	if len(cfg.ClaudeArgs) == 0 { cfg.ClaudeArgs = defaults.ClaudeArgs }
	return cfg
}

// --- Actions ---

// startRemoteSession shows confirmation with context info.
func (a *App) startRemoteSession(cfg remote.Config) (tea.Model, tea.Cmd) {
	if a.remoteSession != nil {
		a.copiedMsg = "Remote session already active — :remote:stop first"
		return a, nil
	}

	// Merge config.yaml remote defaults into the config
	cfg = mergeRemoteConfig(a.remoteDefaults, cfg)
	cfg = cfg.Defaults()

	// Capture session info NOW (before user presses y and selection might change)
	if sess, ok := a.selectedSession(); ok {
		if cfg.LocalDir == "" && sess.ProjectPath != "" {
			cfg.LocalDir = sess.ProjectPath
		}
		if cfg.SessionID == "" {
			cfg.SessionID = sess.ID
			cfg.SessionFile = sess.FilePath
		}
	}

	cfgCopy := cfg
	a.confirmMsg = fmt.Sprintf("Start remote on %s/%s?", cfg.Context, cfg.Namespace)
	a.confirmAction = func() (tea.Model, tea.Cmd) {
		a.remoteConfirmCfg = &cfgCopy
		return a.confirmRemoteStart()
	}
	return a, nil
}

// confirmRemoteStart is called after user confirms with 'y'.
func (a *App) confirmRemoteStart() (tea.Model, tea.Cmd) {
	cfg := *a.remoteConfirmCfg
	a.remoteConfirmCfg = nil

	claudeDir := a.config.ClaudeDir
	projectPath := cfg.LocalDir

	sess, steps := remote.Start(cfg, claudeDir, projectPath)
	a.remoteSession = sess
	a.remoteSetupSteps = steps

	// Persist to disk
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

	// Insert virtual session
	virtualID := "remote-" + sess.PodName
	virtualSess := session.Session{
		ID:              virtualID,
		ShortID:         sess.PodName,
		ProjectPath:     cfg.LocalDir,
		ProjectName:     "remote:" + sess.PodName,
		ModTime:         time.Now(),
		IsRemote:        true,
		RemotePodName:   sess.PodName,
		RemoteContext:   sess.Config.Context,
		RemoteNamespace: sess.Config.Namespace,
		RemoteStatus:    "starting...",
		FirstPrompt:     fmt.Sprintf("%s/%s/%s", sess.Config.Context, sess.Config.Namespace, sess.PodName),
	}
	a.sessions = append([]session.Session{virtualSess}, a.sessions...)
	a.rebuildSessionList()

	// Select it
	for i, item := range a.sessionList.Items() {
		if si, ok := item.(sessionItem); ok && si.sess.ID == virtualID {
			a.sessionList.Select(i)
			break
		}
	}

	// Show progress in preview
	a.remoteProgressSteps = nil
	a.remoteContent = a.buildRemoteProgressView(sess, "Initializing...")
	a.copiedMsg = fmt.Sprintf("Remote → %s/%s/%s", sess.Config.Context, cfg.Namespace, sess.PodName)

	if !a.sessSplit.Show {
		a.sessSplit.Show = true
		contentH := max(a.height-3, 1)
		a.sessionList.SetSize(a.sessSplit.ListWidth(a.width, a.splitRatio), contentH)
	}
	a.sessSplit.CacheKey = "remote:" + virtualID
	a.sessSplit.Preview.SetContent(a.remoteContent)

	return a, readSetupStep(sess.PodName, steps)
}

func readSetupStep(podName string, steps <-chan remote.SetupStep) tea.Cmd {
	return func() tea.Msg {
		step, ok := <-steps
		if !ok {
			return remoteSetupMsg{podName: podName, step: remote.SetupStep{Done: true}}
		}
		return remoteSetupMsg{podName: podName, step: step}
	}
}

// --- Message handlers ---

func (a *App) handleRemoteSetup(msg remoteSetupMsg) (tea.Model, tea.Cmd) {
	if msg.step.Err != nil {
		errMsg := msg.step.Err.Error()
		a.copiedMsg = "Remote failed: " + errMsg
		a.updateRemoteSessionStatus(msg.podName, "failed: "+errMsg)
		// Show error in progress view
		a.remoteProgressSteps = append(a.remoteProgressSteps, "FAILED: "+errMsg)
		if a.remoteSession != nil {
			a.remoteContent = a.buildRemoteProgressView(a.remoteSession, "")
		}
		a.updateRemotePreview(msg.podName)
		return a, nil
	}

	if msg.step.Done {
		a.updateRemoteSessionStatus(msg.podName, "ready")
		a.remoteSetupSteps = nil
		a.remoteProgressSteps = append(a.remoteProgressSteps, "Ready")
		if a.remoteSession != nil {
			a.remoteContent = a.buildRemoteProgressView(a.remoteSession, "")
		}
		a.updateRemotePreview(msg.podName)
		a.copiedMsg = "Remote ready — Enter:attach  L:fetch preview"
		return a, nil
	}

	// Accumulate progress
	a.updateRemoteSessionStatus(msg.podName, msg.step.Message)
	a.remoteProgressSteps = append(a.remoteProgressSteps, msg.step.Message)
	if a.remoteSession != nil {
		a.remoteContent = a.buildRemoteProgressView(a.remoteSession, msg.step.Message)
		// Remove last (it's the "current" one, shown with ◉)
		a.remoteProgressSteps = a.remoteProgressSteps[:len(a.remoteProgressSteps)-1]
	}
	a.updateRemotePreview(msg.podName)

	if a.remoteSetupSteps != nil {
		return a, readSetupStep(msg.podName, a.remoteSetupSteps)
	}
	return a, nil
}

// openRemoteLivePreview spawns kubectl exec in a hidden tmux window and
// uses the existing pane proxy to capture it — same as local live preview.
func (a *App) openRemoteLivePreview(sess session.Session) (tea.Model, tea.Cmd) {
	if !tmux.InTmux() {
		a.copiedMsg = "Requires tmux"
		return a, nil
	}

	// Build the kubectl exec command
	var cfg remote.Config
	if a.remoteSession != nil && a.remoteSession.PodName == sess.RemotePodName {
		cfg = a.remoteSession.Config
	} else {
		for _, saved := range remote.LoadSavedSessions() {
			if saved.PodName == sess.RemotePodName {
				cfg = remote.Config{
					Context:   saved.Context,
					Namespace: saved.Namespace,
					SessionID: saved.SessionID,
					WorkDir:   saved.WorkDir,
				}
				cfg = mergeRemoteConfig(a.remoteDefaults, cfg)
				cfg = cfg.Defaults()
				break
			}
		}
	}
	if cfg.Context == "" {
		a.copiedMsg = "No config for remote session"
		return a, nil
	}

	// Close existing pane proxy
	a.closePaneProxy()

	// Build the shell command for the hidden tmux window (runs as non-root claude user)
	claudeCmd := remote.BuildClaudeCmd(cfg, false)
	kubectlCmd := fmt.Sprintf(
		"kubectl --context=%s -n %s exec -it %s -- su - claude -c 'export PATH=/usr/local/bin:/usr/bin:/bin:$PATH; . ~/.claude_env; cd %s 2>/dev/null; %s'",
		cfg.Context, cfg.Namespace, sess.RemotePodName, cfg.WorkDir, claudeCmd)

	windowName := "ccx-remote-" + sess.RemotePodName[:min(8, len(sess.RemotePodName))]
	a.copiedMsg = fmt.Sprintf("Spawning live → %s/%s...", cfg.Context, sess.RemotePodName)
	pane, err := tmux.SpawnHiddenWindow(windowName, kubectlCmd)
	if err != nil {
		a.copiedMsg = "Spawn failed: " + err.Error()
		return a, nil
	}

	// Use existing pane proxy infrastructure
	pane.Path = sess.ProjectPath
	a.paneProxy = &paneProxyState{pane: pane, sessID: sess.ID, isShell: true}
	a.toggleSessionPreviewMode(sessPreviewLive)
	a.refreshLivePreview()
	return a, liveTickCmd()
}

// remoteFetchMsg carries fetched JSONL data from the pod.
type remoteFetchMsg struct {
	podName string
	data    []byte
	err     error
}

// fetchRemotePreview triggers an async download of the session JSONL from the pod.
func (a *App) fetchRemotePreview(sess session.Session) (tea.Model, tea.Cmd) {
	if !sess.IsRemote {
		return a, nil
	}

	// Find config for this pod
	var cfg remote.Config
	if a.remoteSession != nil && a.remoteSession.PodName == sess.RemotePodName {
		cfg = a.remoteSession.Config
	} else {
		for _, saved := range remote.LoadSavedSessions() {
			if saved.PodName == sess.RemotePodName {
				cfg = remote.Config{
					Context:   saved.Context,
					Namespace: saved.Namespace,
					WorkDir:   saved.WorkDir,
				}
				break
			}
		}
	}
	if cfg.Context == "" {
		a.copiedMsg = "No config found for remote session"
		return a, nil
	}

	podName := sess.RemotePodName
	a.copiedMsg = "Fetching session from pod..."
	return a, func() tea.Msg {
		data, err := remote.FetchSessionJSONL(cfg, podName)
		return remoteFetchMsg{podName: podName, data: data, err: err}
	}
}

// handleRemoteFetch processes the fetched JSONL and enables normal preview.
func (a *App) handleRemoteFetch(msg remoteFetchMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		a.copiedMsg = "Fetch failed: " + msg.err.Error()
		return a, nil
	}

	// Write to temp file
	if a.remoteJSONLFile != nil {
		a.remoteJSONLFile.Close()
		os.Remove(a.remoteJSONLFile.Name())
	}
	tmpFile, err := os.CreateTemp("", "ccx-remote-*.jsonl")
	if err != nil {
		a.copiedMsg = "Temp file failed"
		return a, nil
	}
	tmpFile.Write(msg.data)
	tmpFile.Sync()
	a.remoteJSONLFile = tmpFile

	// Update virtual session's FilePath
	for i := range a.sessions {
		if a.sessions[i].IsRemote && a.sessions[i].RemotePodName == msg.podName {
			a.sessions[i].FilePath = tmpFile.Name()
			break
		}
	}

	a.remoteStreaming = true
	a.sessSplit.CacheKey = ""
	a.sessConvCacheID = ""
	a.copiedMsg = fmt.Sprintf("Loaded %d bytes from pod", len(msg.data))
	return a, nil
}

func (a *App) handleRemoteExecDone(msg remoteExecDoneMsg) (tea.Model, tea.Cmd) {
	a.copiedMsg = "Detached from remote — pod still running"
	return a, nil
}

// updateRemotePreview invalidates cache so the render path picks up new content.
func (a *App) updateRemotePreview(podName string) {
	if a.sessSplit.Show {
		if sess, ok := a.selectedSession(); ok && sess.IsRemote && sess.RemotePodName == podName {
			// Invalidate cache — updateSessionPreview will re-set content
			a.sessSplit.CacheKey = ""
		}
	}
}

// updateRemoteSessionStatus updates both in-memory and on-disk status.
func (a *App) updateRemoteSessionStatus(podName, status string) {
	for i := range a.sessions {
		s := &a.sessions[i]
		if s.IsRemote && s.RemotePodName == podName {
			s.RemoteStatus = status
			s.FirstPrompt = fmt.Sprintf("%s/%s/%s [%s]", s.RemoteContext, s.RemoteNamespace, podName, status)
			break
		}
	}
	remote.UpdateSavedSessionStatus(podName, status)
}

// --- Stop / Attach ---

func (a *App) stopRemoteSession() (tea.Model, tea.Cmd) {
	var podName string

	if a.remoteSession != nil {
		podName = a.remoteSession.PodName
		a.remoteSession.Stop()
		a.remoteSession = nil
		a.remoteContent = ""
		a.remoteProgressSteps = nil
		a.remoteStreaming = false
		if a.remoteJSONLFile != nil {
			name := a.remoteJSONLFile.Name()
			a.remoteJSONLFile.Close()
			os.Remove(name)
			a.remoteJSONLFile = nil
		}
	} else if sess, ok := a.selectedSession(); ok && sess.IsRemote {
		podName = sess.RemotePodName
		for _, saved := range remote.LoadSavedSessions() {
			if saved.PodName == podName {
				cfg := remote.Config{Context: saved.Context, Namespace: saved.Namespace}
				remote.DeletePod(context.Background(), cfg, podName)
				break
			}
		}
	} else {
		a.copiedMsg = "No remote session selected"
		return a, nil
	}

	remote.RemoveSavedSession(podName)

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

func (a *App) reconnectRemoteSession() (tea.Model, tea.Cmd) {
	// Try active session first
	if a.remoteSession != nil {
		return a.attachToRemoteSession(session.Session{
			IsRemote:      true,
			RemotePodName: a.remoteSession.PodName,
		})
	}
	// Try selected session
	if sess, ok := a.selectedSession(); ok && sess.IsRemote {
		return a.attachToRemoteSession(sess)
	}
	// Try any saved remote
	saved := remote.LoadSavedSessions()
	if len(saved) > 0 {
		return a.attachToRemoteSession(session.Session{
			IsRemote:      true,
			RemotePodName: saved[0].PodName,
		})
	}
	a.copiedMsg = "No remote session found"
	return a, nil
}

// attachToRemoteSession opens interactive Claude on the remote pod.
// Works for both active sessions and saved/restored ones.
func (a *App) attachToRemoteSession(sess session.Session) (tea.Model, tea.Cmd) {
	if !sess.IsRemote {
		return a, nil
	}

	// Active session — use its config directly
	if a.remoteSession != nil && a.remoteSession.PodName == sess.RemotePodName {
		cmd := a.remoteSession.AttachCmd()
		podName := a.remoteSession.PodName
		return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
			return remoteExecDoneMsg{podName: podName, err: err}
		})
	}

	// Saved session — build exec command from saved config
	for _, saved := range remote.LoadSavedSessions() {
		if saved.PodName == sess.RemotePodName {
			cfg := remote.Config{
				Context:   saved.Context,
				Namespace: saved.Namespace,
				SessionID: saved.SessionID,
				WorkDir:   saved.WorkDir,
			}
			cmd := remote.BuildAttachCmd(cfg, saved.PodName)
			podName := saved.PodName
			return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
				return remoteExecDoneMsg{podName: podName, err: err}
			})
		}
	}

	a.copiedMsg = "Remote pod not found in saved sessions"
	return a, nil
}

// executeCmdRemoteStart handles "remote:start [prompt...]".
func (a *App) executeCmdRemoteStart(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	var cfg remote.Config

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
