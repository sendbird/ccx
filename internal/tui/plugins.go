package tui

import (
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

// --- Plugin list item ---

type plgItem struct {
	plugin   session.Plugin
	isHeader bool
	label    string // marketplace header label
}

func (p plgItem) FilterValue() string {
	if p.isHeader {
		return p.label
	}
	return p.plugin.Name + " " + p.plugin.Marketplace
}

// --- Plugin delegate ---

type plgDelegate struct {
	searchTerm string
}

func (d plgDelegate) Height() int                             { return 1 }
func (d plgDelegate) Spacing() int                            { return 0 }
func (d plgDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d plgDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	pi, ok := item.(plgItem)
	if !ok {
		return
	}

	selected := index == m.Index()
	width := m.Width()

	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸ ")
	}
	cursorW := 2

	if pi.isHeader {
		style := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
		line := cursor + style.Render(pi.label)
		if w := cursorW + lipgloss.Width(style.Render(pi.label)); w < width {
			line += strings.Repeat(" ", width-w)
		}
		fmt.Fprint(w, line)
		return
	}

	p := pi.plugin
	avail := width - cursorW

	// Name
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))
	if p.Blocked {
		nameStyle = nameStyle.Strikethrough(true).Foreground(lipgloss.Color("#EF4444"))
	}
	if selected {
		nameStyle = nameStyle.Bold(true)
	}
	nameStr := nameStyle.Render(p.Name)
	nameW := lipgloss.Width(nameStr)

	// Version
	verStr := ""
	ver := p.Install.Version
	if p.Manifest != nil && p.Manifest.Version != "" {
		ver = p.Manifest.Version
	}
	if ver != "" && ver != "unknown" {
		verStr = dimStyle.Render(" v" + ver)
	}
	verW := lipgloss.Width(verStr)

	// Component counts badge
	badge := componentBadge(p.Components)
	badgeStr := ""
	badgeW := 0
	if badge != "" {
		badgeStr = dimStyle.Render(" " + badge)
		badgeW = lipgloss.Width(badgeStr)
	}

	// Status badge
	statusStr := ""
	statusW := 0
	if p.Blocked {
		statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render(" BLOCKED")
		statusW = lipgloss.Width(statusStr)
	} else if !p.Installed {
		statusStr = dimStyle.Render(" (available)")
		statusW = lipgloss.Width(statusStr)
	}

	// Tree connector
	connector := dimStyle.Render("├─ ")
	connW := 3

	// Assemble
	usedW := cursorW + connW + nameW + verW + badgeW + statusW
	pad := ""
	if usedW < avail+cursorW {
		pad = strings.Repeat(" ", avail+cursorW-usedW)
	}

	line := cursor + connector + nameStr + verStr + badgeStr + statusStr + pad
	// Highlight search term
	if d.searchTerm != "" {
		line = highlightSearchTerm(line, d.searchTerm)
	}
	fmt.Fprint(w, line)
}

var componentTypeOrder = []string{"agent", "skill", "command", "hook", "mcp", "lsp", "script", "setting", "memory"}

