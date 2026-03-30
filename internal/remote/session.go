package remote

import (
	"context"
	"fmt"
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

	// Create non-root user (--dangerously-skip-permissions blocks root)
	steps <- SetupStep{Message: "Creating user..."}
	ExecInPod(ctx, cfg, s.PodName, "sh", "-c",
		"useradd -m -s /bin/bash claude 2>/dev/null; "+
			"mkdir -p /home/claude/.claude "+cfg.WorkDir+" && "+
			"chown -R claude:claude /home/claude "+cfg.WorkDir+" && "+
			// Write token to file so claude user can source it
			"echo \"export CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN\" > /home/claude/.claude_env && "+
			"chown claude:claude /home/claude/.claude_env && chmod 600 /home/claude/.claude_env")

	// Install prerequisites + Claude Code CLI (as root)
	steps <- SetupStep{Message: "Installing Node.js and Claude Code CLI..."}
	installCmd := "apt-get update -qq && apt-get install -y -qq curl git sudo > /dev/null 2>&1 && " +
		"curl -fsSL https://deb.nodesource.com/setup_22.x | bash - > /dev/null 2>&1 && " +
		"apt-get install -y -qq nodejs > /dev/null 2>&1 && " +
		"npm install -g @anthropic-ai/claude-code 2>&1 | tail -3"
	out, err := ExecInPod(ctx, cfg, s.PodName, "sh", "-c", installCmd)
	if err != nil {
		steps <- SetupStep{Message: fmt.Sprintf("Install issue: %s", string(out))}
	}

	// Sync config to claude user's home
	steps <- SetupStep{Message: "Syncing config..."}
	configTar, err := CreateConfigTarball(claudeDir, projectPath, cfg.WorkDir, cfg.SessionFile)
	if err == nil && len(configTar) > 0 {
		UploadTarball(ctx, cfg, s.PodName, "main", "/home/claude", configTar)
		// Fix ownership
		ExecInPod(ctx, cfg, s.PodName, "chown", "-R", "claude:claude", "/home/claude/.claude", "/home/claude/.claude.json")
	}

	// Sync workdir
	if cfg.LocalDir != "" {
		steps <- SetupStep{Message: "Syncing workdir..."}
		workdirTar, err := CreateWorkdirTarball(cfg.LocalDir)
		if err == nil && len(workdirTar) > 0 {
			UploadTarball(ctx, cfg, s.PodName, "main", cfg.WorkDir, workdirTar)
			ExecInPod(ctx, cfg, s.PodName, "chown", "-R", "claude:claude", cfg.WorkDir)
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
		"su - claude -c '. ~/.claude_env; cd %s 2>/dev/null; %s'",
		cfg.WorkDir, claudeCmd)
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
	encoded := encodeProjectPath(cfg.WorkDir)
	// Find the latest .jsonl file
	findCmd := fmt.Sprintf("ls -t /home/claude/.claude/projects/%s/*.jsonl 2>/dev/null | head -1", encoded)
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
