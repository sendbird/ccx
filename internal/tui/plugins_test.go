package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/sendbird/ccx/internal/session"
)

func testPlugin(name, marketplace string, installed, blocked bool, compTypes ...string) session.Plugin {
	p := session.Plugin{
		ID:          name + "@" + marketplace,
		Name:        name,
		Marketplace: marketplace,
		Installed:   installed,
		Blocked:     blocked,
		Enabled:     installed && !blocked, // default: installed + not blocked = enabled
	}
	for _, t := range compTypes {
		p.Components = append(p.Components, session.PluginComponent{Type: t, Name: t + "-file"})
	}
	return p
}

func testTree(plugins ...session.Plugin) *session.PluginTree {
	return &session.PluginTree{Plugins: plugins}
}

func countNonHeaders(items []list.Item) int {
	n := 0
	for _, item := range items {
		if pi, ok := item.(plgItem); ok && !pi.isHeader {
			n++
		}
	}
	return n
}

func TestBuildPluginItems(t *testing.T) {
	tree := testTree(
		testPlugin("alpha", "mkt1", true, false, "agent"),
		testPlugin("beta", "mkt1", true, false, "skill"),
		testPlugin("gamma", "mkt2", false, false, "hook"),
	)
	items := buildPluginItems(tree)

	nonHeaders := countNonHeaders(items)
	if nonHeaders != 3 {
		t.Errorf("expected 3 plugin items, got %d", nonHeaders)
	}

	// First non-header should be installed
	for _, item := range items {
		if pi, ok := item.(plgItem); ok && !pi.isHeader {
			if !pi.plugin.Installed {
				t.Error("expected first plugin to be installed")
			}
			break
		}
	}
}

func TestFilterPluginItemsTextSearch(t *testing.T) {
	tree := testTree(
		testPlugin("alpha-tool", "mkt", true, false),
		testPlugin("beta-util", "mkt", true, false),
		testPlugin("gamma-tool", "mkt", false, false),
	)
	items := buildPluginItems(tree)

	filtered := filterPluginItems(items, "tool")
	n := countNonHeaders(filtered)
	if n != 2 {
		t.Errorf("expected 2 plugins matching 'tool', got %d", n)
	}
}

func TestFilterPluginItemsIsInstalled(t *testing.T) {
	tree := testTree(
		testPlugin("a", "m", true, false),
		testPlugin("b", "m", true, false),
		testPlugin("c", "m", false, false),
	)
	items := buildPluginItems(tree)

	filtered := filterPluginItems(items, "is:installed")
	n := countNonHeaders(filtered)
	if n != 2 {
		t.Errorf("expected 2 installed plugins, got %d", n)
	}
}

func TestFilterPluginItemsIsAvailable(t *testing.T) {
	tree := testTree(
		testPlugin("a", "m", true, false),
		testPlugin("b", "m", false, false),
		testPlugin("c", "m", false, false),
	)
	items := buildPluginItems(tree)

	filtered := filterPluginItems(items, "is:available")
	n := countNonHeaders(filtered)
	if n != 2 {
		t.Errorf("expected 2 available plugins, got %d", n)
	}
}

func TestFilterPluginItemsIsEnabled(t *testing.T) {
	tree := testTree(
		testPlugin("good", "m", true, false),    // enabled (installed + not blocked)
		testPlugin("bad", "m", true, true),       // installed but blocked
		testPlugin("avail", "m", false, false),   // not installed
	)
	items := buildPluginItems(tree)

	filtered := filterPluginItems(items, "is:enabled")
	n := countNonHeaders(filtered)
	if n != 1 {
		t.Errorf("expected 1 enabled plugin, got %d", n)
	}
	// Verify it's the right one
	for _, item := range filtered {
		if pi, ok := item.(plgItem); ok && !pi.isHeader {
			if pi.plugin.Name != "good" {
				t.Errorf("expected 'good', got %q", pi.plugin.Name)
			}
		}
	}
}

func TestFilterPluginItemsIsDisabled(t *testing.T) {
	enabled := testPlugin("enabled", "m", true, false)
	disabled := testPlugin("disabled", "m", true, false)
	disabled.Enabled = false // explicitly disabled in settings.json
	tree := testTree(enabled, disabled)
	items := buildPluginItems(tree)

	filtered := filterPluginItems(items, "is:disabled")
	n := countNonHeaders(filtered)
	if n != 1 {
		t.Errorf("expected 1 disabled plugin, got %d", n)
	}
	for _, item := range filtered {
		if pi, ok := item.(plgItem); ok && !pi.isHeader {
			if pi.plugin.Name != "disabled" {
				t.Errorf("expected 'disabled', got %q", pi.plugin.Name)
			}
		}
	}
}

