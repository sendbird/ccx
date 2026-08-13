package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/remote"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
)

func shellArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

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

// cleanupStaleRemoteSessions removes saved sessions whose remote no longer exists.
//
// Every saved remote costs a network round-trip here, and an unreachable host
// costs the whole ConnectTimeout — so this must never run on the startup path.
// cleanupStaleRemotesCmd is the way in.
func cleanupStaleRemoteSessions() {
	saved := remote.LoadSavedSessions()
	var kept []remote.SavedSession
	for _, s := range saved {
		cfg := remote.Config{Transport: s.Transport, Host: s.Host, Context: s.Context, Namespace: s.Namespace}
		t := cfg.BuildTransportForPod(s.PodName)
		status, err := t.Status(context.Background())
		if err != nil {
			// Ping error — keep the session, don't delete on transient failures.
			kept = append(kept, s)
			continue
		}
		// Keep running/pending/unreachable/unknown. Only delete explicitly
		// "notfound" (k8s pod gone) or "ended"/"failed"/"stopped".
		switch status {
		case "unreachable", "unknown", "Running", "Pending", "running":
			kept = append(kept, s)
		case "ended", "failed", "stopped", "Succeeded", "Failed":
			// Drop — the remote is gone or the session ended.
		default:
			kept = append(kept, s)
		}
	}
	if len(kept) != len(saved) {
		remote.SaveSessions(kept)
	}
}

// cleanupStaleRemotesCmd runs the staleness sweep off the startup path. It
// reports whether anything was dropped so the caller can rebuild the list only
// when the saved set actually changed.
func cleanupStaleRemotesCmd() tea.Cmd {
	return func() tea.Msg {
		before := len(remote.LoadSavedSessions())
		cleanupStaleRemoteSessions()
		after := len(remote.LoadSavedSessions())
		return remotesCleanedMsg{changed: after != before}
	}
}

// remotesCleanedMsg reports the result of the async staleness sweep.
type remotesCleanedMsg struct{ changed bool }

