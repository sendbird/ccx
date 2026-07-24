package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/session"
)

// --- Conversation inspector actions menu ---

// handleConvActionsMenu processes key events for the conversation actions menu.
func (a *App) handleConvActionsMenu(key string) (tea.Model, tea.Cmd) {
	a.convActionsMenu = false
	actions := a.conversationActionMenuActions()
	switch {
	case interactionKeyMatches(actions, key, interactionActionURLs):
		a.openInspector(inspectorRefs, a.conv.inspector.Scope, false)
	case interactionKeyMatches(actions, key, interactionActionFiles):
		a.openInspector(inspectorFiles, a.conv.inspector.Scope, false)
	case interactionKeyMatches(actions, key, interactionActionChanges):
		a.openInspector(inspectorChanges, a.conv.inspector.Scope, false)
	case interactionKeyMatches(actions, key, interactionActionCopy):
		a.copyConvSelection()
	}
	return a, nil
}

// renderConvActionsHintBox renders the conversation inspector actions hint box.
func (a *App) renderConvActionsHintBox() string {
	return renderInteractionHintBox([][]interactionAction{a.conversationActionMenuActions()}, "esc:cancel")
}

// --- URL menu state & handlers ---

func changeItemLabel(ch extract.ChangeItem) string {
	label := ch.Item.Label + "  " + ch.Summary
	if !ch.Timestamp.IsZero() {
		label += "  " + timeAgo(ch.Timestamp)
	}
	return label
}

func changeItemsFromSlice(changes []extract.ChangeItem) ([]extract.Item, map[string]extract.ChangeItem) {
	items := make([]extract.Item, 0, len(changes))
	cmap := make(map[string]extract.ChangeItem, len(changes))
	for _, ch := range changes {
		cmap[ch.Item.URL] = ch
		items = append(items, extract.Item{
			URL:       ch.Item.URL,
			Label:     changeItemLabel(ch),
			Category:  "change",
			Timestamp: ch.Timestamp,
		})
	}
	return items, cmap
}

func (a *App) initDiffViewport() {
	h := ContentHeight(a.height) - 4
	if h < 5 {
		h = 5
	}
	w := a.width/2 - 4
	if w < 20 {
		w = 20
	}
	a.urlDiffVP = viewport.New(w, h)
	a.urlDiffReady = true
	a.updateChangeDiffPreview()
}

func (a *App) updateChangeDiffPreview() {
	if !a.urlDiffReady || a.urlChangeMap == nil {
		return
	}
	if a.urlCursor < 0 || a.urlCursor >= len(a.urlItems) {
		a.urlDiffVP.SetContent(dimStyle.Render("(no selection)"))
		return
	}
	filePath := a.urlItems[a.urlCursor].URL
	ch, ok := a.urlChangeMap[filePath]
	if !ok {
		a.urlDiffVP.SetContent(dimStyle.Render("(no diff data)"))
		return
	}
	w := a.urlDiffVP.Width
	if w < 20 {
		w = 60
	}
	var buf strings.Builder
	for i, toolInput := range ch.ToolInputs {
		toolName := ""
		if i < len(ch.ToolNames) {
			toolName = ch.ToolNames[i]
		}
		block := session.ContentBlock{
			Type:      "tool_use",
			ToolName:  toolName,
			ToolInput: toolInput,
		}
		diff := toolDiffOutput(block, w)
		if diff != "" {
			if buf.Len() > 0 {
				buf.WriteString("\n")
			}
			buf.WriteString(diff)
		}
	}
	if buf.Len() == 0 {
		a.urlDiffVP.SetContent(dimStyle.Render("(no diff)"))
	} else {
		a.urlDiffVP.SetContent(buf.String())
	}
}

func (a *App) openURLMenuFromItems(items []extract.Item, scope string) (tea.Model, tea.Cmd) {
	if len(items) == 0 {
		if strings.Contains(scope, "files") || strings.Contains(scope, "changes") {
			a.copiedMsg = "No files found"
		} else {
			a.copiedMsg = "No URLs found"
		}
		return a, nil
	}
	a.urlMenu = true
	a.urlAllItems = items
	a.urlItems = items
	a.urlCursor = 0
	a.urlSelected = make(map[string]bool)
	a.urlSearching = false
	a.urlSearchTerm = ""
	a.urlScope = scope
	a.urlRefStatus = make(map[string]session.SessionRef)
	return a, a.resolveURLRefsCmd(items)
}

