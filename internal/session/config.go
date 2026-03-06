package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ConfigCategory identifies which section a config item belongs to.
type ConfigCategory int

const (
	ConfigGlobal  ConfigCategory = iota // CLAUDE.md + memory + contexts + rules
	ConfigProject                       // project-level CLAUDE.md + memory
	ConfigLocal                         // local CLAUDE.md + settings.local.json
	ConfigSkill
	ConfigAgent
	ConfigCommand
	ConfigMCP
)

// ConfigItem represents a single discoverable config file.
type ConfigItem struct {
	Category    ConfigCategory
	Name        string
	Path        string
	Description string // first heading or frontmatter description
	ModTime     time.Time
	Size        int64
}

// ConfigTree holds all discovered config items grouped by category.
type ConfigTree struct {
	ProjectName string // short project name for header
	ProjectPath string // decoded project path for local lookup
	Items       []ConfigItem
}

// ScanConfig discovers all Claude Code configuration files across three scope
// levels (global, project, local) plus skills, agents, commands, and MCP configs.
func ScanConfig(claudeDir, projectPath string) (*ConfigTree, error) {
	tree := &ConfigTree{
		ProjectPath: projectPath,
		ProjectName: filepath.Base(projectPath),
	}
	if projectPath == "" {
		tree.ProjectName = "(none)"
	}

	// --- GLOBAL ---
	addFileIfExists(tree, ConfigGlobal, claudeDir, "CLAUDE.md")
	scanDirFiles(tree, ConfigGlobal, filepath.Join(claudeDir, "memory"), ".md")
	scanDirFiles(tree, ConfigGlobal, filepath.Join(claudeDir, "contexts"), ".md")
	scanDirFiles(tree, ConfigGlobal, filepath.Join(claudeDir, "rules"), ".md")

	// --- PROJECT ---
	if projectPath != "" {
		encoded := EncodeProjectPath(projectPath)
		projDir := filepath.Join(claudeDir, "projects", encoded)
		addFileIfExists(tree, ConfigProject, projDir, "CLAUDE.md")
		scanDirFiles(tree, ConfigProject, filepath.Join(projDir, "memory"), ".md")
	}

	// --- LOCAL ---
	if projectPath != "" {
		addFileIfExists(tree, ConfigLocal, projectPath, "CLAUDE.md")
		addFileIfExists(tree, ConfigLocal, filepath.Join(projectPath, ".claude"), "settings.local.json")
	}

	// --- SKILLS ---
	scanSkills(tree, filepath.Join(claudeDir, "skills"))

	// --- AGENTS ---
	scanDirFiles(tree, ConfigAgent, filepath.Join(claudeDir, "agents"), ".md")

	// --- COMMANDS ---
	scanDirFiles(tree, ConfigCommand, filepath.Join(claudeDir, "commands"), ".md")

	// --- MCP ---
	scanDirFiles(tree, ConfigMCP, filepath.Join(claudeDir, "mcp"), ".json")
	// Also check settings.json for mcpServers
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if info, err := os.Stat(settingsPath); err == nil {
		desc := mcpServerCount(settingsPath)
		tree.Items = append(tree.Items, ConfigItem{
			Category:    ConfigMCP,
			Name:        "settings.json",
			Path:        settingsPath,
			Description: desc,
			ModTime:     info.ModTime(),
			Size:        info.Size(),
		})
	}

	// Sort items within each category by name
	sort.SliceStable(tree.Items, func(i, j int) bool {
		if tree.Items[i].Category != tree.Items[j].Category {
			return tree.Items[i].Category < tree.Items[j].Category
		}
		return tree.Items[i].Name < tree.Items[j].Name
	})

	return tree, nil
}

func addFileIfExists(tree *ConfigTree, cat ConfigCategory, dir, name string) {
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	tree.Items = append(tree.Items, ConfigItem{
		Category:    cat,
		Name:        name,
		Path:        path,
		Description: extractDescription(path),
		ModTime:     info.ModTime(),
		Size:        info.Size(),
	})
}

func scanDirFiles(tree *ConfigTree, cat ConfigCategory, dir, ext string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, _ := e.Info()
		item := ConfigItem{
			Category: cat,
			Name:     e.Name(),
			Path:     path,
		}
		if info != nil {
			item.ModTime = info.ModTime()
			item.Size = info.Size()
		}
		item.Description = extractDescription(path)
		tree.Items = append(tree.Items, item)
	}
}

func scanSkills(tree *ConfigTree, skillsDir string) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillFile := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		info, err := os.Stat(skillFile)
		if err != nil {
			continue
		}
		tree.Items = append(tree.Items, ConfigItem{
			Category:    ConfigSkill,
			Name:        e.Name(),
			Path:        skillFile,
			Description: extractFrontmatter(skillFile, "description"),
			ModTime:     info.ModTime(),
			Size:        info.Size(),
		})
	}
}

// extractDescription returns the first markdown heading from a file.
func extractDescription(path string) string {
	if strings.HasSuffix(path, ".json") {
		return "" // don't try to parse JSON as markdown
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return ""
}

// extractFrontmatter looks for a YAML frontmatter block (--- delimited)
// and returns the value of the given key.
func extractFrontmatter(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	inFrontmatter := false
	prefix := key + ":"

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if inFrontmatter {
				return "" // end of frontmatter, not found
			}
			inFrontmatter = true
			continue
		}
		if inFrontmatter && strings.HasPrefix(trimmed, prefix) {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			val = strings.Trim(val, "\"'")
			return val
		}
	}
	return ""
}

// mcpServerCount reads a JSON file and reports how many top-level keys
// exist in the "mcpServers" object (or top-level keys for mcp/*.json).
func mcpServerCount(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return ""
	}
	// Check for mcpServers key
	if raw, ok := obj["mcpServers"]; ok {
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(raw, &servers); err == nil && len(servers) > 0 {
			return strings.Join(mapKeys(servers), ", ")
		}
	}
	// For mcp/*.json files, count top-level keys as server names
	if len(obj) > 0 && filepath.Base(path) != "settings.json" {
		return strings.Join(mapKeys(obj), ", ")
	}
	return ""
}

func mapKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
