package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sendbird/ccx/internal/claudecmd"
	"github.com/sendbird/ccx/internal/opener"
)

func TestLoadCCXConfigLoadsClaudeCommandTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("claude:\n  command_template: \"ccproxy -- claude {{args}}\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, _, _, cc, _, _ := LoadCCXConfig(path)
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

	_, _, _, _, _, oc, _ := LoadCCXConfig(path)
	if oc.CommandTemplate != "tmux-chrome open {{url}}" {
		t.Fatalf("CommandTemplate = %q", oc.CommandTemplate)
	}
}

// TestLoadCCXConfigResolvesJumpTreeRegionCollision verifies a stale config that
// maps jump_to_tree onto the region-down key is repaired at load: jump_to_tree
// snaps back to its default ("o") so region navigation (K/J) keeps its keys.
func TestLoadCCXConfigResolvesJumpTreeRegionCollision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := "keymaps:\n  conversation:\n    jump_to_tree: J\n    region_up: K\n    region_down: J\n"
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	km, _, _, _, _, _, _ := LoadCCXConfig(path)
	if km.Conversation.RegionDown != "J" {
		t.Fatalf("RegionDown = %q, want J", km.Conversation.RegionDown)
	}
	want := DefaultKeymap().Conversation.JumpToTree
	if km.Conversation.JumpToTree != want {
		t.Fatalf("JumpToTree = %q, want %q (collision with region_down should reset it)", km.Conversation.JumpToTree, want)
	}
	if km.Conversation.JumpToTree == km.Conversation.RegionDown {
		t.Fatal("JumpToTree still collides with RegionDown after load")
	}
}

// TestSavePreferencesPreservesOpenAndClaude verifies that quitting (which
// re-marshals the whole config) does not drop the open/claude sections when the
// on-disk file has them empty — the running app's values backfill them.
func TestSavePreferencesPreservesOpenAndClaude(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)           // configPath() uses os.UserHomeDir()
	t.Setenv("XDG_CONFIG_HOME", "") // ensure ~/.config/ccx path

	// Simulate a file that has already lost its open/claude sections.
	confDir := filepath.Join(dir, ".config", "ccx")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(confDir, "config.yaml")
	if err := os.WriteFile(path, []byte("preferences:\n  group_mode: projects\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	SavePreferences(Preferences{GroupMode: "projects"},
		opener.Config{CommandTemplate: "tmux-chrome open {{url}}"},
		claudecmd.Config{})

	_, _, _, _, cc, oc, _ := LoadCCXConfig(path)
	if oc.CommandTemplate != "tmux-chrome open {{url}}" {
		t.Fatalf("open lost after save: CommandTemplate = %q", oc.CommandTemplate)
	}
	if cc.CommandTemplate != claudecmd.DefaultTemplate {
		t.Fatalf("claude stub not written: CommandTemplate = %q, want default", cc.CommandTemplate)
	}
}
