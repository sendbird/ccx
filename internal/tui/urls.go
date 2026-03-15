package tui

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// urlItem represents a URL extracted from a session.
type urlItem struct {
	URL      string
	Label    string // short display label
	Category string // github, jira, slack, pr, other
}

// urlRegex matches http/https URLs in text.
var urlRegex = regexp.MustCompile(`https?://[^\s<>"'\x60\x29\x5D]+`)

// Package-level vars to avoid per-call allocation.
var (
	urlCleanReplacer = strings.NewReplacer(`\n`, "", `\t`, "", `\r`, "")
	jsonEscReplacer  = strings.NewReplacer(`\/`, `/`, `\\`, `\`)
	categoryOrder    = map[string]int{"pr": 0, "github": 1, "jira": 2, "slack": 3, "other": 4}
	cachedHome       string
	cachedHomeOnce   sync.Once
)

// extractSessionURLs loads all messages from a session file and extracts unique URLs.
func extractSessionURLs(filePath string) []urlItem {
	entries, err := session.LoadMessages(filePath)
	if err != nil {
		return nil
	}
	return extractEntryURLs(entries)
}

// extractEntryURLs extracts unique URLs from a set of entries.
func extractEntryURLs(entries []session.Entry) []urlItem {
	seen := make(map[string]bool)
	var items []urlItem
	for _, entry := range entries {
		extractURLsFromBlocks(entry.Content, seen, &items)
	}
	sortURLItems(items)
	return items
}

// extractBlockURLs extracts unique URLs from content blocks.
func extractBlockURLs(blocks []session.ContentBlock) []urlItem {
	seen := make(map[string]bool)
	var items []urlItem
	extractURLsFromBlocks(blocks, seen, &items)
	sortURLItems(items)
	return items
}

// extractURLsFromBlocks appends unique URLs from blocks to items.
func extractURLsFromBlocks(blocks []session.ContentBlock, seen map[string]bool, items *[]urlItem) {
	for _, block := range blocks {
		for _, text := range [2]string{block.Text, block.ToolInput} {
			if text == "" {
				continue
			}
			for _, raw := range urlRegex.FindAllString(text, -1) {
				u := cleanURL(raw)
				if u == "" || seen[u] {
					continue
				}
				seen[u] = true
				*items = append(*items, categorizeURL(u))
			}
		}
	}
}

func sortURLItems(items []urlItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return categoryOrder[items[i].Category] < categoryOrder[items[j].Category]
	})
}

// cleanURL strips JSON escape artifacts and trailing punctuation.
func cleanURL(raw string) string {
	// Strip literal \n, \t, \r that leak from JSON string values
	raw = urlCleanReplacer.Replace(raw)
	// Strip trailing backslashes (escaped newlines in JSON)
	raw = strings.TrimRight(raw, `\`)
	// Strip trailing punctuation that leaks from prose/markdown
	raw = strings.TrimRight(raw, ".,;:!?)'\"")

	// Validate
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	// Reject URLs with control chars or obviously broken hosts
	if strings.ContainsAny(u.Host, " \t\n\\") {
		return ""
	}
	return raw
}

// categorizeURL classifies a URL and generates a short label.
func categorizeURL(u string) urlItem {
	parsed, _ := url.Parse(u)
	host := strings.ToLower(parsed.Host)

	switch {
	case strings.Contains(host, "github.com"):
		label := githubLabel(parsed)
		cat := "github"
		if strings.Contains(parsed.Path, "/pull/") {
			cat = "pr"
		}
		return urlItem{URL: u, Label: label, Category: cat}

	case strings.Contains(host, "atlassian.net"):
		label := jiraLabel(parsed)
		return urlItem{URL: u, Label: label, Category: "jira"}

	case strings.Contains(host, "slack.com"):
		return urlItem{URL: u, Label: slackLabel(parsed), Category: "slack"}

	default:
		label := u
		if len(label) > 80 {
			label = label[:77] + "..."
		}
		return urlItem{URL: u, Label: label, Category: "other"}
	}
}

func githubLabel(u *url.URL) string {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && (parts[2] == "pull" || parts[2] == "issues") {
		return fmt.Sprintf("%s/%s#%s", parts[0], parts[1], parts[3])
	}
	if len(parts) >= 2 {
		return strings.Join(parts[:2], "/")
	}
	return u.Path
}

func jiraLabel(u *url.URL) string {
	path := u.Path
	if strings.Contains(path, "/browse/") {
		idx := strings.Index(path, "/browse/")
		return path[idx+len("/browse/"):]
	}
	if len(path) > 50 {
		return path[:47] + "..."
	}
	return path
}

func slackLabel(u *url.URL) string {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "archives" {
		return "slack#" + parts[1]
	}
	return "slack"
}

// openInBrowser opens a URL in the default browser.
func openInBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// --- File path extraction ---

// filePathTools maps tool names to their JSON field containing the file path.
var filePathTools = map[string]string{
	"Read":       "file_path",
	"Write":      "file_path",
	"Edit":       "file_path",
	"Glob":       "path",
	"Grep":       "path",
	"LSP":        "filePath",
	"NotebookEdit": "notebook_path",
}

// extractFilePaths extracts unique file paths from tool_use blocks.
func extractFilePaths(blocks []session.ContentBlock) []urlItem {
	seen := make(map[string]bool)
	var items []urlItem

	for _, block := range blocks {
		if block.Type != "tool_use" || block.ToolInput == "" {
			continue
		}
		field, ok := filePathTools[block.ToolName]
		if !ok {
			continue
		}
		path := extractJSONField(block.ToolInput, field)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		items = append(items, urlItem{
			URL:      path,
			Label:    shortenPath(path),
			Category: block.ToolName,
		})
	}
	return items
}

// extractSessionFilePaths loads messages and extracts file paths.
func extractSessionFilePaths(filePath string) []urlItem {
	entries, err := session.LoadMessages(filePath)
	if err != nil {
		return nil
	}
	var blocks []session.ContentBlock
	for _, entry := range entries {
		blocks = append(blocks, entry.Content...)
	}
	return extractFilePaths(blocks)
}

// extractJSONField extracts a string field value from a JSON string.
// Handles both "field":"value" and "field": "value" (with optional space).
func extractJSONField(jsonStr, field string) string {
	needle := `"` + field + `":`
	idx := strings.Index(jsonStr, needle)
	if idx < 0 {
		return ""
	}
	rest := jsonStr[idx+len(needle):]
	// Skip optional whitespace between : and opening quote
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:] // skip opening quote
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return jsonEscReplacer.Replace(rest[:end])
}

// shortenPath creates a display label from a file path.
func shortenPath(path string) string {
	cachedHomeOnce.Do(func() { cachedHome, _ = os.UserHomeDir() })
	return session.ShortenPath(path, cachedHome)
}

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
	}
	return a, nil
}

// renderConvActionsHintBox renders the actions hint box for conversation/message-full views.
func renderConvActionsHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "

	line := hl.Render("u") + d.Render(":urls") + sp + hl.Render("f") + d.Render(":files")
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
	return a.openScopedMenu(extractBlockURLs, extractSessionURLs, "")
}

// openMsgFullURLMenu opens the URL menu scoped to the message-full context.
func (a *App) openMsgFullURLMenu() (tea.Model, tea.Cmd) {
	return a.openScopedMenu(extractBlockURLs, extractSessionURLs, "")
}

// openConvFilesMenu opens the file paths menu scoped by conversation context.
func (a *App) openConvFilesMenu() (tea.Model, tea.Cmd) {
	return a.openScopedMenu(extractFilePaths, extractSessionFilePaths, "files")
}

// openMsgFullFilesMenu opens the file paths menu scoped by message-full context.
func (a *App) openMsgFullFilesMenu() (tea.Model, tea.Cmd) {
	return a.openScopedMenu(extractFilePaths, extractSessionFilePaths, "files")
}

// openScopedMenu tries progressively wider scopes to extract items.
// blockFn extracts from content blocks, sessionFn from a file path.
// suffix is appended to scope labels (e.g. "files" → "message files").
func (a *App) openScopedMenu(
	blockFn func([]session.ContentBlock) []urlItem,
	sessionFn func(string) []urlItem,
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
func (a *App) openURLMenuFromItems(items []urlItem, scope string) (tea.Model, tea.Cmd) {
	if len(items) == 0 {
		if strings.Contains(scope, "files") {
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
		a.urlMenu = false
		return a, nil
	case "q":
		a.urlMenu = false
		return a, nil
	case "up", "k":
		if a.urlCursor > 0 {
			a.urlCursor--
		}
		return a, nil
	case "down", "j":
		if a.urlCursor < len(a.urlItems)-1 {
			a.urlCursor++
		}
		return a, nil
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
		a.urlMenu = false
		if a.isFileScope() {
			// Open first selected file in editor
			return a.openInEditor(urls[0])
		}
		opened := 0
		for _, u := range urls {
			if err := openInBrowser(u); err == nil {
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
		a.urlMenu = false
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

// isFileScope returns true when the URL menu is showing file paths, not URLs.
func (a *App) isFileScope() bool {
	return strings.Contains(a.urlScope, "files")
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
	var filtered []urlItem
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
}

// renderURLMenu renders the URL selection menu as a hint box.
func (a *App) renderURLMenu() string {
	items := a.urlItems
	cursor := a.urlCursor
	maxH := ContentHeight(a.height)

	if len(items) == 0 && a.urlSearchTerm != "" {
		d := dimStyle
		t := "URLs"
		if a.isFileScope() {
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
	if isFiles {
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

	if isFiles {
		lines = append(lines, d.Render("↵:edit  y:copy  space:select  /:search  esc:close"))
	} else {
		lines = append(lines, d.Render("↵:open  y:copy  space:select  /:search  esc:close"))
	}

	body := strings.Join(lines, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}
