package remote

import (
	"context"
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
