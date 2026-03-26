package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// podSpec generates a Kubernetes pod JSON spec.
func podSpec(cfg Config, podName, oauthToken string) ([]byte, error) {
	// Build init command: install Claude Code, optionally clone repo
	initParts := []string{
		"apt-get update -qq && apt-get install -y -qq git rsync > /dev/null 2>&1",
		"npm install -g @anthropic-ai/claude-code > /dev/null 2>&1",
	}
	if cfg.GitRepo != "" && cfg.LocalDir == "" {
		// Git clone mode (fallback when no local dir)
		initParts = append(initParts, fmt.Sprintf(
			"git clone --branch %s --depth 1 %s %s",
			cfg.GitBranch, cfg.GitRepo, cfg.WorkDir))
	} else {
		// Workdir sync mode: just ensure the directory exists
		initParts = append(initParts, "mkdir -p "+cfg.WorkDir)
	}
	initCmd := strings.Join(initParts, " && ")

	mainCmd := "claude --output-format stream-json"
	if cfg.Prompt != "" {
		mainCmd += fmt.Sprintf(" -p %s", shellQuote(cfg.Prompt))
	}

	// Build env vars list
	envVars := []map[string]string{
		{"name": "CLAUDE_CODE_OAUTH_TOKEN", "value": oauthToken},
		{"name": "HOME", "value": "/root"},
	}

	// Add configured env vars
	for k, v := range cfg.EnvVars {
		envVars = append(envVars, map[string]string{"name": k, "value": v})
	}

	// Mirror local env vars
	for _, name := range cfg.MirrorEnv {
		if val := os.Getenv(name); val != "" {
			envVars = append(envVars, map[string]string{"name": name, "value": val})
		}
	}

	spec := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]interface{}{
			"name":      podName,
			"namespace": cfg.Namespace,
			"labels": map[string]string{
				"app":         "ccx-remote",
				"ccx-session": podName,
			},
		},
		"spec": map[string]interface{}{
			"restartPolicy": "Never",
			"nodeSelector": map[string]string{
				"kubernetes.io/arch": cfg.Arch,
			},
			"initContainers": []map[string]interface{}{
				{
					"name":    "setup",
					"image":   cfg.Image,
					"command": []string{"sh", "-c", initCmd},
					"volumeMounts": []map[string]string{
						{"name": "workspace", "mountPath": cfg.WorkDir},
						{"name": "claude-home", "mountPath": "/root/.claude"},
					},
					"resources": map[string]interface{}{
						"limits": map[string]string{
							"cpu":    cfg.CPULimit,
							"memory": cfg.MemoryLimit,
						},
					},
				},
			},
			"containers": []map[string]interface{}{
				{
					"name":       "main",
					"image":      cfg.Image,
					"command":    []string{"sh", "-c", mainCmd},
					"workingDir": cfg.WorkDir,
					"env":        envVars,
					"volumeMounts": []map[string]string{
						{"name": "workspace", "mountPath": cfg.WorkDir},
						{"name": "claude-home", "mountPath": "/root/.claude"},
					},
					"resources": map[string]interface{}{
						"limits": map[string]string{
							"cpu":    cfg.CPULimit,
							"memory": cfg.MemoryLimit,
						},
					},
				},
			},
			"volumes": []map[string]interface{}{
				{"name": "workspace", "emptyDir": map[string]interface{}{}},
				{"name": "claude-home", "emptyDir": map[string]interface{}{}},
			},
		},
	}

	return json.MarshalIndent(spec, "", "  ")
}

// CreatePod creates the pod via kubectl.
func CreatePod(ctx context.Context, cfg Config, podName, oauthToken string) error {
	spec, err := podSpec(cfg, podName, oauthToken)
	if err != nil {
		return fmt.Errorf("generate spec: %w", err)
	}

	cmd := exec.CommandContext(ctx, "kubectl",
		"--context", cfg.Context,
		"-n", cfg.Namespace,
		"apply", "-f", "-")
	cmd.Stdin = strings.NewReader(string(spec))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apply: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// WaitForPod waits until the pod is ready or fails.
func WaitForPod(ctx context.Context, cfg Config, podName string, timeout time.Duration) error {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--context", cfg.Context,
		"-n", cfg.Namespace,
		"wait", "pod/"+podName,
		"--for=condition=Ready",
		fmt.Sprintf("--timeout=%ds", int(timeout.Seconds())))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wait: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// DeletePod removes the pod.
func DeletePod(ctx context.Context, cfg Config, podName string) error {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--context", cfg.Context,
		"-n", cfg.Namespace,
		"delete", "pod", podName,
		"--grace-period=5",
		"--ignore-not-found")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("delete: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// PodPhase returns the current phase of a pod.
func PodPhase(ctx context.Context, cfg Config, podName string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--context", cfg.Context,
		"-n", cfg.Namespace,
		"get", "pod", podName,
		"-o", "jsonpath={.status.phase}")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