func loadSavedRemoteSessions() []session.Session {
	saved := remote.LoadSavedSessions()
	var sessions []session.Session
	for _, s := range saved {
		var label, projectName string
		if s.Transport == "ssh" {
			label = fmt.Sprintf("%s [%s]", s.Host, s.Status)
			projectName = "ssh:" + s.Host
		} else {
			label = fmt.Sprintf("%s/%s/%s [%s]", s.Context, s.Namespace, s.PodName, s.Status)
			projectName = "remote:" + s.PodName
		}
		sessions = append(sessions, session.Session{
			ID:              "remote-" + s.PodName,
			ShortID:         s.PodName,
			ProjectPath:     s.LocalDir,
			ProjectName:     projectName,
			ModTime:         time.Now(),
			IsRemote:        true,
			RemotePodName:   s.PodName,
			RemoteContext:   s.Context,
			RemoteNamespace: s.Namespace,
			RemoteStatus:    s.Status,
			FirstPrompt:     label,
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
	expStyle := lipgloss.NewStyle().Foreground(colorAssistant).Italic(true)
	sb.WriteString(titleStyle.Render("Remote Session") + " " + expStyle.Render("(experimental)") + "\n\n")
	if sess.Config.IsSSH() {
		sb.WriteString(labelStyle.Render("  Host:     ") + valStyle.Render(sess.Config.Host) + "\n")
		sb.WriteString(labelStyle.Render("  Target:   ") + valStyle.Render(sess.Transport.Target()) + "\n")
	} else {
		sb.WriteString(labelStyle.Render("  Context:   ") + valStyle.Render(sess.Config.Context) + "\n")
		sb.WriteString(labelStyle.Render("  Namespace: ") + valStyle.Render(sess.Config.Namespace) + "\n")
		sb.WriteString(labelStyle.Render("  Pod:       ") + valStyle.Render(sess.PodName) + "\n")
		sb.WriteString(labelStyle.Render("  Image:     ") + valStyle.Render(sess.Config.Image) + "\n")
	}
	if sess.Config.LocalDir != "" {
		sb.WriteString(labelStyle.Render("  Local dir: ") + valStyle.Render(sess.Config.LocalDir) + "\n")
	}
	if sess.Config.WorkDir != "" {
		sb.WriteString(labelStyle.Render("  Remote dir:") + " " + valStyle.Render(sess.Config.WorkDir) + "\n")
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
		sb.WriteString("  " + lipgloss.NewStyle().Foreground(colorAccent).Render(iconDone) + " " + step + "\n")
	}
	if currentStep != "" {
		sb.WriteString("  " + lipgloss.NewStyle().Foreground(colorAssistant).Render(iconActive) + " " + currentStep + "\n")
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

// remotePhaseMsg carries refreshed pod phases for saved remote sessions.
type remotePhaseMsg struct {
	phases map[string]string // pod name -> phase (Running, Pending, Failed, NotFound)
}

// remoteExecOutputMsg carries combined output of an ad-hoc kubectl exec.
type remoteExecOutputMsg struct {
	podName string
	out     []byte
	err     error
}

// remoteSnapshotMsg signals snapshot completion.
type remoteSnapshotMsg struct {
	name string
	meta remote.SnapshotMeta
	err  error
}

// remotePullMsg signals workdir write-back completion.
type remotePullMsg struct {
	podName string
	dest    string
	err     error
}

// mergeRemoteConfig applies defaults from config.yaml onto a runtime config.
// Runtime values take precedence over defaults.
func mergeRemoteConfig(defaults, cfg remote.Config) remote.Config {
	if cfg.Transport == "" {
		cfg.Transport = defaults.Transport
	}
	if cfg.Host == "" {
		cfg.Host = defaults.Host
	}
	if len(cfg.SSHExtraArgs) == 0 {
		cfg.SSHExtraArgs = defaults.SSHExtraArgs
	}
	if cfg.Context == "" {
		cfg.Context = defaults.Context
	}
	if cfg.Namespace == "" {
		cfg.Namespace = defaults.Namespace
	}
	if cfg.PodName == "" {
		cfg.PodName = defaults.PodName
	}
	if cfg.Container == "" {
		cfg.Container = defaults.Container
	}
	if cfg.RemoteUser == "" {
		cfg.RemoteUser = defaults.RemoteUser
	}
	if cfg.RemoteHome == "" {
		cfg.RemoteHome = defaults.RemoteHome
	}
	if cfg.RemoteProjectPath == "" {
		cfg.RemoteProjectPath = defaults.RemoteProjectPath
	}
	if cfg.Image == "" {
		cfg.Image = defaults.Image
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = defaults.WorkDir
	}
	if cfg.WorkDirTemplate == "" {
		cfg.WorkDirTemplate = defaults.WorkDirTemplate
	}
	if cfg.CPULimit == "" {
		cfg.CPULimit = defaults.CPULimit
	}
	if cfg.MemoryLimit == "" {
		cfg.MemoryLimit = defaults.MemoryLimit
	}
	if cfg.Arch == "" {
		cfg.Arch = defaults.Arch
	}
	if len(cfg.EnvVars) == 0 {
		cfg.EnvVars = defaults.EnvVars
	}
	if len(cfg.MirrorEnv) == 0 {
		cfg.MirrorEnv = defaults.MirrorEnv
	}
	if len(cfg.Labels) == 0 {
		cfg.Labels = defaults.Labels
	}
	if len(cfg.Tolerations) == 0 {
		cfg.Tolerations = defaults.Tolerations
	}
	if len(cfg.ClaudeArgs) == 0 {
		cfg.ClaudeArgs = defaults.ClaudeArgs
	}
	return cfg
}

func expandRemoteWorkDirTemplate(tmpl string, sess session.Session, remoteHome string) string {
	if tmpl == "" {
		return ""
	}
	project := sess.ProjectName
	if project == "" {
		project = "project"
	}
	shortSession := sess.ShortID
	if shortSession == "" {
		shortSession = sess.ID
		if len(shortSession) > 12 {
			shortSession = shortSession[:12]
		}
	}
	repls := map[string]string{
		"{{project}}":       safePathPart(project),
		"{{session}}":       safePathPart(sess.ID),
		"{{short_session}}": safePathPart(shortSession),
		"{{home_rel}}":      safeHomeRel(sess.ProjectPath),
		"{{remote_home}}":   strings.TrimRight(remoteHome, "/"),
	}
	out := tmpl
	for k, v := range repls {
		out = strings.ReplaceAll(out, k, v)
	}
	return out
}

func safePathPart(s string) string {
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	trimmed := strings.Trim(b.String(), "-.")
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func safeHomeRel(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return safePathPart(path)
	}
	if path == home {
		return ""
	}
	prefix := strings.TrimRight(home, "/") + "/"
	if strings.HasPrefix(path, prefix) {
		parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
		for i, part := range parts {
			parts[i] = safePathPart(part)
		}
		return strings.Join(parts, "/")
	}
	return safePathPart(path)
}

// --- Actions ---

// startRemoteSession shows confirmation with context info.
func (a *App) startRemoteSession(cfg remote.Config) (tea.Model, tea.Cmd) {
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
		if cfg.WorkDirTemplate != "" {
			cfg.WorkDir = expandRemoteWorkDirTemplate(cfg.WorkDirTemplate, sess, cfg.RemoteHome)
		}
	}

	if a.remoteSession != nil && cfg.PodName == "" {
		a.copiedMsg = "Remote session already active — use remote.pod_name to reuse a fixed worker pod"
		return a, nil
	}

	cfgCopy := cfg
	var prompt string
	if cfg.IsSSH() {
		prompt = fmt.Sprintf("Start remote on ssh:%s?", cfg.Host)
	} else {
		prompt = fmt.Sprintf("Start remote on %s/%s?", cfg.Context, cfg.Namespace)
	}
	if cfg.PodName != "" {
		prompt = fmt.Sprintf("Sync session to remote pod %s/%s/%s?", cfg.Context, cfg.Namespace, cfg.PodName)
	}
	if cfg.ArchMismatch() {
		prompt = fmt.Sprintf("Start remote on %s/%s? [arch %s ≠ host %s]",
			cfg.Context, cfg.Namespace, cfg.Arch, remote.HostArch())
	}
	a.confirmMsg = prompt
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
	return a.installRemoteSession(sess, steps)
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
		a.copiedMsg = "Remote ready — fetching session..."
		// Auto-fetch the remote session JSONL so the preview shows the
		// conversation content (like a normal session) instead of just the
		// static progress card.
		return a, a.autoFetchRemoteSession(msg.podName)
	}

	// Accumulate progress
	a.updateRemoteSessionStatus(msg.podName, msg.step.Message)
	a.remoteProgressSteps = append(a.remoteProgressSteps, msg.step.Message)
	if a.remoteSession != nil {
		a.remoteContent = a.buildRemoteProgressView(a.remoteSession, msg.step.Message)
		// Remove last (it's the "current" one, shown with the active icon)
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

	// Build the shell command for the hidden tmux window.
	claudeCmd := remote.BuildClaudeCmd(cfg, false)
	containerArg := ""
	if cfg.Container != "" {
		containerArg = " -c " + shellArg(cfg.Container)
	}
	kubectlCmd := fmt.Sprintf(
		"kubectl --context=%s -n %s exec -it %s%s -- su - %s -c 'export PATH=$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin:$PATH; . ~/.claude_env; cd %s 2>/dev/null; %s'",
		shellArg(cfg.Context), shellArg(cfg.Namespace), shellArg(sess.RemotePodName), containerArg, shellArg(cfg.RemoteUser), cfg.WorkDir, claudeCmd)

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
		t := cfg.BuildTransportForPod(podName)
		data, err := remote.FetchSessionJSONL(cfg, t)
		return remoteFetchMsg{podName: podName, data: data, err: err}
	}
}

// autoFetchRemoteSession returns a tea.Cmd that fetches the remote session
// JSONL so the preview can render the conversation content. Called
// automatically when the remote setup completes ("Ready").
func (a *App) autoFetchRemoteSession(podName string) tea.Cmd {
	return func() tea.Msg {
		if a.remoteSession == nil {
			return remoteFetchMsg{podName: podName, err: fmt.Errorf("no active remote session")}
		}
		cfg := a.remoteSession.Config
		t := cfg.BuildTransportForPod(podName)
		if cfg.IsSSH() {
			t = a.remoteSession.Transport
		}
		data, err := remote.FetchSessionJSONL(cfg, t)
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

	// Also materialize the JSONL into the local ~/.claude/projects/ tree so
	// the session can be resumed locally after the remote is stopped. The
	// file is placed under the LOCAL project path's encoded directory (not
	// the remote one) so a local `claude --resume <id>` finds it.
	if a.remoteSession != nil {
		localProject := a.remoteSession.Config.LocalDir
		if localProject != "" {
			encoded := session.EncodeProjectPath(localProject)
			home := homeDir()
			projDir := filepath.Join(home, ".claude", "projects", encoded)
			os.MkdirAll(projDir, 0o755)
			sessFile := filepath.Join(projDir, filepath.Base(tmpFile.Name())+".jsonl")
			// Use the session ID from the virtual session if available.
			for i := range a.sessions {
				if a.sessions[i].IsRemote && a.sessions[i].RemotePodName == msg.podName {
					if a.sessions[i].ID != "" {
						sessFile = filepath.Join(projDir, a.sessions[i].ID+".jsonl")
					}
					break
				}
			}
			os.WriteFile(sessFile, msg.data, 0o644)
		}
	}

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
	if msg.err != nil {
		a.copiedMsg = "Remote attach error: " + msg.err.Error()
	} else {
		a.copiedMsg = "Detached from remote — session still running"
	}
	// Re-fetch the session JSONL after detach so the preview shows the latest
	// conversation content (everything Claude did while attached).
	if a.remoteSession != nil && a.remoteSession.PodName == msg.podName {
		return a, a.autoFetchRemoteSession(msg.podName)
	}
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
	return a.stopRemoteSessionInternal(false)
}

// stopRemoteSessionWithPull pulls the workdir back to LocalDir before deleting
// the pod. The pull is best-effort — stop proceeds even if it fails.
func (a *App) stopRemoteSessionWithPull() (tea.Model, tea.Cmd) {
	return a.stopRemoteSessionInternal(true)
}

func (a *App) stopRemoteSessionInternal(pull bool) (tea.Model, tea.Cmd) {
	var podName string
	var pullCfg remote.Config
	var pullDest string

	if a.remoteSession != nil {
		podName = a.remoteSession.PodName
		if pull {
			pullCfg = a.remoteSession.Config
			pullDest = a.remoteSession.Config.LocalDir
		}
		// Pull works for both k8s and SSH — FetchWorkdirToDir uses the
		// transport from pullCfg. Guard on pullDest (not Context) so SSH
		// sessions with a LocalDir can pull too.
		if pull && pullDest != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			if err := remote.FetchWorkdirToDirWithTransport(ctx, pullCfg, a.remoteSession.Transport, pullDest); err != nil {
				a.copiedMsg = "Pull failed, stopping anyway: " + err.Error()
			} else {
				a.copiedMsg = fmt.Sprintf("Pulled workdir → %s, stopped %s", pullDest, podName)
			}
			cancel()
		}
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
				cfg := remote.Config{
					Transport: saved.Transport,
					Host:      saved.Host,
					Context:   saved.Context,
					Namespace: saved.Namespace,
					WorkDir:   saved.WorkDir,
					LocalDir:  saved.LocalDir,
				}
				cfg = mergeRemoteConfig(a.remoteDefaults, cfg)
				cfg = cfg.Defaults()
				if pull {
					pullCfg = cfg
					pullDest = saved.LocalDir
					if pullDest != "" {
						ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
						t := cfg.BuildTransportForPod(podName)
						if err := remote.FetchWorkdirToDirWithTransport(ctx, pullCfg, t, pullDest); err != nil {
							a.copiedMsg = "Pull failed, stopping anyway: " + err.Error()
						} else {
							a.copiedMsg = fmt.Sprintf("Pulled workdir → %s, stopped %s", pullDest, podName)
						}
						cancel()
					}
				}
				// Release the transport (k8s: delete pod; ssh: no-op, tmux
				// session stays alive for re-attach).
				t := cfg.BuildTransportForPod(podName)
				t.Release(context.Background())
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
	if a.copiedMsg == "" || !strings.HasPrefix(a.copiedMsg, "Pulled") {
		a.copiedMsg = fmt.Sprintf("Stopped pod %s", podName)
	}
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
				Transport: saved.Transport,
				Host:      saved.Host,
				Context:   saved.Context,
				Namespace: saved.Namespace,
				SessionID: saved.SessionID,
				WorkDir:   saved.WorkDir,
				LocalDir:  saved.LocalDir,
			}
			cfg = mergeRemoteConfig(a.remoteDefaults, cfg)
			cfg = cfg.Defaults()
			cmd := remote.BuildAttachCmd(cfg, cfg.BuildTransportForPod(saved.PodName))
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

	var promptParts []string
	for _, part := range parts[1:] {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			promptParts = append(promptParts, part)
			continue
		}
		switch strings.ToLower(key) {
		case "context", "ctx":
			cfg.Context = val
		case "namespace", "ns":
			cfg.Namespace = val
		case "pod", "pod_name", "pod-name":
			cfg.PodName = val
		case "container":
			cfg.Container = val
		case "user", "remote_user", "remote-user":
			cfg.RemoteUser = val
		case "home", "remote_home", "remote-home":
			cfg.RemoteHome = val
		case "remote_project_path", "remote-project-path", "project_path", "project-path":
			cfg.RemoteProjectPath = val
		case "workdir", "work_dir", "work-dir":
			cfg.WorkDir = val
		case "workdir_template", "work_dir_template", "work-dir-template":
			cfg.WorkDirTemplate = val
		case "transport":
			cfg.Transport = val
		case "host":
			cfg.Host = val
		default:
			promptParts = append(promptParts, part)
		}
	}
	if len(promptParts) > 0 {
		cfg.Prompt = strings.Join(promptParts, " ")
	}

	return a.startRemoteSession(cfg)
}

// pollRemotePhasesCmd returns a Cmd that queries pod phase for every saved
// remote session. The returned remotePhaseMsg drives a single batched UI
// refresh, avoiding one tea.Cmd per pod.
func pollRemotePhasesCmd() tea.Cmd {
	saved := remote.LoadSavedSessions()
	if len(saved) == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		phases := make(map[string]string, len(saved))
		for _, s := range saved {
			cfg := remote.Config{Transport: s.Transport, Host: s.Host, Context: s.Context, Namespace: s.Namespace}
			t := cfg.BuildTransportForPod(s.PodName)
			status, err := t.Status(ctx)
			if err != nil {
				// Keep on error — don't delete a session just because a ping failed.
				phases[s.PodName] = "unknown"
				continue
			}
			// "unreachable" means the host is down but the session definition
			// should survive (VPN off, laptop offline, etc.).
			if status == "unreachable" {
				phases[s.PodName] = "unreachable"
				continue
			}
			phases[s.PodName] = status
		}
		return remotePhaseMsg{phases: phases}
	}
}

// handleRemotePhase merges polled pod phases into in-memory and on-disk state.
// NotFound pods are dropped from saved sessions.
func (a *App) handleRemotePhase(msg remotePhaseMsg) (tea.Model, tea.Cmd) {
	if len(msg.phases) == 0 {
		return a, nil
	}
	changed := false
	for pod, phase := range msg.phases {
		status := strings.ToLower(phase)
		for i := range a.sessions {
			s := &a.sessions[i]
			if !(s.IsRemote && s.RemotePodName == pod) {
				continue
			}
			// Preserve in-progress setup state (e.g. "syncing config...").
			if a.remoteSession != nil && a.remoteSession.PodName == pod && a.remoteSetupSteps != nil {
				break
			}
			if s.RemoteStatus != status {
				s.RemoteStatus = status
				if s.ProjectName != "" && strings.HasPrefix(s.ProjectName, "ssh:") {
					s.FirstPrompt = fmt.Sprintf("%s [%s]", s.ProjectName[4:], status)
				} else {
					s.FirstPrompt = fmt.Sprintf("%s/%s/%s [%s]", s.RemoteContext, s.RemoteNamespace, pod, status)
				}
				changed = true
			}
			break
		}
		if phase == "NotFound" || phase == "notfound" {
			remote.RemoveSavedSession(pod)
			var filtered []session.Session
			for _, s := range a.sessions {
				if !(s.IsRemote && s.RemotePodName == pod) {
					filtered = append(filtered, s)
				}
			}
			if len(filtered) != len(a.sessions) {
				a.sessions = filtered
				changed = true
			}
		} else if phase == "unreachable" || phase == "unknown" {
			// Keep the session but update status — don't delete.
			remote.UpdateSavedSessionStatus(pod, status)
		}
	}
	if changed {
		a.rebuildSessionList()
	}
	return a, nil
}

// executeCmdRemoteExec runs an ad-hoc command in the selected remote pod and
// reports the output via copiedMsg. Long output is truncated.
func (a *App) executeCmdRemoteExec(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		a.copiedMsg = "Usage: remote:exec <command...>"
		return a, nil
	}
	cmdParts := parts[1:]

	sess, ok := a.selectedSession()
	if !ok || !sess.IsRemote {
		a.copiedMsg = "Select a remote session first"
		return a, nil
	}

	cfg, ok := a.resolveRemoteConfig(sess.RemotePodName)
	if !ok {
		a.copiedMsg = "No config for remote session"
		return a, nil
	}

	a.copiedMsg = fmt.Sprintf("Exec on %s: %s", sess.RemotePodName, strings.Join(cmdParts, " "))
	pod := sess.RemotePodName
	return a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		t := cfg.BuildTransportForPod(pod)
		out, err := t.Exec(ctx, cmdParts...)
		return remoteExecOutputMsg{podName: pod, out: out, err: err}
	}
}