func TestFilterPluginItemsIsBlocked(t *testing.T) {
	tree := testTree(
		testPlugin("ok", "m", true, false),
		testPlugin("blocked", "m", true, true),
	)
	items := buildPluginItems(tree)

	filtered := filterPluginItems(items, "is:blocked")
	n := countNonHeaders(filtered)
	if n != 1 {
		t.Errorf("expected 1 blocked plugin, got %d", n)
	}
}

func TestFilterPluginItemsHasComponent(t *testing.T) {
	tree := testTree(
		testPlugin("with-agent", "m", true, false, "agent", "skill"),
		testPlugin("with-mcp", "m", true, false, "mcp"),
		testPlugin("empty", "m", true, false),
	)
	items := buildPluginItems(tree)

	filtered := filterPluginItems(items, "has:agent")
	n := countNonHeaders(filtered)
	if n != 1 {
		t.Errorf("expected 1 plugin with agent, got %d", n)
	}

	filtered = filterPluginItems(items, "has:mcp")
	n = countNonHeaders(filtered)
	if n != 1 {
		t.Errorf("expected 1 plugin with mcp, got %d", n)
	}

	filtered = filterPluginItems(items, "has:skill")
	n = countNonHeaders(filtered)
	if n != 1 {
		t.Errorf("expected 1 plugin with skill, got %d", n)
	}
}

func TestFilterPluginItemsMultipleTerms(t *testing.T) {
	tree := testTree(
		testPlugin("agent-tool", "m", true, false, "agent"),
		testPlugin("agent-blocked", "m", true, true, "agent"),
		testPlugin("mcp-tool", "m", true, false, "mcp"),
	)
	items := buildPluginItems(tree)

	// is:enabled AND has:agent → only agent-tool
	filtered := filterPluginItems(items, "is:enabled has:agent")
	n := countNonHeaders(filtered)
	if n != 1 {
		t.Errorf("expected 1 plugin matching 'is:enabled has:agent', got %d", n)
	}
}

func TestFilterPluginItemsEmptyTerm(t *testing.T) {
	tree := testTree(
		testPlugin("a", "m", true, false),
		testPlugin("b", "m", false, false),
	)
	items := buildPluginItems(tree)

	filtered := filterPluginItems(items, "")
	if len(filtered) != len(items) {
		t.Errorf("empty filter should return all items: got %d, want %d", len(filtered), len(items))
	}
}

func TestPluginSearchText(t *testing.T) {
	p := testPlugin("my-tool", "official", true, false, "agent", "skill")
	p.Manifest = &session.PluginManifest{
		Description: "A great tool",
	}
	p.SubPlugins = []session.SubPlugin{
		{Name: "sub-a", Description: "Sub-plugin A"},
	}

	text := pluginSearchText(p)

	for _, want := range []string{
		"my-tool", "official", "a great tool",
		"is:installed", "is:enabled",
		"has:agent", "has:skill",
		"sub-a", "sub-plugin a",
	} {
		if !plgContains(text, want) {
			t.Errorf("pluginSearchText missing %q in %q", want, text)
		}
	}

	for _, notWant := range []string{"is:available", "is:blocked", "is:disabled"} {
		if plgContains(text, notWant) {
			t.Errorf("pluginSearchText should not contain %q", notWant)
		}
	}
}

