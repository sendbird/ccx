package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCCXConfigLoadsClaudeCommandTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("claude:\n  command_template: \"ccproxy -- claude {{args}}\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, _, _, cc := LoadCCXConfig(path)
	if cc.CommandTemplate != "ccproxy -- claude {{args}}" {
		t.Fatalf("CommandTemplate = %q", cc.CommandTemplate)
	}
}
