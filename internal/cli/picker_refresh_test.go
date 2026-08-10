package cli

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/opener"
)

// TestPickerRefreshReextractsItems verifies pressing R re-reads the session file
// and rebuilds the picker list. A URL added to the transcript after launch shows
// up only after refresh.
func TestPickerRefreshReextractsItems(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-1.jsonl")

	// Initial transcript: one URL.
	before := `{"type":"user","uuid":"u1","message":{"role":"user","content":"see https://example.com/one"}}
`
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	sources := []pickerSource{{filePath: path, sessID: "sess-1", label: "sess-1"}}
	items, err := collectItems("urls", sources)
	if err != nil {
		t.Fatal(err)
	}
	m := newPickerModel("urls", items, opener.Config{}, pickerContext{command: "urls", sources: sources})
	m.width, m.height = 120, 40
	startCount := len(m.allItems)
	if startCount == 0 {
		t.Fatal("expected at least one URL before refresh")
	}

	// Append a second URL to the session file.
	after := before + `{"type":"user","uuid":"u2","message":{"role":"user","content":"and https://example.com/two"}}
`
	if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
		t.Fatal(err)
	}

	// Press R.
	model, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	m = model.(pickerModel)
	if len(m.allItems) <= startCount {
		t.Fatalf("refresh did not pick up the new URL: before=%d after=%d", startCount, len(m.allItems))
	}
}

// TestPickerRefreshKeepsSearchTerm verifies refresh reapplies the active search.
func TestPickerRefreshKeepsSearchTerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-2.jsonl")
	content := `{"type":"user","uuid":"u1","message":{"role":"user","content":"https://alpha.example https://beta.example"}}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sources := []pickerSource{{filePath: path, sessID: "sess-2", label: "sess-2"}}
	items, _ := collectItems("urls", sources)
	m := newPickerModel("urls", items, opener.Config{}, pickerContext{command: "urls", sources: sources})
	m.width, m.height = 120, 40
	m.searchTerm = "alpha"
	m.filterItems()
	filteredCount := len(m.items)

	model, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	m = model.(pickerModel)
	if m.searchTerm != "alpha" {
		t.Fatalf("refresh lost the search term: %q", m.searchTerm)
	}
	if len(m.items) != filteredCount {
		t.Fatalf("refresh changed the filtered set: before=%d after=%d", filteredCount, len(m.items))
	}
}
