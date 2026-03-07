package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// --- Config list item ---

type cfgItem struct {
	item      session.ConfigItem
	isHeader  bool
	label     string // "GLOBAL", "PROJECT: ccx", etc.
	treeDepth int    // 0=header, 1=file
	treeLast  bool
}

func (c cfgItem) FilterValue() string {
	if c.isHeader {
		return c.label
	}
	return c.item.Name + " " + c.item.Description
}

// --- Config delegate ---

type cfgDelegate struct {
	searchTerm string
}

func (d cfgDelegate) Height() int                             { return 1 }
func (d cfgDelegate) Spacing() int                            { return 0 }
func (d cfgDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d cfgDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	ci, ok := item.(cfgItem)
	if !ok {
		return
	}

	selected := index == m.Index()
	width := m.Width()

	// Cursor prefix for selected item
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸ ")
	}
	cursorW := 2

	if ci.isHeader {
		style := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
		line := cursor + style.Render(ci.label)
		if w := cursorW + lipgloss.Width(style.Render(ci.label)); w < width {
			line += strings.Repeat(" ", width-w)
		}
		fmt.Fprint(w, line)
		return
	}

	// Tree connector
	connector := "├─ "
	if ci.treeLast {
		connector = "└─ "
	}
	connStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563"))
	prefix := cursor + connStyle.Render(connector)
	prefixW := cursorW + lipgloss.Width(connStyle.Render(connector))

	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#D1D5DB"))
	descStyle := lipgloss.NewStyle().Foreground(colorDim)
	if selected {
		nameStyle = nameStyle.Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
		descStyle = descStyle.Foreground(lipgloss.Color("#9CA3AF"))
	}

	// Highlight search match in name
	nameStr := ci.item.Name
	var name string
	if d.searchTerm != "" {
		name = highlightInline(nameStr, d.searchTerm, nameStyle)
	} else {
		name = nameStyle.Render(nameStr)
	}
	nameW := lipgloss.Width(name)

	// Description fills remaining space
	desc := ""
	remaining := width - prefixW - nameW - 2
	if remaining > 4 && ci.item.Description != "" {
		dStr := ci.item.Description
		if len(dStr) > remaining {
			dStr = dStr[:remaining-1] + "…"
		}
		if d.searchTerm != "" {
			desc = "  " + highlightInline(dStr, d.searchTerm, descStyle)
		} else {
			desc = descStyle.Render("  " + dStr)
		}
	}

	line := prefix + name + desc
	lineW := lipgloss.Width(line)
	if lineW < width {
		line += strings.Repeat(" ", width-lineW)
	}
	fmt.Fprint(w, line)
}

// --- Config view methods ---

func (a *App) openConfigExplorer() (tea.Model, tea.Cmd) {
	home, err := os.UserHomeDir()
	if err != nil {
		a.copiedMsg = "Cannot find home dir"
		return a, nil
	}
	claudeDir := filepath.Join(home, ".claude")

	// Use selected session's project path for project-level config
	var projectPath string
	if sess, ok := a.selectedSession(); ok {
		projectPath = sess.ProjectPath
	}

	tree, err := session.ScanConfig(claudeDir, projectPath)
	if err != nil {
		a.copiedMsg = "Config scan failed"
		return a, nil
	}

	a.cfgTree = tree
	items := buildConfigItems(tree)
	contentH := ContentHeight(a.height)
	listW := a.cfgSplit.ListWidth(a.width, a.splitRatio)
	a.cfgList = newConfigList(items, listW, contentH)
	a.cfgSplit.Show = true
	a.cfgSplit.Focus = false
	a.cfgSplit.CacheKey = ""
	a.state = viewConfig
	a.updateConfigPreview()
	return a, nil
}

