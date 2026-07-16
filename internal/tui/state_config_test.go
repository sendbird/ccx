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

	_, _, _, _, cc, _ := LoadCCXConfig(path)
	if cc.CommandTemplate != "ccproxy -- claude {{args}}" {
		t.Fatalf("CommandTemplate = %q", cc.CommandTemplate)
	}
}

func TestLoadCCXConfigLoadsOpenCommandTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("open:\n  command_template: \"tmux-chrome open {{url}}\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, _, _, _, oc := LoadCCXConfig(path)
	if oc.CommandTemplate != "tmux-chrome open {{url}}" {
		t.Fatalf("CommandTemplate = %q", oc.CommandTemplate)
	}
}
