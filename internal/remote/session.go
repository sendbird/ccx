package remote

import (
	"context"
	"fmt"
	"time"

	"github.com/sendbird/ccx/internal/tmux"
)

// Session represents an active remote Claude execution.
type Session struct {
	Config  Config
	PodName string
	Stream  <-chan StreamLine
	ctx     context.Context
	cancel  context.CancelFunc
}

// Start creates a pod, syncs config, and begins streaming.
func Start(cfg Config, claudeDir, projectPath string) (*Session, error) {
	cfg = cfg.Defaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Extract OAuth token
	token, err := tmux.ExtractClaudeOAuthToken()
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}

	podName := GeneratePodName()
	ctx, cancel := context.WithCancel(context.Background())

	// Create pod
	if err := CreatePod(ctx, cfg, podName, token); err != nil {
		cancel()
		return nil, fmt.Errorf("create pod: %w", err)
	}

	// Wait for init container (git clone + npm install)
	if err := WaitForPod(ctx, cfg, podName, 5*time.Minute); err != nil {
		DeletePod(context.Background(), cfg, podName)
		cancel()
		return nil, fmt.Errorf("pod not ready: %w", err)
	}

	// Sync Claude config (settings, memory, skills, agents, hooks, etc.)
	configTar, err := CreateConfigTarball(claudeDir, projectPath)
	if err == nil && len(configTar) > 0 {
		UploadTarball(ctx, cfg, podName, "main", "/root", configTar) // best-effort
	}

	// Sync local workdir if specified (replaces git clone)
	if cfg.LocalDir != "" {
		workdirTar, err := CreateWorkdirTarball(cfg.LocalDir)
		if err == nil && len(workdirTar) > 0 {
			UploadTarball(ctx, cfg, podName, "main", cfg.WorkDir, workdirTar)
		}
	}

	// Start streaming
	stream, err := StreamLogs(ctx, cfg, podName)
	if err != nil {
		DeletePod(context.Background(), cfg, podName)
		cancel()
		return nil, fmt.Errorf("stream: %w", err)
	}

	return &Session{
		Config:  cfg,
		PodName: podName,
		Stream:  stream,
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

// Stop cancels the stream and deletes the pod.
func (s *Session) Stop() error {
	s.cancel()
	return DeletePod(context.Background(), s.Config, s.PodName)
}

// IsRunning returns true if the session context hasn't been cancelled.
func (s *Session) IsRunning() bool {
	return s.ctx.Err() == nil
}
