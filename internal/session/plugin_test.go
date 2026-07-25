package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// setupPluginDir creates a minimal ~/.claude/plugins structure for testing.
func setupPluginDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	os.MkdirAll(pluginsDir, 0755)
	return dir
}

func writeJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0755)
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, data, 0644)
}

func TestScanPluginsEmpty(t *testing.T) {
	dir := setupPluginDir(t)
	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Plugins) != 0 {
		t.Errorf("expected 0 plugins, got %d", len(tree.Plugins))
	}
}

func TestScanPluginsNoDir(t *testing.T) {
	dir := t.TempDir()
	// No plugins/ dir at all — should return empty, no error
	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Plugins) != 0 {
		t.Errorf("expected 0 plugins, got %d", len(tree.Plugins))
	}
}

func TestScanPluginsInstalled(t *testing.T) {
	dir := setupPluginDir(t)

	// Create a plugin install directory with components
	installPath := filepath.Join(dir, "plugins", "cache", "my-mkt", "my-plugin", "v1")
	os.MkdirAll(filepath.Join(installPath, "agents"), 0755)
	os.WriteFile(filepath.Join(installPath, "agents", "planner.md"), []byte("# Planner agent"), 0644)
	os.MkdirAll(filepath.Join(installPath, "skills"), 0755)
	os.WriteFile(filepath.Join(installPath, "skills", "deploy.md"), []byte("# Deploy skill"), 0644)
	os.MkdirAll(filepath.Join(installPath, "hooks"), 0755)
	os.WriteFile(filepath.Join(installPath, "hooks", "pre-commit.sh"), []byte("#!/bin/bash\necho ok"), 0644)

	// Write installed_plugins.json
	installed := map[string]interface{}{
		"version": 1,
		"plugins": map[string]interface{}{
			"my-plugin@my-mkt": []interface{}{
				map[string]string{
					"scope":       "user",
					"installPath": installPath,
					"version":     "1.0.0",
					"installedAt": "2025-06-01T00:00:00Z",
					"lastUpdated": "2025-06-15T00:00:00Z",
				},
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), installed)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(tree.Plugins))
	}

	p := tree.Plugins[0]
	if p.Name != "my-plugin" {
		t.Errorf("expected name 'my-plugin', got %q", p.Name)
	}
	if p.Marketplace != "my-mkt" {
		t.Errorf("expected marketplace 'my-mkt', got %q", p.Marketplace)
	}
	if !p.Installed {
		t.Error("expected Installed=true")
	}
	if p.Install.Scope != "user" {
		t.Errorf("expected scope 'user', got %q", p.Install.Scope)
	}

	// Check components were scanned
	compTypes := map[string]int{}
	for _, c := range p.Components {
		compTypes[c.Type]++
	}
	if compTypes["agent"] != 1 {
		t.Errorf("expected 1 agent, got %d", compTypes["agent"])
	}
	if compTypes["skill"] != 1 {
		t.Errorf("expected 1 skill, got %d", compTypes["skill"])
	}
	if compTypes["hook"] != 1 {
		t.Errorf("expected 1 hook, got %d", compTypes["hook"])
	}
}

func TestScanPluginsManifest(t *testing.T) {
	dir := setupPluginDir(t)

	installPath := filepath.Join(dir, "plugins", "cache", "mkt", "fancy-tool", "v2")
	os.MkdirAll(filepath.Join(installPath, ".claude-plugin"), 0755)
	manifest := PluginManifest{
		Name:        "fancy-tool",
		Description: "A fancy tool for testing",
		Version:     "2.0.0",
		Category:    "testing",
		Strict:      true,
	}
	manifest.Author.Name = "Test Author"
	manifest.Author.Email = "test@example.com"
	writeJSON(t, filepath.Join(installPath, ".claude-plugin", "plugin.json"), manifest)

	installed := map[string]interface{}{
		"version": 1,
		"plugins": map[string]interface{}{
			"fancy-tool@mkt": []interface{}{
				map[string]string{
					"scope":       "user",
					"installPath": installPath,
					"version":     "2.0.0",
				},
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), installed)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(tree.Plugins))
	}

	p := tree.Plugins[0]
	if p.Manifest == nil {
		t.Fatal("expected manifest to be parsed")
	}
	if p.Manifest.Description != "A fancy tool for testing" {
		t.Errorf("unexpected description: %q", p.Manifest.Description)
	}
	if p.Manifest.Version != "2.0.0" {
		t.Errorf("unexpected version: %q", p.Manifest.Version)
	}
	if !p.Manifest.Strict {
		t.Error("expected strict=true")
	}
	if p.Manifest.Author.Name != "Test Author" {
		t.Errorf("unexpected author: %q", p.Manifest.Author.Name)
	}
}

