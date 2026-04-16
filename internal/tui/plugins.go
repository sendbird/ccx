package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
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
	searchTerm  string
	selectedSet map[string]bool
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

	isMarked := !pi.isHeader && d.selectedSet[pi.plugin.ID]
	cursor := "  "
	if isMarked {
		cursor = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Bold(true).Render("✓ ")
	} else if selected {
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
	} else if !p.Enabled {
		statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Render(" DISABLED")
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

var componentTypeOrder = []string{"agent", "skill", "command", "hook", "mcp", "lsp", "script", "setting", "memory", "reference"}

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
	initListBase(&l)
	l.SetFilteringEnabled(false)
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
	// Route to detail view if active
	if a.plgDetailActive {
		return a.handlePluginDetailKeys(msg)
	}

	sp := &a.plgSplit
	key := msg.String()

	// Route to text inputs before nav translation
	if a.plgSearching {
		return a.handlePlgSearch(msg)
	}

	// Actions menu
	if a.plgActionsMenu {
		return a.handlePlgActionsMenu(msg.String())
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
		return a.quit()
	case "esc":
		if a.plgHasSelection() {
			a.clearPlgSelection()
			return a, nil
		}
		if a.plgSearchTerm != "" {
			a.plgSearchTerm = ""
			a.rebuildPlgList()
			return a, nil
		}
		a.state = viewSessions
		return a, nil
	case a.keymap.Session.Open:
		// Enter: open plugin detail (skip headers)
		if pi, ok := a.plgList.SelectedItem().(plgItem); ok && !pi.isHeader {
			return a.openPluginDetail(pi.plugin)
		}
		return a, nil
	case a.keymap.Session.Views:
		a.viewsMenu = true
		return a, nil
	case "x":
		a.plgActionsMenu = true
		return a, nil
	case " ":
		// Toggle selection on current plugin
		if pi, ok := a.plgList.SelectedItem().(plgItem); ok && !pi.isHeader {
			if a.plgSelectedSet[pi.plugin.ID] {
				delete(a.plgSelectedSet, pi.plugin.ID)
			} else {
				a.plgSelectedSet[pi.plugin.ID] = true
			}
			a.applyPlgDelegate()
		}
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
	case a.keymap.Session.Refresh:
		a.copiedMsg = "Refreshing plugins…"
		return a.openPluginExplorer()
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
		return a, tea.Batch(cmd, a.schedulePreviewUpdate())
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

	if p.Blocked {
		writeField("Status:", "blocked")
	} else if p.Installed && p.Enabled {
		writeField("Status:", "installed, enabled")
	} else if p.Installed {
		writeField("Status:", "installed, disabled")
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
		"memory":    "Memory",
		"reference": "References",
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
	case "reference":
		return "?"
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

// --- Plugin detail (drill-down into a single plugin) ---

// plgCompItem represents a component or sub-plugin in the detail list.
type plgCompItem struct {
	comp      session.PluginComponent
	subPlugin *session.SubPlugin // non-nil for sub-plugin entries
	isHeader  bool
	label     string
}

func (c plgCompItem) FilterValue() string {
	if c.isHeader {
		return c.label
	}
	if c.subPlugin != nil {
		return c.subPlugin.Name
	}
	return c.comp.Name
}

// plgCompDelegate renders a component row in the detail list.
type plgCompDelegate struct {
	selectedSet map[string]bool
}

func (d plgCompDelegate) Height() int                             { return 1 }
func (d plgCompDelegate) Spacing() int                            { return 0 }
func (d plgCompDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d plgCompDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	ci, ok := item.(plgCompItem)
	if !ok {
		return
	}

	selected := index == m.Index()
	width := m.Width()

	isMarked := !ci.isHeader && ci.comp.Path != "" && d.selectedSet[ci.comp.Path]
	cursor := "  "
	if isMarked {
		cursor = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Bold(true).Render("✓ ")
	} else if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸ ")
	}
	cursorW := 2

	if ci.isHeader {
		style := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
		line := cursor + style.Render(ci.label)
		if lineW := cursorW + lipgloss.Width(style.Render(ci.label)); lineW < width {
			line += strings.Repeat(" ", width-lineW)
		}
		fmt.Fprint(w, line)
		return
	}

	// Sub-plugin entry
	if sp := ci.subPlugin; sp != nil {
		nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))
		if selected {
			nameStyle = nameStyle.Bold(true)
		}
		nameStr := nameStyle.Render(sp.Name)
		badge := componentBadge(sp.Components)
		badgeStr := ""
		if badge != "" {
			badgeStr = dimStyle.Render(" " + badge)
		}
		line := cursor + nameStr + badgeStr
		lineW := cursorW + lipgloss.Width(nameStr) + lipgloss.Width(badgeStr)
		if lineW < width {
			line += strings.Repeat(" ", width-lineW)
		}
		fmt.Fprint(w, line)
		return
	}

	// Component entry
	c := ci.comp
	icon := componentIcon(c.Type)

	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))
	if selected {
		nameStyle = nameStyle.Bold(true)
	}
	nameStr := nameStyle.Render(c.Name)

	sizeStr := ""
	if c.Size > 0 {
		sizeStr = dimStyle.Render("  " + formatSize(c.Size))
	}

	iconStr := dimStyle.Render(icon + " ")
	line := cursor + iconStr + nameStr + sizeStr
	lineW := cursorW + lipgloss.Width(iconStr) + lipgloss.Width(nameStr) + lipgloss.Width(sizeStr)
	if lineW < width {
		line += strings.Repeat(" ", width-lineW)
	}
	fmt.Fprint(w, line)
}

