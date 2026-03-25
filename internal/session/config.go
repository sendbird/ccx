package session

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	ConfigHook
	ConfigEnterprise // managed enterprise settings
	ConfigKeymap     // keybindings from config.yaml
	ConfigShortcut   // number key shortcuts
	configCategoryCount // must be last
)

// CategoryLabel returns a short label for the category.
func CategoryLabel(cat ConfigCategory) string {
	switch cat {
	case ConfigGlobal:
		return "MEMORY"
	case ConfigProject:
		return "PROJECT"
	case ConfigLocal:
		return "LOCAL"
	case ConfigSkill:
		return "SKILLS"
	case ConfigAgent:
		return "AGENTS"
	case ConfigCommand:
		return "COMMANDS"
	case ConfigMCP:
		return "MCP"
	case ConfigHook:
		return "HOOKS"
	case ConfigEnterprise:
		return "ENTERPRISE"
	case ConfigKeymap:
		return "KEYMAPS"
	case ConfigShortcut:
		return "SHORTCUTS"
	default:
		return "ALL"
	}
}

// ConfigCategoryCount returns the number of config categories.
func ConfigCategoryCount() int { return int(configCategoryCount) }

// ConfigItem represents a single discoverable config file.
type ConfigItem struct {
	Category    ConfigCategory
	Name        string
	Path        string
	Description string // first heading or frontmatter description
	ModTime     time.Time
	Size        int64
	RefBy       string // path of referencing file (empty for root)
	RefDepth    int    // 0 = root (CLAUDE.md), 1+ = referenced depth
	Group       string // sub-group within category (e.g. hook event type)
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

	home, _ := os.UserHomeDir()

	// --- GLOBAL ---
	// Start from CLAUDE.md and walk @references to discover memory/contexts/rules
	claudeMdPath := filepath.Join(claudeDir, "CLAUDE.md")
	addFileIfExists(tree, ConfigGlobal, claudeDir, "CLAUDE.md")
	visited := map[string]bool{claudeMdPath: true}
	walkReferences(tree, ConfigGlobal, claudeDir, claudeMdPath, visited, 1)

	// --- PROJECT ---
	if projectPath != "" {
		encoded := EncodeProjectPath(projectPath)
		projDir := filepath.Join(claudeDir, "projects", encoded)
		projClaude := filepath.Join(projDir, "CLAUDE.md")
		addFileIfExists(tree, ConfigProject, projDir, "CLAUDE.md")
		// Walk @references from project CLAUDE.md
		projVisited := map[string]bool{projClaude: true}
		// Also merge global visited to avoid duplicating global refs
		for k := range visited {
			projVisited[k] = true
		}
		walkReferences(tree, ConfigProject, claudeDir, projClaude, projVisited, 1)
		// Auto-memory dir (not reference-based); skip already-discovered files
		memDir := filepath.Join(projDir, "memory")
		scanDirFilesExcept(tree, ConfigProject, memDir, ".md", projVisited)
	}

	// --- LOCAL ---
	// Walk up from project dir to find CLAUDE.md at each parent level
	// (Claude Code loads these reverse-recursively)
	if projectPath != "" {
		localVisited := make(map[string]bool)
		// Merge global+project visited to avoid duplicates
		for k := range visited {
			localVisited[k] = true
		}
		dir := projectPath
		for {
			claudePath := filepath.Join(dir, "CLAUDE.md")
			if !localVisited[claudePath] {
				if info, err := os.Stat(claudePath); err == nil {
					// Name: use dir basename for parent dirs, plain for project root
					name := "CLAUDE.md"
					if dir != projectPath {
						name = filepath.Base(dir) + "/CLAUDE.md"
					}
					tree.Items = append(tree.Items, ConfigItem{
						Category:    ConfigLocal,
						Name:        name,
						Path:        claudePath,
						Description: extractDescription(claudePath),
						ModTime:     info.ModTime(),
						Size:        info.Size(),
					})
					localVisited[claudePath] = true
					walkReferences(tree, ConfigLocal, claudeDir, claudePath, localVisited, 1)
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir || parent == "/" || (home != "" && parent == filepath.Dir(home)) {
				break
			}
			dir = parent
		}
		addFileIfExists(tree, ConfigLocal, filepath.Join(projectPath, ".claude"), "settings.local.json")
	}

	// --- SKILLS ---
	scanSkills(tree, filepath.Join(claudeDir, "skills"))

	// --- AGENTS ---
	scanDirFiles(tree, ConfigAgent, filepath.Join(claudeDir, "agents"), ".md")

	// --- COMMANDS ---
	scanDirFiles(tree, ConfigCommand, filepath.Join(claudeDir, "commands"), ".md")

	// --- HOOKS ---
	// Hooks are defined in settings.json / settings.local.json, not a hooks/ dir.
	// Extract hook scripts referenced in these files.
	scanHooksFromSettings(tree, filepath.Join(claudeDir, "settings.json"))
	if projectPath != "" {
		scanHooksFromSettings(tree, filepath.Join(projectPath, ".claude", "settings.local.json"))
	}

	// --- MCP ---
	// MCP servers come from:
	// 1. ~/.claude/settings.json → mcpServers key (user-level)
	// 2. .mcp.json at project root (project-level)
	// 3. ~/.claude.json → user-level mcpServers (top-level) and per-project (keyed by path)
	// 4. --mcp-config flags on live Claude processes
	scanMCPFromJSON(tree, filepath.Join(claudeDir, "settings.json"), "settings.json")
	scanMCPFromClaudeJSON(tree, filepath.Join(home, ".claude.json"), projectPath)
	if projectPath != "" {
		scanMCPFromJSON(tree, filepath.Join(projectPath, ".mcp.json"), ".mcp.json")
	}
	scanMCPFromLiveProcesses(tree)

	// --- ENTERPRISE ---
	// macOS: /Library/Application Support/ClaudeCode/
	enterpriseDir := "/Library/Application Support/ClaudeCode"
	if entries, err := os.ReadDir(enterpriseDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			path := filepath.Join(enterpriseDir, e.Name())
			info, _ := e.Info()
			item := ConfigItem{
				Category: ConfigEnterprise,
				Name:     e.Name(),
				Path:     path,
			}
			if info != nil {
				item.ModTime = info.ModTime()
				item.Size = info.Size()
			}
			if strings.HasSuffix(e.Name(), ".json") {
				item.Description = "managed settings"
			}
			tree.Items = append(tree.Items, item)
		}
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
	scanDirFilesExcept(tree, cat, dir, ext, nil)
}

func scanDirFilesExcept(tree *ConfigTree, cat ConfigCategory, dir, ext string, skip map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if skip != nil && skip[path] {
			continue
		}
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

// scanHookFiles scans the hooks directory for executable scripts and directories.
// scanHooksFromSettings parses a settings JSON file for the "hooks" key
// and adds each unique hook script as a ConfigItem.
func scanHooksFromSettings(tree *ConfigTree, settingsPath string) {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return
	}
	var obj struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &obj); err != nil || len(obj.Hooks) == 0 {
		return
	}

	home, _ := os.UserHomeDir()
	seen := make(map[string]bool)
	settingsName := filepath.Base(settingsPath)

	for event, matchers := range obj.Hooks {
		for _, m := range matchers {
			for _, h := range m.Hooks {
				cmd := h.Command
				if cmd == "" {
					continue
				}
				scriptPath := ExtractScriptPath(cmd, home)
				key := event + ":" + scriptPath
				if scriptPath == "" || seen[key] {
					continue
				}
				seen[key] = true

				name := filepath.Base(scriptPath)
				matcher := m.Matcher
				desc := settingsName
				if matcher != "" {
					desc = "match:" + matcher + " (" + settingsName + ")"
				}
				info, err := os.Stat(scriptPath)
				item := ConfigItem{
					Category:    ConfigHook,
					Name:        name,
					Path:        scriptPath,
					Description: desc,
					Group:       event,
					RefBy:       settingsPath,
				}
				if err == nil {
					item.ModTime = info.ModTime()
					item.Size = info.Size()
				}
				tree.Items = append(tree.Items, item)
			}
		}
	}
}

// ExtractScriptPath extracts the script file path from a hook command string.
// Handles patterns like "python3 ~/.claude/hooks/foo.py", "uv run ~/.claude/hooks/bar.py".
func ExtractScriptPath(cmd string, home string) string {
	parts := strings.Fields(cmd)
	// Find the first argument that looks like a file path
	for _, p := range parts {
		if p == "" {
			continue
		}
		// Skip common command prefixes
		if p == "python" || p == "python3" || p == "uv" || p == "run" ||
			p == "bash" || p == "sh" || p == "-c" || p == "node" {
			continue
		}
		// Expand ~
		if strings.HasPrefix(p, "~/") && home != "" {
			p = filepath.Join(home, p[2:])
		}
		// Must look like a path
		if strings.Contains(p, "/") || strings.Contains(p, ".") {
			return p
		}
	}
	return ""
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

// reAtRef matches @path references in backticks (e.g. `@~/.claude/memory/k8s.md`).
var reAtRef = regexp.MustCompile("`@(~?[^`\\s]+)`")

// reAtRefBare matches bare @path references without backticks (e.g. @~/.claude/memory/k8s.md).
// Must start with @~/ or @/ to avoid false positives.
var reAtRefBare = regexp.MustCompile(`(?:^|[\s:])@(~?/[^\s,;)]+)`)

// reKeywordLine matches lines like "bash, command, output:  @~/.claude/memory/command.md"
// capturing the keyword list before the @reference (with or without backticks).
var reKeywordLine = regexp.MustCompile(`^([a-zA-Z0-9, $]+\S):\s+` + "(?:`)?@")

// fileRef holds a resolved file reference with optional keyword context.
type fileRef struct {
	path     string
	keywords string // e.g. "bash, command, output" — empty if no keyword line
}

// ExtractFileReferences parses a file for @path references and returns resolved absolute paths.
func ExtractFileReferences(filePath string) []string {
	refs := extractFileRefsWithContext(filePath)
	paths := make([]string, len(refs))
	for i, r := range refs {
		paths[i] = r.path
	}
	return paths
}

// extractFileRefsWithContext parses a file for @path references, also capturing
// keyword triggers from lines like "keywords: @path".
func extractFileRefsWithContext(filePath string) []fileRef {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	home, _ := os.UserHomeDir()

	resolvePath := func(p string) string {
		if strings.HasPrefix(p, "~/") && home != "" {
			p = filepath.Join(home, p[2:])
		}
		return filepath.Clean(p)
	}

	// Build a per-line map of keyword triggers and collect all refs
	lineKeywords := make(map[string]string) // resolved path → keywords
	seen := make(map[string]bool)
	var refs []fileRef

	for _, line := range strings.Split(string(data), "\n") {
		// Check for keyword trigger pattern
		kwMatch := reKeywordLine.FindStringSubmatch(line)

		// Find all @refs on this line (both backtick and bare)
		var linePaths []string
		for _, m := range reAtRef.FindAllStringSubmatch(line, -1) {
			linePaths = append(linePaths, resolvePath(m[1]))
		}
		for _, m := range reAtRefBare.FindAllStringSubmatch(line, -1) {
			linePaths = append(linePaths, resolvePath(m[1]))
		}

		for _, p := range linePaths {
			if kwMatch != nil {
				lineKeywords[p] = strings.TrimSpace(kwMatch[1])
			}
			if !seen[p] {
				seen[p] = true
				refs = append(refs, fileRef{path: p, keywords: lineKeywords[p]})
			}
		}
	}

	return refs
}

// walkReferences recursively discovers files referenced via @path from a root file.
func walkReferences(tree *ConfigTree, cat ConfigCategory, claudeDir, rootPath string, visited map[string]bool, depth int) {
	refs := extractFileRefsWithContext(rootPath)
	for _, ref := range refs {
		if visited[ref.path] {
			continue
		}
		info, err := os.Stat(ref.path)
		if err != nil || info.IsDir() {
			continue
		}
		visited[ref.path] = true

		// Compute display name relative to ~/.claude/ if possible
		name := filepath.Base(ref.path)
		if rel, err := filepath.Rel(claudeDir, ref.path); err == nil && !strings.HasPrefix(rel, "..") {
			name = rel
		}

		tree.Items = append(tree.Items, ConfigItem{
			Category:    cat,
			Name:        name,
			Path:        ref.path,
			Description: extractDescription(ref.path),
			ModTime:     info.ModTime(),
			Size:        info.Size(),
			RefBy:       rootPath,
			RefDepth:    depth,
			Group:       ref.keywords,
		})

		// Recurse (only for .md files to avoid parsing binaries)
		if strings.HasSuffix(ref.path, ".md") {
			walkReferences(tree, cat, claudeDir, ref.path, visited, depth+1)
		}
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
// scanMCPFromJSON adds an MCP config item if the JSON file has mcpServers.
func scanMCPFromJSON(tree *ConfigTree, path, displayName string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	desc := mcpServerCount(path)
	if desc == "" {
		return
	}
	tree.Items = append(tree.Items, ConfigItem{
		Category:    ConfigMCP,
		Name:        displayName,
		Path:        path,
		Description: desc,
		ModTime:     info.ModTime(),
		Size:        info.Size(),
	})
}

// scanMCPFromClaudeJSON checks ~/.claude.json for per-project mcpServers.
// The file has project paths as keys, each with a mcpServers map.
func scanMCPFromClaudeJSON(tree *ConfigTree, claudeJSONPath, projectPath string) {
	data, err := os.ReadFile(claudeJSONPath)
	if err != nil {
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return
	}
	info, _ := os.Stat(claudeJSONPath)

	// Top-level mcpServers (user-level)
	if raw, ok := obj["mcpServers"]; ok {
		var servers map[string]json.RawMessage
		if json.Unmarshal(raw, &servers) == nil && len(servers) > 0 {
			item := ConfigItem{
				Category:    ConfigMCP,
				Name:        ".claude.json (user)",
				Path:        claudeJSONPath,
				Description: strings.Join(mapKeys(servers), ", "),
			}
			if info != nil {
				item.ModTime = info.ModTime()
				item.Size = info.Size()
			}
			tree.Items = append(tree.Items, item)
		}
	}

	// Project-scoped mcpServers (keyed by project path)
	if projectPath == "" {
		return
	}
	for key, raw := range obj {
		if key != projectPath {
			continue
		}
		var proj struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal(raw, &proj); err != nil || len(proj.MCPServers) == 0 {
			continue
		}
		item := ConfigItem{
			Category:    ConfigMCP,
			Name:        ".claude.json (project)",
			Path:        claudeJSONPath,
			Description: strings.Join(mapKeys(proj.MCPServers), ", "),
		}
		if info != nil {
			item.ModTime = info.ModTime()
			item.Size = info.Size()
		}
		tree.Items = append(tree.Items, item)
		return
	}
}

// scanMCPFromLiveProcesses finds --mcp-config flags on running Claude processes.
func scanMCPFromLiveProcesses(tree *ConfigTree) {
	out, err := exec.Command("pgrep", "-af", "claude.*--mcp-config").Output()
	if err != nil || len(out) == 0 {
		return
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		args := strings.Fields(line)
		for i, arg := range args {
			if arg == "--mcp-config" && i+1 < len(args) {
				cfgPath := args[i+1]
				if strings.HasPrefix(cfgPath, "~/") {
					home, _ := os.UserHomeDir()
					if home != "" {
						cfgPath = filepath.Join(home, cfgPath[2:])
					}
				}
				if seen[cfgPath] {
					continue
				}
				seen[cfgPath] = true
				info, err := os.Stat(cfgPath)
				if err != nil {
					continue
				}
				desc := mcpServerCount(cfgPath)
				if desc == "" {
					desc = "live --mcp-config"
				} else {
					desc += " (live)"
				}
				tree.Items = append(tree.Items, ConfigItem{
					Category:    ConfigMCP,
					Name:        filepath.Base(cfgPath),
					Path:        cfgPath,
					Description: desc,
					ModTime:     info.ModTime(),
					Size:        info.Size(),
					Group:       "live",
				})
			}
		}
	}
}

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