// handleRemoteExecOutput renders ad-hoc exec output as a transient status line.
func (a *App) handleRemoteExecOutput(msg remoteExecOutputMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		out := strings.TrimSpace(string(msg.out))
		if out != "" {
			a.copiedMsg = fmt.Sprintf("Exec failed: %s: %s", msg.err.Error(), trimLine(out, 80))
		} else {
			a.copiedMsg = "Exec failed: " + msg.err.Error()
		}
		return a, nil
	}
	out := strings.TrimSpace(string(msg.out))
	if out == "" {
		a.copiedMsg = "Exec ok (no output)"
		return a, nil
	}
	a.copiedMsg = trimLine(strings.ReplaceAll(out, "\n", " | "), 160)
	return a, nil
}

// resolveRemoteConfig returns the kubectl-target config for a saved or active pod.
func (a *App) resolveRemoteConfig(podName string) (remote.Config, bool) {
	if a.remoteSession != nil && a.remoteSession.PodName == podName {
		return a.remoteSession.Config, true
	}
	for _, saved := range remote.LoadSavedSessions() {
		if saved.PodName == podName {
			cfg := remote.Config{
				Transport: saved.Transport,
				Host:      saved.Host,
				Context:   saved.Context,
				Namespace: saved.Namespace,
				WorkDir:   saved.WorkDir,
				SessionID: saved.SessionID,
				LocalDir:  saved.LocalDir,
			}
			cfg = mergeRemoteConfig(a.remoteDefaults, cfg)
			cfg = cfg.Defaults()
			return cfg, true
		}
	}
	return remote.Config{}, false
}

