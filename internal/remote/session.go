package remote

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/sendbird/ccx/internal/tmux"
)

// ProgressFunc is called with status messages during setup.
type ProgressFunc func(msg string)

// Session represents an active remote Claude pod.
type Session struct {
	Config  Config
	PodName string
	ctx     context.Context
	cancel  context.CancelFunc
}

// Start creates a pod, installs Claude, syncs config/workdir/session,
// and returns a Session ready for interactive exec.
// progress is called with status updates during each step.
func Start(cfg Config, claudeDir, projectPath string, progress ProgressFunc) (*Session, error) {
	cfg = cfg.Defaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if progress == nil {
		progress = func(string) {}
	}

	// Extract OAuth token
	progress("Extracting auth token...")
	token, err := tmux.ExtractClaudeOAuthToken()
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}

	podName := GeneratePodName()
	ctx, cancel := context.WithCancel(context.Background())

	// Create pod
	progress(fmt.Sprintf("Creating pod %s...", podName))
	if err := CreatePod(ctx, cfg, podName, token); err != nil {
		cancel()
		return nil, fmt.Errorf("create pod: %w", err)
	}

	// Wait for pod ready
	progress("Waiting for pod ready...")
	if err := WaitForPod(ctx, cfg, podName, 3*time.Minute); err != nil {
		DeletePod(context.Background(), cfg, podName)
		cancel()
		return nil, fmt.Errorf("pod not ready: %w", err)
	}

	// Install Claude Code CLI in the pod
	progress("Installing Claude Code CLI...")
	out, err := ExecInPod(ctx, cfg, podName,
		"sh", "-c", "mkdir -p /root/.npm-global && npm install -g @anthropic-ai/claude-code 2>&1 | tail -1")
	if err != nil {
		progress(fmt.Sprintf("Install warning: %s", string(out)))
	}

	// Sync Claude config
	progress("Syncing config (settings, memory, skills, agents)...")
	configTar, err := CreateConfigTarball(claudeDir, projectPath, cfg.SessionFile)
	if err == nil && len(configTar) > 0 {
		UploadTarball(ctx, cfg, podName, "main", "/root", configTar)
	}

	// Sync local workdir
	if cfg.LocalDir != "" {
		progress(fmt.Sprintf("Syncing workdir %s...", cfg.LocalDir))
		// Ensure target dir exists
		ExecInPod(ctx, cfg, podName, "mkdir", "-p", cfg.WorkDir)
		workdirTar, err := CreateWorkdirTarball(cfg.LocalDir)
		if err == nil && len(workdirTar) > 0 {
			UploadTarball(ctx, cfg, podName, "main", cfg.WorkDir, workdirTar)
		}
	}

	progress("Ready — attaching to Claude...")

	return &Session{
		Config:  cfg,
		PodName: podName,
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

// ClaudeCmd returns an exec.Cmd for interactive Claude in the pod.
func (s *Session) ClaudeCmd() *exec.Cmd {
	args := []string{"claude"}
	if s.Config.SessionID != "" {
		args = append(args, "--resume", s.Config.SessionID)
	}
	if s.Config.Prompt != "" {
		args = append(args, "-p", s.Config.Prompt)
	}
	return ExecInteractive(s.Config, s.PodName, args...)
}

// Stop cancels and deletes the pod.
func (s *Session) Stop() error {
	s.cancel()
	return DeletePod(context.Background(), s.Config, s.PodName)
}
