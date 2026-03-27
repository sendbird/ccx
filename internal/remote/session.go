package remote

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/sendbird/ccx/internal/tmux"
)

// Session represents an active remote Claude pod.
type Session struct {
	Config  Config
	PodName string
	Stream  <-chan StreamLine // live output stream (nil until Claude starts)
	Status  string           // current status for display
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
	sess := &Session{
		Config:  cfg,
		PodName: GeneratePodName(),
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

	// Create pod
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

	// Install prerequisites + Claude Code CLI
	steps <- SetupStep{Message: "Installing Node.js and Claude Code CLI..."}
	installCmd := "apt-get update -qq && apt-get install -y -qq curl git > /dev/null 2>&1 && " +
		"curl -fsSL https://deb.nodesource.com/setup_22.x | bash - > /dev/null 2>&1 && " +
		"apt-get install -y -qq nodejs > /dev/null 2>&1 && " +
		"mkdir -p /root/.npm-global && npm install -g @anthropic-ai/claude-code 2>&1 | tail -3"
	out, err := ExecInPod(ctx, cfg, s.PodName, "sh", "-c", installCmd)
	if err != nil {
		steps <- SetupStep{Message: fmt.Sprintf("Install issue: %s", string(out))}
	}

	// Sync config
	steps <- SetupStep{Message: "Syncing config..."}
	configTar, err := CreateConfigTarball(claudeDir, projectPath, cfg.SessionFile)
	if err == nil && len(configTar) > 0 {
		UploadTarball(ctx, cfg, s.PodName, "main", "/root", configTar)
	}

	// Sync workdir
	if cfg.LocalDir != "" {
		steps <- SetupStep{Message: "Syncing workdir..."}
		ExecInPod(ctx, cfg, s.PodName, "mkdir", "-p", cfg.WorkDir)
		workdirTar, err := CreateWorkdirTarball(cfg.LocalDir)
		if err == nil && len(workdirTar) > 0 {
			UploadTarball(ctx, cfg, s.PodName, "main", cfg.WorkDir, workdirTar)
		}
	}

	// Start Claude with streaming output
	steps <- SetupStep{Message: "Starting Claude..."}
	claudeArgs := []string{"sh", "-c", "cd " + cfg.WorkDir + " && claude --output-format stream-json"}
	if cfg.SessionID != "" {
		claudeArgs = []string{"sh", "-c", "cd " + cfg.WorkDir + " && claude --output-format stream-json --resume " + cfg.SessionID}
	}

	stream, err := StreamExec(ctx, cfg, s.PodName, claudeArgs...)
	if err != nil {
		return fmt.Errorf("start claude: %w", err)
	}
	s.Stream = stream

	return nil
}

// AttachCmd returns an exec.Cmd for interactive Claude in the pod.
func (s *Session) AttachCmd() *exec.Cmd {
	return BuildAttachCmd(s.Config, s.PodName)
}

// BuildAttachCmd creates a kubectl exec command for interactive Claude.
func BuildAttachCmd(cfg Config, podName string) *exec.Cmd {
	claudeCmd := "claude"
	if cfg.SessionID != "" {
		claudeCmd += " --resume " + cfg.SessionID
	}
	shellCmd := fmt.Sprintf("cd %s 2>/dev/null; %s", cfg.WorkDir, claudeCmd)
	return ExecInteractive(cfg, podName, "sh", "-c", shellCmd)
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