func (a *App) handleConfigKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sp := &a.cfgSplit
	key := msg.String()

	// Route to search input when active
	if a.cfgSearching {
		return a.handleCfgSearch(msg)
	}

	switch key {
	case "q":
		return a, tea.Quit
	case "esc":
		if a.cfgSearchTerm != "" {
			a.clearCfgSearch()
			return a, nil
		}
		a.state = viewSessions
		return a, nil
	case "/":
		a.startCfgSearch()
		return a, nil
	case "n":
		if a.cfgSearchTerm != "" {
			a.nextCfgMatch()
			sp.CacheKey = "" // force preview refresh with highlights
			a.updateConfigPreview()
		}
		return a, nil
	case "N":
		if a.cfgSearchTerm != "" {
			a.prevCfgMatch()
			sp.CacheKey = "" // force preview refresh with highlights
			a.updateConfigPreview()
		}
		return a, nil
	case "e":
		// Open selected file in $EDITOR
		if ci, ok := a.cfgList.SelectedItem().(cfgItem); ok && !ci.isHeader {
			return a.openInEditor(ci.item.Path)
		}
		return a, nil
	}

	// Split pane navigation
	result := sp.HandleSplitKey(key, a.width, a.height, a.splitRatio, func(delta int) {
		newRatio := a.splitRatio + delta
		if newRatio < 20 {
			newRatio = 20
		}
		if newRatio > 80 {
			newRatio = 80
		}
		a.splitRatio = newRatio
	})
	switch result {
	case splitKeyClosed:
		// esc in split closes preview but stays in config view
		return a, nil
	case splitKeyOpened, splitKeyFocused:
		a.updateConfigPreview()
		return a, nil
	case splitKeyUnfocused:
		return a, nil
	case splitKeyHandled, splitKeyScrolled:
		return a, nil
	}

	// Preview focused: scroll keys
	if sp.Focus && sp.Show {
		if sp.HandlePreviewScroll(key) {
			return a, nil
		}
	}

	// List navigation - handle boundary scrolling
	if sp.HandleListBoundary(key) {
		return a, nil
	}

	// Pass remaining keys to list for cursor movement
	if a.listReady(&a.cfgList) {
		var cmd tea.Cmd
		a.cfgList, cmd = a.cfgList.Update(msg)
		a.updateConfigPreview()
		return a, cmd
	}

	return a, nil
}

func (a *App) updateConfigPreview() {
	sp := &a.cfgSplit
	if !sp.Show {
		return
	}

	ci, ok := a.cfgList.SelectedItem().(cfgItem)
	if !ok {
		return
	}

	// Use path + search term as cache key
	cacheKey := ci.item.Path
	if ci.isHeader {
		cacheKey = "header:" + ci.label
	}
	if a.cfgSearchTerm != "" {
		cacheKey += "\x00" + a.cfgSearchTerm
	}
	if sp.CacheKey == cacheKey {
		return
	}
	sp.CacheKey = cacheKey

	if ci.isHeader {
		sp.Preview.SetContent(dimStyle.Render("(section header)"))
		return
	}

	data, err := os.ReadFile(ci.item.Path)
	if err != nil {
		sp.Preview.SetContent(dimStyle.Render("(cannot read file)"))
		return
	}

	content := string(data)

	// Pretty-print JSON
	if strings.HasSuffix(ci.item.Path, ".json") {
		var buf interface{}
		if json.Unmarshal(data, &buf) == nil {
			if pretty, err := json.MarshalIndent(buf, "", "  "); err == nil {
				content = string(pretty)
			}
		}
	}

	// Word-wrap to preview width
	previewW := sp.PreviewWidth(a.width, a.splitRatio)
	wrapped := wordWrap(content, previewW)

	// Highlight search matches in preview and scroll to first match
	if a.cfgSearchTerm != "" {
		wrapped = highlightSearchMatches(wrapped, a.cfgSearchTerm, -1)
		// Scroll to first occurrence
		lower := strings.ToLower(content)
		lowerTerm := strings.ToLower(a.cfgSearchTerm)
		if idx := strings.Index(lower, lowerTerm); idx >= 0 {
			// Count newlines before match to find the line number
			line := strings.Count(content[:idx], "\n")
			maxOffset := max(sp.Preview.TotalLineCount()-sp.Preview.Height, 0)
			sp.Preview.SetContent(wrapped)
			sp.Preview.YOffset = min(max(line-sp.Preview.Height/3, 0), maxOffset)
			return
		}
	}
	sp.Preview.SetContent(wrapped)
	sp.Preview.GotoTop()
}

