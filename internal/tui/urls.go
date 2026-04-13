package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/session"
)

// --- Conversation/message-full actions menu ---

// handleConvActionsMenu processes key events for the conversation actions menu.
func (a *App) handleConvActionsMenu(key string) (tea.Model, tea.Cmd) {
	a.convActionsMenu = false
	switch key {
	case "u":
		if a.state == viewMessageFull {
			return a.openMsgFullURLMenu()
		}
		return a.openConvURLMenu()
	case "f":
		if a.state == viewMessageFull {
			return a.openMsgFullFilesMenu()
		}
		return a.openConvFilesMenu()
	case "g":
		if a.state == viewMessageFull {
			return a.openMsgFullChangesMenu()
		}
		return a.openConvChangesMenu()
	}
	return a, nil
}

// renderConvActionsHintBox renders the actions hint box for conversation/message-full views.
func renderConvActionsHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "

	line := hl.Render("u") + d.Render(":urls") + sp + hl.Render("f") + d.Render(":files") + sp + hl.Render("g") + d.Render(":changes")
	body := line + "\n" + d.Render("esc:cancel")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

// --- URL menu state & handlers ---

// openConvURLMenu opens the URL menu scoped to the conversation context.
func (a *App) openConvURLMenu() (tea.Model, tea.Cmd) {
	return a.openScopedMenu(extract.BlockURLs, extract.SessionURLs, "")
}

// openMsgFullURLMenu opens the URL menu scoped to the message-full context.
func (a *App) openMsgFullURLMenu() (tea.Model, tea.Cmd) {
	return a.openScopedMenu(extract.BlockURLs, extract.SessionURLs, "")
}

// openConvFilesMenu opens the file paths menu scoped by conversation context.
func (a *App) openConvFilesMenu() (tea.Model, tea.Cmd) {
	return a.openScopedMenu(extract.BlockFilePaths, extract.SessionFilePaths, "files")
}

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
			URL:      ch.Item.URL,
			Label:    changeItemLabel(ch),
			Category: "change",
		})
	}
	return items, cmap
}

func blockChangeItems(blocks []session.ContentBlock) []extract.Item {
	items, _ := changeItemsFromSlice(extract.BlockChanges(blocks))
	return items
}

func sessionChangeItems(filePath string) []extract.Item {
	items, _ := changeItemsFromSlice(extract.SessionChanges(filePath))
	return items
}

func (a *App) openConvChangesMenu() (tea.Model, tea.Cmd) {
	return a.openScopedChangesMenu()
}

func (a *App) openMsgFullChangesMenu() (tea.Model, tea.Cmd) {
	return a.openScopedChangesMenu()
}

// openScopedChangesMenu is like openScopedMenu but preserves ChangeItem data for diff preview.
func (a *App) openScopedChangesMenu() (tea.Model, tea.Cmd) {
	scopeLabel := func(base string) string { return base + " changes" }

	tryOpen := func(changes []extract.ChangeItem, scope string) (tea.Model, tea.Cmd, bool) {
		if len(changes) == 0 {
			return nil, nil, false
		}
		items, cmap := changeItemsFromSlice(changes)
		a.urlChangeMap = cmap
		a.initDiffViewport()
		m, cmd := a.openURLMenuFromItems(items, scope)
		return m, cmd, true
	}

	if a.state == viewMessageFull {
		fs := &a.msgFull.folds
		if fs.Entry.Role != "" {
			if m, cmd, ok := tryOpen(extract.BlockChanges(fs.Entry.Content), scopeLabel("message")); ok {
				return m, cmd
			}
		}
	} else {
		sp := &a.conv.split
		if sp.Show && sp.Folds != nil && sp.Folds.Entry.Role != "" {
			if m, cmd, ok := tryOpen(extract.BlockChanges(sp.Folds.Entry.Content), scopeLabel("message")); ok {
				return m, cmd
			}
		}
		if item, ok := a.convList.SelectedItem().(convItem); ok && item.kind == convMsg {
			if m, cmd, ok := tryOpen(extract.BlockChanges(item.merged.entry.Content), scopeLabel("message")); ok {
				return m, cmd
			}
		}
	}

	// Fall back: entire session with timestamps
	changes := extract.SessionChanges(a.currentSess.FilePath)
	items, cmap := changeItemsFromSlice(changes)
	a.urlChangeMap = cmap
	a.initDiffViewport()
	return a.openURLMenuFromItems(items, scopeLabel("session"))
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

// openMsgFullFilesMenu opens the file paths menu scoped by message-full context.
func (a *App) openMsgFullFilesMenu() (tea.Model, tea.Cmd) {
	return a.openScopedMenu(extract.BlockFilePaths, extract.SessionFilePaths, "files")
}

// openScopedMenu tries progressively wider scopes to extract items.
// blockFn extracts from content blocks, sessionFn from a file path.
// suffix is appended to scope labels (e.g. "files" → "message files").
func (a *App) openScopedMenu(
	blockFn func([]session.ContentBlock) []extract.Item,
	sessionFn func(string) []extract.Item,
	suffix string,
) (tea.Model, tea.Cmd) {
	scopeLabel := func(base string) string {
		if suffix != "" {
			return base + " " + suffix
		}
		return base
	}

	// Message-full view: try current block first
	if a.state == viewMessageFull {
		fs := &a.msgFull.folds
		if suffix == "" && fs.BlockCursor >= 0 && fs.BlockCursor < len(fs.Entry.Content) {
			if items := blockFn([]session.ContentBlock{fs.Entry.Content[fs.BlockCursor]}); len(items) > 0 {
				return a.openURLMenuFromItems(items, scopeLabel("block"))
			}
		}
		if fs.Entry.Role != "" {
			if items := blockFn(fs.Entry.Content); len(items) > 0 {
				return a.openURLMenuFromItems(items, scopeLabel("message"))
			}
		}
	} else {
		// Conversation view: try focused preview → selected list item
		sp := &a.conv.split
		if sp.Show && sp.Folds != nil && sp.Folds.Entry.Role != "" {
			if items := blockFn(sp.Folds.Entry.Content); len(items) > 0 {
				return a.openURLMenuFromItems(items, scopeLabel("message"))
			}
		}
		if item, ok := a.convList.SelectedItem().(convItem); ok && item.kind == convMsg {
			if items := blockFn(item.merged.entry.Content); len(items) > 0 {
				return a.openURLMenuFromItems(items, scopeLabel("message"))
			}
		}
	}

	// Fall back: entire session
	return a.openURLMenuFromItems(sessionFn(a.currentSess.FilePath), scopeLabel("session"))
}

// openURLMenuFromItems opens the URL menu with pre-extracted items and a scope label.
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
	return a, nil
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
			if err := extract.OpenInBrowser(u); err == nil {
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
	terms := strings.Fields(term)
	var filtered []extract.Item
	for _, item := range a.urlAllItems {
		text := strings.ToLower(item.URL + " " + item.Label + " " + item.Category)
		match := true
		for _, t := range terms {
			if !strings.Contains(text, t) {
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
	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
	d := dimStyle
	catStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6366F1")).Bold(true)

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
		if i == cursor {
			lines = append(lines, sel.Render(">")+check+badge+" "+sel.Render(label))
		} else {
			lines = append(lines, " "+check+badge+" "+hl.Render(label))
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