func componentBadge(components []session.PluginComponent) string {
	counts := map[string]int{}
	for _, c := range components {
		counts[c.Type]++
	}
	if len(counts) == 0 {
		return ""
	}
	var parts []string
	for _, typ := range componentTypeOrder {
		if n := counts[typ]; n > 0 {
			abbrev := string(typ[0])
			parts = append(parts, fmt.Sprintf("%d%s", n, abbrev))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// --- Plugin list builder ---

func buildPluginItems(tree *session.PluginTree) []list.Item {
	var items []list.Item

	// Split into installed and available
	var installed, available []session.Plugin
	for _, p := range tree.Plugins {
		if p.Installed {
			installed = append(installed, p)
		} else {
			available = append(available, p)
		}
	}

	// Installed plugins
	if len(installed) > 0 {
		items = append(items, plgItem{isHeader: true, label: "INSTALLED"})
		lastMkt := ""
		for _, p := range installed {
			mkt := p.Marketplace
			if mkt == "" {
				mkt = "(local)"
			}
			if mkt != lastMkt {
				items = append(items, plgItem{isHeader: true, label: "  " + mkt})
				lastMkt = mkt
			}
			items = append(items, plgItem{plugin: p})
		}
	}

	// Available (not-installed) plugins
	if len(available) > 0 {
		items = append(items, plgItem{isHeader: true, label: "AVAILABLE"})
		lastMkt := ""
		for _, p := range available {
			mkt := p.Marketplace
			if mkt == "" {
				mkt = "(local)"
			}
			if mkt != lastMkt {
				items = append(items, plgItem{isHeader: true, label: "  " + mkt})
				lastMkt = mkt
			}
			items = append(items, plgItem{plugin: p})
		}
	}

	return items
}

func newPluginList(items []list.Item, width, height int) list.Model {
	l := list.New(items, plgDelegate{}, width, height)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowFilter(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	l.DisableQuitKeybindings()
	l.KeyMap.Filter.SetEnabled(false)
	l.KeyMap.ClearFilter.SetEnabled(false)
	l.SetSize(width, height)

	// Skip to first non-header item
	for i, item := range items {
		if pi, ok := item.(plgItem); ok && !pi.isHeader {
			l.Select(i)
			break
		}
	}

	return l
}

// --- App integration ---

func (a *App) openPluginExplorer() (tea.Model, tea.Cmd) {
	claudeDir := a.config.ClaudeDir
	if claudeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			a.copiedMsg = "Cannot find home dir"
			return a, nil
		}
		claudeDir = filepath.Join(home, ".claude")
	}

	tree, err := session.ScanPlugins(claudeDir)
	if err != nil {
		a.copiedMsg = "Plugin scan failed: " + err.Error()
		return a, nil
	}

	a.plgTree = tree
	items := buildPluginItems(tree)
	contentH := ContentHeight(a.height)
	listW := a.plgSplit.ListWidth(a.width, a.splitRatio)
	a.plgList = newPluginList(items, listW, contentH)
	a.plgSplit.Show = true
	a.plgSplit.Focus = false
	a.plgSplit.CacheKey = ""
	a.state = viewPlugins
	a.updatePluginPreview()
	return a, nil
}

func (a *App) handlePluginKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sp := &a.plgSplit
	key := msg.String()

	// Route to text inputs before nav translation
	if a.plgSearching {
		return a.handlePlgSearch(msg)
	}

	// Translate navigation aliases
	if nav, navMsg := a.keymap.TranslateNav(key, msg); nav != "" {
		key = nav
		msg = navMsg
	}

	// Views menu
	if a.viewsMenu {
		return a.handleViewsMenu(key)
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
		if a.plgSearchTerm != "" {
			a.plgSearchTerm = ""
			a.rebuildPlgList()
			return a, nil
		}
		return a, nil
	case splitKeyOpened, splitKeyFocused:
		a.updatePluginPreview()
		return a, nil
	case splitKeyUnfocused:
		return a, nil
	case splitKeyHandled, splitKeyScrolled:
		return a, nil
	}

	switch key {
	case a.keymap.Session.Quit:
		return a, tea.Quit
	case "esc":
		if a.plgSearchTerm != "" {
			a.plgSearchTerm = ""
			a.rebuildPlgList()
			return a, nil
		}
		a.state = viewSessions
		return a, nil
	case a.keymap.Session.Views:
		a.viewsMenu = true
		return a, nil
	case a.keymap.Session.Search:
		return a, a.startPlgSearch()
	case "n":
		if a.plgSearchTerm != "" {
			a.plgSearchNext(1)
		}
		return a, nil
	case "N":
		if a.plgSearchTerm != "" {
			a.plgSearchNext(-1)
		}
		return a, nil
	}

	// Pass navigation keys to list
	if isNavKey(msg) {
		if sp.Focus {
			var cmd tea.Cmd
			sp.Preview, cmd = sp.Preview.Update(msg)
			return a, cmd
		}
		var cmd tea.Cmd
		a.plgList, cmd = a.plgList.Update(msg)
		a.updatePluginPreview()
		return a, cmd
	}

	return a, nil
}

func (a *App) renderPluginSplit() string {
	if a.plgList.Width() == 0 {
		return ""
	}
	clampPaginator(&a.plgList)
	return a.plgSplit.Render(a.width, a.height, a.splitRatio)
}

func (a *App) updatePluginPreview() {
	sp := &a.plgSplit
	if !sp.Show {
		return
	}

	pi, ok := a.plgList.SelectedItem().(plgItem)
	if !ok {
		return
	}

	cacheKey := pi.plugin.ID
	if pi.isHeader {
		cacheKey = "header:" + pi.label
	}
	if sp.CacheKey == cacheKey {
		return
	}
	sp.CacheKey = cacheKey

	if pi.isHeader {
		// Show marketplace info if available
		mktName := strings.ToLower(pi.label)
		if a.plgTree != nil {
			if info, ok := a.plgTree.Marketplaces[mktName]; ok {
				content := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("Marketplace: "+pi.label) + "\n"
				content += dimStyle.Render("Source: "+info.SourceType) + "\n"
				if info.SourceURL != "" {
					content += dimStyle.Render("URL: "+info.SourceURL) + "\n"
				}
				sp.Preview.SetContent(content)
				return
			}
		}
		sp.Preview.SetContent(dimStyle.Render("(marketplace header)"))
		return
	}

	previewW := sp.PreviewWidth(a.width, a.splitRatio)
	content := renderPluginDetail(pi.plugin, previewW)
	sp.Preview.SetContent(content)
	sp.Preview.GotoTop()
}

func renderPluginDetail(p session.Plugin, width int) string {
	var b strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF"))
	labelStyle := lipgloss.NewStyle().Foreground(colorPrimary)
	valStyle := lipgloss.NewStyle()

	// Header
	b.WriteString(titleStyle.Render(p.Name))
	b.WriteString("\n")

	// Description
	desc := ""
	if p.Manifest != nil && p.Manifest.Description != "" {
		desc = p.Manifest.Description
	}
	if desc != "" {
		b.WriteString(wordWrap(dimStyle.Render(desc), width))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Fields
	writeField := func(label, value string) {
		if value == "" {
			return
		}
		b.WriteString(labelStyle.Render(fmt.Sprintf("%-14s", label)))
		b.WriteString(valStyle.Render(value))
		b.WriteString("\n")
	}

	ver := p.Install.Version
	if p.Manifest != nil && p.Manifest.Version != "" {
		ver = p.Manifest.Version
	}
	writeField("Version:", ver)
	writeField("Marketplace:", p.Marketplace)
	writeField("Scope:", p.Install.Scope)

	if p.Manifest != nil && p.Manifest.Author.Name != "" {
		author := p.Manifest.Author.Name
		if p.Manifest.Author.Email != "" {
			author += " <" + p.Manifest.Author.Email + ">"
		}
		writeField("Author:", author)
	}
	if p.Manifest != nil && p.Manifest.Category != "" {
		writeField("Category:", p.Manifest.Category)
	}

	if p.Installed {
		writeField("Status:", "installed")
	} else {
		writeField("Status:", "available")
	}

	if !p.Install.InstalledAt.IsZero() {
		writeField("Installed:", p.Install.InstalledAt.Format("2006-01-02"))
	}
	if !p.Install.LastUpdated.IsZero() {
		writeField("Updated:", p.Install.LastUpdated.Format("2006-01-02"))
	}
	if p.Install.GitCommitSha != "" {
		sha := p.Install.GitCommitSha
		if len(sha) > 8 {
			sha = sha[:8]
		}
		writeField("Commit:", sha)
	}

	// Status
	if p.Blocked {
		b.WriteString("\n")
		blocked := lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true)
		b.WriteString(blocked.Render("BLOCKED"))
		if p.BlockReason != "" {
			b.WriteString(dimStyle.Render(" — " + p.BlockReason))
		}
		b.WriteString("\n")
	}

	if p.Manifest != nil && p.Manifest.Strict {
		b.WriteString(labelStyle.Render("Strict mode: "))
		b.WriteString(valStyle.Render("enabled"))
		b.WriteString("\n")
	}

	// Components
	b.WriteString("\n")
	separator := lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", min(width, 40)))
	b.WriteString(separator)
	b.WriteString("\n")

	componentsByType := map[string][]session.PluginComponent{}
	for _, c := range p.Components {
		componentsByType[c.Type] = append(componentsByType[c.Type], c)
	}

	typeLabels := map[string]string{
		"agent":   "Agents",
		"skill":   "Skills",
		"command": "Commands",
		"hook":    "Hooks",
		"mcp":     "MCP Servers",
		"lsp":     "LSP Servers",
		"script":  "Scripts",
		"setting": "Settings",
		"memory":  "Memory",
	}

	hasAny := false
	for _, typ := range componentTypeOrder {
		comps := componentsByType[typ]
		if len(comps) == 0 {
			continue
		}
		hasAny = true
		label := typeLabels[typ]
		countStr := fmt.Sprintf("%d", len(comps))

		header := labelStyle.Render(label) + dimStyle.Render(" ("+countStr+")")
		b.WriteString(header)
		b.WriteString("\n")

		for _, c := range comps {
			icon := componentIcon(c.Type)
			name := c.Name
			b.WriteString(fmt.Sprintf("  %s %s", icon, name))
			if c.Size > 0 {
				b.WriteString(dimStyle.Render(fmt.Sprintf("  %s", formatSize(c.Size))))
			}
			b.WriteString("\n")
		}
	}

	if !hasAny {
		b.WriteString(dimStyle.Render("  (no components)"))
		b.WriteString("\n")
	}

	// Sub-plugins
	if len(p.SubPlugins) > 0 {
		b.WriteString("\n")
		b.WriteString(separator)
		b.WriteString("\n")
		b.WriteString(labelStyle.Render(fmt.Sprintf("Sub-plugins (%d)", len(p.SubPlugins))))
		b.WriteString("\n")
		for _, sp := range p.SubPlugins {
			spBadge := componentBadge(sp.Components)
			b.WriteString("  ")
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Render(sp.Name))
			if spBadge != "" {
				b.WriteString(dimStyle.Render(" " + spBadge))
			}
			b.WriteString("\n")
			if sp.Description != "" {
				b.WriteString("    ")
				b.WriteString(dimStyle.Render(sp.Description))
				b.WriteString("\n")
			}
		}
	}

	// Install path
	if p.Install.InstallPath != "" {
		b.WriteString("\n")
		b.WriteString(separator)
		b.WriteString("\n")
		b.WriteString(labelStyle.Render("Path: "))
		b.WriteString(dimStyle.Render(p.Install.InstallPath))
		b.WriteString("\n")
	}

	return b.String()
}

