package remote

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// CurrentContext returns the current kubectl context.
func CurrentContext() (string, error) {
	out, err := exec.Command("kubectl", "config", "current-context").Output()
	if err != nil {
		return "", fmt.Errorf("no current context: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Config holds settings for a remote Claude execution.
type Config struct {
	Transport         string            `yaml:"transport"`           // "k8s" (default) or "ssh"
	Host              string            `yaml:"host"`                // SSH target: alias / user@host[:port] / ssh://…
	SSHExtraArgs      []string          `yaml:"ssh_extra_args"`      // extra args appended to ssh
	Context           string            `yaml:"context"`             // kubectl --context (k8s, required)
	Namespace         string            `yaml:"namespace"`           // target namespace (k8s)
	PodName           string            `yaml:"pod_name"`            // fixed pod name to reuse (optional, k8s)
	Container         string            `yaml:"container"`           // target container name (optional, k8s)
	RemoteUser        string            `yaml:"remote_user"`         // user to run Claude as
	RemoteHome        string            `yaml:"remote_home"`         // remote user's home directory
	Image             string            `yaml:"image"`               // container image
	LocalDir          string            `yaml:"local_dir"`           // local workdir to sync
	RemoteProjectPath string            `yaml:"remote_project_path"` // project path key to use for Claude session JSONL
	GitRepo           string            `yaml:"git_repo"`            // repo URL to clone (fallback if no local_dir)
	GitBranch         string            `yaml:"git_branch"`          // branch to checkout
	WorkDir           string            `yaml:"work_dir"`            // remote working directory
	WorkDirTemplate   string            `yaml:"work_dir_template"`   // optional template for per-session workdirs
	Prompt            string            `yaml:"-"`                   // initial prompt (not persisted)
	CPULimit          string            `yaml:"cpu_limit"`           // e.g. "2"
	MemoryLimit       string            `yaml:"memory_limit"`        // e.g. "4Gi"
	Arch              string            `yaml:"arch"`                // "amd64" or "arm64"
	EnvVars           map[string]string `yaml:"env_vars"`            // extra env vars to inject into pod
	MirrorEnv         []string          `yaml:"mirror_env"`          // local env var names to mirror to pod
	Labels            map[string]string `yaml:"labels"`              // extra pod labels
	Tolerations       []string          `yaml:"tolerations"`         // toleration keys
	ClaudeArgs        []string          `yaml:"claude_args"`         // extra args for claude CLI (e.g. --model, --allowedTools)
	SessionID         string            `yaml:"-"`                   // session ID to resume
	SessionFile       string            `yaml:"-"`                   // local path to session JSONL
	// WorkdirTarball, when non-nil, is uploaded verbatim into WorkDir on the pod
	// instead of re-tarring LocalDir. Used by snapshot restore / fork to avoid
	// host-side changes leaking into the resumed pod.
	WorkdirTarball []byte `yaml:"-"`
}

// Defaults returns a Config with sensible defaults filled in.
func (c Config) Defaults() Config {
	if c.Transport == "" {
		c.Transport = transportK8s
	}
	if c.Transport == transportSSH {
		// SSH mode: keep user/home defaults; skip k8s-only fields.
		if c.RemoteUser == "" {
			c.RemoteUser = "claude"
		}
		if c.RemoteHome == "" {
			c.RemoteHome = "/home/" + c.RemoteUser
		}
		if c.GitBranch == "" {
			c.GitBranch = "main"
		}
		// WorkDir: if not explicitly set, mirror the local project's
		// home-relative path on the remote (e.g. local /Users/x/src/proj
		// with home /Users/x → remote /home/coder/src/proj). This keeps
		// the project structure consistent and the encoded .claude/projects
		// path matches between local and remote.
		if c.WorkDir == "" {
			c.WorkDir = mirrorLocalPath(c.LocalDir, c.RemoteHome)
		}
		// RemoteProjectPath must equal WorkDir so the session JSONL is
		// placed where Claude (cd'd into WorkDir) will find it via --resume.
		if c.RemoteProjectPath == "" {
			c.RemoteProjectPath = c.WorkDir
		}
		return c
	}
	if c.Context == "" {
		c.Context, _ = CurrentContext()
	}
	if c.Namespace == "" {
		c.Namespace = "default"
	}
	if c.Image == "" {
		c.Image = "ubuntu:24.04"
	}
	if c.Container == "" {
		c.Container = "main"
	}
	if c.RemoteUser == "" {
		c.RemoteUser = "claude"
	}
	if c.RemoteHome == "" {
		c.RemoteHome = "/home/" + c.RemoteUser
	}
	if c.GitBranch == "" {
		c.GitBranch = "main"
	}
	if c.WorkDir == "" {
		c.WorkDir = "/workspace"
	}
	if c.CPULimit == "" {
		c.CPULimit = "2"
	}
	if c.MemoryLimit == "" {
		c.MemoryLimit = "4Gi"
	}
	return c
}

// Validate checks required fields.
func (c Config) Validate() error {
	if c.Transport == transportSSH {
		if c.Host == "" {
			return fmt.Errorf("host is required for ssh transport")
		}
		return nil
	}
	if c.Context == "" {
		return fmt.Errorf("context is required")
	}
	return nil
}

// IsSSH reports whether this config uses the SSH transport.
func (c Config) IsSSH() bool { return c.Transport == transportSSH }

// BuildTransport returns the Transport implementation for this config. It does
// not Prepare the transport; callers do that during setup.
func (c Config) BuildTransport() Transport {
	if c.IsSSH() {
		return &sshTransport{cfg: c}
	}
	return &kubectlTransport{cfg: c}
}

// BuildTransportForPod returns a kubectlTransport bound to podName. Convenience
// for callers that operate on saved k8s pods without a full Start() flow.
// For SSH transports, use BuildTransport() instead — this always builds a k8s
// transport. When the saved session's Transport field is "ssh", callers should
// check IsSSH and use BuildTransport().
func (c Config) BuildTransportForPod(podName string) Transport {
	if c.IsSSH() {
		return &sshTransport{cfg: c}
	}
	t := &kubectlTransport{cfg: c, podName: podName}
	return t
}

// GeneratePodName creates a unique pod name.
func GeneratePodName() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("ccx-remote-%x", b)
}

// HostArch returns the local machine's GOARCH normalized to the k8s
// kubernetes.io/arch convention (amd64, arm64).
func HostArch() string {
	return runtime.GOARCH
}

// mirrorLocalPath computes the remote workdir by mirroring the local project
// path's home-relative portion onto the remote home. For example:
//
//	localDir = /Users/gavin.jeong/src/keyolk/ccx
//	remoteHome = /home/coder
//	→ /home/coder/src/keyolk/ccx
//
// If localDir is not under the local home, falls back to remoteHome/basename.
// If localDir is empty, returns remoteHome (the remote home itself).
func mirrorLocalPath(localDir, remoteHome string) string {
	if localDir == "" {
		return remoteHome
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(remoteHome, filepath.Base(localDir))
	}
	rel, err := filepath.Rel(home, localDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		// localDir is not under home — use basename as fallback.
		return filepath.Join(remoteHome, filepath.Base(localDir))
	}
	return filepath.Join(remoteHome, rel)
}

// hostSlug converts an SSH host string (alias, user@host, user@host:port) to a
// safe identifier suitable for pod-name-style keys and display labels. Non-
// alphanumeric characters are replaced with hyphens.
func hostSlug(host string) string {
	// Strip user@ prefix if present.
	if idx := strings.Index(host, "@"); idx >= 0 {
		host = host[idx+1:]
	}
	// Strip port suffix if present.
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	var b strings.Builder
	for _, r := range host {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "unknown"
	}
	return s
}

// ArchMismatch reports whether cfg.Arch is set and differs from the host arch.
// Comparison is case-insensitive; "" never mismatches.
func (c Config) ArchMismatch() bool {
	if c.Arch == "" {
		return false
	}
	return !strings.EqualFold(c.Arch, HostArch())
}
