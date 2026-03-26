package remote

import (
	"crypto/rand"
	"fmt"
	"os/exec"
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
	Context     string            `yaml:"context"`       // kubectl --context (required)
	Namespace   string            `yaml:"namespace"`     // target namespace
	Image       string            `yaml:"image"`         // container image
	LocalDir    string            `yaml:"local_dir"`     // local workdir to sync
	GitRepo     string            `yaml:"git_repo"`      // repo URL to clone (fallback if no local_dir)
	GitBranch   string            `yaml:"git_branch"`    // branch to checkout
	WorkDir     string            `yaml:"work_dir"`      // remote working directory
	Prompt      string            `yaml:"-"`             // initial prompt (not persisted)
	CPULimit    string            `yaml:"cpu_limit"`     // e.g. "2"
	MemoryLimit string            `yaml:"memory_limit"`  // e.g. "4Gi"
	Arch        string            `yaml:"arch"`          // "amd64" or "arm64"
	EnvVars     map[string]string `yaml:"env_vars"`      // extra env vars to inject into pod
	MirrorEnv   []string          `yaml:"mirror_env"`    // local env var names to mirror to pod
	Labels      map[string]string `yaml:"labels"`        // extra pod labels
	Tolerations []string          `yaml:"tolerations"`   // toleration keys (e.g. "sendbird.com/system")
	SessionID   string            `yaml:"-"`             // session ID to resume
	SessionFile string            `yaml:"-"`             // local path to session JSONL
}

// Defaults returns a Config with sensible defaults filled in.
func (c Config) Defaults() Config {
	if c.Context == "" {
		c.Context, _ = CurrentContext()
	}
	if c.Namespace == "" {
		c.Namespace = "default"
	}
	if c.Image == "" {
		c.Image = "node:22-slim"
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
	if c.Arch == "" {
		c.Arch = "amd64"
	}
	return c
}

// Validate checks required fields.
func (c Config) Validate() error {
	if c.Context == "" {
		return fmt.Errorf("context is required")
	}
	return nil
}

// GeneratePodName creates a unique pod name.
func GeneratePodName() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("ccx-remote-%x", b)
}