// plgContains checks substring membership (avoids collision with contains in live_preview_test.go).
func plgContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestComponentBadge(t *testing.T) {
	tests := []struct {
		name  string
		comps []session.PluginComponent
		want  string
	}{
		{"empty", nil, ""},
		{"single", []session.PluginComponent{{Type: "agent"}}, "[1a]"},
		{"multiple", []session.PluginComponent{
			{Type: "agent"}, {Type: "agent"}, {Type: "skill"}, {Type: "mcp"},
		}, "[2a 1s 1m]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := componentBadge(tt.comps)
			if got != tt.want {
				t.Errorf("componentBadge() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildComponentItems(t *testing.T) {
	p := session.Plugin{
		Components: []session.PluginComponent{
			{Type: "agent", Name: "a1.md"},
			{Type: "agent", Name: "a2.md"},
			{Type: "skill", Name: "s1.md"},
		},
		SubPlugins: []session.SubPlugin{
			{Name: "sub-a", Description: "First sub"},
		},
	}

	items := buildComponentItems(p)

	// Should have: Agents header, 2 agents, Skills header, 1 skill, Sub-plugins header, 1 sub-plugin
	headers := 0
	agents := 0
	skills := 0
	subs := 0
	for _, item := range items {
		ci := item.(plgCompItem)
		if ci.isHeader {
			headers++
		} else if ci.subPlugin != nil {
			subs++
		} else {
			switch ci.comp.Type {
			case "agent":
				agents++
			case "skill":
				skills++
			}
		}
	}
	if headers != 3 {
		t.Errorf("expected 3 headers, got %d", headers)
	}
	if agents != 2 {
		t.Errorf("expected 2 agents, got %d", agents)
	}
	if skills != 1 {
		t.Errorf("expected 1 skill, got %d", skills)
	}
	if subs != 1 {
		t.Errorf("expected 1 sub-plugin, got %d", subs)
	}
}

func TestBuildComponentItemsEmpty(t *testing.T) {
	p := session.Plugin{}
	items := buildComponentItems(p)
	if len(items) != 0 {
		t.Errorf("expected 0 items for empty plugin, got %d", len(items))
	}
}

func TestPlgHasSelection(t *testing.T) {
	set := map[string]bool{}
	if len(set) > 0 {
		t.Error("empty set should not have selection")
	}
	set["a@m"] = true
	if len(set) != 1 {
		t.Error("expected 1 selection")
	}
	delete(set, "a@m")
	if len(set) != 0 {
		t.Error("expected 0 after delete")
	}
}

func TestSelectedPlugins(t *testing.T) {
	tree := testTree(
		testPlugin("alpha", "m", true, false),
		testPlugin("beta", "m", true, false),
		testPlugin("gamma", "m", false, false),
	)

	selectedSet := map[string]bool{
		"alpha@m": true,
		"gamma@m": true,
	}

	var selected []session.Plugin
	for _, p := range tree.Plugins {
		if selectedSet[p.ID] {
			selected = append(selected, p)
		}
	}

	if len(selected) != 2 {
		t.Errorf("expected 2 selected, got %d", len(selected))
	}
}

func TestBuildPluginTestEnv(t *testing.T) {
	p := session.Plugin{
		ID:      "test-plugin@test-mkt",
		Name:    "test-plugin",
		Enabled: true,
	}

	env, err := buildPluginTestEnv([]session.Plugin{p})
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()

	// Check settings.json has enabledPlugins
	data, err := os.ReadFile(env.SettingsPath())
	if err != nil {
		t.Fatal("settings.json not created:", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal("invalid settings.json:", err)
	}
	ep, ok := settings["enabledPlugins"].(map[string]interface{})
	if !ok {
		t.Fatal("enabledPlugins not found in settings.json")
	}
	if enabled, ok := ep["test-plugin@test-mkt"].(bool); !ok || !enabled {
		t.Error("expected test-plugin@test-mkt to be enabled")
	}

	// Check installed_plugins.json was symlinked (if real file exists)
	ipPath := filepath.Join(env.ConfigDir, "plugins", "installed_plugins.json")
	if info, err := os.Lstat(ipPath); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			t.Error("expected installed_plugins.json to be a symlink")
		}
	}

	// Check MCP config exists
	if _, err := os.Stat(env.MCPConfigPath()); err != nil {
		t.Error("mcp-config.json not created:", err)
	}
}

func TestBuildPluginTestEnvAvailablePlugin(t *testing.T) {
	p := session.Plugin{
		ID:        "avail@mkt",
		Name:      "avail",
		Installed: false,
	}

	env, err := buildPluginTestEnv([]session.Plugin{p})
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()

	// Available (not installed) plugin should still appear in enabledPlugins
	data, _ := os.ReadFile(env.SettingsPath())
	var settings map[string]interface{}
	json.Unmarshal(data, &settings)
	ep := settings["enabledPlugins"].(map[string]interface{})
	if enabled, ok := ep["avail@mkt"].(bool); !ok || !enabled {
		t.Error("expected avail@mkt to be enabled in settings")
	}
}

func TestBuildComponentItemsSubPluginsOnly(t *testing.T) {
	p := session.Plugin{
		SubPlugins: []session.SubPlugin{
			{Name: "sub-a"},
			{Name: "sub-b"},
		},
	}
	items := buildComponentItems(p)

	// Should have: Sub-plugins header + 2 sub-plugin items
	if len(items) != 3 {
		t.Errorf("expected 3 items (1 header + 2 subs), got %d", len(items))
	}
}
