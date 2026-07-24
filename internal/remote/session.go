package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/sendbird/ccx/internal/tmux"
)

// Session represents an active remote Claude execution (k8s pod or SSH host).
type Session struct {
	Config    Config
	PodName   string
	Transport Transport
	Stream    <-chan StreamLine // live output stream (nil until Claude starts)
	Status    string            // current status for display
	ctx       context.Context
	cancel    context.CancelFunc
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
		Config:    cfg,
		PodName:   podName,
		Transport: cfg.BuildTransport(),
		Status:    "starting",
		ctx:       ctx,
		cancel:    cancel,
	}
	// kubectlTransport needs the pod name; set it after construction.
	if kt, ok := sess.Transport.(*kubectlTransport); ok {
		kt.podName = podName
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
		Config:    cfg,
		PodName:   podName,
		Transport: cfg.BuildTransport(),
		Status:    "running",
		ctx:       ctx,
		cancel:    cancel,
	}
	if kt, ok := sess.Transport.(*kubectlTransport); ok {
		kt.podName = podName
	}
	steps <- SetupStep{Done: true, Message: "Reusing existing pod"}
	close(steps)
	return sess, steps
}

func (s *Session) setup(cfg Config, claudeDir, projectPath string, steps chan<- SetupStep) error {
	ctx := s.ctx

	if err := cfg.Validate(); err != nil {
		return err
	}

	// Auth
	steps <- SetupStep{Message: "Extracting auth token..."}
	token, err := tmux.ExtractClaudeOAuthToken()
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	// Prepare the remote (k8s: create/reuse pod; ssh: ensure dirs + reachability)
	steps <- SetupStep{Message: "Preparing remote..."}
	if err := s.Transport.Prepare(ctx); err != nil {
		return fmt.Errorf("prepare: %w", err)
	}

	// Write auth env and (k8s only) create the remote user.
	steps <- SetupStep{Message: "Configuring remote user..."}
	tokenExport := "export CLAUDE_CODE_OAUTH_TOKEN=" + token + "\n"
	if cfg.IsSSH() {
		// SSH: the login user already exists; just write the env file.
		writeEnv := fmt.Sprintf("printf %%s %s > %s/.claude_env && chmod 600 %s/.claude_env",
			shellQuote(tokenExport), cfg.RemoteHome, cfg.RemoteHome)
		s.Transport.Exec(ctx, "sh", "-c", writeEnv)
	} else {
		// k8s: create the non-root user and write the env file.
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
		s.Transport.Exec(ctx, "sh", "-c", setupUserCmd)
	}

	// Install prerequisites + Claude Code CLI (+ tmux for SSH persistence).
	steps <- SetupStep{Message: "Checking Claude Code CLI..."}
	if _, err := s.Transport.Exec(ctx, "sh", "-c", "command -v claude >/dev/null 2>&1"); err != nil {
		steps <- SetupStep{Message: "Installing Node.js and Claude Code CLI..."}
		pkgs := "apt-get update -qq && apt-get install -y -qq curl git > /dev/null 2>&1"
		if cfg.IsSSH() {
			pkgs += " && apt-get install -y -qq tmux > /dev/null 2>&1 || true"
		}
		installCmd := pkgs + " && " +
			"curl -fsSL https://deb.nodesource.com/setup_22.x | bash - > /dev/null 2>&1 && " +
			"apt-get install -y -qq nodejs > /dev/null 2>&1 && " +
			"npm install -g @anthropic-ai/claude-code 2>&1 | tail -3"
		out, err := s.Transport.Exec(ctx, "sh", "-c", installCmd)
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
		s.Transport.Upload(ctx, cfg.RemoteHome, configTar)
		if !cfg.IsSSH() {
			s.Transport.Exec(ctx, "sh", "-c", fmt.Sprintf("chown -R %s:%s %s/.claude %s/.claude.json 2>/dev/null || true", cfg.RemoteUser, cfg.RemoteUser, cfg.RemoteHome, cfg.RemoteHome))
		}
	}

	// Sync workdir — prefer prebuilt tarball (snapshot/fork), else tar LocalDir.
	if len(cfg.WorkdirTarball) > 0 {
		steps <- SetupStep{Message: "Restoring workdir from snapshot..."}
		s.Transport.Upload(ctx, cfg.WorkDir, cfg.WorkdirTarball)
		if !cfg.IsSSH() {
			s.Transport.Exec(ctx, "sh", "-c", fmt.Sprintf("chown -R %s:%s %s 2>/dev/null || true", cfg.RemoteUser, cfg.RemoteUser, cfg.WorkDir))
		}
	} else if cfg.LocalDir != "" {
		steps <- SetupStep{Message: "Syncing workdir..."}
		workdirTar, err := CreateWorkdirTarball(cfg.LocalDir)
		if err == nil && len(workdirTar) > 0 {
			s.Transport.Upload(ctx, cfg.WorkDir, workdirTar)
			if !cfg.IsSSH() {
				s.Transport.Exec(ctx, "sh", "-c", fmt.Sprintf("chown -R %s:%s %s 2>/dev/null || true", cfg.RemoteUser, cfg.RemoteUser, cfg.WorkDir))
			}
		}
	}

	steps <- SetupStep{Message: "Ready — use Enter to attach, L to preview"}
	return nil
}