func componentIcon(typ string) string {
	switch typ {
	case "agent":
		return ">"
	case "skill":
		return "*"
	case "command":
		return "$"
	case "hook":
		return "#"
	case "mcp":
		return "@"
	case "lsp":
		return "~"
	case "script":
		return "!"
	case "setting":
		return "="
	case "memory":
		return "+"
	default:
		return "-"
	}
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1fM", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1fK", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// --- Plugin search ---

func (a *App) startPlgSearch() tea.Cmd {
	a.plgSearching = true
	ti := textinput.New()
	ti.Prompt = "Search: "
	ti.Focus()
	a.plgSearchInput = ti
	return ti.Cursor.BlinkCmd()
}

func (a *App) handlePlgSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		a.plgSearching = false
		a.plgSearchTerm = a.plgSearchInput.Value()
		a.rebuildPlgList()
		return a, nil
	case "esc":
		a.plgSearching = false
		return a, nil
	}
	var cmd tea.Cmd
	a.plgSearchInput, cmd = a.plgSearchInput.Update(msg)
	return a, cmd
}

func (a *App) rebuildPlgList() {
	if a.plgTree == nil {
		return
	}
	items := buildPluginItems(a.plgTree)
	if a.plgSearchTerm != "" {
		items = filterPluginItems(items, a.plgSearchTerm)
	}
	contentH := ContentHeight(a.height)
	listW := a.plgSplit.ListWidth(a.width, a.splitRatio)
	a.plgList = newPluginList(items, listW, contentH)
	// Update delegate search term for highlighting
	a.plgList.SetDelegate(plgDelegate{searchTerm: a.plgSearchTerm})
	a.plgSplit.CacheKey = ""
	a.updatePluginPreview()
}

