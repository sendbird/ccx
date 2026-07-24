package remote

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

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
// command), applying config options and extra args.
func (t *sshTransport) sshArgs() []string {
	args := []string{"-o", "BatchMode=no"}
	args = append(args, t.cfg.SSHExtraArgs...)
	args = append(args, t.cfg.Host)
	return args
}

func (t *sshTransport) Exec(ctx context.Context, cmd ...string) ([]byte, error) {
	args := append(t.sshArgs(), cmd...)
	c := sshCommandContext(ctx, "ssh", args...)
	return c.CombinedOutput()
}

func (t *sshTransport) ExecInteractive(cmd ...string) *exec.Cmd {
	// -t forces a TTY for the interactive Claude attach.
	args := []string{"-t"}
	args = append(args, t.cfg.SSHExtraArgs...)
	args = append(args, t.cfg.Host)
	args = append(args, cmd...)
	return sshCommand("ssh", args...)
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
