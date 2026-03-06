package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractDescription(t *testing.T) {
	dir := t.TempDir()
	mdFile := filepath.Join(dir, "test.md")
	os.WriteFile(mdFile, []byte("# My Title\n\nSome content"), 0644)

	desc := extractDescription(mdFile)
	if desc != "My Title" {
		t.Errorf("expected 'My Title', got %q", desc)
	}
}

func TestExtractDescriptionNoHeading(t *testing.T) {
	dir := t.TempDir()
	mdFile := filepath.Join(dir, "test.md")
	os.WriteFile(mdFile, []byte("No heading here\njust text"), 0644)

	desc := extractDescription(mdFile)
	if desc != "" {
		t.Errorf("expected empty, got %q", desc)
	}
}

func TestExtractFrontmatter(t *testing.T) {
	dir := t.TempDir()
	mdFile := filepath.Join(dir, "skill.md")
	os.WriteFile(mdFile, []byte("---\nname: my-skill\ndescription: \"Does cool stuff\"\n---\n# Heading"), 0644)

	desc := extractFrontmatter(mdFile, "description")
	if desc != "Does cool stuff" {
		t.Errorf("expected 'Does cool stuff', got %q", desc)
	}
}

func TestExtractFrontmatterMissing(t *testing.T) {
	dir := t.TempDir()
	mdFile := filepath.Join(dir, "skill.md")
	os.WriteFile(mdFile, []byte("---\nname: my-skill\n---\n# Heading"), 0644)

	desc := extractFrontmatter(mdFile, "description")
	if desc != "" {
		t.Errorf("expected empty, got %q", desc)
	}
}

func TestScanConfig(t *testing.T) {
	dir := t.TempDir()

	// Create global CLAUDE.md
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# Global Config"), 0644)

	// Create memory dir with a file
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0755)
	os.WriteFile(filepath.Join(memDir, "notes.md"), []byte("# Notes"), 0644)

	// Create agents dir
	agentsDir := filepath.Join(dir, "agents")
	os.MkdirAll(agentsDir, 0755)
	os.WriteFile(filepath.Join(agentsDir, "planner.md"), []byte("# Planner"), 0644)

	tree, err := ScanConfig(dir, "")
	if err != nil {
		t.Fatal(err)
	}

	// Count items per category
	counts := make(map[ConfigCategory]int)
	for _, item := range tree.Items {
		counts[item.Category]++
	}
	if counts[ConfigGlobal] != 2 { // CLAUDE.md + memory/notes.md
		t.Errorf("expected 2 global items, got %d", counts[ConfigGlobal])
	}
	if counts[ConfigAgent] != 1 {
		t.Errorf("expected 1 agent item, got %d", counts[ConfigAgent])
	}
}

func TestScanConfigWithProject(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "myproject")
	os.MkdirAll(projectPath, 0755)

	// Local CLAUDE.md
	os.WriteFile(filepath.Join(projectPath, "CLAUDE.md"), []byte("# Local"), 0644)

	// Project-level memory
	encoded := EncodeProjectPath(projectPath)
	projMemDir := filepath.Join(dir, "projects", encoded, "memory")
	os.MkdirAll(projMemDir, 0755)
	os.WriteFile(filepath.Join(projMemDir, "MEMORY.md"), []byte("# Memory"), 0644)

	tree, err := ScanConfig(dir, projectPath)
	if err != nil {
		t.Fatal(err)
	}

	counts := make(map[ConfigCategory]int)
	for _, item := range tree.Items {
		counts[item.Category]++
	}
	if counts[ConfigProject] != 1 { // memory/MEMORY.md
		t.Errorf("expected 1 project item, got %d", counts[ConfigProject])
	}
	if counts[ConfigLocal] != 1 { // CLAUDE.md
		t.Errorf("expected 1 local item, got %d", counts[ConfigLocal])
	}
}