// resolveURLRefsCmd asynchronously resolves PR/Jira status for every GitHub-PR
// or Jira URL in the menu, streaming each result back as a urlRefStatusMsg. It
// reuses session.ResolveRef, so results are TTL-cached and share the same
// bounded concurrency as the References preview. File/changes scopes contain no
// PR/Jira links, so this is a no-op for them.
func (a *App) resolveURLRefsCmd(items []extract.Item) tea.Cmd {
	if a.isFileScope() {
		return nil
	}
	var cmds []tea.Cmd
	for _, it := range items {
		if it.Category != "pr" && it.Category != "jira" {
			continue
		}
		ref, ok := session.ClassifyURLRef(it.URL)
		if !ok {
			continue
		}
		r := ref
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			return urlRefStatusMsg{ref: session.ResolveRef(ctx, r)}
		})
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// handleURLMenu processes key events while the URL menu is open.
func (a *App) handleURLMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Search input mode
	if a.urlSearching {
		switch key {
		case "esc":
			a.urlSearching = false
			// Keep filter applied; press esc again to close menu
			return a, nil
		case "enter":
			a.urlSearching = false
			a.urlSearchTerm = a.urlSearchInput.Value()
			a.filterURLItems()
			return a, nil
		default:
			var cmd tea.Cmd
			a.urlSearchInput, cmd = a.urlSearchInput.Update(msg)
			// Live filter as user types
			a.urlSearchTerm = a.urlSearchInput.Value()
			a.filterURLItems()
			return a, cmd
		}
	}

	switch key {
	case "esc":
		if a.urlSearchTerm != "" {
			// First esc clears search
			a.urlSearchTerm = ""
			a.urlItems = a.urlAllItems
			a.urlCursor = 0
			return a, nil
		}
		a.closeURLMenu()
		return a, nil
	case "q":
		a.closeURLMenu()
		return a, nil
	case "up", "k":
		if a.urlCursor > 0 {
			a.urlCursor--
			a.updateChangeDiffPreview()
		}
		return a, nil
	case "down", "j":
		if a.urlCursor < len(a.urlItems)-1 {
			a.urlCursor++
			a.updateChangeDiffPreview()
		}
		return a, nil
	case "ctrl+d":
		if a.isChangesScope() {
			a.urlDiffVP.HalfViewDown()
			return a, nil
		}
	case "ctrl+u":
		if a.isChangesScope() {
			a.urlDiffVP.HalfViewUp()
			return a, nil
		}
	case "/":
		a.urlSearching = true
		ti := textinput.New()
		ti.Prompt = "/"
		ti.Width = 40
		ti.SetValue(a.urlSearchTerm)
		ti.Focus()
		a.urlSearchInput = ti
		return a, nil
	case " ":
		// Toggle selection on current item
		if a.urlCursor >= 0 && a.urlCursor < len(a.urlItems) {
			u := a.urlItems[a.urlCursor].URL
			if a.urlSelected[u] {
				delete(a.urlSelected, u)
			} else {
				a.urlSelected[u] = true
			}
			// Move cursor down after toggle
			if a.urlCursor < len(a.urlItems)-1 {
				a.urlCursor++
			}
		}
		return a, nil
	case "enter":
		urls := a.selectedURLs()
		if len(urls) == 0 {
			return a, nil
		}
		a.closeURLMenu()
		// Memory import: copy selected files instead of opening
		if a.memImportActive {
			a.commitMemoryImport()
			return a, nil
		}
		// Memory remove: delete selected files
		if a.memRemoveActive {
			a.commitMemoryRemove()
			return a, nil
		}
		// Worktree align: move selected worktrees
		if a.worktreeAlignActive {
			a.commitWorktreeAlign()
			return a, nil
		}
		if a.isFileScope() {
			// Open first selected file in editor
			return a.openInEditor(urls[0])
		}
		opened := 0
		for _, u := range urls {
			if err := a.openInBrowser(u); err == nil {
				opened++
			}
		}
		a.copiedMsg = fmt.Sprintf("Opened %d URL(s)", opened)
		return a, nil
	case "y":
		urls := a.selectedURLs()
		if len(urls) == 0 {
			return a, nil
		}
		copyToClipboard(strings.Join(urls, "\n"))
		a.copiedMsg = fmt.Sprintf("Copied %d URL(s)", len(urls))
		a.closeURLMenu()
		return a, nil
	}
	return a, nil
}

