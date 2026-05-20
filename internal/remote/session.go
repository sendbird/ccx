package remote

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sendbird/ccx/internal/tmux"
)

// Session represents an active remote Claude pod.
type Session struct {
	Config  Config
	PodName string
	Stream  <-chan StreamLine // live output stream (nil until Claude starts)
	Status  string            // current status for display
	ctx     context.Context
	cancel  context.CancelFunc
}

// SetupStep represents a progress update during pod setup.
type SetupStep struct {
	Message string
	Done    bool
	Err     error
}

// Start creates a pod, installs Claude, syncs everything, then starts Claude
// with output streaming. Returns a channel of setup progress steps.
func Start(cfg Config, claudeDir, projectPath string) (*Session, <-chan SetupStep) {
	cfg = cfg.Defaults()
	steps := make(chan SetupStep, 16)

	ctx, cancel := context.WithCancel(context.Background())
	podName := cfg.PodName
	if podName == "" {
		podName = GeneratePodName()
	}
	sess := &Session{
		Config:  cfg,
		PodName: podName,
		Status:  "starting",
		ctx:     ctx,
		cancel:  cancel,
	}

	go func() {
		defer close(steps)
		err := sess.setup(cfg, claudeDir, projectPath, steps)
		if err != nil {
			sess.Status = "failed: " + err.Error()
			steps <- SetupStep{Err: err, Message: err.Error()}
			return
		}
		sess.Status = "running"
		steps <- SetupStep{Done: true, Message: "Claude is running"}
	}()

	return sess, steps
}

// Adopt attaches ccx state to an already-running remote pod. It does not sync
// config or workdir; callers should only use it for pods previously created by
// ccx with matching metadata.
func Adopt(cfg Config, podName string) (*Session, <-chan SetupStep) {
	cfg = cfg.Defaults()
	steps := make(chan SetupStep, 1)
	ctx, cancel := context.WithCancel(context.Background())
	sess := &Session{
		Config:  cfg,
		PodName: podName,
		Status:  "running",
		ctx:     ctx,
		cancel:  cancel,
	}
	steps <- SetupStep{Done: true, Message: "Reusing existing pod"}
	close(steps)
	return sess, steps
}