// executeCmdRemotePhase queries the current pod phase for the selected remote
// session and surfaces it as a status line.
func (a *App) executeCmdRemotePhase() (tea.Model, tea.Cmd) {
	sess, ok := a.selectedSession()
	if !ok || !sess.IsRemote {
		a.copiedMsg = "Select a remote session first"
		return a, nil
	}
	cfg, ok := a.resolveRemoteConfig(sess.RemotePodName)
	if !ok {
		a.copiedMsg = "No config for remote session"
		return a, nil
	}
	pod := sess.RemotePodName
	target := cfg.Host
	if target == "" {
		target = fmt.Sprintf("%s/%s/%s", cfg.Context, cfg.Namespace, pod)
	}
	a.copiedMsg = fmt.Sprintf("Querying %s...", target)
	return a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		t := cfg.BuildTransportForPod(pod)
		status, _ := t.Status(ctx)
		return remotePhaseMsg{phases: map[string]string{pod: status}}
	}
}

// executeCmdRemoteLs jumps to the first remote session in the list. If none
// exist, surfaces a hint.
func (a *App) executeCmdRemoteLs() (tea.Model, tea.Cmd) {
	items := a.sessionList.Items()
	for i, item := range items {
		if si, ok := item.(sessionItem); ok && si.sess.IsRemote {
			a.sessionList.Select(i)
			a.copiedMsg = fmt.Sprintf("Remote: %s", si.sess.RemotePodName)
			return a, nil
		}
	}
	a.copiedMsg = "No remote sessions"
	return a, nil
}

func trimLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// executeCmdRemoteSnapshot captures the selected pod's transcript + workdir
// into ~/.config/ccx/snapshots/<name>/.
func (a *App) executeCmdRemoteSnapshot(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	name := ""
	if len(parts) >= 2 {
		name = parts[1]
	}

	sess, ok := a.selectedSession()
	if !ok || !sess.IsRemote {
		a.copiedMsg = "Select a remote session first"
		return a, nil
	}
	cfg, ok := a.resolveRemoteConfig(sess.RemotePodName)
	if !ok {
		a.copiedMsg = "No config for remote session"
		return a, nil
	}

	src := remote.SavedSession{LocalDir: sess.ProjectPath}
	for _, s := range remote.LoadSavedSessions() {
		if s.PodName == sess.RemotePodName {
			src = s
			break
		}
	}

	pod := sess.RemotePodName
	a.copiedMsg = fmt.Sprintf("Snapshotting %s...", pod)
	return a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		meta, err := remote.SaveSnapshot(ctx, cfg, pod, name, src)
		return remoteSnapshotMsg{name: meta.Name, meta: meta, err: err}
	}
}

func (a *App) handleRemoteSnapshot(msg remoteSnapshotMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		a.copiedMsg = "Snapshot failed: " + msg.err.Error()
		return a, nil
	}
	parts := []string{"snapshot " + msg.name}
	if msg.meta.HasSession {
		parts = append(parts, "session")
	}
	if msg.meta.HasWorkdir {
		parts = append(parts, fmt.Sprintf("workdir %s", formatBytes(msg.meta.WorkdirSize)))
	}
	a.copiedMsg = strings.Join(parts, " · ")
	return a, nil
}

