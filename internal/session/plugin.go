package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PluginInstall represents one installation entry from installed_plugins.json.
type PluginInstall struct {
	Scope        string
	InstallPath  string
	Version      string
	InstalledAt  time.Time
	LastUpdated  time.Time
	GitCommitSha string
}

// PluginManifest represents .claude-plugin/plugin.json contents.
type PluginManifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Category    string `json:"category"`
	Strict      bool   `json:"strict"`
	Source      string `json:"source"`
	Author      struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"author"`
	LspServers map[string]json.RawMessage `json:"lspServers"`
}

// SubPlugin is a named sub-plugin defined in a marketplace manifest.
type SubPlugin struct {
	Name        string
	Description string
	Version     string
	Components  []PluginComponent
}

// PluginComponent represents a discovered file within a plugin.
type PluginComponent struct {
	Type string // "agent", "hook", "command", "mcp", "skill", "script", "setting", "memory", "lsp"
	Name string
	Path string
	Size int64
}

// Plugin is a fully resolved plugin for display.
type Plugin struct {
	ID          string // "plugin-name@marketplace"
	Name        string // just the plugin name part
	Marketplace string // just the marketplace part
	Installed   bool
	Install     PluginInstall
	Manifest    *PluginManifest
	Components  []PluginComponent
	SubPlugins  []SubPlugin
	Blocked     bool
	BlockReason string
}

// MarketplaceInfo from known_marketplaces.json.
type MarketplaceInfo struct {
	SourceType string // "git" or "github"
	SourceURL  string // URL or "owner/repo"
}

// PluginTree holds all discovered plugins grouped by marketplace.
type PluginTree struct {
	Plugins      []Plugin
	Marketplaces map[string]MarketplaceInfo
}

// componentDirs defines all scanned component subdirectory names and their types.
var componentDirs = []struct {
	Dir  string
	Type string
	Exts []string // empty = all files
}{
	{"agents", "agent", []string{".md"}},
	{"hooks", "hook", []string{".py", ".sh", ".bash"}},
	{"commands", "command", []string{".md"}},
	{"mcps", "mcp", []string{".json"}},
	{"skills", "skill", []string{".md"}},
	{"scripts", "script", nil},
	{"settings", "setting", []string{".md", ".json", ".yaml", ".yml"}},
	{"memory", "memory", []string{".md"}},
}

// ScanPlugins discovers installed and available plugins from claudeDir/plugins/.
func ScanPlugins(claudeDir string) (*PluginTree, error) {
	pluginsDir := filepath.Join(claudeDir, "plugins")

	// Parse installed_plugins.json
	installed, err := parseInstalledPlugins(filepath.Join(pluginsDir, "installed_plugins.json"))
	if err != nil {
		return nil, err
	}

	// Parse blocklist
	blocklist := parseBlocklist(filepath.Join(pluginsDir, "blocklist.json"))

	// Parse marketplaces
	marketplaces := parseMarketplaces(filepath.Join(pluginsDir, "known_marketplaces.json"))

	// Build installed plugins
	seen := map[string]bool{} // plugin ID → true
	var plugins []Plugin

	for id, installs := range installed {
		if len(installs) == 0 {
			continue
		}
		inst := installs[0]
		name, mkt := splitPluginID(id)
		seen[id] = true

		p := Plugin{
			ID:          id,
			Name:        name,
			Marketplace: mkt,
			Installed:   true,
			Install:     inst,
		}

		// Read manifest
		p.Manifest = readPluginManifest(inst.InstallPath)

		// Scan components from install path
		p.Components = scanAllComponents(inst.InstallPath)

		// Parse sub-plugins from marketplace manifest
		p.SubPlugins = parseSubPlugins(inst.InstallPath)

		// If no top-level components found, collect from sub-plugins
		if len(p.Components) == 0 {
			for _, sp := range p.SubPlugins {
				p.Components = append(p.Components, sp.Components...)
			}
		}

		// LSP servers from manifest
		if p.Manifest != nil {
			for lspName := range p.Manifest.LspServers {
				p.Components = append(p.Components, PluginComponent{
					Type: "lsp",
					Name: lspName,
				})
			}
		}

		if reason, blocked := blocklist[id]; blocked {
			p.Blocked = true
			p.BlockReason = reason
		}

		plugins = append(plugins, p)
	}

	// Scan marketplace dirs for available (not-installed) plugins
	mktDir := filepath.Join(pluginsDir, "marketplaces")
	mktEntries, _ := os.ReadDir(mktDir)
	for _, mktEntry := range mktEntries {
		if !mktEntry.IsDir() {
			continue
		}
		mktName := mktEntry.Name()
		mktPath := filepath.Join(mktDir, mktName)

		// Check for marketplace manifest with sub-plugins
		available := discoverMarketplacePlugins(mktPath, mktName)
		for _, ap := range available {
			if seen[ap.ID] {
				continue
			}
			seen[ap.ID] = true

			if reason, blocked := blocklist[ap.ID]; blocked {
				ap.Blocked = true
				ap.BlockReason = reason
			}
			plugins = append(plugins, ap)
		}
	}

	// Sort: installed first, then by marketplace, then name
	sort.Slice(plugins, func(i, j int) bool {
		if plugins[i].Installed != plugins[j].Installed {
			return plugins[i].Installed
		}
		if plugins[i].Marketplace != plugins[j].Marketplace {
			return plugins[i].Marketplace < plugins[j].Marketplace
		}
		return plugins[i].Name < plugins[j].Name
	})

	return &PluginTree{
		Plugins:      plugins,
		Marketplaces: marketplaces,
	}, nil
}

// --- internal helpers ---

type rawInstalledPlugins struct {
	Version int                          `json:"version"`
	Plugins map[string][]json.RawMessage `json:"plugins"`
}

type rawInstall struct {
	Scope        string `json:"scope"`
	InstallPath  string `json:"installPath"`
	Version      string `json:"version"`
	InstalledAt  string `json:"installedAt"`
	LastUpdated  string `json:"lastUpdated"`
	GitCommitSha string `json:"gitCommitSha"`
}

func parseInstalledPlugins(path string) (map[string][]PluginInstall, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var raw rawInstalledPlugins
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	result := make(map[string][]PluginInstall, len(raw.Plugins))
	for id, entries := range raw.Plugins {
		var installs []PluginInstall
		for _, entry := range entries {
			var ri rawInstall
			if json.Unmarshal(entry, &ri) != nil {
				continue
			}
			inst := PluginInstall{
				Scope:        ri.Scope,
				InstallPath:  ri.InstallPath,
				Version:      ri.Version,
				GitCommitSha: ri.GitCommitSha,
			}
			inst.InstalledAt, _ = time.Parse(time.RFC3339, ri.InstalledAt)
			inst.LastUpdated, _ = time.Parse(time.RFC3339, ri.LastUpdated)
			installs = append(installs, inst)
		}
		result[id] = installs
	}
	return result, nil
}

func parseBlocklist(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw struct {
		Plugins []struct {
			Plugin string `json:"plugin"`
			Reason string `json:"reason"`
			Text   string `json:"text"`
		} `json:"plugins"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	result := make(map[string]string, len(raw.Plugins))
	for _, p := range raw.Plugins {
		reason := p.Reason
		if p.Text != "" {
			reason += ": " + p.Text
		}
		result[p.Plugin] = reason
	}
	return result
}

