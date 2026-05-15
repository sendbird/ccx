package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ContextNodeKind string

const (
	ContextRoot         ContextNodeKind = "root"
	ContextSessionState ContextNodeKind = "session_state"
	ContextMemory       ContextNodeKind = "memory"
	ContextPlan         ContextNodeKind = "plan"
	ContextTask         ContextNodeKind = "task"
	ContextSkill        ContextNodeKind = "skill"
	ContextHook         ContextNodeKind = "hook"
	ContextMCP          ContextNodeKind = "mcp"
	ContextCommand      ContextNodeKind = "command"
	ContextAgent        ContextNodeKind = "agent"
	ContextFile         ContextNodeKind = "file"
)

type ContextNode struct {
	Kind                       ContextNodeKind
	Label                      string
	Detail                     string
	Path                       string
	Status                     string
	Count                      int
	Used                       bool
	RelatedView                string
	RelatedPath                string
	RelatedPluginID            string
	RelatedPluginComponentPath string
	RelatedPluginComponentType string
	Children                   []ContextNode
}

type SessionContextTree struct {
	SessionID   string
	ProjectPath string
	Roots       []ContextNode
	Warnings    []string
}

func BuildSessionContextTree(claudeDir string, sess Session) (*SessionContextTree, error) {
	if claudeDir == "" {
		home, _ := os.UserHomeDir()
		claudeDir = filepath.Join(home, ".claude")
	}
	tree := &SessionContextTree{
		SessionID:   sess.ID,
		ProjectPath: sess.ProjectPath,
	}

	entries, err := loadContextEntries(sess)
	if err != nil {
		tree.Warnings = append(tree.Warnings, err.Error())
	}

	cfg, cfgErr := ScanConfig(claudeDir, sess.ProjectPath)
	if cfgErr != nil {
		tree.Warnings = append(tree.Warnings, cfgErr.Error())
	}
	plugins, _ := ScanPlugins(claudeDir)

	tree.Roots = append(tree.Roots,
		buildRuntimeContextRoot(claudeDir, sess, entries),
		buildInstructionContextRoot(cfg, plugins),
		buildSkillsContextRoot(cfg, entries, plugins),
		buildHooksContextRoot(cfg, entries, plugins),
		buildMCPContextRoot(cfg, entries, plugins),
		buildCommandsAgentsContextRoot(cfg, sess, entries, plugins),
	)
	return tree, nil
}

func loadContextEntries(sess Session) ([]Entry, error) {
	if sess.FilePath == "" {
		return nil, nil
	}
	entries, err := LoadMessages(sess.FilePath)
	if err != nil {
		return nil, fmt.Errorf("load session messages: %w", err)
	}
	return entries, nil
}

func buildRuntimeContextRoot(claudeDir string, sess Session, entries []Entry) ContextNode {
	root := ContextNode{Kind: ContextSessionState, Label: "Internal session state"}

	if len(sess.Todos) > 0 {
		completed := 0
		for _, todo := range sess.Todos {
			if todo.Status == "completed" {
				completed++
			}
		}
		root.Children = append(root.Children, ContextNode{Kind: ContextTask, Label: "Todos", Detail: fmt.Sprintf("%d/%d completed", completed, len(sess.Todos)), Count: len(sess.Todos)})
	}

	tasks := sess.Tasks
	if len(tasks) == 0 && sess.HasTasks && len(entries) > 0 {
		tasks = LoadTasksFromEntries(entries)
	}
	if len(tasks) > 0 {
		root.Children = append(root.Children, taskContextNode(tasks))
	}

	crons := sess.Crons
	if len(crons) == 0 && sess.HasCrons && len(entries) > 0 {
		crons = LoadCronsFromEntries(entries)
	}
	if len(crons) > 0 {
		root.Children = append(root.Children, cronContextNode(crons))
	}

	if len(sess.PlanSlugs) > 0 {
		root.Children = append(root.Children, planContextNode(sess.PlanSlugs))
	}

	if sess.HasMemory && sess.ProjectPath != "" {
		root.Children = append(root.Children, projectMemoryContextNode(claudeDir, sess.ProjectPath))
	}

	shells := sess.ShellJobs
	if len(shells) == 0 && sess.HasShellJobs && len(entries) > 0 {
		shells = LoadShellJobsFromEntries(entries)
	}
	if len(shells) > 0 {
		root.Children = append(root.Children, shellContextNode(shells))
	}

	if len(root.Children) == 0 {
		root.Detail = "no runtime context found"
	}
	root.Count = len(root.Children)
	return root
}

