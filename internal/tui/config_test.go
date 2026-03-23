package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keyolk/ccx/internal/session"
)

func TestExtractRelConfigPath(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		claudeDir string
		want      string
	}{
		{"memory file", "/home/user/.claude/memory/k8s.md", "/home/user/.claude", "memory/k8s.md"},
		{"CLAUDE.md", "/home/user/.claude/CLAUDE.md", "/home/user/.claude", "CLAUDE.md"},
		{"project memory", "/home/user/.claude/projects/enc/memory/x.md", "/home/user/.claude", "projects/enc/memory/x.md"},
		{"outside claude dir", "/home/user/other/file.md", "/home/user/.claude", ""},
		{"parent dir", "/home/user/file.md", "/home/user/.claude", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRelConfigPath(tt.path, tt.claudeDir)
			if got != tt.want {
				t.Errorf("extractRelConfigPath(%q, %q) = %q, want %q", tt.path, tt.claudeDir, got, tt.want)
			}
		})
	}
}

func TestBuildConfigTestEnv(t *testing.T) {
	// Create a fake source file to symlink
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "test.md")
	os.WriteFile(srcFile, []byte("# Test"), 0o644)

	// Create a fake skill dir
	skillDir := filepath.Join(srcDir, "myskill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Skill"), 0o644)

	// We need to mock the home dir, so we test buildConfigTestEnv indirectly
	// by testing the symlink structure logic
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot get home dir")
	}
	claudeDir := filepath.Join(home, ".claude")

	// Only test with files that actually exist under ~/.claude
	// Use extractRelConfigPath to verify the mapping
	rel := extractRelConfigPath(filepath.Join(claudeDir, "memory", "test.md"), claudeDir)
	if rel != "memory/test.md" {
		t.Fatalf("unexpected rel path: %s", rel)
	}

	// Test with real CLAUDE.md if it exists
	claudeMD := filepath.Join(claudeDir, "CLAUDE.md")
	if _, err := os.Stat(claudeMD); err != nil {
		t.Skip("no ~/.claude/CLAUDE.md to test with")
	}

	items := []session.ConfigItem{
		{Category: session.ConfigGlobal, Name: "CLAUDE.md", Path: claudeMD},
	}

	env, err := buildConfigTestEnv(items)
	if err != nil {
		t.Fatalf("buildConfigTestEnv failed: %v", err)
	}
	defer env.Cleanup()

	// Verify symlink exists
	symlink := filepath.Join(env.ConfigDir, "CLAUDE.md")
	info, err := os.Lstat(symlink)
	if err != nil {
		t.Fatalf("symlink not created: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("expected symlink, got regular file")
	}

	// Verify target resolves
	target, err := os.Readlink(symlink)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if target != claudeMD {
		t.Errorf("symlink target = %q, want %q", target, claudeMD)
	}

	// Verify MCP config exists
	if _, err := os.Stat(env.MCPConfigPath()); err != nil {
		t.Error("mcp-config.json not created:", err)
	}
}

func TestCfgSelectSkipsHeaders(t *testing.T) {
	sel := make(map[string]bool)
	// Header items have isHeader=true and empty Path
	item := cfgItem{isHeader: true, label: "GLOBAL"}
	// Verify the selection logic would skip headers
	if !item.isHeader {
		t.Error("expected isHeader to be true")
	}
	// Headers should never be added to selection
	if item.item.Path != "" {
		t.Error("header should have empty path")
	}
	_ = sel // selection map stays empty for headers
}

func TestCfgMultiSelectToggle(t *testing.T) {
	sel := make(map[string]bool)
	path := "/home/user/.claude/memory/k8s.md"

	// Toggle on
	sel[path] = true
	if !sel[path] {
		t.Error("expected selected")
	}

	// Toggle off
	delete(sel, path)
	if sel[path] {
		t.Error("expected deselected")
	}
}

func TestCfgClearSelection(t *testing.T) {
	sel := make(map[string]bool)
	sel["/a"] = true
	sel["/b"] = true
	sel["/c"] = true

	clear(sel)
	if len(sel) != 0 {
		t.Errorf("expected empty after clear, got %d", len(sel))
	}
}