func TestScanPluginsBlocklist(t *testing.T) {
	dir := setupPluginDir(t)

	installPath := filepath.Join(dir, "plugins", "cache", "mkt", "bad-plugin", "v1")
	os.MkdirAll(installPath, 0755)

	installed := map[string]interface{}{
		"version": 1,
		"plugins": map[string]interface{}{
			"bad-plugin@mkt": []interface{}{
				map[string]string{
					"scope":       "user",
					"installPath": installPath,
				},
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), installed)

	blocklist := map[string]interface{}{
		"plugins": []interface{}{
			map[string]string{
				"plugin": "bad-plugin@mkt",
				"reason": "security",
				"text":   "contains malicious code",
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "blocklist.json"), blocklist)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(tree.Plugins))
	}

	p := tree.Plugins[0]
	if !p.Blocked {
		t.Error("expected Blocked=true")
	}
	if p.BlockReason != "security: contains malicious code" {
		t.Errorf("unexpected block reason: %q", p.BlockReason)
	}
}

func TestScanPluginsMarketplace(t *testing.T) {
	dir := setupPluginDir(t)

	// Create marketplace with plugins/ subdirectory strategy
	mktPath := filepath.Join(dir, "plugins", "marketplaces", "test-mkt")
	pluginDir := filepath.Join(mktPath, "plugins", "cool-tool")
	os.MkdirAll(filepath.Join(pluginDir, "agents"), 0755)
	os.WriteFile(filepath.Join(pluginDir, "agents", "helper.md"), []byte("# Helper"), 0644)
	os.MkdirAll(filepath.Join(pluginDir, "commands"), 0755)
	os.WriteFile(filepath.Join(pluginDir, "commands", "deploy.md"), []byte("# Deploy"), 0644)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(tree.Plugins))
	}

	p := tree.Plugins[0]
	if p.Name != "cool-tool" {
		t.Errorf("expected name 'cool-tool', got %q", p.Name)
	}
	if p.Marketplace != "test-mkt" {
		t.Errorf("expected marketplace 'test-mkt', got %q", p.Marketplace)
	}
	if p.Installed {
		t.Error("expected Installed=false for marketplace plugin")
	}

	compTypes := map[string]int{}
	for _, c := range p.Components {
		compTypes[c.Type]++
	}
	if compTypes["agent"] != 1 {
		t.Errorf("expected 1 agent, got %d", compTypes["agent"])
	}
	if compTypes["command"] != 1 {
		t.Errorf("expected 1 command, got %d", compTypes["command"])
	}
}

func TestScanPluginsMarketplaceManifest(t *testing.T) {
	dir := setupPluginDir(t)

	// Create marketplace with marketplace.json sub-plugin strategy
	mktPath := filepath.Join(dir, "plugins", "marketplaces", "sub-mkt")
	os.MkdirAll(filepath.Join(mktPath, ".claude-plugin"), 0755)
	os.MkdirAll(filepath.Join(mktPath, "agents"), 0755)
	os.WriteFile(filepath.Join(mktPath, "agents", "bot.md"), []byte("# Bot"), 0644)

	manifest := map[string]interface{}{
		"plugins": []interface{}{
			map[string]interface{}{
				"name":        "sub-tool",
				"description": "A sub-plugin",
				"version":     "1.0.0",
				"agents":      []string{"./agents/bot.md"},
			},
		},
	}
	writeJSON(t, filepath.Join(mktPath, ".claude-plugin", "marketplace.json"), manifest)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(tree.Plugins))
	}

	p := tree.Plugins[0]
	if p.Name != "sub-tool" {
		t.Errorf("expected name 'sub-tool', got %q", p.Name)
	}
	if p.Manifest == nil {
		t.Fatal("expected manifest")
	}
	if p.Manifest.Description != "A sub-plugin" {
		t.Errorf("unexpected description: %q", p.Manifest.Description)
	}

	// Components should be resolved from sub-plugin paths
	if len(p.Components) != 1 {
		t.Fatalf("expected 1 component, got %d", len(p.Components))
	}
	if p.Components[0].Type != "agent" {
		t.Errorf("expected type 'agent', got %q", p.Components[0].Type)
	}
}