func taskContextNode(tasks []TaskItem) ContextNode {
	completed := 0
	children := make([]ContextNode, 0, len(tasks))
	for _, task := range tasks {
		if task.Status == "completed" {
			completed++
		}
		label := task.Subject
		if label == "" {
			label = task.ID
		}
		children = append(children, ContextNode{Kind: ContextTask, Label: label, Detail: task.Description, Status: task.Status, Used: true})
	}
	return ContextNode{Kind: ContextTask, Label: "Task board", Detail: fmt.Sprintf("%d/%d completed", completed, len(tasks)), Count: len(tasks), Children: children}
}

func cronContextNode(crons []CronItem) ContextNode {
	active := 0
	children := make([]ContextNode, 0, len(crons))
	for _, cron := range crons {
		status := cron.Status
		if status == "" {
			status = "active"
		}
		if status != "deleted" {
			active++
		}
		label := cron.ID
		if label == "" {
			label = cron.Cron
		}
		children = append(children, ContextNode{Kind: ContextTask, Label: label, Detail: cron.Prompt, Status: status, Used: true})
	}
	return ContextNode{Kind: ContextTask, Label: "Scheduled tasks", Detail: fmt.Sprintf("%d/%d active", active, len(crons)), Count: len(crons), Children: children}
}

func planContextNode(slugs []string) ContextNode {
	home, _ := os.UserHomeDir()
	children := make([]ContextNode, 0, len(slugs))
	for _, slug := range slugs {
		path := filepath.Join(home, ".claude", "plans", slug+".md")
		children = append(children, ContextNode{Kind: ContextPlan, Label: slug, Path: path, Status: fileStatus(path), Used: true})
	}
	return ContextNode{Kind: ContextPlan, Label: "Plans", Count: len(slugs), Children: children}
}

func projectMemoryContextNode(claudeDir, projectPath string) ContextNode {
	encoded := EncodeProjectPath(projectPath)
	memDir := filepath.Join(claudeDir, "projects", encoded, "memory")
	entries, err := os.ReadDir(memDir)
	if err != nil {
		return ContextNode{Kind: ContextMemory, Label: "Project auto-memory", Path: memDir, Status: "missing"}
	}
	children := make([]ContextNode, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(memDir, entry.Name())
		children = append(children, ContextNode{Kind: ContextMemory, Label: entry.Name(), Path: path, Status: fileStatus(path), Used: true})
	}
	return ContextNode{Kind: ContextMemory, Label: "Project auto-memory", Path: memDir, Count: len(children), Children: children}
}

func shellContextNode(jobs []ShellJob) ContextNode {
	children := make([]ContextNode, 0, len(jobs))
	for _, job := range jobs {
		label := job.ToolName
		if job.Description != "" {
			label += ": " + job.Description
		}
		children = append(children, ContextNode{Kind: ContextSessionState, Label: label, Detail: job.Command, Status: job.Status, Used: true})
	}
	return ContextNode{Kind: ContextSessionState, Label: "Background shells", Count: len(jobs), Children: children}
}

func subagentContextNode(agents []Subagent) ContextNode {
	children := make([]ContextNode, 0, len(agents))
	for _, agent := range agents {
		label := agent.AgentType
		if label == "" {
			label = "agent"
		}
		if agent.ShortID != "" {
			label += " " + agent.ShortID
		}
		children = append(children, ContextNode{Kind: ContextAgent, Label: label, Detail: agent.FirstPrompt, Path: agent.FilePath, Used: true})
	}
	return ContextNode{Kind: ContextAgent, Label: "Subagents", Count: len(agents), Children: children}
}

func buildInstructionContextRoot(cfg *ConfigTree, plugins *PluginTree) ContextNode {
	root := ContextNode{Kind: ContextMemory, Label: "Instructions and memory"}
	if cfg == nil {
		root.Detail = "configuration scan unavailable"
		return root
	}
	for _, section := range []struct {
		label string
		cat   ConfigCategory
	}{
		{"Global", ConfigGlobal},
		{"Project", ConfigProject},
		{"Local", ConfigLocal},
	} {
		node := configCategoryNode(section.label, ContextFile, cfg.Items, section.cat, plugins)
		if len(node.Children) > 0 {
			root.Children = append(root.Children, node)
		}
	}
	if len(root.Children) == 0 {
		root.Detail = "no instruction files found"
	}
	root.Count = len(root.Children)
	return root
}

