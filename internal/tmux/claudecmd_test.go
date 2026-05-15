package tmux

import (
	"testing"

	"github.com/sendbird/ccx/internal/claudecmd"
)

func TestClaudeWindowShellCommandUsesConfiguredTemplate(t *testing.T) {
	got, err := ClaudeWindowShellCommand("/tmp/a dir", claudecmd.Config{CommandTemplate: "ccproxy -- claude {{args}}"}, "--resume", "abc; touch /tmp/pwned")
	if err != nil {
		t.Fatalf("ClaudeWindowShellCommand failed: %v", err)
	}
	want := "cd '/tmp/a dir' && 'ccproxy' '--' 'claude' '--resume' 'abc; touch /tmp/pwned'"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