// executeCmdRemoteRestore boots a fresh pod from a saved snapshot.
// Usage: remote:restore <snapshot-name> [context] [namespace]
func (a *App) executeCmdRemoteRestore(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		a.copiedMsg = "Usage: remote:restore <snapshot-name> [context] [namespace]"
		return a, nil
	}
	name := parts[1]

	overrides := mergeRemoteConfig(a.remoteDefaults, remote.Config{})
	if len(parts) >= 3 {
		overrides.Context = parts[2]
	}
	if len(parts) >= 4 {
		overrides.Namespace = parts[3]
	}
	sess, steps, err := remote.StartFromSnapshot(name, overrides, a.config.ClaudeDir)
	if err != nil {
		a.copiedMsg = "Restore failed: " + err.Error()
		return a, nil
	}
	return a.installRemoteSession(sess, steps)
}

// executeCmdRemoteFork clones the selected pod's config + workdir snapshot
// into a fresh sibling pod. Captures from the live pod first (no on-disk
// snapshot is left behind).
func (a *App) executeCmdRemoteFork(input string) (tea.Model, tea.Cmd) {
	sess, ok := a.selectedSession()
	if !ok || !sess.IsRemote {
		a.copiedMsg = "Select a remote session first to fork"
		return a, nil
	}
	cfg, ok := a.resolveRemoteConfig(sess.RemotePodName)
	if !ok {
		a.copiedMsg = "No config for remote session"
		return a, nil
	}
	pod := sess.RemotePodName

	a.copiedMsg = fmt.Sprintf("Forking %s...", pod)
	return a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		src := remote.SavedSession{LocalDir: sess.ProjectPath}
		for _, s := range remote.LoadSavedSessions() {
			if s.PodName == pod {
				src = s
				break
			}
		}
		name := fmt.Sprintf("fork-%s-%d", pod, time.Now().Unix())
		meta, err := remote.SaveSnapshot(ctx, cfg, pod, name, src)
		if err != nil {
			return remoteSnapshotMsg{err: fmt.Errorf("fork: %w", err)}
		}
		// Tag this snapshot as a pending fork-restore; handled in handler.
		return remoteForkReadyMsg{snapshot: meta.Name, cleanup: true}
	}
}

type remoteForkReadyMsg struct {
	snapshot string
	cleanup  bool
}