func buildSkillsContextRoot(cfg *ConfigTree, entries []Entry, plugins *PluginTree) ContextNode {
	used := usedSkillNodes(entries)
	configured := configNodes(cfg, ConfigSkill, ContextSkill, plugins)
	return usedConfiguredRoot(ContextSkill, "Skills", used, configured)
}

func buildHooksContextRoot(cfg *ConfigTree, entries []Entry, plugins *PluginTree) ContextNode {
	used := usedHookNodes(entries)
	configured := configNodes(cfg, ConfigHook, ContextHook, plugins)
	return usedConfiguredRoot(ContextHook, "Hooks", used, configured)
}

func buildMCPContextRoot(cfg *ConfigTree, entries []Entry, plugins *PluginTree) ContextNode {
	used := usedMCPNodes(entries)
	configured := configNodes(cfg, ConfigMCP, ContextMCP, plugins)
	return usedConfiguredRoot(ContextMCP, "MCP", used, configured)
}

func buildCommandsAgentsContextRoot(cfg *ConfigTree, sess Session, entries []Entry, plugins *PluginTree) ContextNode {
	root := ContextNode{Kind: ContextCommand, Label: "Commands and agents"}
	commands := usedCommandNodes(entries)
	if len(commands) > 0 {
		root.Children = append(root.Children, ContextNode{Kind: ContextCommand, Label: "Slash commands used", Count: len(commands), Children: commands})
	}
	configuredCommands := configNodes(cfg, ConfigCommand, ContextCommand, plugins)
	if len(configuredCommands) > 0 {
		root.Children = append(root.Children, ContextNode{Kind: ContextCommand, Label: "Configured commands", Count: len(configuredCommands), Children: configuredCommands})
	}
	if sess.HasAgents {
		agents, err := FindSubagents(sess.FilePath)
		if err == nil && len(agents) > 0 {
			root.Children = append(root.Children, subagentContextNode(agents))
		}
	}
	configuredAgents := configNodes(cfg, ConfigAgent, ContextAgent, plugins)
	if len(configuredAgents) > 0 {
		root.Children = append(root.Children, ContextNode{Kind: ContextAgent, Label: "Configured agents", Count: len(configuredAgents), Children: configuredAgents})
	}
	if len(root.Children) == 0 {
		root.Detail = "no commands or agents found"
	}
	root.Count = len(root.Children)
	return root
}

func usedConfiguredRoot(kind ContextNodeKind, label string, used, configured []ContextNode) ContextNode {
	root := ContextNode{Kind: kind, Label: label}
	if len(used) > 0 {
		root.Children = append(root.Children, ContextNode{Kind: kind, Label: "Used in session", Count: len(used), Children: used})
	}
	if len(configured) > 0 {
		root.Children = append(root.Children, ContextNode{Kind: kind, Label: "Available/configured", Count: len(configured), Children: configured})
	}
	if len(root.Children) == 0 {
		root.Detail = "none found"
	}
	root.Count = len(root.Children)
	return root
}

func configCategoryNode(label string, kind ContextNodeKind, items []ConfigItem, cat ConfigCategory, plugins *PluginTree) ContextNode {
	root := ContextNode{Kind: kind, Label: label}
	for _, item := range items {
		if item.Category != cat {
			continue
		}
		root.Children = append(root.Children, configItemNode(item, kind, plugins))
	}
	root.Count = len(root.Children)
	return root
}

func configNodes(cfg *ConfigTree, cat ConfigCategory, kind ContextNodeKind, plugins *PluginTree) []ContextNode {
	if cfg == nil {
		return nil
	}
	var nodes []ContextNode
	for _, item := range cfg.Items {
		if item.Category == cat {
			nodes = append(nodes, configItemNode(item, kind, plugins))
		}
	}
	return nodes
}

