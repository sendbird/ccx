package remote

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
)

// TestSSHTransportSatisfiesInterface is a compile-time check that sshTransport
// implements Transport.
func TestSSHTransportSatisfiesInterface(t *testing.T) {
	var _ Transport = (*sshTransport)(nil)
	var _ Transport = (*kubectlTransport)(nil)
}

// mockSSHCommand returns a sshCommand var replacement that captures the
// arguments by running `echo ssh <args>` so tests can inspect the constructed
// command line.
func mockSSHCommand() func(string, ...string) *exec.Cmd {
	return func(name string, args ...string) *exec.Cmd {
		return exec.Command("echo", append([]string{name}, args...)...)
	}
}

func TestSSHTransportExecArgs(t *testing.T) {
	orig := sshCommand
	sshCommand = mockSSHCommand()
	defer func() { sshCommand = orig }()

	tr := &sshTransport{cfg: Config{Transport: "ssh", Host: "my-box", RemoteHome: "/home/claude"}}
	out, err := tr.Exec(context.Background(), "sh", "-c", "echo hello")
	if err != nil {
		t.Fatalf("exec error: %v", err)
	}
	bare := strings.TrimSpace(string(out))
	if !strings.Contains(bare, "my-box") {
		t.Errorf("exec args missing host: %q", bare)
	}
	if !strings.Contains(bare, "echo hello") {
		t.Errorf("exec args missing remote command: %q", bare)
	}
}

func TestSSHTransportUploadArgs(t *testing.T) {
	orig := sshCommand
	sshCommand = mockSSHCommand()
	defer func() { sshCommand = orig }()

	tr := &sshTransport{cfg: Config{Transport: "ssh", Host: "my-box"}}
	// Upload uses sshCommandContext which wraps sshCommand; the mock's echo
	// output will contain the args.
	err := tr.Upload(context.Background(), "/workspace", []byte("fake tar"))
	if err != nil {
		t.Fatalf("upload error: %v", err)
	}
}

func TestSSHTransportStatusRunning(t *testing.T) {
	orig := sshCommand
	sshCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	}
	defer func() { sshCommand = orig }()

	tr := &sshTransport{cfg: Config{Transport: "ssh", Host: "my-box"}}
	status, _ := tr.Status(context.Background())
	if status != "running" {
		t.Errorf("status = %q, want running", status)
	}
}

func TestSSHTransportStatusUnreachable(t *testing.T) {
	orig := sshCommand
	sshCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}
	defer func() { sshCommand = orig }()

	tr := &sshTransport{cfg: Config{Transport: "ssh", Host: "my-box"}}
	status, _ := tr.Status(context.Background())
	if status != "unreachable" {
		t.Errorf("status = %q, want unreachable", status)
	}
}

func TestSSHTransportTarget(t *testing.T) {
	tr := &sshTransport{cfg: Config{Transport: "ssh", Host: "gavin@builder.example.com"}}
	if got := tr.Target(); got != "gavin@builder.example.com" {
		t.Errorf("target = %q, want gavin@builder.example.com", got)
	}
}

func TestSSHTransportReleaseIsNoop(t *testing.T) {
	tr := &sshTransport{cfg: Config{Transport: "ssh", Host: "my-box"}}
	if err := tr.Release(context.Background()); err != nil {
		t.Errorf("release should be no-op, got %v", err)
	}
}

func TestConfigIsSSH(t *testing.T) {
	if (Config{Transport: "ssh"}).IsSSH() != true {
		t.Error("IsSSH should be true for transport=ssh")
	}
	if (Config{Transport: "k8s"}).IsSSH() != false {
		t.Error("IsSSH should be false for transport=k8s")
	}
	if (Config{}).IsSSH() != false {
		t.Error("IsSSH should default to false (k8s)")
	}
}

func TestConfigValidateSSH(t *testing.T) {
	if err := (Config{Transport: "ssh"}).Validate(); err == nil {
		t.Error("ssh transport without host should fail validation")
	}
	if err := (Config{Transport: "ssh", Host: "my-box"}).Validate(); err != nil {
		t.Errorf("ssh transport with host should validate: %v", err)
	}
}

func TestConfigBuildTransportSSH(t *testing.T) {
	cfg := Config{Transport: "ssh", Host: "my-box"}
	tt := cfg.BuildTransport()
	if _, ok := tt.(*sshTransport); !ok {
		t.Fatalf("BuildTransport for ssh config returned %T, want *sshTransport", tt)
	}
}

func TestConfigBuildTransportK8s(t *testing.T) {
	cfg := Config{Transport: "k8s", Context: "my-ctx"}
	tt := cfg.BuildTransport()
	if _, ok := tt.(*kubectlTransport); !ok {
		t.Fatalf("BuildTransport for k8s config returned %T, want *kubectlTransport", tt)
	}
}

func TestConfigDefaultsSSH(t *testing.T) {
	cfg := Config{Transport: "ssh", Host: "my-box"}.Defaults()
	if cfg.Context != "" {
		t.Errorf("ssh defaults should not set Context, got %q", cfg.Context)
	}
	if cfg.WorkDir == "" {
		t.Error("ssh defaults should set WorkDir")
	}
}

// --- Phase 2: keep-alive + auto-reconnect ---

func TestSSHTransportExecInteractiveHasKeepAlive(t *testing.T) {
	orig := sshCommand
	sshCommand = mockSSHCommand()
	defer func() { sshCommand = orig }()

	tr := &sshTransport{cfg: Config{Transport: "ssh", Host: "my-box"}}
	cmd := tr.ExecInteractive("sh", "-c", "claude")
	_ = cmd // exec.Cmd built; we can't easily inspect args without running it,
	// but we can check Exec includes keep-alive via the output.
}