// AttachCmd returns an exec.Cmd for interactive Claude on the remote.
func (s *Session) AttachCmd() *exec.Cmd {
	return BuildAttachCmd(s.Config, s.Transport)
}

// BuildAttachCmd creates an interactive command for Claude on the remote via
// the transport. The transport handles resilience (k8s: su to non-root user;
// ssh: tmux persistence + keep-alive + auto-reconnect).
func BuildAttachCmd(cfg Config, t Transport) *exec.Cmd {
	claudeCmd := BuildClaudeCmd(cfg, false)
	if cfg.IsSSH() {
		shellCmd := fmt.Sprintf("cd %s 2>/dev/null; . ~/.claude_env; %s", cfg.WorkDir, claudeCmd)
		return t.AttachCmd(shellCmd)
	}
	shellCmd := fmt.Sprintf(
		"export PATH=$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin:$PATH; . ~/.claude_env; cd %s 2>/dev/null; %s",
		cfg.WorkDir, claudeCmd)
	return t.AttachCmd(shellCmd)
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

// FetchSessionJSONL downloads the latest session JSONL from the remote.
// It finds the most recent .jsonl file under the remote workdir's project path.
func FetchSessionJSONL(cfg Config, t Transport) ([]byte, error) {
	projectPath := cfg.RemoteProjectPath
	if projectPath == "" {
		projectPath = cfg.WorkDir
	}
	encoded := encodeProjectPath(projectPath)
	findCmd := fmt.Sprintf("ls -t %s/.claude/projects/%s/*.jsonl 2>/dev/null | head -1", cfg.RemoteHome, encoded)
	out, err := t.Exec(context.Background(), "sh", "-c", findCmd)
	if err != nil || len(out) == 0 {
		return nil, fmt.Errorf("no session file found on remote")
	}
	jsonlPath := strings.TrimSpace(string(out))
	if jsonlPath == "" {
		return nil, fmt.Errorf("no session file found on remote")
	}
	data, err := t.Exec(context.Background(), "cat", jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("fetch session: %w", err)
	}
	return data, nil
}

// StreamSessionJSONL opens a live stream of the latest session JSONL on the
// remote (tail -f from byte 0). The caller must close the returned ReadCloser
// to terminate the stream. Returns the resolved remote path alongside the
// stream so the caller can display it.
func StreamSessionJSONL(cfg Config, t Transport) (string, io.ReadCloser, error) {
	projectPath := cfg.RemoteProjectPath
	if projectPath == "" {
		projectPath = cfg.WorkDir
	}
	encoded := encodeProjectPath(projectPath)
	findCmd := fmt.Sprintf("ls -t %s/.claude/projects/%s/*.jsonl 2>/dev/null | head -1", cfg.RemoteHome, encoded)
	out, err := t.Exec(context.Background(), "sh", "-c", findCmd)
	if err != nil || len(out) == 0 {
		return "", nil, fmt.Errorf("no session file found on remote")
	}
	jsonlPath := strings.TrimSpace(string(out))
	if jsonlPath == "" {
		return "", nil, fmt.Errorf("no session file found on remote")
	}
	rc, err := t.StreamFile(context.Background(), jsonlPath)
	if err != nil {
		return "", nil, fmt.Errorf("stream session: %w", err)
	}
	return jsonlPath, rc, nil
}

// Stop cancels and releases the remote (k8s: deletes the pod; ssh: no-op).
func (s *Session) Stop() error {
	s.cancel()
	return s.Transport.Release(context.Background())
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