func (a *App) openPluginDetail(p session.Plugin) (tea.Model, tea.Cmd) {
	a.plgDetailActive = true
	a.plgDetailPlugin = p
	clear(a.plgCompSelectedSet)
	a.plgCompActionsMenu = false

	items := buildComponentItems(p)
	contentH := ContentHeight(a.height)
	listW := a.plgDetailSplit.ListWidth(a.width, a.splitRatio)

	l := list.New(items, plgCompDelegate{selectedSet: a.plgCompSelectedSet}, listW, contentH)
	initListBase(&l)
	l.SetFilteringEnabled(false)
	l.KeyMap.Filter.SetEnabled(false)
	l.KeyMap.ClearFilter.SetEnabled(false)
	l.SetSize(listW, contentH)

	// Skip to first non-header item
	for i, item := range items {
		if ci, ok := item.(plgCompItem); ok && !ci.isHeader {
			l.Select(i)
			break
		}
	}

	a.plgDetailList = l
	a.plgDetailSplit.Show = true
	a.plgDetailSplit.Focus = false
	a.plgDetailSplit.CacheKey = ""
	a.updatePluginDetailPreview()
	return a, nil
}

func buildComponentItems(p session.Plugin) []list.Item {
	typeLabels := map[string]string{
		"agent":   "Agents",
		"skill":   "Skills",
		"command": "Commands",
		"hook":    "Hooks",
		"mcp":     "MCP Servers",
		"lsp":     "LSP Servers",
		"script":  "Scripts",
		"setting": "Settings",
		"memory":    "Memory",
		"reference": "References",
	}

	byType := map[string][]session.PluginComponent{}
	for _, c := range p.Components {
		byType[c.Type] = append(byType[c.Type], c)
	}

	var items []list.Item

	// Component sections
	for _, typ := range componentTypeOrder {
		comps := byType[typ]
		if len(comps) == 0 {
			continue
		}
		label := typeLabels[typ]
		if label == "" {
			label = strings.ToUpper(typ)
		}
		items = append(items, plgCompItem{
			isHeader: true,
			label:    fmt.Sprintf("%s (%d)", label, len(comps)),
		})
		for _, c := range comps {
			items = append(items, plgCompItem{comp: c})
		}
	}

	// Sub-plugins section
	if len(p.SubPlugins) > 0 {
		items = append(items, plgCompItem{
			isHeader: true,
			label:    fmt.Sprintf("Sub-plugins (%d)", len(p.SubPlugins)),
		})
		for i := range p.SubPlugins {
			items = append(items, plgCompItem{subPlugin: &p.SubPlugins[i]})
		}
	}

	return items
}

