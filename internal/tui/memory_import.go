package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/session"
)

// importWorktreeMemory imports memory files from a worktree session's project
// into the main (parent) project's memory directory.
func (a *App) importWorktreeMemory(sess session.Session) (tea.Model, tea.Cmd) {
	if !sess.IsWorktree {
		a.copiedMsg = "Not a worktree session"
		return a, nil
	}

	mainPath := session.ResolveMainProjectPath(sess.ProjectPath, a.config.WorktreeDir)
	if mainPath == sess.ProjectPath {
		a.copiedMsg = "Cannot resolve main project"
		return a, nil
	}

	// List memory files with conflict info
	files, err := session.ListProjectMemoryConflicts(sess.ProjectPath, mainPath)
	if err != nil {
		a.copiedMsg = "Error: " + err.Error()
		return a, nil
	}
	if len(files) == 0 {
		a.copiedMsg = "No memory files in worktree"
		return a, nil
	}

	// Build items for URL menu picker (reuse existing multi-select UI)
	var items []extract.Item
	for _, f := range files {
		label := f.Name
		if f.ExistsIn {
			label += " (overwrite)"
		}
		items = append(items, extract.Item{
			URL:      f.Name,
			Category: "memory",
			Label:    label,
		})
	}

	// Store import context for when selection is confirmed
	a.memImportSrc = sess.ProjectPath
	a.memImportDst = mainPath
	a.memImportActive = true

	// Open URL menu with the memory files
	a.urlMenu = true
	a.urlAllItems = items
	a.urlItems = items
	a.urlCursor = 0
	a.urlSelected = make(map[string]bool)
	// Pre-select all files
	for _, item := range items {
		a.urlSelected[item.URL] = true
	}
	a.urlSearching = false
	a.urlSearchTerm = ""
	a.urlScope = "memory import"
	return a, nil
}

// removeSessionMemory opens a picker to delete memory files from a session's project.
func (a *App) removeSessionMemory(sess session.Session) (tea.Model, tea.Cmd) {
	if !sess.HasMemory {
		a.copiedMsg = "No memory files"
		return a, nil
	}

	files, err := session.ListProjectMemory(sess.ProjectPath)
	if err != nil {
		a.copiedMsg = "Error: " + err.Error()
		return a, nil
	}
	if len(files) == 0 {
		a.copiedMsg = "No memory files"
		return a, nil
	}

	var items []extract.Item
	for _, f := range files {
		items = append(items, extract.Item{
			URL:      f.Name,
			Category: "memory",
			Label:    fmt.Sprintf("%s (%d bytes)", f.Name, f.Size),
		})
	}

	a.memRemoveSrc = sess.ProjectPath
	a.memRemoveActive = true

	a.urlMenu = true
	a.urlAllItems = items
	a.urlItems = items
	a.urlCursor = 0
	a.urlSelected = make(map[string]bool)
	a.urlSearching = false
	a.urlSearchTerm = ""
	a.urlScope = "remove memory"
	return a, nil
}

// commitMemoryRemove is called when the URL menu is confirmed with memory remove active.
func (a *App) commitMemoryRemove() {
	if !a.memRemoveActive {
		return
	}

	var selected []string
	for name, sel := range a.urlSelected {
		if sel {
			selected = append(selected, name)
		}
	}

	a.memRemoveActive = false

	if len(selected) == 0 {
		a.copiedMsg = "No files selected"
		return
	}

	deleted, err := session.DeleteMemoryFiles(a.memRemoveSrc, selected)
	if err != nil {
		a.copiedMsg = fmt.Sprintf("Delete error: %s", err)
		return
	}

	names := strings.Join(selected, ", ")
	if len(names) > 50 {
		names = names[:47] + "..."
	}
	a.copiedMsg = fmt.Sprintf("Deleted %d file(s): %s", deleted, names)
}

// commitMemoryImport is called when the URL menu is confirmed with memory import active.
func (a *App) commitMemoryImport() {
	if !a.memImportActive {
		return
	}

	var selected []string
	for name, sel := range a.urlSelected {
		if sel {
			selected = append(selected, name)
		}
	}

	a.memImportActive = false

	if len(selected) == 0 {
		a.copiedMsg = "No files selected"
		return
	}

	copied, err := session.ImportMemoryFiles(a.memImportSrc, a.memImportDst, selected)
	if err != nil {
		a.copiedMsg = fmt.Sprintf("Import error: %s", err)
		return
	}

	shortDst := extract.ShortenPath(a.memImportDst)
	a.copiedMsg = fmt.Sprintf("Imported %d memory file(s) → %s", copied, shortDst)

	// Show details if multiple
	if copied > 0 {
		names := strings.Join(selected, ", ")
		if len(names) > 50 {
			names = names[:47] + "..."
		}
		a.copiedMsg = fmt.Sprintf("Imported %d file(s): %s", copied, names)
	}
}