func (s *Session) setup(cfg Config, claudeDir, projectPath string, steps chan<- SetupStep) error {
	ctx := s.ctx

	// Validate
	if err := cfg.Validate(); err != nil {
		return err
	}

	// Auth
	steps <- SetupStep{Message: "Extracting auth token..."}
	token, err := tmux.ExtractClaudeOAuthToken()
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	// Create pod or reuse fixed existing pod.
	if phase, err := PodPhase(ctx, cfg, s.PodName); err == nil && (phase == "Running" || phase == "Pending") {
		steps <- SetupStep{Message: fmt.Sprintf("Reusing pod %s (%s)...", s.PodName, phase)}
	} else {
		steps <- SetupStep{Message: fmt.Sprintf("Creating pod %s...", s.PodName)}
		if err := CreatePod(ctx, cfg, s.PodName, token); err != nil {
			return fmt.Errorf("create pod: %w", err)
		}

		// Wait ready
		steps <- SetupStep{Message: "Waiting for pod ready..."}
		if err := WaitForPod(ctx, cfg, s.PodName, 3*time.Minute); err != nil {
			DeletePod(context.Background(), cfg, s.PodName)
			return fmt.Errorf("pod not ready: %w", err)
		}
	}

	// Create/update remote user and auth env.
	steps <- SetupStep{Message: "Preparing remote user..."}
	tokenExport := "export CLAUDE_CODE_OAUTH_TOKEN=" + token + "\n"
	setupUserCmd := fmt.Sprintf(
		"id -u %s >/dev/null 2>&1 || useradd -m -s /bin/bash %s 2>/dev/null; "+
			"mkdir -p %s/.claude %s && "+
			"chown -R %s:%s %s %s 2>/dev/null || true && "+
			"printf %%s %s > %s/.claude_env && "+
			"chown %s:%s %s/.claude_env 2>/dev/null || true && chmod 600 %s/.claude_env",
		cfg.RemoteUser, cfg.RemoteUser,
		cfg.RemoteHome, cfg.WorkDir,
		cfg.RemoteUser, cfg.RemoteUser, cfg.RemoteHome, cfg.WorkDir,
		shellQuote(tokenExport), cfg.RemoteHome,
		cfg.RemoteUser, cfg.RemoteUser, cfg.RemoteHome, cfg.RemoteHome)
	ExecInPod(ctx, cfg, s.PodName, "sh", "-c", setupUserCmd)

	// Install prerequisites + Claude Code CLI (as root), but skip on warm worker pods.
	steps <- SetupStep{Message: "Checking Claude Code CLI..."}
	if _, err := ExecInPod(ctx, cfg, s.PodName, "sh", "-c", "command -v claude >/dev/null 2>&1"); err != nil {
		steps <- SetupStep{Message: "Installing Node.js and Claude Code CLI..."}
		installCmd := "apt-get update -qq && apt-get install -y -qq curl git > /dev/null 2>&1 && " +
			"curl -fsSL https://deb.nodesource.com/setup_22.x | bash - > /dev/null 2>&1 && " +
			"apt-get install -y -qq nodejs > /dev/null 2>&1 && " +
			"npm install -g @anthropic-ai/claude-code 2>&1 | tail -3"
		out, err := ExecInPod(ctx, cfg, s.PodName, "sh", "-c", installCmd)
		if err != nil {
			steps <- SetupStep{Message: fmt.Sprintf("Install issue: %s", string(out))}
		}
	}

	// Sync config to remote user's home.
	steps <- SetupStep{Message: "Syncing config..."}
	remoteProjectPath := cfg.RemoteProjectPath
	if remoteProjectPath == "" {
		remoteProjectPath = cfg.WorkDir
	}
	configTar, err := CreateConfigTarball(claudeDir, projectPath, remoteProjectPath, cfg.SessionFile)
	if err == nil && len(configTar) > 0 {
		UploadTarball(ctx, cfg, s.PodName, cfg.Container, cfg.RemoteHome, configTar)
		ExecInPod(ctx, cfg, s.PodName, "sh", "-c", fmt.Sprintf("chown -R %s:%s %s/.claude %s/.claude.json 2>/dev/null || true", cfg.RemoteUser, cfg.RemoteUser, cfg.RemoteHome, cfg.RemoteHome))
	}

	// Sync workdir — prefer prebuilt tarball (snapshot/fork), else tar LocalDir.
	if len(cfg.WorkdirTarball) > 0 {
		steps <- SetupStep{Message: "Restoring workdir from snapshot..."}
		UploadTarball(ctx, cfg, s.PodName, cfg.Container, cfg.WorkDir, cfg.WorkdirTarball)
		ExecInPod(ctx, cfg, s.PodName, "sh", "-c", fmt.Sprintf("chown -R %s:%s %s 2>/dev/null || true", cfg.RemoteUser, cfg.RemoteUser, cfg.WorkDir))
	} else if cfg.LocalDir != "" {
		steps <- SetupStep{Message: "Syncing workdir..."}
		workdirTar, err := CreateWorkdirTarball(cfg.LocalDir)
		if err == nil && len(workdirTar) > 0 {
			UploadTarball(ctx, cfg, s.PodName, cfg.Container, cfg.WorkDir, workdirTar)
			ExecInPod(ctx, cfg, s.PodName, "sh", "-c", fmt.Sprintf("chown -R %s:%s %s 2>/dev/null || true", cfg.RemoteUser, cfg.RemoteUser, cfg.WorkDir))
		}
	}

	steps <- SetupStep{Message: "Ready — use Enter to attach, L to preview"}
	return nil
}

// AttachCmd returns an exec.Cmd for interactive Claude in the pod.
func (s *Session) AttachCmd() *exec.Cmd {
	return BuildAttachCmd(s.Config, s.PodName)
}