func configItemNode(item ConfigItem, kind ContextNodeKind, plugins *PluginTree) ContextNode {
	detail := item.Description
	if item.Group != "" {
		detail = strings.TrimSpace(strings.Join([]string{item.Group, detail}, " — "))
	}
	node := ContextNode{
		Kind:        kind,
		Label:       item.Name,
		Detail:      detail,
		Path:        item.Path,
		Status:      refStatus(item),
		Used:        item.RefBy != "",
		RelatedView: "config",
		RelatedPath: item.Path,
	}
	if pid, componentPath, componentType := pluginTargetForPath(item.Path, plugins); pid != "" {
		node.RelatedPluginID = pid
		node.RelatedPluginComponentPath = componentPath
		node.RelatedPluginComponentType = componentType
	}
	return node
}

func pluginTargetForPath(path string, plugins *PluginTree) (pluginID, componentPath, componentType string) {
	if path == "" || plugins == nil {
		return "", "", ""
	}
	clean := filepath.Clean(path)
	for _, plugin := range plugins.Plugins {
		installPath := filepath.Clean(plugin.Install.InstallPath)
		if installPath != "" && (clean == installPath || strings.HasPrefix(clean, installPath+string(filepath.Separator))) {
			for _, component := range plugin.Components {
				cp := filepath.Clean(component.Path)
				if cp == clean {
					return plugin.ID, cp, component.Type
				}
			}
			return plugin.ID, "", ""
		}
	}
	return "", "", ""
}

func refStatus(item ConfigItem) string {
	if item.RefBy != "" {
		return "referenced"
	}
	return "configured"
}

func usedSkillNodes(entries []Entry) []ContextNode {
	seen := map[string]bool{}
	var nodes []ContextNode
	for _, entry := range entries {
		for _, block := range entry.Content {
			if block.Type != "tool_use" || block.ToolName != "Skill" {
				continue
			}
			var input struct {
				Skill string `json:"skill"`
				Args  string `json:"args"`
			}
			if json.Unmarshal([]byte(block.ToolInput), &input) != nil || input.Skill == "" || seen[input.Skill] {
				continue
			}
			seen[input.Skill] = true
			nodes = append(nodes, ContextNode{Kind: ContextSkill, Label: input.Skill, Detail: input.Args, Used: true})
		}
	}
	return nodes
}

func usedMCPNodes(entries []Entry) []ContextNode {
	seen := map[string]bool{}
	var nodes []ContextNode
	for _, entry := range entries {
		for _, block := range entry.Content {
			if block.Type != "tool_use" || !strings.HasPrefix(block.ToolName, "mcp__") || seen[block.ToolName] {
				continue
			}
			seen[block.ToolName] = true
			nodes = append(nodes, ContextNode{Kind: ContextMCP, Label: block.ToolName, Detail: mcpServerLabel(block.ToolName), Used: true})
		}
	}
	return nodes
}

func mcpServerLabel(toolName string) string {
	trimmed := strings.TrimPrefix(toolName, "mcp__")
	parts := strings.Split(trimmed, "__")
	if len(parts) == 0 {
		return ""
	}
	return strings.ReplaceAll(parts[0], "_", " ")
}

func usedHookNodes(entries []Entry) []ContextNode {
	seen := map[string]ContextNode{}
	for _, entry := range entries {
		for _, block := range entry.Content {
			for _, hook := range block.Hooks {
				key := strings.Join([]string{hook.Event, hook.Name, hook.Command}, "\x00")
				if _, ok := seen[key]; ok {
					continue
				}
				label := hook.Name
				if label == "" {
					label = hook.Event
				}
				seen[key] = ContextNode{Kind: ContextHook, Label: label, Detail: hook.Command, Status: hook.Event, Used: true}
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	nodes := make([]ContextNode, 0, len(keys))
	for _, key := range keys {
		nodes = append(nodes, seen[key])
	}
	return nodes
}

func usedCommandNodes(entries []Entry) []ContextNode {
	seen := map[string]bool{}
	var nodes []ContextNode
	for _, entry := range entries {
		for _, block := range entry.Content {
			if block.Type != "system_tag" || block.TagName != "command-name" {
				continue
			}
			name := strings.TrimSpace(block.Text)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			nodes = append(nodes, ContextNode{Kind: ContextCommand, Label: name, Used: true})
		}
	}
	return nodes
}

func fileStatus(path string) string {
	if _, err := os.Stat(path); err == nil {
		return "present"
	}
	return "missing"
}
