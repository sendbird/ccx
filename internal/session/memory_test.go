package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMemoryNote_Frontmatter(t *testing.T) {
	content := `---
name: k7s-search-input-ux
description: "One-sentence rule, quoted."
metadata:
  node_type: memory
  type: project
  originSessionId: f3b138d3
---

Lead paragraph stating the rule.

**Why:** rationale here.

Related: [[k7s-cache-and-autocomplete]] [[k7s-label-based-search]]`

	note := parseMemoryNote("k7s-search-input-ux.md", content)
	if note.Name != "k7s-search-input-ux" {
		t.Errorf("name = %q", note.Name)
	}
	if note.Description != "One-sentence rule, quoted." {
		t.Errorf("description = %q", note.Description)
	}
	if note.Type != "project" {
		t.Errorf("type = %q, want project", note.Type)
	}
	if note.IsIndex {
		t.Error("IsIndex = true, want false")
	}
	if len(note.Links) != 2 || note.Links[0] != "k7s-cache-and-autocomplete" {
		t.Errorf("links = %v", note.Links)
	}
	if strings.Contains(note.Body, "---") {
		t.Errorf("frontmatter leaked into body: %q", note.Body)
	}
	if !strings.HasPrefix(note.Body, "Lead paragraph") {
		t.Errorf("body = %q", note.Body)
	}
}

func TestParseMemoryNote_Index(t *testing.T) {
	content := "- [k7s pure scope](k7s-pure-kubeconfig-scope.md) — stay generic.\n"
	note := parseMemoryNote("MEMORY.md", content)
	if !note.IsIndex {
		t.Error("MEMORY.md should be flagged as index")
	}
	if note.Name != "MEMORY" {
		t.Errorf("name = %q", note.Name)
	}
}

func TestParseMemoryNote_NoFrontmatter(t *testing.T) {
	note := parseMemoryNote("plain.md", "just a body, no frontmatter")
	if note.Name != "plain" {
		t.Errorf("name = %q", note.Name)
	}
	if note.Description != "" || note.Type != "" {
		t.Errorf("expected empty meta, got desc=%q type=%q", note.Description, note.Type)
	}
	if note.Body != "just a body, no frontmatter" {
		t.Errorf("body = %q", note.Body)
	}
}

func TestLoadMemoryNotes_IndexFirst(t *testing.T) {
	home := t.TempDir()
	projectPath := "/Users/x/proj"
	encoded := EncodeProjectPath(projectPath)
	memDir := filepath.Join(home, ".claude", "projects", encoded, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(memDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("zebra.md", "---\nname: zebra\n---\nz body")
	write("alpha.md", "---\nname: alpha\n---\na body")
	write("MEMORY.md", "- index line\n")

	notes := LoadMemoryNotes(projectPath, home)
	if len(notes) != 3 {
		t.Fatalf("got %d notes, want 3", len(notes))
	}
	if !notes[0].IsIndex {
		t.Errorf("first note should be index, got %q", notes[0].Name)
	}
	// Remaining sorted by name: alpha before zebra.
	if notes[1].Name != "alpha" || notes[2].Name != "zebra" {
		t.Errorf("order = %q, %q; want alpha, zebra", notes[1].Name, notes[2].Name)
	}
}

func TestLoadMemoryNotes_None(t *testing.T) {
	if notes := LoadMemoryNotes("/nonexistent", t.TempDir()); notes != nil {
		t.Errorf("got %v, want nil", notes)
	}
}