// selectedURLs returns the URLs to act on: selected set if any, otherwise current cursor item.
func (a *App) selectedURLs() []string {
	if len(a.urlSelected) > 0 {
		// Preserve display order
		var urls []string
		for _, item := range a.urlItems {
			if a.urlSelected[item.URL] {
				urls = append(urls, item.URL)
			}
		}
		return urls
	}
	if a.urlCursor >= 0 && a.urlCursor < len(a.urlItems) {
		return []string{a.urlItems[a.urlCursor].URL}
	}
	return nil
}

func (a *App) closeURLMenu() {
	a.urlMenu = false
	a.memImportActive = false
	a.memRemoveActive = false
	a.worktreeAlignActive = false
	a.urlChangeMap = nil
	a.urlDiffReady = false
	a.urlRefStatus = nil
}

// urlRefStatusText returns the styled, right-hand PR/Jira status suffix for a
// URL-menu row (leading with two spaces), or "" when the URL is not a resolved
// PR/Jira link. A resolve in flight renders a dim "…".
func (a *App) urlRefStatusText(url string) string {
	if a.urlRefStatus == nil {
		return ""
	}
	ref, ok := a.urlRefStatus[url]
	if !ok {
		return ""
	}
	txt := session.RefStatusText(ref)
	if txt == "" {
		return ""
	}
	return "  " + dimStyle.Render(txt)
}

// isFileScope returns true when the URL menu is showing file-like paths, not URLs.
func (a *App) isFileScope() bool {
	return strings.Contains(a.urlScope, "files") || strings.Contains(a.urlScope, "changes")
}

// isChangesScope returns true when the URL menu is showing change diffs.
func (a *App) isChangesScope() bool {
	return strings.Contains(a.urlScope, "changes")
}

// filterURLItems filters urlItems based on the search term.
func (a *App) filterURLItems() {
	term := strings.ToLower(a.urlSearchTerm)
	if term == "" {
		a.urlItems = a.urlAllItems
		a.urlCursor = 0
		return
	}
	// Fuzzy: every whitespace-separated term must fuzzy-match the row text (its
	// chars appear in order), so "grui" matches "internal/gui" and space-joined
	// terms narrow further. Matches the project's project-picker search feel.
	terms := strings.Fields(term)
	var filtered []extract.Item
	for _, item := range a.urlAllItems {
		text := strings.ToLower(item.URL + " " + item.Label + " " + item.Category)
		match := true
		for _, t := range terms {
			if !fuzzyMatch(text, t) {
				match = false
				break
			}
		}
		if match {
			filtered = append(filtered, item)
		}
	}
	a.urlItems = filtered
	if a.urlCursor >= len(a.urlItems) {
		a.urlCursor = max(len(a.urlItems)-1, 0)
	}
	a.updateChangeDiffPreview()
}