func TestScanPluginsSubPlugins(t *testing.T) {
	dir := setupPluginDir(t)

	installPath := filepath.Join(dir, "plugins", "cache", "mkt", "multi-plugin", "v1")
	os.MkdirAll(filepath.Join(installPath, ".claude-plugin"), 0755)
	os.MkdirAll(filepath.Join(installPath, "agents"), 0755)
	os.WriteFile(filepath.Join(installPath, "agents", "a1.md"), []byte("# Agent 1"), 0644)
	os.WriteFile(filepath.Join(installPath, "agents", "a2.md"), []byte("# Agent 2"), 0644)
	os.MkdirAll(filepath.Join(installPath, "skills"), 0755)
	os.WriteFile(filepath.Join(installPath, "skills", "s1.md"), []byte("# Skill 1"), 0644)

	manifest := map[string]interface{}{
		"plugins": []interface{}{
			map[string]interface{}{
				"name":        "plugin-a",
				"description": "First sub-plugin",
				"version":     "1.0.0",
				"agents":      []string{"./agents/a1.md"},
			},
			map[string]interface{}{
				"name":        "plugin-b",
				"description": "Second sub-plugin",
				"version":     "1.1.0",
				"agents":      []string{"./agents/a2.md"},
				"skills":      []string{"./skills/s1.md"},
			},
		},
	}
	writeJSON(t, filepath.Join(installPath, ".claude-plugin", "marketplace.json"), manifest)

	installed := map[string]interface{}{
		"version": 1,
		"plugins": map[string]interface{}{
			"multi-plugin@mkt": []interface{}{
				map[string]string{
					"scope":       "user",
					"installPath": installPath,
				},
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), installed)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(tree.Plugins))
	}

	p := tree.Plugins[0]
	if len(p.SubPlugins) != 2 {
		t.Fatalf("expected 2 sub-plugins, got %d", len(p.SubPlugins))
	}
	if p.SubPlugins[0].Name != "plugin-a" {
		t.Errorf("expected sub-plugin 'plugin-a', got %q", p.SubPlugins[0].Name)
	}
	if p.SubPlugins[1].Name != "plugin-b" {
		t.Errorf("expected sub-plugin 'plugin-b', got %q", p.SubPlugins[1].Name)
	}
	if len(p.SubPlugins[1].Components) != 2 {
		t.Errorf("expected 2 components in plugin-b, got %d", len(p.SubPlugins[1].Components))
	}
}

func TestScanPluginsKnownMarketplaces(t *testing.T) {
	dir := setupPluginDir(t)

	marketplaces := map[string]interface{}{
		"official": map[string]interface{}{
			"source": map[string]string{
				"source": "github",
				"repo":   "anthropics/claude-plugins",
			},
		},
		"custom": map[string]interface{}{
			"source": map[string]string{
				"source": "git",
				"url":    "https://example.com/plugins.git",
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "known_marketplaces.json"), marketplaces)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Marketplaces) != 2 {
		t.Fatalf("expected 2 marketplaces, got %d", len(tree.Marketplaces))
	}
	if tree.Marketplaces["official"].SourceType != "github" {
		t.Errorf("expected github source, got %q", tree.Marketplaces["official"].SourceType)
	}
	if tree.Marketplaces["official"].SourceURL != "anthropics/claude-plugins" {
		t.Errorf("unexpected URL: %q", tree.Marketplaces["official"].SourceURL)
	}
	if tree.Marketplaces["custom"].SourceURL != "https://example.com/plugins.git" {
		t.Errorf("unexpected URL: %q", tree.Marketplaces["custom"].SourceURL)
	}
}

func TestScanPluginsSortOrder(t *testing.T) {
	dir := setupPluginDir(t)

	// Installed plugin
	installPath := filepath.Join(dir, "plugins", "cache", "beta-mkt", "installed-plugin", "v1")
	os.MkdirAll(installPath, 0755)

	installed := map[string]interface{}{
		"version": 1,
		"plugins": map[string]interface{}{
			"installed-plugin@beta-mkt": []interface{}{
				map[string]string{"scope": "user", "installPath": installPath},
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), installed)

	// Available plugin from marketplace (sorts after installed)
	mktPath := filepath.Join(dir, "plugins", "marketplaces", "alpha-mkt")
	pluginDir := filepath.Join(mktPath, "plugins", "avail-plugin")
	os.MkdirAll(pluginDir, 0755)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Plugins) != 2 {
		t.Fatalf("expected 2 plugins, got %d", len(tree.Plugins))
	}

	// Installed should come first regardless of marketplace name
	if !tree.Plugins[0].Installed {
		t.Error("expected first plugin to be installed")
	}
	if tree.Plugins[1].Installed {
		t.Error("expected second plugin to be available (not installed)")
	}
}