func parseMarketplaces(path string) map[string]MarketplaceInfo {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw map[string]struct {
		Source struct {
			Source string `json:"source"`
			URL    string `json:"url"`
			Repo   string `json:"repo"`
		} `json:"source"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	result := make(map[string]MarketplaceInfo, len(raw))
	for name, m := range raw {
		info := MarketplaceInfo{SourceType: m.Source.Source}
		if m.Source.URL != "" {
			info.SourceURL = m.Source.URL
		} else if m.Source.Repo != "" {
			info.SourceURL = m.Source.Repo
		}
		result[name] = info
	}
	return result
}

func splitPluginID(id string) (name, marketplace string) {
	if idx := strings.LastIndex(id, "@"); idx > 0 {
		return id[:idx], id[idx+1:]
	}
	return id, ""
}

func readPluginManifest(installPath string) *PluginManifest {
	for _, name := range []string{"plugin.json", "marketplace.json"} {
		path := filepath.Join(installPath, ".claude-plugin", name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m PluginManifest
		if json.Unmarshal(data, &m) == nil && m.Name != "" {
			return &m
		}
	}
	return nil
}

// scanAllComponents scans standard component directories, recursing into subdirectories.
func scanAllComponents(installPath string) []PluginComponent {
	var components []PluginComponent

	for _, cd := range componentDirs {
		dir := filepath.Join(installPath, cd.Dir)
		components = append(components, scanDirRecursive(dir, cd.Type, cd.Exts)...)
	}

	return components
}

// scanDirRecursive walks a directory recursively collecting files matching extensions.
func scanDirRecursive(dir, typ string, exts []string) []PluginComponent {
	var components []PluginComponent

	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !matchExt(d.Name(), exts) {
			return nil
		}
		info, _ := d.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}
		// Use relative path from the component dir as the display name
		rel, _ := filepath.Rel(dir, path)
		if rel == "" {
			rel = d.Name()
		}
		components = append(components, PluginComponent{
			Type: typ,
			Name: rel,
			Path: path,
			Size: size,
		})
		return nil
	})

	return components
}

func matchExt(name string, exts []string) bool {
	if len(exts) == 0 {
		return true // accept all
	}
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range exts {
		if ext == e {
			return true
		}
	}
	return false
}

// parseSubPlugins reads a marketplace.json and extracts sub-plugin definitions.
func parseSubPlugins(installPath string) []SubPlugin {
	path := filepath.Join(installPath, ".claude-plugin", "marketplace.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var raw struct {
		Plugins []rawSubPlugin `json:"plugins"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}

	var subs []SubPlugin
	for _, rsp := range raw.Plugins {
		sp := SubPlugin{
			Name:        rsp.Name,
			Description: rsp.Description,
			Version:     rsp.Version,
		}
		sp.Components = resolveComponentPaths(installPath, rsp)
		subs = append(subs, sp)
	}
	return subs
}