// BuildAttachCmd creates a kubectl exec command for interactive Claude.
// Runs as the non-root 'claude' user to allow --dangerously-skip-permissions.
// Passes CLAUDE_CODE_OAUTH_TOKEN via env since su doesn't inherit it.
func BuildAttachCmd(cfg Config, podName string) *exec.Cmd {
	claudeCmd := BuildClaudeCmd(cfg, false)
	shellCmd := fmt.Sprintf(
		"su - %s -c 'export PATH=$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin:$PATH; . ~/.claude_env; cd %s 2>/dev/null; %s'",
		cfg.RemoteUser, cfg.WorkDir, claudeCmd)
	return ExecInteractive(cfg, podName, "sh", "-c", shellCmd)
}

// BuildClaudeCmd constructs the claude command string with all configured args.
func BuildClaudeCmd(cfg Config, streaming bool) string {
	cmd := "claude"
	if streaming {
		cmd += " --output-format stream-json --verbose"
	}
	if cfg.SessionID != "" {
		cmd += " --resume " + cfg.SessionID
	}
	for _, arg := range cfg.ClaudeArgs {
		cmd += " " + arg
	}
	return cmd
}

// FetchSessionJSONL downloads the latest session JSONL from the pod.
// It finds the most recent .jsonl file under the remote workdir's project path.
func FetchSessionJSONL(cfg Config, podName string) ([]byte, error) {
	projectPath := cfg.RemoteProjectPath
	if projectPath == "" {
		projectPath = cfg.WorkDir
	}
	encoded := encodeProjectPath(projectPath)
	// Find the latest .jsonl file
	findCmd := fmt.Sprintf("ls -t %s/.claude/projects/%s/*.jsonl 2>/dev/null | head -1", cfg.RemoteHome, encoded)
	out, err := ExecInPod(context.Background(), cfg, podName, "sh", "-c", findCmd)
	if err != nil || len(out) == 0 {
		return nil, fmt.Errorf("no session file found on pod")
	}
	jsonlPath := strings.TrimSpace(string(out))
	if jsonlPath == "" {
		return nil, fmt.Errorf("no session file found on pod")
	}
	// Cat the file
	data, err := ExecInPod(context.Background(), cfg, podName, "cat", jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("fetch session: %w", err)
	}
	return data, nil
}

// Stop cancels and deletes the pod.
func (s *Session) Stop() error {
	s.cancel()
	return DeletePod(context.Background(), s.Config, s.PodName)
}

// IsRunning returns true if not cancelled.
func (s *Session) IsRunning() bool {
	return s.ctx.Err() == nil
}

// StartFromSnapshot resolves a snapshot by name and starts a new pod whose
// workdir and (optionally) session file come from the snapshot rather than
// from the local host.
//
// overrides lets the caller change the target Context/Namespace/Image without
// editing the snapshot meta on disk — anything zero in overrides falls back to
// the snapshot's recorded value.
func StartFromSnapshot(name string, overrides Config, claudeDir string) (*Session, <-chan SetupStep, error) {
	meta, sessionPath, workdirPath, err := LoadSnapshot(name)
	if err != nil {
		return nil, nil, fmt.Errorf("load snapshot %q: %w", name, err)
	}

	cfg := overrides
	if cfg.Context == "" {
		cfg.Context = meta.Context
	}
	if cfg.Namespace == "" {
		cfg.Namespace = meta.Namespace
	}
	if cfg.Image == "" {
		cfg.Image = meta.Image
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = meta.WorkDir
	}
	if cfg.LocalDir == "" {
		cfg.LocalDir = meta.LocalDir
	}
	if cfg.SessionID == "" {
		cfg.SessionID = meta.SessionID
	}
	if cfg.SessionFile == "" && sessionPath != "" {
		cfg.SessionFile = sessionPath
	}
	if workdirPath != "" {
		data, rerr := os.ReadFile(workdirPath)
		if rerr == nil {
			cfg.WorkdirTarball = data
		}
	}

	sess, steps := Start(cfg, claudeDir, cfg.LocalDir)
	return sess, steps, nil
}