func (a *App) handlePluginDetailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sp := &a.plgDetailSplit
	key := msg.String()

	// Actions menu
	if a.plgCompActionsMenu {
		return a.handlePlgCompActionsMenu(key)
	}

	// Translate navigation aliases
	if nav, navMsg := a.keymap.TranslateNav(key, msg); nav != "" {
		key = nav
		msg = navMsg
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
		if a.plgCompHasSelection() {
			a.clearPlgCompSelection()
			return a, nil
		}
		// esc closes detail, go back to plugin list
		a.plgDetailActive = false
		return a, nil
	case splitKeyOpened, splitKeyFocused:
		a.updatePluginDetailPreview()
		return a, nil
	case splitKeyUnfocused:
		return a, nil
	case splitKeyHandled, splitKeyScrolled:
		return a, nil
	}

	switch key {
	case a.keymap.Session.Quit:
		return a.quit()
	case "esc":
		if a.plgCompHasSelection() {
			a.clearPlgCompSelection()
			return a, nil
		}
		a.plgDetailActive = false
		return a, nil
	case "e":
		return a.editPluginComponent()
	case "c":
		return a.copyPluginPath()
	case "o":
		return a.openPluginShell()
	case "x":
		a.plgCompActionsMenu = true
		return a, nil
	case " ":
		// Toggle selection on current component
		if ci, ok := a.plgDetailList.SelectedItem().(plgCompItem); ok && !ci.isHeader && ci.comp.Path != "" {
			if a.plgCompSelectedSet[ci.comp.Path] {
				delete(a.plgCompSelectedSet, ci.comp.Path)
			} else {
				a.plgCompSelectedSet[ci.comp.Path] = true
			}
			a.applyPlgCompDelegate()
		}
		return a, nil
	}

	// Pass navigation keys to list or viewport
	if isNavKey(msg) {
		if sp.Focus {
			var cmd tea.Cmd
			sp.Preview, cmd = sp.Preview.Update(msg)
			return a, cmd
		}
		var cmd tea.Cmd
		a.plgDetailList, cmd = a.plgDetailList.Update(msg)
		a.updatePluginDetailPreview()
		return a, cmd
	}

	return a, nil
}

func (a *App) plgCompHasSelection() bool {
	return len(a.plgCompSelectedSet) > 0
}

func (a *App) clearPlgCompSelection() {
	clear(a.plgCompSelectedSet)
	a.applyPlgCompDelegate()
}

func (a *App) applyPlgCompDelegate() {
	a.plgDetailList.SetDelegate(plgCompDelegate{selectedSet: a.plgCompSelectedSet})
}

func (a *App) handlePlgCompActionsMenu(key string) (tea.Model, tea.Cmd) {
	a.plgCompActionsMenu = false

	switch key {
	case "e":
		return a.editPluginComponent()
	case "c":
		return a.copyPluginPath()
	case "o":
		return a.openPluginShell()
	}
	// Any other key cancels
	return a, nil
}