func TestScanPluginsAllComponentTypes(t *testing.T) {
	dir := setupPluginDir(t)

	installPath := filepath.Join(dir, "plugins", "cache", "mkt", "full-plugin", "v1")

	// Create every component type
	for _, cd := range componentDirs {
		cdPath := filepath.Join(installPath, cd.Dir)
		os.MkdirAll(cdPath, 0755)
		ext := ".txt"
		if len(cd.Exts) > 0 {
			ext = cd.Exts[0]
		}
		os.WriteFile(filepath.Join(cdPath, "test"+ext), []byte("content"), 0644)
	}

	installed := map[string]interface{}{
		"version": 1,
		"plugins": map[string]interface{}{
			"full-plugin@mkt": []interface{}{
				map[string]string{"scope": "user", "installPath": installPath},
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), installed)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(tree.Plugins))
	}

	compTypes := map[string]bool{}
	for _, c := range tree.Plugins[0].Components {
		compTypes[c.Type] = true
	}

	for _, cd := range componentDirs {
		if !compTypes[cd.Type] {
			t.Errorf("missing component type %q", cd.Type)
		}
	}
}

func TestScanPluginsLSPFromManifest(t *testing.T) {
	dir := setupPluginDir(t)

	installPath := filepath.Join(dir, "plugins", "cache", "mkt", "lsp-plugin", "v1")
	os.MkdirAll(filepath.Join(installPath, ".claude-plugin"), 0755)

	manifest := map[string]interface{}{
		"name":    "lsp-plugin",
		"version": "1.0.0",
		"lspServers": map[string]interface{}{
			"typescript-lsp": map[string]string{"command": "tsserver"},
			"python-lsp":     map[string]string{"command": "pylsp"},
		},
	}
	writeJSON(t, filepath.Join(installPath, ".claude-plugin", "plugin.json"), manifest)

	installed := map[string]interface{}{
		"version": 1,
		"plugins": map[string]interface{}{
			"lsp-plugin@mkt": []interface{}{
				map[string]string{"scope": "user", "installPath": installPath},
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), installed)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	p := tree.Plugins[0]
	lspCount := 0
	for _, c := range p.Components {
		if c.Type == "lsp" {
			lspCount++
		}
	}
	if lspCount != 2 {
		t.Errorf("expected 2 LSP components, got %d", lspCount)
	}
}

func TestSplitPluginID(t *testing.T) {
	tests := []struct {
		id   string
		name string
		mkt  string
	}{
		{"my-plugin@marketplace", "my-plugin", "marketplace"},
		{"plugin-no-mkt", "plugin-no-mkt", ""},
		{"multi@at@signs", "multi@at", "signs"},
		{"@leading-at", "@leading-at", ""}, // idx=0 not > 0, treated as no separator
	}
	for _, tt := range tests {
		name, mkt := splitPluginID(tt.id)
		if name != tt.name || mkt != tt.mkt {
			t.Errorf("splitPluginID(%q) = (%q, %q), want (%q, %q)", tt.id, name, mkt, tt.name, tt.mkt)
		}
	}
}

func TestScanPluginsRecursiveComponents(t *testing.T) {
	dir := setupPluginDir(t)

	installPath := filepath.Join(dir, "plugins", "cache", "mkt", "nested-plugin", "v1")
	// Create nested agent directory
	nestedDir := filepath.Join(installPath, "agents", "sub", "deep")
	os.MkdirAll(nestedDir, 0755)
	os.WriteFile(filepath.Join(nestedDir, "nested-agent.md"), []byte("# Nested"), 0644)
	// Also a top-level agent
	os.WriteFile(filepath.Join(installPath, "agents", "top.md"), []byte("# Top"), 0644)

	installed := map[string]interface{}{
		"version": 1,
		"plugins": map[string]interface{}{
			"nested-plugin@mkt": []interface{}{
				map[string]string{"scope": "user", "installPath": installPath},
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), installed)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	agentCount := 0
	for _, c := range tree.Plugins[0].Components {
		if c.Type == "agent" {
			agentCount++
		}
	}
	if agentCount != 2 {
		t.Errorf("expected 2 agents (top + nested), got %d", agentCount)
	}
}

func TestScanPluginsEnabledFromSettings(t *testing.T) {
	dir := setupPluginDir(t)

	// Create two installed plugins
	installA := filepath.Join(dir, "plugins", "cache", "mkt", "plugin-a", "v1")
	installB := filepath.Join(dir, "plugins", "cache", "mkt", "plugin-b", "v1")
	os.MkdirAll(installA, 0755)
	os.MkdirAll(installB, 0755)

	installed := map[string]interface{}{
		"version": 1,
		"plugins": map[string]interface{}{
			"plugin-a@mkt": []interface{}{
				map[string]string{"scope": "user", "installPath": installA},
			},
			"plugin-b@mkt": []interface{}{
				map[string]string{"scope": "user", "installPath": installB},
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), installed)

	// settings.json: plugin-a enabled, plugin-b disabled
	settings := map[string]interface{}{
		"enabledPlugins": map[string]bool{
			"plugin-a@mkt": true,
			"plugin-b@mkt": false,
		},
	}
	writeJSON(t, filepath.Join(dir, "settings.json"), settings)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Plugins) != 2 {
		t.Fatalf("expected 2 plugins, got %d", len(tree.Plugins))
	}

	byName := map[string]Plugin{}
	for _, p := range tree.Plugins {
		byName[p.Name] = p
	}

	if !byName["plugin-a"].Enabled {
		t.Error("expected plugin-a to be enabled")
	}
	if byName["plugin-b"].Enabled {
		t.Error("expected plugin-b to be disabled")
	}
}

func TestScanPluginsEnabledDefaultNoSettings(t *testing.T) {
	dir := setupPluginDir(t)

	// Installed plugin with no settings.json
	installPath := filepath.Join(dir, "plugins", "cache", "mkt", "default-plugin", "v1")
	os.MkdirAll(installPath, 0755)

	installed := map[string]interface{}{
		"version": 1,
		"plugins": map[string]interface{}{
			"default-plugin@mkt": []interface{}{
				map[string]string{"scope": "user", "installPath": installPath},
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), installed)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(tree.Plugins))
	}

	// Default: installed + not blocked = enabled
	if !tree.Plugins[0].Enabled {
		t.Error("expected default-enabled for installed, non-blocked plugin")
	}
}

func TestScanPluginsBlockedNotEnabled(t *testing.T) {
	dir := setupPluginDir(t)

	installPath := filepath.Join(dir, "plugins", "cache", "mkt", "blocked-plugin", "v1")
	os.MkdirAll(installPath, 0755)

	installed := map[string]interface{}{
		"version": 1,
		"plugins": map[string]interface{}{
			"blocked-plugin@mkt": []interface{}{
				map[string]string{"scope": "user", "installPath": installPath},
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), installed)

	blocklist := map[string]interface{}{
		"plugins": []interface{}{
			map[string]string{
				"plugin": "blocked-plugin@mkt",
				"reason": "security",
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "blocklist.json"), blocklist)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	p := tree.Plugins[0]
	if !p.Blocked {
		t.Error("expected blocked")
	}
	if p.Enabled {
		t.Error("expected blocked plugin to NOT be enabled by default")
	}
}

func TestScanPluginsInstalledSkipsMarketplaceDuplicates(t *testing.T) {
	dir := setupPluginDir(t)

	// Plugin is both installed and exists in marketplace
	installPath := filepath.Join(dir, "plugins", "cache", "my-mkt", "dupe-plugin", "v1")
	os.MkdirAll(installPath, 0755)

	installed := map[string]interface{}{
		"version": 1,
		"plugins": map[string]interface{}{
			"dupe-plugin@my-mkt": []interface{}{
				map[string]string{"scope": "user", "installPath": installPath},
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), installed)

	// Same plugin in marketplace
	mktPluginDir := filepath.Join(dir, "plugins", "marketplaces", "my-mkt", "plugins", "dupe-plugin")
	os.MkdirAll(mktPluginDir, 0755)

	tree, err := ScanPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Should appear only once (installed version wins)
	if len(tree.Plugins) != 1 {
		t.Fatalf("expected 1 plugin (no duplicates), got %d", len(tree.Plugins))
	}
	if !tree.Plugins[0].Installed {
		t.Error("expected the installed version to be kept")
	}
}