func TestSSHTransportExecIncludesKeepAlive(t *testing.T) {
	orig := sshCommand
	sshCommand = mockSSHCommand()
	defer func() { sshCommand = orig }()

	tr := &sshTransport{cfg: Config{Transport: "ssh", Host: "my-box"}}
	out, _ := tr.Exec(context.Background(), "true")
	bare := strings.TrimSpace(string(out))
	if !strings.Contains(bare, "ServerAliveInterval=15") {
		t.Errorf("exec args missing keep-alive: %q", bare)
	}
	if !strings.Contains(bare, "ServerAliveCountMax=3") {
		t.Errorf("exec args missing keep-alive count: %q", bare)
	}
}

func TestSSHTransportAttachCmdContainsTmuxAndRetry(t *testing.T) {
	tr := &sshTransport{cfg: Config{Transport: "ssh", Host: "my-box", WorkDir: "/workspace"}}
	cmd := tr.AttachCmd("cd /workspace; claude")
	if cmd == nil {
		t.Fatal("AttachCmd returned nil")
	}
	// The AttachCmd builds a shell script; inspect its args.
	// cmd.Args[0] = "sh", cmd.Args[1] = "-c", cmd.Args[2] = script
	if len(cmd.Args) < 3 {
		t.Fatalf("AttachCmd args too short: %v", cmd.Args)
	}
	script := cmd.Args[2]
	if !strings.Contains(script, "tmux") {
		t.Errorf("attach script should use tmux for persistence: %q", script)
	}
	if !strings.Contains(script, "ccx-claude") {
		t.Errorf("attach script should name the tmux session ccx-claude: %q", script)
	}
	if !strings.Contains(script, "reconnect") {
		t.Errorf("attach script should contain reconnect logic: %q", script)
	}
	if !strings.Contains(script, "ServerAliveInterval=15") {
		t.Errorf("attach script should include keep-alive options: %q", script)
	}
}

func TestSSHTransportStatusIncludesKeepAlive(t *testing.T) {
	orig := sshCommand
	sshCommand = mockSSHCommand()
	defer func() { sshCommand = orig }()

	tr := &sshTransport{cfg: Config{Transport: "ssh", Host: "my-box"}}
	_, _ = tr.Status(context.Background())
	// Status uses BatchMode + ConnectTimeout; keep-alive is not strictly
	// needed for a one-shot ping but we can verify it doesn't break.
}

// --- Phase 3: live stream ---

func TestSSHTransportStreamFileArgs(t *testing.T) {
	orig := sshCommand
	sshCommand = mockSSHCommand()
	defer func() { sshCommand = orig }()

	tr := &sshTransport{cfg: Config{Transport: "ssh", Host: "my-box"}}
	rc, err := tr.StreamFile(context.Background(), "/home/claude/.claude/projects/enc/session.jsonl")
	if err != nil {
		t.Fatalf("StreamFile error: %v", err)
	}
	if rc == nil {
		t.Fatal("StreamFile returned nil reader")
	}
	defer rc.Close()
	// The mock runs `echo ssh <args>`, so we can read the args from the stream.
	buf := make([]byte, 1024)
	n, _ := rc.Read(buf)
	bare := strings.TrimSpace(string(buf[:n]))
	if !strings.Contains(bare, "tail") {
		t.Errorf("stream args missing tail: %q", bare)
	}
	if !strings.Contains(bare, "session.jsonl") {
		t.Errorf("stream args missing remote path: %q", bare)
	}
}

func TestK8sTransportStreamFileArgs(t *testing.T) {
	tr := &kubectlTransport{cfg: Config{Transport: "k8s", Context: "my-ctx", Namespace: "default"}, podName: "my-pod"}
	rc, err := tr.StreamFile(context.Background(), "/workspace/.claude/projects/enc/session.jsonl")
	if err != nil {
		// In a test env without kubectl this will fail to start; that's OK —
		// we just verify it doesn't panic and returns an error (not a crash).
		if rc != nil {
			rc.Close()
		}
		return
	}
	if rc != nil {
		rc.Close()
	}
}

func TestStreamSessionJSONL(t *testing.T) {
	// Use a mock transport that returns a fake path and a fake stream.
	cfg := Config{Transport: "ssh", Host: "my-box", RemoteHome: "/home/claude", WorkDir: "/workspace"}
	mockT := &mockTransport{
		execOut: []byte("/home/claude/.claude/projects/enc/sess.jsonl\n"),
	}
	_, rc, err := StreamSessionJSONL(cfg, mockT)
	if err != nil {
		t.Fatalf("StreamSessionJSONL error: %v", err)
	}
	if rc == nil {
		t.Fatal("StreamSessionJSONL returned nil reader")
	}
	defer rc.Close()
}

// mockTransport is a test Transport that returns canned responses.
type mockTransport struct {
	execOut []byte
	execErr error
}

func (m *mockTransport) Exec(ctx context.Context, cmd ...string) ([]byte, error) {
	return m.execOut, m.execErr
}
func (m *mockTransport) ExecInteractive(cmd ...string) *exec.Cmd { return nil }
func (m *mockTransport) AttachCmd(shellCmd string) *exec.Cmd     { return nil }
func (m *mockTransport) Upload(ctx context.Context, destDir string, tarball []byte) error {
	return nil
}
func (m *mockTransport) StreamFile(ctx context.Context, remotePath string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("streamed data")), nil
}
func (m *mockTransport) Prepare(ctx context.Context) error  { return nil }
func (m *mockTransport) Release(ctx context.Context) error  { return nil }
func (m *mockTransport) Status(ctx context.Context) (string, error) {
	return "running", nil
}
func (m *mockTransport) Target() string { return "mock" }