// wordWrap wraps text to the given width, breaking long lines.
func wordWrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	var sb strings.Builder
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if len(line) <= width {
			sb.WriteString(line)
			continue
		}
		// Simple character-level wrap for long lines
		for len(line) > width {
			sb.WriteString(line[:width])
			sb.WriteByte('\n')
			line = line[width:]
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// --- Build config items ---

func buildConfigItems(tree *session.ConfigTree) []list.Item {
	var items []list.Item

	type section struct {
		category session.ConfigCategory
		label    string
	}
	sections := []section{
		{session.ConfigGlobal, "  GLOBAL"},
		{session.ConfigProject, fmt.Sprintf("  PROJECT: %s", tree.ProjectName)},
		{session.ConfigLocal, fmt.Sprintf("  LOCAL: %s", tree.ProjectPath)},
		{session.ConfigSkill, "  SKILLS"},
		{session.ConfigAgent, "  AGENTS"},
		{session.ConfigCommand, "  COMMANDS"},
		{session.ConfigMCP, "  MCP SERVERS"},
	}

	for _, sec := range sections {
		// Collect items for this category
		var catItems []session.ConfigItem
		for _, item := range tree.Items {
			if item.Category == sec.category {
				catItems = append(catItems, item)
			}
		}

		// Add header
		items = append(items, cfgItem{
			isHeader: true,
			label:    sec.label,
		})

		if len(catItems) == 0 {
			items = append(items, cfgItem{
				isHeader: true,
				label:    "    (empty)",
			})
			continue
		}

		for i, ci := range catItems {
			items = append(items, cfgItem{
				item:      ci,
				treeDepth: 1,
				treeLast:  i == len(catItems)-1,
			})
		}
	}

	return items
}

func newConfigList(items []list.Item, width, height int) list.Model {
	l := list.New(items, cfgDelegate{}, width, height)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowFilter(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	l.DisableQuitKeybindings()
	l.SetSize(width, height)

	// Skip to first non-header item
	for i, item := range items {
		if ci, ok := item.(cfgItem); ok && !ci.isHeader {
			l.Select(i)
			break
		}
	}

	return l
}

func (a *App) refreshConfigExplorer() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	claudeDir := filepath.Join(home, ".claude")
	projectPath := ""
	if a.cfgTree != nil {
		projectPath = a.cfgTree.ProjectPath
	}
	tree, err := session.ScanConfig(claudeDir, projectPath)
	if err != nil {
		return
	}
	selectedIdx := a.cfgList.Index()
	a.cfgTree = tree
	items := buildConfigItems(tree)
	contentH := ContentHeight(a.height)
	listW := a.cfgSplit.ListWidth(a.width, a.splitRatio)
	a.cfgList = newConfigList(items, listW, contentH)
	if selectedIdx < len(items) {
		a.cfgList.Select(selectedIdx)
	}
	a.cfgSplit.CacheKey = ""
	a.updateConfigPreview()
}

func (a *App) renderConfigSplit() string {
	if a.cfgList.Width() == 0 {
		return ""
	}
	clampPaginator(&a.cfgList)
	return a.cfgSplit.Render(a.width, a.height, a.splitRatio)
}

// --- Config search (custom, not bubbles filter) ---

func (a *App) startCfgSearch() {
	a.cfgSearching = true
	ti := textinput.New()
	ti.Prompt = "Search: "
	ti.Focus()
	a.cfgSearchInput = ti
}

func (a *App) handleCfgSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		term := a.cfgSearchInput.Value()
		a.cfgSearching = false
		if term == "" {
			a.clearCfgSearch()
		} else {
			a.cfgSearchTerm = term
			a.buildCfgSearchMatches()
			a.applyCfgDelegate()
			if len(a.cfgSearchMatch) > 0 {
				a.cfgSearchIdx = 0
				a.cfgList.Select(a.cfgSearchMatch[0])
				a.updateConfigPreview()
			}
		}
		return a, nil
	case "esc":
		a.cfgSearching = false
		return a, nil
	}
	var cmd tea.Cmd
	a.cfgSearchInput, cmd = a.cfgSearchInput.Update(msg)
	return a, cmd
}

func (a *App) buildCfgSearchMatches() {
	lower := strings.ToLower(a.cfgSearchTerm)
	a.cfgSearchMatch = nil
	items := a.cfgList.Items()
	for i, item := range items {
		ci, ok := item.(cfgItem)
		if !ok || ci.isHeader {
			continue
		}
		text := strings.ToLower(ci.item.Name + " " + ci.item.Description)
		if strings.Contains(text, lower) {
			a.cfgSearchMatch = append(a.cfgSearchMatch, i)
		}
	}
}

func (a *App) applyCfgDelegate() {
	a.cfgList.SetDelegate(cfgDelegate{searchTerm: a.cfgSearchTerm})
}

func (a *App) clearCfgSearch() {
	a.cfgSearchTerm = ""
	a.cfgSearchMatch = nil
	a.cfgSearchIdx = 0
	a.cfgList.SetDelegate(cfgDelegate{})
	a.cfgSplit.CacheKey = "" // force preview re-render without highlights
	a.updateConfigPreview()
}

func (a *App) nextCfgMatch() {
	if len(a.cfgSearchMatch) == 0 {
		return
	}
	a.cfgSearchIdx = (a.cfgSearchIdx + 1) % len(a.cfgSearchMatch)
	a.cfgList.Select(a.cfgSearchMatch[a.cfgSearchIdx])
}

func (a *App) prevCfgMatch() {
	if len(a.cfgSearchMatch) == 0 {
		return
	}
	a.cfgSearchIdx = (a.cfgSearchIdx - 1 + len(a.cfgSearchMatch)) % len(a.cfgSearchMatch)
	a.cfgList.Select(a.cfgSearchMatch[a.cfgSearchIdx])
}
