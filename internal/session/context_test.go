package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildSessionContextTreeSeparatesUsedAndConfigured(t *testing.T) {
	claudeDir := t.TempDir()
	projectPath := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(projectPath, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(projectPath, "CLAUDE.md"), []byte("# Project"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, "AGENTS.md"), []byte("# Agents"), 0644); err != nil {
		t.Fatal(err)
	}

	skillDir := filepath.Join(claudeDir, "skills", "demo-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: Demo skill\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(claudeDir, "hooks", "post.sh")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	settings := `{"hooks":{"PostToolUse":[{"matcher":"Read","hooks":[{"command":"bash ` + hookPath + `"}]}]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0644); err != nil {
		t.Fatal(err)
	}

	sessionFile := filepath.Join(t.TempDir(), "session.jsonl")
	jsonl := `{"type":"user","timestamp":"2026-05-14T00:00:00Z","message":{"role":"user","content":"<command-name>/clear</command-name>"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-05-14T00:00:01Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"skill-1","name":"Skill","input":{"skill":"demo-skill","args":"x"}},{"type":"tool_use","id":"mcp-1","name":"mcp__claude_ai_Atlassian__search","input":{"query":"demo"}},{"type":"tool_use","id":"task-1","name":"TaskCreate","input":{"id":"1","subject":"Check context","status":"pending"}}]}}` + "\n" +
		`{"type":"progress","timestamp":"2026-05-14T00:00:02Z","toolUseID":"skill-1","data":{"type":"hook_progress","hookEvent":"PostToolUse","hookName":"PostToolUse:Skill","command":"bash ` + hookPath + `"}}` + "\n"
	if err := os.WriteFile(sessionFile, []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	tree, err := BuildSessionContextTree(claudeDir, Session{
		ID:          "session-1",
		FilePath:    sessionFile,
		ProjectPath: projectPath,
		ModTime:     time.Now(),
		HasTasks:    true,
		HasSkills:   true,
		HasMCP:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !hasContextPath(tree.Roots, "Skills", "Used in session", "demo-skill") {
		t.Fatalf("expected used skill in context tree: %#v", tree.Roots)
	}
	if !hasContextPath(tree.Roots, "Skills", "Available/configured", "demo-skill") {
		t.Fatalf("expected configured skill in context tree: %#v", tree.Roots)
	}
	if !hasContextPath(tree.Roots, "MCP", "Used in session", "mcp__claude_ai_Atlassian__search") {
		t.Fatalf("expected used MCP tool in context tree: %#v", tree.Roots)
	}
	if !hasContextPath(tree.Roots, "Hooks", "Used in session", "PostToolUse:Skill") {
		t.Fatalf("expected executed hook in context tree: %#v", tree.Roots)
	}
	if !hasContextPath(tree.Roots, "Commands and agents", "Slash commands used", "/clear") {
		t.Fatalf("expected slash command in context tree: %#v", tree.Roots)
	}
	if !hasContextPath(tree.Roots, "Internal session state", "Task board", "Check context") {
		t.Fatalf("expected task board item in context tree: %#v", tree.Roots)
	}
}

func hasContextPath(nodes []ContextNode, labels ...string) bool {
	if len(labels) == 0 {
		return true
	}
	for _, node := range nodes {
		if node.Label == labels[0] && hasContextPath(node.Children, labels[1:]...) {
			return true
		}
	}
	return false
}
