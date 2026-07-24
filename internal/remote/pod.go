package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sendbird/ccx/internal/tmux"
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
		{"name": "PATH", "value": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
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
	if cfg.Arch != "" {
		podSpecMap["nodeSelector"] = map[string]string{"kubernetes.io/arch": cfg.Arch}
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
		"exec", podName,
	}
	if cfg.Container != "" {
		args = append(args, "-c", cfg.Container)
	}
	args = append(args, "--")
	args = append(args, cmd...)
	c := exec.CommandContext(ctx, "kubectl", args...)
	return c.CombinedOutput()
}

// PodPhase returns the current phase of a pod (Running, Pending, Succeeded, Failed, Unknown).
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

// ExecInteractive opens an interactive exec session to the pod.
// This takes over the terminal (like tea.ExecProcess).
func ExecInteractive(cfg Config, podName string, cmd ...string) *exec.Cmd {
	args := []string{
		"--context", cfg.Context,
		"-n", cfg.Namespace,
		"exec", "-it", podName,
	}
	if cfg.Container != "" {
		args = append(args, "-c", cfg.Container)
	}
	args = append(args, "--")
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

// kubectlTransport runs Claude in a Kubernetes pod via kubectl exec.
type kubectlTransport struct {
	cfg     Config
	podName string
}

func (t *kubectlTransport) Exec(ctx context.Context, cmd ...string) ([]byte, error) {
	return ExecInPod(ctx, t.cfg, t.podName, cmd...)
}

func (t *kubectlTransport) ExecInteractive(cmd ...string) *exec.Cmd {
	return ExecInteractive(t.cfg, t.podName, cmd...)
}

// AttachCmd wraps the shell command with su to the non-root user so
// --dangerously-skip-permissions works.
func (t *kubectlTransport) AttachCmd(shellCmd string) *exec.Cmd {
	wrapped := fmt.Sprintf(
		"su - %s -c 'export PATH=$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin:$PATH; %s'",
		t.cfg.RemoteUser, shellCmd)
	return t.ExecInteractive("sh", "-c", wrapped)
}

// StreamFile opens a live tail -f stream of a remote file via kubectl exec.
func (t *kubectlTransport) StreamFile(ctx context.Context, remotePath string) (io.ReadCloser, error) {
	args := []string{
		"--context", t.cfg.Context,
		"-n", t.cfg.Namespace,
		"exec", t.podName,
	}
	if t.cfg.Container != "" {
		args = append(args, "-c", t.cfg.Container)
	}
	args = append(args, "--", "tail", "-f", "-c", "+0", remotePath)
	c := exec.CommandContext(ctx, "kubectl", args...)
	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
		return nil, err
	}
	return &streamCloser{cmd: c, reader: stdout}, nil
}

func (t *kubectlTransport) Upload(ctx context.Context, destDir string, tarball []byte) error {
	return UploadTarball(ctx, t.cfg, t.podName, t.cfg.Container, destDir, tarball)
}

// Prepare creates or reuses the pod and waits for it to be ready.
func (t *kubectlTransport) Prepare(ctx context.Context) error {
	if phase, err := PodPhase(ctx, t.cfg, t.podName); err == nil && (phase == "Running" || phase == "Pending") {
		return nil
	}
	token, err := tmux.ExtractClaudeOAuthToken()
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if err := CreatePod(ctx, t.cfg, t.podName, token); err != nil {
		return fmt.Errorf("create pod: %w", err)
	}
	if err := WaitForPod(ctx, t.cfg, t.podName, 3*time.Minute); err != nil {
		DeletePod(context.Background(), t.cfg, t.podName)
		return fmt.Errorf("pod not ready: %w", err)
	}
	return nil
}

func (t *kubectlTransport) Release(ctx context.Context) error {
	return DeletePod(ctx, t.cfg, t.podName)
}

func (t *kubectlTransport) Status(ctx context.Context) (string, error) {
	return PodPhase(ctx, t.cfg, t.podName)
}

func (t *kubectlTransport) Target() string {
	return fmt.Sprintf("%s/%s/%s", t.cfg.Context, t.cfg.Namespace, t.podName)
}
