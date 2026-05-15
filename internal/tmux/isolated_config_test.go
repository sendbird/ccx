package tmux

import (
	"strings"
	"testing"

	"github.com/sendbird/ccx/internal/claudecmd"
)

func TestIsolatedEnvScriptWithConfigUsesClaudeTemplate(t *testing.T) {
	env, err := NewIsolatedEnv("ccx-isolated-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()

	script, err := env.ScriptWithConfig(claudecmd.Config{CommandTemplate: "ccproxy -- claude {{args}}"}, "--print")
	if err != nil {
		t.Fatalf("ScriptWithConfig failed: %v", err)
	}
	if !strings.Contains(script, "'ccproxy' '--' 'claude'") {
		t.Fatalf("script does not use wrapper: %s", script)
	}
	if !strings.Contains(script, "'--mcp-config'") || !strings.Contains(script, "'--strict-mcp-config'") || !strings.Contains(script, "'--print'") {
		t.Fatalf("script missing expected Claude args: %s", script)
	}
}