// renderURLMenu renders the URL selection menu as a hint box.
func (a *App) renderURLMenu() string {
	items := a.urlItems
	cursor := a.urlCursor
	maxH := ContentHeight(a.height)

	if len(items) == 0 && a.urlSearchTerm != "" {
		d := dimStyle
		t := "URLs"
		if strings.Contains(a.urlScope, "changes") {
			t = "Changes"
		} else if a.isFileScope() {
			t = "Files"
		}
		body := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(t) + "\n" +
			d.Render("No matches for: "+a.urlSearchTerm) + "\n" +
			d.Render("/:search  esc:clear")
		boxStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorDim).
			Padding(0, 1)
		return boxStyle.Render(body)
	}
	if len(items) == 0 {
		return ""
	}

	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	sel := lipgloss.NewStyle().Foreground(colorBorderFocused).Bold(true)
	d := dimStyle
	catStyle := lipgloss.NewStyle().Foreground(colorIndigo).Bold(true)

	isFiles := a.isFileScope()
	catBadge := func(cat string) string {
		switch cat {
		case "pr":
			return catStyle.Render("PR   ")
		case "github":
			return catStyle.Render("GH   ")
		case "jira":
			return catStyle.Render("JIRA ")
		case "slack":
			return catStyle.Render("SLACK")
		case "Read":
			return catStyle.Render("READ ")
		case "Write":
			return catStyle.Render("WRITE")
		case "Edit":
			return catStyle.Render("EDIT ")
		case "change":
			return catStyle.Render("CHG  ")
		case "Glob":
			return catStyle.Render("GLOB ")
		case "Grep":
			return catStyle.Render("GREP ")
		default:
			if isFiles {
				return catStyle.Render("FILE ")
			}
			return catStyle.Render("URL  ")
		}
	}

	// Determine visible window
	visibleMax := maxH - 4 // border + header + footer + search hint
	if visibleMax < 3 {
		visibleMax = 3
	}
	start := 0
	if cursor >= start+visibleMax {
		start = cursor - visibleMax + 1
	}
	end := start + visibleMax
	if end > len(items) {
		end = len(items)
	}

	var lines []string

	// Header with scope, count and search indicator
	scopeLabel := ""
	if a.urlScope != "" && a.urlScope != "session" {
		scopeLabel = " [" + a.urlScope + "]"
	}
	title := "URLs"
	if strings.Contains(a.urlScope, "changes") {
		title = "Changes"
	} else if isFiles {
		title = "Files"
	}
	header := fmt.Sprintf("%s%s (%d", title, scopeLabel, len(items))
	if a.urlSearchTerm != "" {
		header += fmt.Sprintf("/%d", len(a.urlAllItems))
	}
	header += ")"
	if a.urlSearching {
		header += " " + a.urlSearchInput.View()
	} else if a.urlSearchTerm != "" {
		header += " [" + a.urlSearchTerm + "]"
	}
	lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(header))

	selCount := len(a.urlSelected)
	for i := start; i < end; i++ {
		item := items[i]
		badge := catBadge(item.Category)
		label := item.Label
		if len(label) > 70 {
			label = label[:67] + "..."
		}
		check := "  "
		if a.urlSelected[item.URL] {
			check = sel.Render("* ")
		}
		status := a.urlRefStatusText(item.URL)
		// Change rows already carry a timeAgo suffix in their label; for URL and
		// file rows, append it here so every scope reads newest-first with time.
		ts := ""
		if item.Category != "change" && !item.Timestamp.IsZero() {
			ts = "  " + dimStyle.Render(timeAgo(item.Timestamp))
		}
		if i == cursor {
			lines = append(lines, sel.Render(">")+check+badge+" "+sel.Render(label)+status+ts)
		} else {
			lines = append(lines, " "+check+badge+" "+hl.Render(label)+status+ts)
		}
	}

	// Scroll indicator
	if len(items) > visibleMax {
		pos := fmt.Sprintf("[%d/%d]", cursor+1, len(items))
		if selCount > 0 {
			pos += fmt.Sprintf(" %d selected", selCount)
		}
		lines = append(lines, d.Render(pos))
	} else if selCount > 0 {
		lines = append(lines, d.Render(fmt.Sprintf("%d selected", selCount)))
	}

	if a.isChangesScope() {
		lines = append(lines, d.Render("↵:edit  y:copy  /:search  ^d/^u:scroll  esc:close"))
	} else if isFiles {
		lines = append(lines, d.Render("↵:edit  y:copy  space:select  /:search  esc:close"))
	} else {
		lines = append(lines, d.Render("↵:open  y:copy  space:select  /:search  esc:close"))
	}

	listBody := strings.Join(lines, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)

	// Split pane with diff preview for changes scope
	if a.isChangesScope() && a.urlDiffReady {
		listBox := boxStyle.Render(listBody)

		diffBoxStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorDim).
			Padding(0, 1)
		diffContent := a.urlDiffVP.View()
		diffBox := diffBoxStyle.Render(diffContent)

		return lipgloss.JoinHorizontal(lipgloss.Top, listBox, diffBox)
	}

	return boxStyle.Render(listBody)
}
