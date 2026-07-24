package remote

import (
	"context"
	"os/exec"
)

// Transport abstracts how ccx reaches the remote host that runs Claude.
// The two implementations are kubectlTransport (run Claude in a k8s pod) and
// sshTransport (run Claude on a plain SSH host, orca-style).
type Transport interface {
	// Exec runs cmd on the remote non-interactively and returns stdout.
	Exec(ctx context.Context, cmd ...string) ([]byte, error)
	// ExecInteractive returns an exec.Cmd attached to the TTY for interactive
	// use (e.g. attaching to Claude).
	ExecInteractive(cmd ...string) *exec.Cmd
	// Upload extracts a tar.gz archive into destDir on the remote.
	Upload(ctx context.Context, destDir string, tarball []byte) error
	// Prepare makes the remote usable: for k8s it creates/reuses a pod and
	// waits for it; for ssh it verifies reachability and ensures dirs exist.
	Prepare(ctx context.Context) error
	// Release tears down transport-allocated resources (k8s: deletes the pod;
	// ssh: no-op).
	Release(ctx context.Context) error
	// Status returns a short status string for the saved-session list
	// ("running", "ended", "unreachable", or a k8s pod phase).
	Status(ctx context.Context) (string, error)
	// Target returns a human label for the remote ("ctx/ns/pod" or "user@host").
	Target() string
}

// transportKind identifies the transport for config/persistence.
const (
	transportK8s = "k8s"
	transportSSH = "ssh"
)