func (a *App) handleRemoteForkReady(msg remoteForkReadyMsg) (tea.Model, tea.Cmd) {
	overrides := mergeRemoteConfig(a.remoteDefaults, remote.Config{})
	sess, steps, err := remote.StartFromSnapshot(msg.snapshot, overrides, a.config.ClaudeDir)
	if err != nil {
		a.copiedMsg = "Fork restore failed: " + err.Error()
		return a, nil
	}
	if msg.cleanup {
		_ = remote.DeleteSnapshot(msg.snapshot)
	}
	return a.installRemoteSession(sess, steps)
}

// executeCmdRemotePull tars the pod's workdir and extracts it over the
// selected session's LocalDir on the host. Equivalent of machinen `mount-live`
// write-back, condensed to a single explicit pull instead of streaming.
func (a *App) executeCmdRemotePull(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	sess, ok := a.selectedSession()
	if !ok || !sess.IsRemote {
		a.copiedMsg = "Select a remote session first"
		return a, nil
	}
	cfg, ok := a.resolveRemoteConfig(sess.RemotePodName)
	if !ok {
		a.copiedMsg = "No config for remote session"
		return a, nil
	}

	dest := sess.ProjectPath
	if len(parts) >= 2 {
		dest = parts[1]
	}
	if dest == "" {
		a.copiedMsg = "Usage: remote:pull [target-dir]"
		return a, nil
	}

	pod := sess.RemotePodName
	a.copiedMsg = fmt.Sprintf("Pulling workdir %s → %s...", pod, dest)
	return a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		err := remote.FetchWorkdirToDir(ctx, cfg, pod, dest)
		return remotePullMsg{podName: pod, dest: dest, err: err}
	}
}

// executeCmdRemotePullSession fetches the remote session JSONL (conversation
// transcript) and writes it into the local ~/.claude/projects/ tree, over-
// writing the local session file so `claude --resume <id>` picks up everything
// that happened on the remote. This is the "remote → local session sync" that
// lets you work on a beefy remote and finish locally.
func (a *App) executeCmdRemotePullSession() (tea.Model, tea.Cmd) {
	sess, ok := a.selectedSession()
	if !ok || !sess.IsRemote {
		a.copiedMsg = "Select a remote session first"
		return a, nil
	}

	cfg, ok := a.resolveRemoteConfig(sess.RemotePodName)
	if !ok {
		a.copiedMsg = "No config for remote session"
		return a, nil
	}

	// Determine the local project path to materialize into.
	localProject := cfg.LocalDir
	if localProject == "" {
		a.copiedMsg = "No local_dir configured — cannot determine local project path"
		return a, nil
	}

	pod := sess.RemotePodName
	a.copiedMsg = fmt.Sprintf("Pulling session %s → local...", pod)
	return a, func() tea.Msg {
		t := cfg.BuildTransportForPod(pod)
		data, err := remote.FetchSessionJSONL(cfg, t)
		if err != nil {
			return remotePullMsg{podName: pod, dest: "", err: err}
		}

		// Write into ~/.claude/projects/<encoded local path>/<session-id>.jsonl
		encoded := session.EncodeProjectPath(localProject)
		home := homeDir()
		projDir := filepath.Join(home, ".claude", "projects", encoded)
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			return remotePullMsg{podName: pod, dest: "", err: err}
		}
		sessFile := filepath.Join(projDir, sess.ID+".jsonl")
		if err := os.WriteFile(sessFile, data, 0o644); err != nil {
			return remotePullMsg{podName: pod, dest: "", err: err}
		}
		return remotePullMsg{podName: pod, dest: sessFile, err: nil}
	}
}

func (a *App) handleRemotePull(msg remotePullMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		a.copiedMsg = "Pull failed: " + msg.err.Error()
		return a, nil
	}
	a.copiedMsg = fmt.Sprintf("Pulled workdir → %s", msg.dest)
	return a, nil
}

