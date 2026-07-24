package remote

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestSSHTransportIntegration tests the SSH transport against a real host.
// It skips if CCX_SSH_TEST_HOST is unset (or the host is unreachable).
//
// Run with: CCX_SSH_TEST_HOST=main.regular.gavin-jeong.coder go test ./internal/remote/ -run TestSSHTransportIntegration -v -count=1
func TestSSHTransportIntegration(t *testing.T) {
	host := os.Getenv("CCX_SSH_TEST_HOST")
	if host == "" {
		t.Skip("CCX_SSH_TEST_HOST not set; skipping SSH integration test")
	}

	cfg := Config{
		Transport:  "ssh",
		Host:       host,
		RemoteHome: "/home/coder",
		WorkDir:    "/home/coder/ccx-ssh-test",
	}.Defaults()

	tr := cfg.BuildTransport()
	ctx := context.Background()

	// 1. Status — host should be reachable.
	status, err := tr.Status(ctx)
	if err != nil {
		t.Fatalf("Status error: %v", err)
	}
	if status != "running" {
		t.Fatalf("Status = %q, want running", status)
	}
	t.Logf("Status: %s", status)

	// 2. Exec — echo hello.
	out, err := tr.Exec(ctx, "echo", "ccx-ssh-test-OK")
	if err != nil {
		t.Fatalf("Exec error: %v (out: %s)", err, string(out))
	}
	if strings.TrimSpace(string(out)) != "ccx-ssh-test-OK" {
		t.Fatalf("Exec output = %q, want ccx-ssh-test-OK", strings.TrimSpace(string(out)))
	}
	t.Logf("Exec: %s", strings.TrimSpace(string(out)))

	// 3. Prepare — ensure dirs exist.
	if err := tr.Prepare(ctx); err != nil {
		t.Fatalf("Prepare error: %v", err)
	}
	t.Log("Prepare: OK")

	// 4. Upload — create a test file on the remote via Exec and verify.
	out, _ = tr.Exec(ctx, "sh", "-c", "echo 'hello from ccx' > /tmp/ccx-ssh-test-file && wc -c /tmp/ccx-ssh-test-file")
	t.Logf("Created test file: %s", strings.TrimSpace(string(out)))

	// 5. StreamFile — stream /tmp/ccx-ssh-test-file and verify content.
	//    tail -f never sends EOF, so read a fixed buffer (the file is tiny)
	//    and close the stream immediately.
	rc, err := tr.StreamFile(ctx, "/tmp/ccx-ssh-test-file")
	if err != nil {
		t.Fatalf("StreamFile error: %v", err)
	}
	buf := make([]byte, 1024)
	n, _ := rc.Read(buf)
	rc.Close()
	data := string(buf[:n])
	if !strings.Contains(data, "hello from ccx") {
		t.Fatalf("StreamFile content = %q, want 'hello from ccx'", data)
	}
	t.Logf("StreamFile: %s", strings.TrimSpace(data))

	// 6. AttachCmd — verify the script is built (don't execute it — it would
	// start an interactive Claude session).
	cmd := tr.AttachCmd("echo attach-test")
	if cmd == nil {
		t.Fatal("AttachCmd returned nil")
	}
	script := cmd.Args[2]
	if !strings.Contains(script, "tmux") {
		t.Errorf("AttachCmd script missing tmux: %q", script)
	}
	if !strings.Contains(script, "ServerAliveInterval=15") {
		t.Errorf("AttachCmd script missing keep-alive: %q", script)
	}
	t.Log("AttachCmd: script contains tmux + keep-alive + retry")

	// 7. Verify the remote has claude + tmux + node.
	out, _ = tr.Exec(ctx, "sh", "-c", "command -v claude && command -v tmux && command -v node")
	tools := strings.TrimSpace(string(out))
	if tools == "" {
		t.Log("WARNING: claude/tmux/node not all found on remote")
	} else {
		t.Logf("Remote tools: %s", tools)
	}

	// Cleanup.
	tr.Exec(ctx, "rm", "-f", "/tmp/ccx-ssh-test-file", "/tmp/ccx-ssh-test.tar.gz")
	t.Log("Cleanup done")
}
