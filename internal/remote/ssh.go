package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// sshKeepAliveOpts are SSH options that keep idle connections alive and detect
// dead peers quickly (orca-style resilience).
var sshKeepAliveOpts = []string{
	"-o", "ServerAliveInterval=15",
	"-o", "ServerAliveCountMax=3",
}

// sshReconnectMax is the maximum number of auto-reconnect attempts for the
// interactive attach.
const sshReconnectMax = 3

// sshCommand is the command used to invoke ssh. It's a var so tests can stub it
// to assert argument construction without a real SSH server.
var sshCommand = exec.Command

// sshTransport runs Claude on a plain SSH host, orca-style: the target is a
// ~/.ssh/config alias or a user@host[:port] string resolved with `ssh -G`, and
// OpenSSH handles keys/agent-forwarding/config.
type sshTransport struct {
	cfg Config
}

// sshArgs builds the ssh argument list for the transport (without the remote
// command), applying keep-alive options and extra args.
func (t *sshTransport) sshArgs() []string {
	args := []string{"-o", "BatchMode=no"}
	args = append(args, sshKeepAliveOpts...)
	args = append(args, t.cfg.SSHExtraArgs...)
	args = append(args, t.cfg.Host)
	return args
}

// quoteForSSH ensures that a `sh -c "command"` invocation survives SSH's
// space-joining of argv. When cmd is ["sh", "-c", "rest..."], the rest is
// shell-quoted as one string so the remote `-c` receives it intact. Without
// this, `ssh host sh -c "echo hello > f"` becomes `sh -c echo` on the remote
// (the `-c` only gets `echo`, losing the rest).
func quoteForSSH(cmd ...string) []string {
	if len(cmd) >= 3 && cmd[0] == "sh" && cmd[1] == "-c" {
		rest := strings.Join(cmd[2:], " ")
		return []string{cmd[0], cmd[1], shellQuote(rest)}
	}
	return cmd
}

func (t *sshTransport) Exec(ctx context.Context, cmd ...string) ([]byte, error) {
	args := append(t.sshArgs(), quoteForSSH(cmd...)...)
	c := sshCommandContext(ctx, "ssh", args...)
	return c.CombinedOutput()
}

func (t *sshTransport) ExecInteractive(cmd ...string) *exec.Cmd {
	// -t forces a TTY; keep-alive prevents idle disconnects.
	args := []string{"-t"}
	args = append(args, sshKeepAliveOpts...)
	args = append(args, t.cfg.SSHExtraArgs...)
	args = append(args, t.cfg.Host)
	args = append(args, cmd...)
	return sshCommand("ssh", args...)
}

// AttachCmd returns an exec.Cmd that runs the Claude attach inside a remote
// tmux session with auto-reconnect. tmux new-session -A creates the session if
// it doesn't exist or attaches if it does, so a dropped SSH connection can be
// re-attached without losing Claude. The local wrapper retries on SSH exit 255
// (connection error) up to sshReconnectMax times.
func (t *sshTransport) AttachCmd(shellCmd string) *exec.Cmd {
	tmuxCmd := fmt.Sprintf("tmux new-session -A -s ccx-claude %s", shellQuote(shellCmd))
	sshArgs := []string{"-t"}
	sshArgs = append(sshArgs, sshKeepAliveOpts...)
	sshArgs = append(sshArgs, t.cfg.SSHExtraArgs...)
	sshArgs = append(sshArgs, t.cfg.Host)
	sshArgs = append(sshArgs, "sh", "-c", tmuxCmd)

	// Build a local retry wrapper script: retry on SSH exit 255 (connection
	// dropped) up to sshReconnectMax times with a 2-second backoff.
	script := fmt.Sprintf(`#!/bin/sh
max=%d
for i in $(seq 1 $max); do
	ssh %s
	rc=$?
	if [ $rc -eq 255 ] && [ $i -lt $max ]; then
		echo "SSH disconnected (exit $rc), reconnecting ($i/$max)..." >&2
		sleep 2
		continue
	fi
	exit $rc
done
`, sshReconnectMax, strings.Join(sshArgs, " "))

	return exec.Command("sh", "-c", script)
}

// StreamFile opens a live `tail -f -c +0` stream of a remote file via SSH.
// The caller must close the returned ReadCloser to terminate the stream and
// kill the backing ssh process.
func (t *sshTransport) StreamFile(ctx context.Context, remotePath string) (io.ReadCloser, error) {
	args := append(t.sshArgs(), "tail", "-f", "-c", "+0", remotePath)
	c := sshCommandContext(ctx, "ssh", args...)
	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
		return nil, err
	}
	return &streamCloser{cmd: c, reader: stdout}, nil
}

func (t *sshTransport) Upload(ctx context.Context, destDir string, tarball []byte) error {
	args := append(t.sshArgs(), "tar", "xzf", "-", "-C", destDir)
	c := sshCommandContext(ctx, "ssh", args...)
	c.Stdin = bytes.NewReader(tarball)
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("upload: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Prepare verifies SSH reachability and ensures the remote home and work
// directories exist. Unlike k8s it does not allocate a pod; the host is
// expected to already exist. User creation is best-effort (skipped if the
// login user can't create it).
func (t *sshTransport) Prepare(ctx context.Context) error {
	mkdir := fmt.Sprintf("mkdir -p %s %s 2>/dev/null; id -u %s >/dev/null 2>&1 || true",
		t.cfg.RemoteHome, t.cfg.WorkDir, t.cfg.RemoteUser)
	if _, err := t.Exec(ctx, "sh", "-c", mkdir); err != nil {
		return fmt.Errorf("ssh prepare: %w", err)
	}
	return nil
}

func (t *sshTransport) Release(ctx context.Context) error { return nil }

func (t *sshTransport) Status(ctx context.Context) (string, error) {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5"}
	args = append(args, t.cfg.SSHExtraArgs...)
	args = append(args, t.cfg.Host, "true")
	c := sshCommandContext(ctx, "ssh", args...)
	if err := c.Run(); err != nil {
		return "unreachable", nil
	}
	return "running", nil
}

func (t *sshTransport) Target() string {
	return t.cfg.Host
}

// sshCommandContext wraps sshCommand with context support so tests can stub a
// single sshCommand var and still get cancellation.
func sshCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	c := sshCommand(name, args...)
	c = contextCmd(ctx, c)
	return c
}

// contextCmd attaches ctx to an *exec.Cmd built by the sshCommand seam. When
// the seam returns a real *exec.Cmd (the default), exec.CommandContext is used
// instead so cancellation works; when tests stub sshCommand, the returned cmd
// is used as-is.
func contextCmd(ctx context.Context, c *exec.Cmd) *exec.Cmd {
	if ctx == nil {
		return c
	}
	// Rebuild via CommandContext to bind context, preserving Path/Args.
	if c == nil {
		return exec.CommandContext(ctx, "")
	}
	nc := exec.CommandContext(ctx, c.Path, c.Args[1:]...)
	nc.Stdin = c.Stdin
	nc.Stdout = c.Stdout
	nc.Stderr = c.Stderr
	nc.Env = c.Env
	nc.Dir = c.Dir
	return nc
}
