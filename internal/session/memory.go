package session

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MemoryNote is one parsed memory file from a project's memory/ directory.
// Claude Code writes these as YAML-frontmatter + markdown body; MEMORY.md is a
// frontmatter-less index of the others.
type MemoryNote struct {
	Name        string   // frontmatter name, else filename sans .md
	FileName    string   // base filename (e.g. "k7s-search-input-ux.md")
	Description string   // frontmatter description (one-line summary)
	Type        string   // metadata.type: user|feedback|project|reference
	Body        string   // markdown body (frontmatter stripped)
	Links       []string // [[wikilink]] targets referenced in the body
	IsIndex     bool     // true for MEMORY.md
}

var (
	memFrontmatterRe = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n?`)
	memWikilinkRe    = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
)

// LoadMemoryNotes reads and parses every memory note for a project, returning
// the index (MEMORY.md) first when present, then the remaining notes sorted by
// name. Returns nil when the memory directory is absent or empty.
func LoadMemoryNotes(projectPath, home string) []MemoryNote {
	if projectPath == "" {
		return nil
	}
	encoded := EncodeProjectPath(projectPath)
	memDir := filepath.Join(home, ".claude", "projects", encoded, "memory")
	entries, err := os.ReadDir(memDir)
	if err != nil {
		return nil
	}

	var index *MemoryNote
	var notes []MemoryNote
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(memDir, e.Name()))
		if err != nil || len(data) == 0 {
			continue
		}
		note := parseMemoryNote(e.Name(), string(data))
		if note.IsIndex {
			n := note
			index = &n
			continue
		}
		notes = append(notes, note)
	}

	sort.Slice(notes, func(i, j int) bool {
		return notes[i].Name < notes[j].Name
	})

	if index != nil {
		return append([]MemoryNote{*index}, notes...)
	}
	return notes
}

func parseMemoryNote(fileName, content string) MemoryNote {
	note := MemoryNote{
		FileName: fileName,
		Name:     strings.TrimSuffix(fileName, ".md"),
		IsIndex:  fileName == "MEMORY.md",
	}

	body := content
	if m := memFrontmatterRe.FindStringSubmatch(content); m != nil {
		parseMemoryFrontmatter(m[1], &note)
		body = content[len(m[0]):]
	}
	note.Body = strings.Trim(body, "\n")

	// Collect wikilink targets from the body.
	for _, lm := range memWikilinkRe.FindAllStringSubmatch(note.Body, -1) {
		note.Links = append(note.Links, lm[1])
	}
	return note
}

// parseMemoryFrontmatter does a minimal, dependency-free parse of the small,
// well-known frontmatter shape (name/description + a nested metadata block with
// type). It is not a general YAML parser — it only extracts the fields ccx
// displays, tolerating the exact layout Claude Code writes.
func parseMemoryFrontmatter(fm string, note *MemoryNote) {
	inMetadata := false
	for _, raw := range strings.Split(fm, "\n") {
		line := strings.TrimRight(raw, " \t")
		if line == "" {
			continue
		}
		indented := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
		trimmed := strings.TrimSpace(line)
		key, val, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)

		switch {
		case !indented && key == "name":
			if val != "" {
				note.Name = val
			}
		case !indented && key == "description":
			note.Description = val
		case !indented && key == "metadata":
			inMetadata = true
		case indented && inMetadata && key == "type":
			note.Type = val
		case !indented:
			// A new top-level key ends the metadata block.
			inMetadata = false
		}
	}
}