// installRemoteSession wires a freshly-Started remote.Session into the App
// (virtual session, persistence, preview). Shared by restore + fork paths.
func (a *App) installRemoteSession(sess *remote.Session, steps <-chan remote.SetupStep) (tea.Model, tea.Cmd) {
	if a.remoteSession != nil {
		a.copiedMsg = "Remote session already active — :remote:stop first"
		return a, nil
	}
	a.remoteSession = sess
	a.remoteSetupSteps = steps

	saved := remote.SavedSession{
		PodName:   sess.PodName,
		Transport: sess.Config.Transport,
		Host:      sess.Config.Host,
		Context:   sess.Config.Context,
		Namespace: sess.Config.Namespace,
		Image:     sess.Config.Image,
		LocalDir:  sess.Config.LocalDir,
		SessionID: sess.Config.SessionID,
		WorkDir:   sess.Config.WorkDir,
		Status:    "starting",
	}
	// For SSH, clear k8s context/namespace from saved session so they don't
	// leak into labels on restart.
	if sess.Config.IsSSH() {
		saved.Context = ""
		saved.Namespace = ""
	}
	remote.AddSavedSession(saved)

	var projectName, firstPrompt string
	if sess.Config.IsSSH() {
		projectName = "ssh:" + sess.Config.Host
		firstPrompt = fmt.Sprintf("%s [starting...]", sess.Config.Host)
	} else {
		projectName = "remote:" + sess.PodName
		firstPrompt = fmt.Sprintf("%s/%s/%s", sess.Config.Context, sess.Config.Namespace, sess.PodName)
	}

	virtualID := "remote-" + sess.PodName
	// Use a unique project path for remote sessions so they appear as their own
	// project row in the project-centric view, not buried under the local
	// project they were started from.
	remoteProjectPath := sess.Config.LocalDir
	if sess.Config.IsSSH() {
		remoteProjectPath = "ssh:" + sess.Config.Host
	} else {
		remoteProjectPath = "remote:" + sess.PodName
	}
	virtualSess := session.Session{
		ID:              virtualID,
		ShortID:         sess.PodName,
		ProjectPath:     remoteProjectPath,
		ProjectName:     projectName,
		ModTime:         time.Now(),
		IsRemote:        true,
		RemotePodName:   sess.PodName,
		RemoteContext:   sess.Config.Context,
		RemoteNamespace: sess.Config.Namespace,
		RemoteStatus:    "starting...",
		FirstPrompt:     firstPrompt,
	}
	inserted := false
	for i := range a.sessions {
		if a.sessions[i].IsRemote && a.sessions[i].RemotePodName == sess.PodName {
			a.sessions[i] = virtualSess
			inserted = true
			break
		}
	}
	if !inserted {
		a.sessions = append([]session.Session{virtualSess}, a.sessions...)
	}
	a.rebuildSessionList()

	for i, item := range a.sessionList.Items() {
		if si, ok := item.(sessionItem); ok && si.sess.ID == virtualID {
			a.sessionList.Select(i)
			break
		}
	}

	a.remoteProgressSteps = nil
	a.remoteContent = a.buildRemoteProgressView(sess, "Initializing...")
	a.copiedMsg = fmt.Sprintf("Remote → %s/%s/%s", sess.Config.Context, sess.Config.Namespace, sess.PodName)
	if sess.Config.ArchMismatch() {
		a.copiedMsg += fmt.Sprintf(" [arch %s ≠ host %s]", sess.Config.Arch, remote.HostArch())
	}

	if !a.sessSplit.Show {
		a.sessSplit.Show = true
		contentH := max(a.height-3, 1)
		a.sessionList.SetSize(a.sessSplit.ListWidth(a.width, a.splitRatio), contentH)
	}
	a.sessSplit.CacheKey = "remote:" + virtualID
	a.sessSplit.Preview.SetContent(a.remoteContent)

	return a, readSetupStep(sess.PodName, steps)
}

// executeCmdRemoteSnapshots lists saved snapshots as a status line.
func (a *App) executeCmdRemoteSnapshots() (tea.Model, tea.Cmd) {
	snaps := remote.ListSnapshots()
	if len(snaps) == 0 {
		a.copiedMsg = "No snapshots"
		return a, nil
	}
	var names []string
	for _, s := range snaps {
		names = append(names, s.Name)
		if len(names) >= 5 {
			break
		}
	}
	more := ""
	if len(snaps) > len(names) {
		more = fmt.Sprintf(" (+%d more)", len(snaps)-len(names))
	}
	a.copiedMsg = "Snapshots: " + strings.Join(names, ", ") + more
	return a, nil
}

// executeCmdRemoteRmSnap deletes a snapshot directory on disk.
func (a *App) executeCmdRemoteRmSnap(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		a.copiedMsg = "Usage: remote:rm-snap <snapshot-name>"
		return a, nil
	}
	name := parts[1]
	if err := remote.DeleteSnapshot(name); err != nil {
		a.copiedMsg = "Delete failed: " + err.Error()
		return a, nil
	}
	a.copiedMsg = "Deleted snapshot " + name
	return a, nil
}

func (a *App) executeCmdRemoteExportSnap(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	if len(parts) < 3 {
		a.copiedMsg = "Usage: remote:export-snap <snapshot-name> <output.tgz>"
		return a, nil
	}
	name, out := parts[1], parts[2]
	if err := remote.ExportSnapshot(name, out); err != nil {
		a.copiedMsg = "Export failed: " + err.Error()
		return a, nil
	}
	a.copiedMsg = fmt.Sprintf("Exported %s → %s", name, out)
	return a, nil
}

func (a *App) executeCmdRemoteImportSnap(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		a.copiedMsg = "Usage: remote:import-snap <bundle.tgz> [snapshot-name]"
		return a, nil
	}
	name := ""
	if len(parts) >= 3 {
		name = parts[2]
	}
	meta, err := remote.ImportSnapshot(parts[1], name)
	if err != nil {
		a.copiedMsg = "Import failed: " + err.Error()
		return a, nil
	}
	a.copiedMsg = "Imported snapshot " + meta.Name
	return a, nil
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