func (a *App) renderPlgCompActionsHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "

	var lines []string
	if a.plgCompHasSelection() {
		header := fmt.Sprintf("%d selected", len(a.plgCompSelectedSet))
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(header))
	}
	line := hl.Render("e") + d.Render(":edit") + sp + hl.Render("c") + d.Render(":copy-path") + sp + hl.Render("o") + d.Render(":open-shell")
	lines = append(lines, line)
	lines = append(lines, d.Render("esc:cancel"))

	body := strings.Join(lines, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

// copyPluginPath copies the plugin install path to clipboard.
func (a *App) copyPluginPath() (tea.Model, tea.Cmd) {
	p := a.plgDetailPlugin
	path := p.Install.InstallPath
	if path == "" {
		a.copiedMsg = "No install path"
		return a, nil
	}
	if err := copyToClipboard(path); err != nil {
		a.copiedMsg = "Copy failed"
		return a, nil
	}
	a.copiedMsg = "Copied: " + path
	return a, nil
}

// openPluginShell opens a shell at the plugin install path.
func (a *App) openPluginShell() (tea.Model, tea.Cmd) {
	p := a.plgDetailPlugin
	path := p.Install.InstallPath
	if path == "" {
		a.copiedMsg = "No install path"
		return a, nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "sh"
	}
	c := exec.Command(shell)
	c.Dir = path
	return a, tea.ExecProcess(c, func(err error) tea.Msg {
		return editorDoneMsg{}
	})
}

// editPluginComponent opens selected component files (or current item) in $EDITOR.
func (a *App) editPluginComponent() (tea.Model, tea.Cmd) {
	if a.plgCompHasSelection() {
		// Multi-select: open all selected files in editor
		var paths []string
		for path := range a.plgCompSelectedSet {
			paths = append(paths, path)
		}
		if len(paths) == 0 {
			return a, nil
		}
		sort.Strings(paths)
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		c := exec.Command(editor, paths...)
		return a, tea.ExecProcess(c, func(err error) tea.Msg {
			return editorDoneMsg{}
		})
	}

	// Single item
	ci, ok := a.plgDetailList.SelectedItem().(plgCompItem)
	if !ok || ci.isHeader {
		return a, nil
	}

	if ci.subPlugin != nil {
		a.copiedMsg = "Sub-plugin (no single file to edit)"
		return a, nil
	}
	if ci.comp.Path == "" {
		a.copiedMsg = "No file path"
		return a, nil
	}

	return a.openInEditor(ci.comp.Path)
}

func (a *App) renderPluginDetailSplit() string {
	if a.plgDetailList.Width() == 0 {
		return ""
	}
	clampPaginator(&a.plgDetailList)
	return a.plgDetailSplit.Render(a.width, a.height, a.splitRatio)
}

func (a *App) updatePluginDetailPreview() {
	sp := &a.plgDetailSplit
	if !sp.Show {
		return
	}

	ci, ok := a.plgDetailList.SelectedItem().(plgCompItem)
	if !ok {
		// No items (empty component list) — show plugin metadata
		previewW := sp.PreviewWidth(a.width, a.splitRatio)
		if sp.CacheKey != "plugin-meta" {
			sp.CacheKey = "plugin-meta"
			sp.Preview.SetContent(renderPluginDetail(a.plgDetailPlugin, previewW))
			sp.Preview.GotoTop()
		}
		return
	}

	cacheKey := ci.comp.Path
	if ci.isHeader {
		cacheKey = "header:" + ci.label
	} else if ci.subPlugin != nil {
		cacheKey = "subplugin:" + ci.subPlugin.Name
	}
	if sp.CacheKey == cacheKey {
		return
	}
	sp.CacheKey = cacheKey

	if ci.isHeader {
		sp.Preview.SetContent(dimStyle.Render("(section header)"))
		return
	}

	previewW := sp.PreviewWidth(a.width, a.splitRatio)

	// Sub-plugin entry: show description + its components
	if sp := ci.subPlugin; sp != nil {
		var b strings.Builder
		titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF"))
		labelStyle := lipgloss.NewStyle().Foreground(colorPrimary)

		b.WriteString(titleStyle.Render(sp.Name))
		b.WriteString("\n")
		if sp.Description != "" {
			b.WriteString(wordWrap(dimStyle.Render(sp.Description), previewW))
			b.WriteString("\n")
		}
		if sp.Version != "" {
			b.WriteString(labelStyle.Render("Version: "))
			b.WriteString(sp.Version)
			b.WriteString("\n")
		}
		if len(sp.Components) > 0 {
			b.WriteString("\n")
			for _, c := range sp.Components {
				icon := componentIcon(c.Type)
				b.WriteString(fmt.Sprintf("  %s %s", icon, c.Name))
				if c.Size > 0 {
					b.WriteString(dimStyle.Render("  " + formatSize(c.Size)))
				}
				b.WriteString("\n")
			}
		}
		a.plgDetailSplit.Preview.SetContent(b.String())
		a.plgDetailSplit.Preview.GotoTop()
		return
	}

	c := ci.comp

	// LSP components have no file
	if c.Path == "" {
		a.plgDetailSplit.Preview.SetContent(dimStyle.Render(fmt.Sprintf("(%s: %s — no file)", c.Type, c.Name)))
		return
	}

	data, err := os.ReadFile(c.Path)
	if err != nil {
		a.plgDetailSplit.Preview.SetContent(dimStyle.Render("(cannot read file)"))
		return
	}

	content := string(data)

	// Pretty-print JSON
	if strings.HasSuffix(c.Path, ".json") {
		var buf interface{}
		if json.Unmarshal(data, &buf) == nil {
			if pretty, err := json.MarshalIndent(buf, "", "  "); err == nil {
				content = string(pretty)
			}
		}
	}

	wrapped := wordWrap(content, previewW)
	a.plgDetailSplit.Preview.SetContent(wrapped)
	a.plgDetailSplit.Preview.GotoTop()
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
	a.applyPlgDelegate()
	a.plgSplit.CacheKey = ""
	a.updatePluginPreview()
}

func filterPluginItems(items []list.Item, term string) []list.Item {
	// Split into terms — all must match (AND logic)
	var terms []string
	for _, t := range strings.Fields(strings.ToLower(term)) {
		if t != "" {
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		return items
	}

	var filtered []list.Item
	var lastHeader list.Item
	headerUsed := false

	for _, item := range items {
		pi := item.(plgItem)
		if pi.isHeader {
			lastHeader = item
			headerUsed = false
			continue
		}
		searchable := pluginSearchText(pi.plugin)
		match := true
		for _, t := range terms {
			if !strings.Contains(searchable, t) {
				match = false
				break
			}
		}
		if match {
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

	// Synthetic is: tags for filtering
	if p.Installed {
		s += " is:installed"
	} else {
		s += " is:available"
	}
	if p.Blocked {
		s += " is:blocked"
	}
	if p.Enabled {
		s += " is:enabled"
	} else {
		s += " is:disabled"
	}

	// Component type tags: has:agent, has:skill, etc.
	seen := map[string]bool{}
	for _, c := range p.Components {
		if !seen[c.Type] {
			s += " has:" + c.Type
			seen[c.Type] = true
		}
	}

	return s
}

func (a *App) plgSearchNext(dir int) {
	if a.plgSearchTerm == "" {
		return
	}
	var terms []string
	for _, t := range strings.Fields(strings.ToLower(a.plgSearchTerm)) {
		if t != "" {
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		return
	}
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
		searchable := pluginSearchText(pi.plugin)
		match := true
		for _, t := range terms {
			if !strings.Contains(searchable, t) {
				match = false
				break
			}
		}
		if match {
			a.plgList.Select(idx)
			a.updatePluginPreview()
			return
		}
	}
}

// --- Plugin selection & test ---

type pluginTestDoneMsg struct{ tmpDir string }

func (a *App) plgHasSelection() bool {
	return len(a.plgSelectedSet) > 0
}

func (a *App) clearPlgSelection() {
	clear(a.plgSelectedSet)
	a.applyPlgDelegate()
}

func (a *App) applyPlgDelegate() {
	a.plgList.SetDelegate(plgDelegate{searchTerm: a.plgSearchTerm, selectedSet: a.plgSelectedSet})
}

func (a *App) selectedPlugins() []session.Plugin {
	if a.plgTree == nil {
		return nil
	}
	var plugins []session.Plugin
	for _, p := range a.plgTree.Plugins {
		if a.plgSelectedSet[p.ID] {
			plugins = append(plugins, p)
		}
	}
	return plugins
}

func (a *App) handlePlgActionsMenu(key string) (tea.Model, tea.Cmd) {
	// Clear uninstall confirm on any key except x
	if a.plgUninstallConfirm && key != "x" {
		a.plgUninstallConfirm = false
	}

	switch key {
	case "t":
		a.plgActionsMenu = false
		return a.launchPluginTest()
	case "i":
		a.plgActionsMenu = false
		return a.runPluginInstall()
	case "e":
		a.plgActionsMenu = false
		return a.togglePluginEnabled(true)
	case "d":
		a.plgActionsMenu = false
		return a.togglePluginEnabled(false)
	case "u":
		a.plgActionsMenu = false
		return a.runPluginCmd("update")
	case "c":
		a.plgActionsMenu = false
		targets := a.plgActionTargets()
		if len(targets) == 0 {
			return a, nil
		}
		path := targets[0].Install.InstallPath
		if path == "" {
			a.copiedMsg = "No install path"
			return a, nil
		}
		if err := copyToClipboard(path); err != nil {
			a.copiedMsg = "Copy failed"
			return a, nil
		}
		a.copiedMsg = "Copied: " + path
		return a, nil
	case "o":
		a.plgActionsMenu = false
		targets := a.plgActionTargets()
		if len(targets) == 0 {
			return a, nil
		}
		path := targets[0].Install.InstallPath
		if path == "" {
			a.copiedMsg = "No install path"
			return a, nil
		}
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "sh"
		}
		c := exec.Command(shell)
		c.Dir = path
		return a, tea.ExecProcess(c, func(err error) tea.Msg {
			return editorDoneMsg{}
		})
	case "x":
		// Second press confirms uninstall
		if a.plgUninstallConfirm {
			a.plgUninstallConfirm = false
			a.plgActionsMenu = false
			return a.runPluginCmd("uninstall")
		}
		// First press: show confirmation
		a.plgUninstallConfirm = true
		targets := a.plgActionTargets()
		if len(targets) == 1 {
			a.copiedMsg = "Uninstall " + targets[0].Name + "? Press x to confirm"
		} else {
			a.copiedMsg = fmt.Sprintf("Uninstall %d plugins? Press x to confirm", len(targets))
		}
		return a, nil
	default:
		a.plgActionsMenu = false
	}
	return a, nil
}

// plgActionTargets returns either selected plugins or the current cursor plugin.
func (a *App) plgActionTargets() []session.Plugin {
	if a.plgHasSelection() {
		return a.selectedPlugins()
	}
	if pi, ok := a.plgList.SelectedItem().(plgItem); ok && !pi.isHeader {
		return []session.Plugin{pi.plugin}
	}
	return nil
}

// togglePluginEnabled enables or disables target plugins by updating settings.json.
func (a *App) togglePluginEnabled(enable bool) (tea.Model, tea.Cmd) {
	targets := a.plgActionTargets()
	if len(targets) == 0 {
		return a, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		a.copiedMsg = "Error: " + err.Error()
		return a, nil
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Read existing settings
	data, _ := os.ReadFile(settingsPath)
	var settings map[string]json.RawMessage
	if json.Unmarshal(data, &settings) != nil {
		settings = make(map[string]json.RawMessage)
	}

	// Parse enabledPlugins
	var enabled map[string]bool
	if raw, ok := settings["enabledPlugins"]; ok {
		json.Unmarshal(raw, &enabled)
	}
	if enabled == nil {
		enabled = make(map[string]bool)
	}

	// Toggle targets
	var names []string
	for _, p := range targets {
		enabled[p.ID] = enable
		names = append(names, p.Name)
	}

	// Write back
	raw, _ := json.Marshal(enabled)
	settings["enabledPlugins"] = raw
	out, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(settingsPath, out, 0o644); err != nil {
		a.copiedMsg = "Write failed: " + err.Error()
		return a, nil
	}

	// Update in-memory state
	for i := range a.plgTree.Plugins {
		if enabled, ok := enabled[a.plgTree.Plugins[i].ID]; ok {
			a.plgTree.Plugins[i].Enabled = enabled
		}
	}

	action := "Enabled"
	if !enable {
		action = "Disabled"
	}
	a.copiedMsg = fmt.Sprintf("%s %s", action, strings.Join(names, ", "))
	a.clearPlgSelection()
	a.rebuildPlgList()
	return a, nil
}

type pluginCmdDoneMsg struct {
	action string
	err    error
}

// runPluginCmd runs `claude plugin <action> <id>` for each target plugin.
func (a *App) runPluginCmd(action string) (tea.Model, tea.Cmd) {
	targets := a.plgActionTargets()
	if len(targets) == 0 {
		return a, nil
	}

	// Only installed plugins can be updated/uninstalled
	var ids []string
	for _, p := range targets {
		if p.Installed {
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		a.copiedMsg = "No installed plugins to " + action
		return a, nil
	}

	label := action
	if len(label) > 0 {
		label = strings.ToUpper(label[:1]) + label[1:]
	}
	a.copiedMsg = fmt.Sprintf("%sing %d plugin(s)…", label, len(ids))

	return a, func() tea.Msg {
		var lastErr error
		for _, id := range ids {
			cmd := exec.Command("claude", "plugin", action, id)
			if err := cmd.Run(); err != nil {
				lastErr = fmt.Errorf("%s %s: %w", action, id, err)
			}
		}
		return pluginCmdDoneMsg{action: action, err: lastErr}
	}
}

// runPluginInstall installs available (not-installed) plugins via `claude plugin install`.
func (a *App) runPluginInstall() (tea.Model, tea.Cmd) {
	targets := a.plgActionTargets()
	if len(targets) == 0 {
		return a, nil
	}

	var ids []string
	for _, p := range targets {
		if !p.Installed {
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		a.copiedMsg = "Already installed"
		return a, nil
	}

	a.copiedMsg = fmt.Sprintf("Installing %d plugin(s)…", len(ids))

	return a, func() tea.Msg {
		var lastErr error
		for _, id := range ids {
			cmd := exec.Command("claude", "plugin", "install", id)
			if err := cmd.Run(); err != nil {
				lastErr = fmt.Errorf("install %s: %w", id, err)
			}
		}
		return pluginCmdDoneMsg{action: "install", err: lastErr}
	}
}

// renderPlgActionsHintBox renders the plugin actions menu popup.
func (a *App) renderPlgActionsHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle

	var lines []string
	if a.plgHasSelection() {
		header := fmt.Sprintf("%d selected", len(a.plgSelectedSet))
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(header))
	}
	sp := "  "
	line1 := hl.Render("i") + d.Render(":install") + sp + hl.Render("e") + d.Render(":enable") + sp + hl.Render("d") + d.Render(":disable")
	line2 := hl.Render("u") + d.Render(":update") + sp + hl.Render("x") + d.Render(":uninstall")
	if a.plgHasSelection() {
		line2 += sp + hl.Render("t") + d.Render(":test")
	}
	line3 := hl.Render("c") + d.Render(":copy-path") + sp + hl.Render("o") + d.Render(":open-shell")
	lines = append(lines, line1, line2, line3)
	lines = append(lines, d.Render("esc:cancel"))

	body := strings.Join(lines, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

// launchPluginTest launches a sandboxed Claude session with only selected plugins active.
func (a *App) launchPluginTest() (tea.Model, tea.Cmd) {
	if !a.plgHasSelection() {
		a.copiedMsg = "No plugins selected (space to select)"
		return a, nil
	}
	if !tmux.InTmux() {
		a.copiedMsg = "Requires tmux"
		return a, nil
	}

	plugins := a.selectedPlugins()
	env, err := buildPluginTestEnv(plugins)
	if err != nil {
		a.copiedMsg = "Failed: " + err.Error()
		return a, nil
	}

	script := env.Script()
	a.copiedMsg = fmt.Sprintf("Testing %d plugins…", len(plugins))

	return a, func() tea.Msg {
		env.RunPopup(script)
		return pluginTestDoneMsg{tmpDir: env.HomeDir}
	}
}

// buildPluginTestEnv creates an isolated environment for plugin testing.
// Symlinks real plugin data so Claude can load selected plugins.
func buildPluginTestEnv(plugins []session.Plugin) (*tmux.IsolatedEnv, error) {
	env, err := tmux.NewIsolatedEnv("ccx-plgtest-")
	if err != nil {
		return nil, err
	}

	// Symlink real plugin files so Claude can load them
	home, _ := os.UserHomeDir()
	realClaude := filepath.Join(home, ".claude")
	pluginsDir := filepath.Join(env.ConfigDir, "plugins")
	os.MkdirAll(pluginsDir, 0o755)

	// Symlink installed_plugins.json, cache, marketplaces
	for _, name := range []string{
		"plugins/installed_plugins.json",
		"plugins/cache",
		"plugins/marketplaces",
		"plugins/known_marketplaces.json",
	} {
		src := filepath.Join(realClaude, name)
		if _, err := os.Stat(src); err == nil {
			os.Symlink(src, filepath.Join(env.ConfigDir, name))
		}
	}

	// Write settings.json: only selected plugins enabled
	enabledMap := make(map[string]bool)
	for _, p := range plugins {
		enabledMap[p.ID] = true
	}
	settingsData, _ := json.MarshalIndent(map[string]interface{}{
		"enabledPlugins": enabledMap,
	}, "", "  ")
	env.WriteSettings(settingsData)

	return env, nil
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
