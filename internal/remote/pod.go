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
// Uses a single container that stays alive for exec attachment.
func podSpec(cfg Config, podName, oauthToken string) ([]byte, error) {
	// The main container just sleeps — we exec into it for setup and Claude
	// This ensures we can stream progress and attach interactively.

	// Build tolerations
	var tolerations []map[string]interface{}
	for _, key := range cfg.Tolerations {
		tolerations = append(tolerations, map[string]interface{}{
			"key":      key,
			"operator": "Exists",
			"effect":   "NoSchedule",
		})
	}

	// Build env vars list
	envVars := []map[string]string{
		{"name": "CLAUDE_CODE_OAUTH_TOKEN", "value": oauthToken},
		{"name": "HOME", "value": "/root"},
		{"name": "PATH", "value": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/root/.npm-global/bin"},
		{"name": "NPM_CONFIG_PREFIX", "value": "/root/.npm-global"},
	}
	for k, v := range cfg.EnvVars {
		envVars = append(envVars, map[string]string{"name": k, "value": v})
	}
	for _, name := range cfg.MirrorEnv {
		if val := os.Getenv(name); val != "" {
			envVars = append(envVars, map[string]string{"name": name, "value": val})
		}
	}

	// Build labels
	labels := map[string]string{
		"app":         "ccx-remote",
		"ccx-session": podName,
	}
	for k, v := range cfg.Labels {
		labels[k] = v
	}

	// Build spec
	podSpecMap := map[string]interface{}{
		"restartPolicy": "Never",
		"containers": []map[string]interface{}{
			{
				"name":    "main",
				"image":   cfg.Image,
				"command": []string{"sleep", "infinity"},
				"stdin":   true,
				"tty":     true,
				"env":     envVars,
				"resources": map[string]interface{}{
					"limits": map[string]string{
						"cpu":    cfg.CPULimit,
						"memory": cfg.MemoryLimit,
					},
				},
			},
		},
	}
	if len(tolerations) > 0 {
		podSpecMap["tolerations"] = tolerations
	}

	spec := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]interface{}{
			"name":      podName,
			"namespace": cfg.Namespace,
			"labels":    labels,
		},
		"spec": podSpecMap,
	}

	return json.MarshalIndent(spec, "", "  ")
}

// CreatePod creates the pod.
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

// WaitForPod waits until the pod is ready.
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

// ExecInPod runs a command in the pod and returns combined output.
func ExecInPod(ctx context.Context, cfg Config, podName string, cmd ...string) ([]byte, error) {
	args := []string{
		"--context", cfg.Context,
		"-n", cfg.Namespace,
		"exec", podName, "--",
	}
	args = append(args, cmd...)
	c := exec.CommandContext(ctx, "kubectl", args...)
	return c.CombinedOutput()
}

// ExecInteractive opens an interactive exec session to the pod.
// This takes over the terminal (like tea.ExecProcess).
func ExecInteractive(cfg Config, podName string, cmd ...string) *exec.Cmd {
	args := []string{
		"--context", cfg.Context,
		"-n", cfg.Namespace,
		"exec", "-it", podName, "--",
	}
	args = append(args, cmd...)
	return exec.Command("kubectl", args...)
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

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