type rawSubPlugin struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Agents      []string `json:"agents"`
	Commands    []string `json:"commands"`
	Hooks       []string `json:"hooks"`
	McpServers  []string `json:"mcpServers"`
	Skills      []string `json:"skills"`
	Scripts     []string `json:"scripts"`
	Settings    []string `json:"settings"`
	Memory      []string `json:"memory"`
}

func resolveComponentPaths(installPath string, rsp rawSubPlugin) []PluginComponent {
	var components []PluginComponent

	type pathGroup struct {
		paths []string
		typ   string
	}
	groups := []pathGroup{
		{rsp.Agents, "agent"},
		{rsp.Commands, "command"},
		{rsp.Hooks, "hook"},
		{rsp.McpServers, "mcp"},
		{rsp.Skills, "skill"},
		{rsp.Scripts, "script"},
		{rsp.Settings, "setting"},
		{rsp.Memory, "memory"},
	}

	for _, g := range groups {
		for _, relPath := range g.paths {
			// Paths in manifest are relative (e.g. "./cli-tool/components/agents/foo.md")
			absPath := filepath.Join(installPath, filepath.Clean(relPath))
			name := filepath.Base(absPath)
			// Try to get the parent dir for context
			dir := filepath.Base(filepath.Dir(absPath))
			if dir != "." && dir != g.typ+"s" && dir != "components" {
				name = dir + "/" + name
			}

			var size int64
			if info, err := os.Stat(absPath); err == nil {
				size = info.Size()
			}

			components = append(components, PluginComponent{
				Type: g.typ,
				Name: name,
				Path: absPath,
				Size: size,
			})
		}
	}

	return components
}

// discoverMarketplacePlugins discovers available plugins from a marketplace directory.
func discoverMarketplacePlugins(mktPath, mktName string) []Plugin {
	// Strategy 1: marketplace.json with sub-plugins list
	mktManifest := filepath.Join(mktPath, ".claude-plugin", "marketplace.json")
	if data, err := os.ReadFile(mktManifest); err == nil {
		return discoverFromMarketplaceManifest(data, mktPath, mktName)
	}

	// Strategy 2: plugins/ subdirectory with individual plugin dirs
	pluginsSubdir := filepath.Join(mktPath, "plugins")
	entries, err := os.ReadDir(pluginsSubdir)
	if err != nil {
		return nil
	}

	var plugins []Plugin
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pluginDir := filepath.Join(pluginsSubdir, e.Name())
		p := Plugin{
			ID:          e.Name() + "@" + mktName,
			Name:        e.Name(),
			Marketplace: mktName,
		}
		p.Manifest = readPluginManifest(pluginDir)
		p.Components = scanAllComponents(pluginDir)
		if p.Manifest != nil && p.Manifest.LspServers != nil {
			for lspName := range p.Manifest.LspServers {
				p.Components = append(p.Components, PluginComponent{
					Type: "lsp",
					Name: lspName,
				})
			}
		}
		plugins = append(plugins, p)
	}
	return plugins
}

func discoverFromMarketplaceManifest(data []byte, mktPath, mktName string) []Plugin {
	var raw struct {
		Plugins []rawSubPlugin `json:"plugins"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}

	var plugins []Plugin
	for _, rsp := range raw.Plugins {
		p := Plugin{
			ID:          rsp.Name + "@" + mktName,
			Name:        rsp.Name,
			Marketplace: mktName,
		}
		p.SubPlugins = []SubPlugin{{
			Name:        rsp.Name,
			Description: rsp.Description,
			Version:     rsp.Version,
			Components:  resolveComponentPaths(mktPath, rsp),
		}}
		// Flatten sub-plugin components to top level
		for _, sp := range p.SubPlugins {
			p.Components = append(p.Components, sp.Components...)
		}
		if rsp.Description != "" {
			p.Manifest = &PluginManifest{
				Name:        rsp.Name,
				Description: rsp.Description,
				Version:     rsp.Version,
			}
		}
		plugins = append(plugins, p)
	}
	return plugins
}
