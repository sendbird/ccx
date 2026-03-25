package tui

import (
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/session"
)

// startWorktreeAlign lists misaligned worktrees for the selected session's repo
// and opens the URL menu picker to select which ones to move.
func (a *App) startWorktreeAlign() (tea.Model, tea.Cmd) {
	sess, ok := a.selectedSession()
	if !ok {
		a.copiedMsg = "No session selected"
		return a, nil
	}

	// Get repo root
	out, err := exec.Command("git", "-C", sess.ProjectPath, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		a.copiedMsg = "Not a git repo"
		return a, nil
	}
	repoRoot := strings.TrimSpace(string(out))

	// List worktrees
	worktrees, err := session.ListWorktrees(repoRoot)
	if err != nil {
		a.copiedMsg = "Error: " + err.Error()
		return a, nil
	}

	// Filter misaligned ones
	misaligned := session.MisalignedWorktrees(repoRoot, worktrees, a.config.WorktreeDir)
	if len(misaligned) == 0 {
		a.copiedMsg = "All worktrees aligned"
		return a, nil
	}

	// Build items for URL menu picker
	var items []extract.Item
	for _, wt := range misaligned {
		label := wt.Path
		if wt.Branch != "" {
			label = fmt.Sprintf("%s [%s]", wt.Path, wt.Branch)
		}
		items = append(items, extract.Item{
			URL:      wt.Path,
			Category: "worktree",
			Label:    label,
		})
	}

	// Store context
	a.worktreeAlignActive = true
	a.worktreeAlignRepo = repoRoot

	// Open URL menu with misaligned worktrees
	a.urlMenu = true
	a.urlAllItems = items
	a.urlItems = items
	a.urlCursor = 0
	a.urlSelected = make(map[string]bool)
	// Pre-select all
	for _, item := range items {
		a.urlSelected[item.URL] = true
	}
	a.urlSearching = false
	a.urlSearchTerm = ""
	a.urlScope = "worktree align"
	return a, nil
}

// commitWorktreeAlign moves selected misaligned worktrees to the configured worktree directory.
func (a *App) commitWorktreeAlign() {
	if !a.worktreeAlignActive {
		return
	}

	var selected []string
	for path, sel := range a.urlSelected {
		if sel {
			selected = append(selected, path)
		}
	}

	a.worktreeAlignActive = false

	if len(selected) == 0 {
		a.copiedMsg = "No worktrees selected"
		return
	}

	moved := 0
	var lastErr error
	for _, oldPath := range selected {
		_, err := session.AlignWorktree(a.worktreeAlignRepo, oldPath, a.config.WorktreeDir)
		if err != nil {
			lastErr = err
			continue
		}
		moved++
	}

	if lastErr != nil && moved == 0 {
		a.copiedMsg = fmt.Sprintf("Align failed: %s", lastErr)
		return
	}

	a.copiedMsg = fmt.Sprintf("Aligned %d worktree(s) → %s/", moved, a.config.WorktreeDir)
}