func filterPluginItems(items []list.Item, term string) []list.Item {
	lower := strings.ToLower(term)
	var filtered []list.Item
	var lastHeader list.Item
	headerUsed := false

	for _, item := range items {
		pi := item.(plgItem)
		if pi.isHeader {
			if lastHeader != nil && !headerUsed {
				// Previous header had no matches, skip it
			}
			lastHeader = item
			headerUsed = false
			continue
		}
		searchable := pluginSearchText(pi.plugin)
		if strings.Contains(searchable, lower) {
			if lastHeader != nil && !headerUsed {
				filtered = append(filtered, lastHeader)
				headerUsed = true
			}
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func pluginSearchText(p session.Plugin) string {
	s := strings.ToLower(p.Name + " " + p.Marketplace)
	if p.Manifest != nil {
		s += " " + strings.ToLower(p.Manifest.Description)
	}
	for _, sp := range p.SubPlugins {
		s += " " + strings.ToLower(sp.Name+" "+sp.Description)
	}
	return s
}

func (a *App) plgSearchNext(dir int) {
	if a.plgSearchTerm == "" {
		return
	}
	lower := strings.ToLower(a.plgSearchTerm)
	items := a.plgList.Items()
	n := len(items)
	if n == 0 {
		return
	}
	start := a.plgList.Index() + dir
	for i := 0; i < n; i++ {
		idx := ((start + i*dir) % n + n) % n
		pi, ok := items[idx].(plgItem)
		if !ok || pi.isHeader {
			continue
		}
		if strings.Contains(pluginSearchText(pi.plugin), lower) {
			a.plgList.Select(idx)
			a.updatePluginPreview()
			return
		}
	}
}

// highlightSearchTerm is a simple in-line highlighter for search matches.
func highlightSearchTerm(line, term string) string {
	if term == "" {
		return line
	}
	lower := strings.ToLower(line)
	lowerTerm := strings.ToLower(term)
	idx := strings.Index(lower, lowerTerm)
	if idx < 0 {
		return line
	}
	// Simple highlight — just return as-is since ANSI escapes make replacement complex
	return line
}
