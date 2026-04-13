package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
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
	searchTerm  string
	selectedSet map[string]bool // config Path → selected
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

	// Cursor prefix for selected/multi-selected item
	isMultiSelected := !ci.isHeader && d.selectedSet != nil && d.selectedSet[ci.item.Path]
	cursor := "  "
	if selected && isMultiSelected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("✓ ")
	} else if isMultiSelected {
		cursor = lipgloss.NewStyle().Foreground(colorPrimary).Render("✓ ")
	} else if selected {
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

	// Tree connector with depth indentation
	indent := ""
	if ci.treeDepth > 1 {
		indent = strings.Repeat("  ", ci.treeDepth-1)
	}
	connector := "├─ "
	if ci.treeLast {
		connector = "└─ "
	}
	connStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563"))
	prefix := cursor + connStyle.Render(indent+connector)
	prefixW := cursorW + lipgloss.Width(connStyle.Render(indent+connector))

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

	// Append keymap and shortcut items to config tree
	tree.Items = append(tree.Items, a.keymapConfigItems()...)
	tree.Items = append(tree.Items, a.shortcutConfigItems()...)

	a.cfgTree = tree
	a.cfgSelectedSet = make(map[string]bool)
	items := buildConfigItemsFiltered(tree, a.cfgFilterCat, a.cfgSearchTerm)
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

// keymapConfigItems creates synthetic config items for current keybindings.
func (a *App) keymapConfigItems() []session.ConfigItem {
	km := a.keymap
	type kv struct{ key, val string }
	sections := []struct {
		name  string
		binds []kv
	}{
		{"session", []kv{
			{"quit", km.Session.Quit}, {"open", km.Session.Open}, {"edit", km.Session.Edit},
			{"actions", km.Session.Actions}, {"views", km.Session.Views}, {"refresh", km.Session.Refresh},
			{"search", km.Session.Search}, {"global_search", km.Session.GlobalSearch},
			{"live", km.Session.Live}, {"select", km.Session.Select},
			{"preview", km.Session.Preview}, {"preview_back", km.Session.PreviewBack},
			{"command", km.Session.Command}, {"help", km.Session.Help},
		}},
		{"actions", []kv{
			{"delete", km.Actions.Delete}, {"move", km.Actions.Move}, {"resume", km.Actions.Resume},
			{"copy_path", km.Actions.CopyPath}, {"worktree", km.Actions.Worktree},
			{"kill", km.Actions.Kill}, {"input", km.Actions.Input}, {"jump", km.Actions.Jump},
			{"urls", km.Actions.URLs}, {"files", km.Actions.Files},
			{"import_mem", km.Actions.ImportMem}, {"remove_mem", km.Actions.RemoveMem},
		}},
		{"views", []kv{
			{"stats", km.Views.Stats}, {"config", km.Views.Config}, {"plugins", km.Views.Plugins},
		}},
		{"conversation", []kv{
			{"jump_to_tree", km.Conversation.JumpToTree}, {"live_toggle", km.Conversation.LiveToggle},
			{"edit", km.Conversation.Edit}, {"actions", km.Conversation.Actions},
			{"input", km.Conversation.Input},
		}},
		{"preview", []kv{
			{"fold_all", km.Preview.FoldAll}, {"expand_all", km.Preview.ExpandAll},
			{"filter", km.Preview.Filter}, {"copy_mode", km.Preview.CopyMode},
			{"copy_all", km.Preview.CopyAll},
		}},
	}
	var items []session.ConfigItem
	for _, sec := range sections {
		for _, b := range sec.binds {
			if b.val == "" {
				continue
			}
			items = append(items, session.ConfigItem{
				Category:    session.ConfigKeymap,
				Name:        b.key + " = " + b.val,
				Description: sec.name + " keymap",
				Group:       sec.name,
			})
		}
	}
	return items
}

// shortcutConfigItems creates synthetic config items for number shortcuts.
func (a *App) shortcutConfigItems() []session.ConfigItem {
	var items []session.ConfigItem
	for viewName, vs := range a.shortcuts {
		addSide := func(side string, sm ShortcutMap) {
			for i := '1'; i <= '9'; i++ {
				k := string(i)
				if cmd, ok := sm[k]; ok {
					items = append(items, session.ConfigItem{
						Category:    session.ConfigShortcut,
						Name:        k + " → " + cmd,
						Description: viewName + " " + side,
						Group:       viewName + ":" + side,
					})
				}
			}
		}
		if len(vs.Left) > 0 {
			addSide("left", vs.Left)
		}
		if len(vs.Right) > 0 {
			addSide("right", vs.Right)
		}
	}
	return items
}

func (a *App) handleConfigKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sp := &a.cfgSplit
	key := msg.String()

	// Route to text inputs BEFORE nav translation — otherwise vim keys
	// (j/k/h/l) get converted to arrow keys and won't type in the input.
	if a.cfgSearching {
		return a.handleCfgSearch(msg)
	}
	if a.cfgNaming {
		return a.handleCfgNaming(msg)
	}
	if a.cfgProjectPicker {
		return a.handleCfgProjectPicker(msg)
	}

	// Page jump menu: second key picks the section (before nav translation so h/l work)
	if a.cfgPageMenu {
		a.cfgPageMenu = false
		return a.handleCfgPageMenu(key)
	}

	// Translate navigation aliases (vim hjkl, etc.)
	if nav, navMsg := a.keymap.TranslateNav(key, msg); nav != "" {
		key = nav
		msg = navMsg
	}

	// Views menu: pick a view
	if a.viewsMenu {
		return a.handleViewsMenu(key)
	}

	// Actions menu: pick an action
	if a.cfgActionsMenu {
		return a.handleCfgActionsMenu(key)
	}

	// Clear delete confirm on any key except d
	if a.cfgDeleteConfirm && key != "d" {
		a.cfgDeleteConfirm = false
		a.copiedMsg = ""
	}

	switch key {
	case "q":
		return a.quit()
	case "esc":
		if a.cfgHasSelection() {
			a.clearCfgSelection()
			return a, nil
		}
		if a.cfgSearchTerm != "" {
			a.clearCfgSearch()
			return a, nil
		}
		if a.cfgFilterCat != cfgFilterAll {
			a.cfgFilterCat = cfgFilterAll
			a.rebuildCfgList()
			return a, nil
		}
		// Top-level view: esc does nothing (use v to switch views)
		return a, nil
	case a.keymap.Session.Views:
		a.viewsMenu = true
		return a, nil
	case "x":
		a.cfgActionsMenu = true
		return a, nil
	case "p":
		a.cfgPageMenu = true
		return a, nil
	case " ":
		// Toggle multi-select on non-header items
		if ci, ok := a.cfgList.SelectedItem().(cfgItem); ok && !ci.isHeader {
			if a.cfgSelectedSet[ci.item.Path] {
				delete(a.cfgSelectedSet, ci.item.Path)
			} else {
				a.cfgSelectedSet[ci.item.Path] = true
			}
			a.applyCfgDelegate()
			// Auto-advance cursor
			idx := a.cfgList.Index()
			if idx < len(a.cfgList.Items())-1 {
				a.cfgList.Select(idx + 1)
				a.updateConfigPreview()
			}
		}
		return a, nil
	case "tab":
		a.cycleCfgFilter(1)
		return a, nil
	case "shift+tab":
		a.cycleCfgFilter(-1)
		return a, nil
	case "P":
		return a.openCfgProjectPicker()
	case "a":
		return a.startCfgNaming()
	case "/":
		return a, a.startCfgSearch()
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
	case "u":
		return a.undoCfgDelete()
	case a.keymap.Session.Refresh:
		a.copiedMsg = "Refreshing configs…"
		return a.openConfigExplorer()
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

	// Only pass navigation keys to list — block all others to prevent
	// the bubbles list from entering its built-in filter mode on character keys.
	if a.listReady(&a.cfgList) && isNavKey(msg) {
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

// cfgSection defines a category with its display label.
type cfgSection struct {
	category session.ConfigCategory
	label    string
}

// cfgScopeGroup defines a scope (User/Project/Local) containing one or more sections.
type cfgScopeGroup struct {
	scope    string
	sections []cfgSection
}

func cfgScopeGroups(tree *session.ConfigTree) []cfgScopeGroup {
	return []cfgScopeGroup{
		{"USER", []cfgSection{
			{session.ConfigGlobal, "MEMORY"},
			{session.ConfigSkill, "SKILLS"},
			{session.ConfigAgent, "AGENTS"},
			{session.ConfigCommand, "COMMANDS"},
			{session.ConfigHook, "HOOKS"},
			{session.ConfigMCP, "MCP SERVERS"},
		}},
		{fmt.Sprintf("PROJECT: %s", tree.ProjectName), []cfgSection{
			{session.ConfigProject, ""},
		}},
		{"LOCAL", []cfgSection{
			{session.ConfigLocal, ""},
		}},
		{"ENTERPRISE", []cfgSection{
			{session.ConfigEnterprise, ""},
		}},
	}
}


func buildConfigItems(tree *session.ConfigTree) []list.Item {
	var items []list.Item

	for _, group := range cfgScopeGroups(tree) {
		// Check if any section in this group has items
		groupHasItems := false
		for _, sec := range group.sections {
			for _, item := range tree.Items {
				if item.Category == sec.category {
					groupHasItems = true
					break
				}
			}
			if groupHasItems {
				break
			}
		}

		// Scope header
		items = append(items, cfgItem{isHeader: true, label: "  " + group.scope})

		if !groupHasItems {
			items = append(items, cfgItem{isHeader: true, label: "    (empty)"})
			continue
		}

		for _, sec := range group.sections {
			var catItems []session.ConfigItem
			for _, item := range tree.Items {
				if item.Category == sec.category {
					catItems = append(catItems, item)
				}
			}
			if len(catItems) == 0 {
				continue
			}

			// Sub-header for groups with multiple sections
			if sec.label != "" && len(group.sections) > 1 {
				items = append(items, cfgItem{isHeader: true, label: "    " + sec.label})
			}

			items = appendGroupedItems(items, catItems)
		}
	}

	return items
}

// appendGroupedItems appends config items to the list, inserting sub-headers
// when items have different Group values (e.g. hook event types).
func appendGroupedItems(items []list.Item, catItems []session.ConfigItem) []list.Item {
	hasGroups := false
	for _, ci := range catItems {
		if ci.Group != "" {
			hasGroups = true
			break
		}
	}

	if !hasGroups {
		for i, ci := range catItems {
			items = append(items, cfgItem{
				item:      ci,
				treeDepth: 1,
				treeLast:  i == len(catItems)-1,
			})
		}
		return items
	}

	// Group items by Group field, preserving order of first appearance
	type groupEntry struct {
		name  string
		items []session.ConfigItem
	}
	var ungrouped []session.ConfigItem
	var groups []groupEntry
	idx := make(map[string]int)
	for _, ci := range catItems {
		if ci.Group == "" {
			ungrouped = append(ungrouped, ci)
			continue
		}
		if i, ok := idx[ci.Group]; ok {
			groups[i].items = append(groups[i].items, ci)
		} else {
			idx[ci.Group] = len(groups)
			groups = append(groups, groupEntry{ci.Group, []session.ConfigItem{ci}})
		}
	}

	// Render ungrouped first
	lastUngrouped := len(ungrouped) - 1
	if len(groups) > 0 {
		lastUngrouped = -1
	}
	for i, ci := range ungrouped {
		items = append(items, cfgItem{
			item:      ci,
			treeDepth: 1,
			treeLast:  i == lastUngrouped,
		})
	}

	// Render grouped items with sub-headers
	for _, g := range groups {
		items = append(items, cfgItem{isHeader: true, label: "      " + g.name})
		for i, ci := range g.items {
			items = append(items, cfgItem{
				item:      ci,
				treeDepth: 1,
				treeLast:  i == len(g.items)-1,
			})
		}
	}
	return items
}

func newConfigList(items []list.Item, width, height int) list.Model {
	l := list.New(items, cfgDelegate{}, width, height)
	initListBase(&l)
	l.SetFilteringEnabled(false)
	// Unbind the filter key so the list never enters filtering mode
	l.KeyMap.Filter.SetEnabled(false)
	l.KeyMap.ClearFilter.SetEnabled(false)
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
	// Rebuild with current search/filter preserved
	a.rebuildCfgList()
	if selectedIdx < len(a.cfgList.Items()) {
		a.cfgList.Select(selectedIdx)
	}
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

func (a *App) startCfgSearch() tea.Cmd {
	a.cfgSearching = true
	a.cfgSearchHistI = -1 // new input mode
	ti := textinput.New()
	ti.Prompt = "Search: "
	ti.Focus()
	a.cfgSearchInput = ti
	return ti.Cursor.BlinkCmd()
}

const cfgSearchHistMax = 20

func (a *App) pushCfgSearchHist(term string) {
	// Remove duplicate if exists
	for i, h := range a.cfgSearchHist {
		if h == term {
			a.cfgSearchHist = append(a.cfgSearchHist[:i], a.cfgSearchHist[i+1:]...)
			break
		}
	}
	a.cfgSearchHist = append(a.cfgSearchHist, term)
	if len(a.cfgSearchHist) > cfgSearchHistMax {
		a.cfgSearchHist = a.cfgSearchHist[len(a.cfgSearchHist)-cfgSearchHistMax:]
	}
}

func (a *App) handleCfgSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		term := a.cfgSearchInput.Value()
		a.cfgSearching = false
		if term == "" {
			a.clearCfgSearch()
		} else {
			a.pushCfgSearchHist(term)
			a.cfgSearchTerm = term
			a.rebuildCfgList()
		}
		return a, nil
	case "esc":
		a.cfgSearching = false
		return a, nil
	case "up":
		// Browse history (newest to oldest)
		if len(a.cfgSearchHist) == 0 {
			return a, nil
		}
		if a.cfgSearchHistI < 0 {
			// Entering history from new input — start at most recent
			a.cfgSearchHistI = len(a.cfgSearchHist) - 1
		} else if a.cfgSearchHistI > 0 {
			a.cfgSearchHistI--
		}
		a.cfgSearchInput.SetValue(a.cfgSearchHist[a.cfgSearchHistI])
		a.cfgSearchInput.CursorEnd()
		return a, nil
	case "down":
		if a.cfgSearchHistI < 0 {
			return a, nil
		}
		if a.cfgSearchHistI < len(a.cfgSearchHist)-1 {
			a.cfgSearchHistI++
			a.cfgSearchInput.SetValue(a.cfgSearchHist[a.cfgSearchHistI])
			a.cfgSearchInput.CursorEnd()
		} else {
			// Past end of history — back to empty new input
			a.cfgSearchHistI = -1
			a.cfgSearchInput.SetValue("")
		}
		return a, nil
	}
	// Any typing resets history browsing position
	oldVal := a.cfgSearchInput.Value()
	var cmd tea.Cmd
	a.cfgSearchInput, cmd = a.cfgSearchInput.Update(msg)
	if a.cfgSearchInput.Value() != oldVal {
		a.cfgSearchHistI = -1
	}
	return a, cmd
}

func (a *App) applyCfgDelegate() {
	a.cfgList.SetDelegate(cfgDelegate{searchTerm: a.cfgSearchTerm, selectedSet: a.cfgSelectedSet})
}

func (a *App) clearCfgSearch() {
	a.cfgSearchTerm = ""
	a.cfgSearchMatch = nil
	a.cfgSearchIdx = 0
	a.rebuildCfgList()
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

// --- Config multi-select helpers ---

func (a *App) cfgHasSelection() bool {
	return len(a.cfgSelectedSet) > 0
}

func (a *App) clearCfgSelection() {
	clear(a.cfgSelectedSet)
	a.applyCfgDelegate()
}

func (a *App) selectedConfigItems() []session.ConfigItem {
	if a.cfgTree == nil {
		return nil
	}
	// Use full tree items, not filtered list, so selections persist across filters
	var items []session.ConfigItem
	for _, item := range a.cfgTree.Items {
		if a.cfgSelectedSet[item.Path] {
			items = append(items, item)
		}
	}
	return items
}

// extractRelConfigPath returns the relative path from ~/.claude/ for a config file.
// For files outside ~/.claude/, returns empty string.
func extractRelConfigPath(path, claudeDir string) string {
	rel, err := filepath.Rel(claudeDir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return rel
}

// buildTestConfigDir creates a temp directory with symlinks to selected config files
// in the correct structure for Claude Code to discover them.
func buildConfigTestEnv(items []session.ConfigItem) (*tmux.IsolatedEnv, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	claudeDir := filepath.Join(home, ".claude")

	env, err := tmux.NewIsolatedEnv("ccx-cfgtest-")
	if err != nil {
		return nil, err
	}

	// Create empty settings.json so hooks/MCP from the real HOME are not loaded.
	// Selected settings.json/hooks will be symlinked below if the user chose them.
	env.WriteSettings([]byte("{}"))

	hasHooks := false
	for _, item := range items {
		// Determine destination path based on whether file is inside ~/.claude/
		rel := extractRelConfigPath(item.Path, claudeDir)
		var dst string
		if rel != "" {
			// Inside ~/.claude/ → symlink into fake ~/.claude/
			dst = filepath.Join(env.ConfigDir, rel)
		} else {
			// Outside ~/.claude/ (project/local configs) → place relative to cwd (tmpDir).
			// Claude looks for project CLAUDE.md at cwd/CLAUDE.md and cwd/.claude/
			name := filepath.Base(item.Path)
			dir := filepath.Base(filepath.Dir(item.Path))
			if dir == ".claude" {
				// e.g. project/.claude/CLAUDE.md → tmpDir/.claude/CLAUDE.md (already env.ConfigDir)
				dst = filepath.Join(env.ConfigDir, name)
			} else {
				// e.g. project/CLAUDE.md → tmpDir/CLAUDE.md
				dst = filepath.Join(env.HomeDir, name)
			}
		}

		// For skills, symlink the entire skill directory
		if item.Category == session.ConfigSkill {
			skillDir := filepath.Dir(item.Path)
			dstDir := filepath.Dir(dst)
			if err := os.MkdirAll(filepath.Dir(dstDir), 0o755); err != nil {
				continue
			}
			os.Symlink(skillDir, dstDir)
			continue
		}

		// For hooks: symlink file or directory, track that we have hooks
		if item.Category == session.ConfigHook {
			hasHooks = true
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				continue
			}
			os.Symlink(item.Path, dst)
			continue
		}

		// Symlink individual file (remove placeholder if exists)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			continue
		}
		os.Remove(dst) // remove placeholder (e.g. empty settings.json)
		os.Symlink(item.Path, dst)
	}

	// If hooks were selected, copy the hooks config from settings.json
	// so Claude knows which events trigger which hook scripts.
	if hasHooks {
		injectHooksConfig(filepath.Join(claudeDir, "settings.json"), env.SettingsPath())
	}

	// Ensure Claude discovers memory files in the test env.
	// Two modes:
	//   1. Root CLAUDE.md selected → symlink it + all its @referenced files
	//   2. Only referenced files selected → generate a CLAUDE.md referencing them
	ensureTestMemory(env, items, claudeDir)

	return env, nil
}

// ensureTestMemory handles memory file discovery in the test env.
func ensureTestMemory(env *tmux.IsolatedEnv, items []session.ConfigItem, claudeDir string) {
	rootCLAUDE := filepath.Join(claudeDir, "CLAUDE.md")
	claudeMdDst := filepath.Join(env.ConfigDir, "CLAUDE.md")

	// Check if the root CLAUDE.md was selected
	rootSelected := false
	for _, item := range items {
		if item.Path == rootCLAUDE {
			rootSelected = true
			break
		}
	}

	if rootSelected {
		// Mode 1: root CLAUDE.md selected → symlink it and ALL its referenced files.
		// The symlink for CLAUDE.md itself was already created in the main loop.
		// Now symlink every file it references so the @refs resolve.
		allRefs := session.ExtractFileReferences(rootCLAUDE)
		for _, refPath := range allRefs {
			rel := extractRelConfigPath(refPath, claudeDir)
			if rel == "" || rel == "CLAUDE.md" {
				continue
			}
			dst := filepath.Join(env.ConfigDir, rel)
			// Skip if already symlinked (user also selected this file)
			if _, err := os.Lstat(dst); err == nil {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				continue
			}
			os.Symlink(refPath, dst)
		}
		return
	}

	// Mode 2: only referenced memory files selected → generate a minimal CLAUDE.md
	var refs []string
	for _, item := range items {
		rel := extractRelConfigPath(item.Path, claudeDir)
		if rel == "" || rel == "CLAUDE.md" {
			continue
		}
		if !strings.HasSuffix(rel, ".md") && !strings.HasSuffix(rel, ".yaml") {
			continue
		}
		refs = append(refs, rel)
	}
	if len(refs) == 0 {
		return
	}

	var buf strings.Builder
	buf.WriteString("# Test Environment\n\nSelected configs:\n\n")
	for _, rel := range refs {
		buf.WriteString("@~/.claude/" + rel + "\n")
	}

	os.Remove(claudeMdDst)
	os.WriteFile(claudeMdDst, []byte(buf.String()), 0o644)
}

// injectHooksConfig copies the "hooks" key from srcSettings into dstSettings.
// If dstSettings already has content (e.g. from being selected as MCP config),
// it merges; otherwise it creates a new JSON with just the hooks key.
func injectHooksConfig(srcSettings, dstSettings string) {
	srcData, err := os.ReadFile(srcSettings)
	if err != nil {
		return
	}
	var src map[string]json.RawMessage
	if err := json.Unmarshal(srcData, &src); err != nil {
		return
	}
	hooksRaw, ok := src["hooks"]
	if !ok {
		return
	}

	// Read existing dst settings (may be empty "{}" or user-selected settings.json)
	dstData, _ := os.ReadFile(dstSettings)
	var dst map[string]json.RawMessage
	if err := json.Unmarshal(dstData, &dst); err != nil {
		dst = make(map[string]json.RawMessage)
	}
	dst["hooks"] = hooksRaw

	out, err := json.MarshalIndent(dst, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(dstSettings, out, 0o644)
}

// launchConfigTest launches a sandboxed Claude session using only selected configs.
func (a *App) launchConfigTest() (tea.Model, tea.Cmd) {
	if !a.cfgHasSelection() {
		a.copiedMsg = "No configs selected (space to select)"
		return a, nil
	}
	if !tmux.InTmux() {
		a.copiedMsg = "Requires tmux"
		return a, nil
	}

	items := a.selectedConfigItems()
	env, err := buildConfigTestEnv(items)
	if err != nil {
		a.copiedMsg = "Failed: " + err.Error()
		return a, nil
	}

	script := env.Script()

	a.copiedMsg = fmt.Sprintf("Testing %d configs…", len(items))

	return a, func() tea.Msg {
		env.RunPopup(script)
		return configTestDoneMsg{tmpDir: env.HomeDir}
	}
}

type configTestDoneMsg struct{ tmpDir string }


// --- Category filter ---

const cfgFilterAll = -1    // show everything
const cfgFilterMemory = -2 // show global + project + local (all "memory" scopes)

func (a *App) cycleCfgFilter(dir int) {
	a.cfgFilterCat += dir
	count := session.ConfigCategoryCount()
	if a.cfgFilterCat >= count {
		a.cfgFilterCat = cfgFilterAll
	} else if a.cfgFilterCat < cfgFilterAll {
		a.cfgFilterCat = count - 1
	}
	a.rebuildCfgList()
}

func (a *App) cfgFilterLabel() string {
	if a.cfgFilterCat == cfgFilterMemory {
		return "MEMORY"
	}
	if a.cfgFilterCat < 0 {
		return ""
	}
	return session.CategoryLabel(session.ConfigCategory(a.cfgFilterCat))
}

func (a *App) rebuildCfgList() {
	items := buildConfigItemsFiltered(a.cfgTree, a.cfgFilterCat, a.cfgSearchTerm)
	contentH := ContentHeight(a.height)
	listW := a.cfgSplit.ListWidth(a.width, a.splitRatio)
	a.cfgList = newConfigList(items, listW, contentH)
	a.applyCfgDelegate()

	// Build match indices for n/N navigation
	a.cfgSearchMatch = nil
	a.cfgSearchIdx = 0
	if a.cfgSearchTerm != "" {
		for i, item := range items {
			if ci, ok := item.(cfgItem); ok && !ci.isHeader {
				a.cfgSearchMatch = append(a.cfgSearchMatch, i)
			}
		}
	}

	a.cfgSplit.CacheKey = ""
	a.updateConfigPreview()
}

// cfgSearchTags maps config categories to searchable "is:" tags.
var cfgSearchTags = map[session.ConfigCategory][]string{
	session.ConfigGlobal:  {"is:user", "is:memory"},
	session.ConfigProject: {"is:project"},
	session.ConfigLocal:   {"is:local"},
	session.ConfigSkill:   {"is:user", "is:skill"},
	session.ConfigAgent:   {"is:user", "is:agent"},
	session.ConfigCommand: {"is:user", "is:command", "is:cmd"},
	session.ConfigHook:    {"is:user", "is:hook"},
	session.ConfigMCP:        {"is:user", "is:mcp"},
	session.ConfigEnterprise: {"is:enterprise"},
}

// cfgItemSearchText returns the full searchable text for a config item,
// including synthetic is: tags for scope and type filtering.
func cfgItemSearchText(item session.ConfigItem) string {
	parts := []string{
		strings.ToLower(item.Name),
		strings.ToLower(item.Description),
	}
	if tags, ok := cfgSearchTags[item.Category]; ok {
		parts = append(parts, tags...)
	}
	return strings.Join(parts, " ")
}

// cfgMatchesSearch checks if a config item matches all search terms.
// Supports "is:" prefix filters and plain text substring matching.
func cfgMatchesSearch(item session.ConfigItem, terms []string) bool {
	text := cfgItemSearchText(item)
	for _, term := range terms {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}

func buildConfigItemsFiltered(tree *session.ConfigTree, filterCat int, searchTerm string) []list.Item {
	if filterCat == cfgFilterAll && searchTerm == "" {
		return buildConfigItems(tree)
	}

	// Split search into terms (all must match)
	var terms []string
	if searchTerm != "" {
		for _, t := range strings.Fields(strings.ToLower(searchTerm)) {
			if t != "" {
				terms = append(terms, t)
			}
		}
	}

	if filterCat == cfgFilterMemory || filterCat == int(session.ConfigGlobal) {
		return buildMemoryFilterItems(tree, terms)
	}

	filterActive := filterCat >= 0
	scopeGroups := cfgScopeGroups(tree)

	var items []list.Item
	for _, group := range scopeGroups {
		var groupItems []list.Item

		for _, sec := range group.sections {
			if filterActive && int(sec.category) != filterCat {
				continue
			}

			var catItems []session.ConfigItem
			for _, item := range tree.Items {
				if item.Category != sec.category {
					continue
				}
				if len(terms) > 0 && !cfgMatchesSearch(item, terms) {
					continue
				}
				catItems = append(catItems, item)
			}
			if len(catItems) == 0 {
				continue
			}

			// Sub-header for groups with multiple sections
			if sec.label != "" && len(group.sections) > 1 {
				groupItems = append(groupItems, cfgItem{isHeader: true, label: "    " + sec.label})
			}

			groupItems = appendGroupedItems(groupItems, catItems)
		}

		// Skip empty scope groups when searching
		if len(groupItems) == 0 {
			continue
		}

		items = append(items, cfgItem{isHeader: true, label: "  " + group.scope})
		items = append(items, groupItems...)
	}
	return items
}

// buildMemoryFilterItems builds items for the MEMORY filter:
// 1. CLAUDE.md files (user / project / local)
// 2. All @-referenced files with source and keyword info
func buildMemoryFilterItems(tree *session.ConfigTree, terms []string) []list.Item {
	memoryCategories := map[session.ConfigCategory]bool{
		session.ConfigGlobal:  true,
		session.ConfigProject: true,
		session.ConfigLocal:   true,
	}

	var claudeMDs []session.ConfigItem
	var referenced []session.ConfigItem

	for _, item := range tree.Items {
		if !memoryCategories[item.Category] {
			continue
		}
		if strings.HasSuffix(item.Name, ".json") {
			continue
		}
		if len(terms) > 0 && !cfgMatchesSearch(item, terms) {
			continue
		}
		if item.RefDepth == 0 {
			claudeMDs = append(claudeMDs, item)
		} else {
			referenced = append(referenced, item)
		}
	}

	var items []list.Item

	// --- CLAUDE.md files grouped by scope ---
	if len(claudeMDs) > 0 {
		items = append(items, cfgItem{isHeader: true, label: "  CLAUDE.md"})

		// Group by scope
		type scopeGroup struct {
			label string
			items []session.ConfigItem
		}
		scopes := []scopeGroup{
			{"user", nil},
			{"project", nil},
			{"local", nil},
		}
		for _, ci := range claudeMDs {
			switch ci.Category {
			case session.ConfigGlobal:
				scopes[0].items = append(scopes[0].items, ci)
			case session.ConfigProject:
				scopes[1].items = append(scopes[1].items, ci)
			case session.ConfigLocal:
				scopes[2].items = append(scopes[2].items, ci)
			}
		}

		// Count non-empty scopes for tree connectors
		nonEmpty := 0
		for _, s := range scopes {
			if len(s.items) > 0 {
				nonEmpty++
			}
		}
		scopeIdx := 0
		for _, s := range scopes {
			if len(s.items) == 0 {
				continue
			}
			scopeIdx++
			isLastScope := scopeIdx == nonEmpty
			items = append(items, cfgItem{isHeader: true, label: "    " + s.label})
			for i, ci := range s.items {
				items = append(items, cfgItem{
					item: ci, treeDepth: 2, treeLast: isLastScope && i == len(s.items)-1,
				})
			}
		}
	}

	// Build scope map: path → scope label (e.g. "user", "local/build-catalog")
	scopeMap := buildScopeMap(claudeMDs)

	// --- @-referenced files ---
	if len(referenced) > 0 {
		items = append(items, cfgItem{isHeader: true, label: "  REFERENCED (@)"})
		for i, ci := range referenced {
			item := ci
			desc := scopedRefDesc(item.RefBy, scopeMap)
			item.Description = desc
			items = append(items, cfgItem{
				item: item, treeDepth: 1, treeLast: i == len(referenced)-1,
			})
		}
	}

	return items
}

// buildScopeMap maps CLAUDE.md file paths to scope-qualified labels.
// e.g. "/home/user/.claude/CLAUDE.md" → "user/CLAUDE.md"
//
//	"/path/to/project/CLAUDE.md" → "local/CLAUDE.md"
func buildScopeMap(claudeMDs []session.ConfigItem) map[string]string {
	m := make(map[string]string, len(claudeMDs))
	for _, ci := range claudeMDs {
		scope := ""
		switch ci.Category {
		case session.ConfigGlobal:
			scope = "user"
		case session.ConfigProject:
			scope = "project"
		case session.ConfigLocal:
			scope = "local"
		}
		m[ci.Path] = scope + "/" + ci.Name
	}
	return m
}

// scopedRefDesc returns "@ <scope/file>" showing which CLAUDE.md references this item.
func scopedRefDesc(refBy string, scopeMap map[string]string) string {
	if refBy == "" {
		return ""
	}
	if label, ok := scopeMap[refBy]; ok {
		return "@ " + label
	}
	return "@ " + filepath.Base(refBy)
}

// --- Draft config creation ---

// cfgTemplate describes how to create a new config file.
type cfgTemplate struct {
	subdir   string // relative to ~/.claude/
	ext      string
	template string
	perm     os.FileMode
}

// cfgTemplates maps categories to their default creation template.
var cfgTemplates = map[session.ConfigCategory]cfgTemplate{
	session.ConfigGlobal:  {"memory", ".md", "# Title\n\n", 0o644},
	session.ConfigProject: {"", ".md", "# Title\n\n", 0o644}, // resolved dynamically
	session.ConfigAgent:   {"agents", ".md", "# Agent Name\n\nYou are an agent that...\n", 0o644},
	session.ConfigSkill:   {"skills", "", "---\ndescription: \"\"\n---\n# Skill Name\n\n...\n", 0o644},
	session.ConfigCommand: {"commands", ".md", "# Command Name\n\n$ARGUMENTS\n", 0o644},
	session.ConfigHook:    {"hooks", ".sh", "#!/bin/bash\nset -euo pipefail\n\n# Hook: \n", 0o755},
}

// cfgGlobalSubdirs are the subdirs under ~/.claude/ where global items live.
var cfgGlobalSubdirs = []string{"memory", "contexts", "rules"}

func (a *App) startCfgNaming() (tea.Model, tea.Cmd) {
	// Determine category: use filter if active, else use current item's category
	cat := session.ConfigGlobal // default
	if a.cfgFilterCat >= 0 {
		cat = session.ConfigCategory(a.cfgFilterCat)
	} else if ci, ok := a.cfgList.SelectedItem().(cfgItem); ok && !ci.isHeader {
		cat = ci.item.Category
	}

	// Check if this category supports creation
	if _, ok := cfgTemplates[cat]; !ok {
		a.copiedMsg = "Cannot create " + session.CategoryLabel(cat) + " items"
		return a, nil
	}

	// For Global/Project, detect subdir from current item path
	prompt := "New " + strings.ToLower(session.CategoryLabel(cat))
	if cat == session.ConfigGlobal {
		subdir := a.detectCfgSubdir()
		if subdir != "" {
			prompt = "New " + subdir
		} else {
			prompt = "New memory"
		}
	}

	a.cfgNaming = true
	a.cfgNamingCat = cat
	ti := textinput.New()
	ti.Prompt = prompt + ": "
	ti.Focus()
	a.cfgNamingInput = ti
	return a, nil
}

// detectCfgSubdir determines which global subdir the cursor is in (memory, contexts, rules).
func (a *App) detectCfgSubdir() string {
	ci, ok := a.cfgList.SelectedItem().(cfgItem)
	if !ok || ci.isHeader || ci.item.Category != session.ConfigGlobal {
		return ""
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	claudeDir := filepath.Join(home, ".claude")
	rel := extractRelConfigPath(ci.item.Path, claudeDir)
	if rel == "" {
		return ""
	}
	// rel is like "memory/k8s.md" or "contexts/dev.md" or "rules/security.md"
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

func (a *App) handleCfgNaming(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		name := strings.TrimSpace(a.cfgNamingInput.Value())
		a.cfgNaming = false
		if name == "" {
			return a, nil
		}
		return a.createDraftConfig(name, a.cfgNamingCat)
	case "esc":
		a.cfgNaming = false
		return a, nil
	}
	var cmd tea.Cmd
	a.cfgNamingInput, cmd = a.cfgNamingInput.Update(msg)
	return a, cmd
}

func (a *App) createDraftConfig(name string, cat session.ConfigCategory) (tea.Model, tea.Cmd) {
	tmpl, ok := cfgTemplates[cat]
	if !ok {
		return a, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		a.copiedMsg = "Cannot find home dir"
		return a, nil
	}

	// Sanitize name
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "-")

	var filePath string
	switch {
	case cat == session.ConfigGlobal:
		// Detect subdir from cursor position (memory, contexts, rules)
		subdir := a.detectCfgSubdir()
		if subdir == "" {
			subdir = "memory"
		}
		dir := filepath.Join(home, ".claude", subdir)
		os.MkdirAll(dir, 0o755)
		filePath = filepath.Join(dir, name+".md")
	case cat == session.ConfigProject:
		if a.cfgTree == nil || a.cfgTree.ProjectPath == "" {
			a.copiedMsg = "No project selected"
			return a, nil
		}
		encoded := session.EncodeProjectPath(a.cfgTree.ProjectPath)
		dir := filepath.Join(home, ".claude", "projects", encoded, "memory")
		os.MkdirAll(dir, 0o755)
		filePath = filepath.Join(dir, name+".md")
	case cat == session.ConfigSkill:
		// Skills use skills/<name>/SKILL.md
		dir := filepath.Join(home, ".claude", tmpl.subdir, name)
		os.MkdirAll(dir, 0o755)
		filePath = filepath.Join(dir, "SKILL.md")
	default:
		dir := filepath.Join(home, ".claude", tmpl.subdir)
		os.MkdirAll(dir, 0o755)
		filePath = filepath.Join(dir, name+tmpl.ext)
	}

	// Don't overwrite existing
	if _, err := os.Stat(filePath); err == nil {
		a.copiedMsg = "Already exists: " + filepath.Base(filePath)
		return a, nil
	}

	if err := os.WriteFile(filePath, []byte(tmpl.template), tmpl.perm); err != nil {
		a.copiedMsg = "Create failed: " + err.Error()
		return a, nil
	}

	return a.openInEditor(filePath)
}

// --- Delete config items (with confirm + undo) ---

// cfgTrashEntry stores a deleted item for undo.
type cfgTrashEntry struct {
	origPath     string // original file path
	tmpPath      string // temp backup path
	isDir        bool   // true for skill directories
	hookSettings string // settings.json path (non-empty if hook entry was removed)
	hookBackup   []byte // original settings.json content for undo
}

// cfgDeletableCategories lists categories where files can be deleted.
var cfgDeletableCategories = map[session.ConfigCategory]bool{
	session.ConfigGlobal:  true,
	session.ConfigProject: true,
	session.ConfigAgent:   true,
	session.ConfigSkill:   true,
	session.ConfigCommand: true,
	session.ConfigHook:    true,
}

func (a *App) deleteCfgItems() (tea.Model, tea.Cmd) {
	// If confirm is pending, this is the second press — execute delete
	if a.cfgDeleteConfirm {
		a.cfgDeleteConfirm = false
		return a.executeCfgDelete()
	}

	// Gather items to delete
	var targets []session.ConfigItem
	if a.cfgHasSelection() {
		for _, item := range a.selectedConfigItems() {
			if cfgDeletableCategories[item.Category] {
				targets = append(targets, item)
			}
		}
	} else {
		ci, ok := a.cfgList.SelectedItem().(cfgItem)
		if !ok || ci.isHeader {
			return a, nil
		}
		if !cfgDeletableCategories[ci.item.Category] {
			a.copiedMsg = "Cannot delete " + session.CategoryLabel(ci.item.Category) + " items"
			return a, nil
		}
		targets = append(targets, ci.item)
	}

	if len(targets) == 0 {
		return a, nil
	}

	// Show confirmation — keep actions menu open so next d goes through handleCfgActionsMenu
	a.cfgDeleteConfirm = true
	a.cfgActionsMenu = true
	if len(targets) == 1 {
		a.copiedMsg = "Delete " + targets[0].Name + "? Press d to confirm"
	} else {
		a.copiedMsg = fmt.Sprintf("Delete %d items? Press d to confirm", len(targets))
	}
	return a, nil
}

func (a *App) executeCfgDelete() (tea.Model, tea.Cmd) {
	var targets []session.ConfigItem
	if a.cfgHasSelection() {
		for _, item := range a.selectedConfigItems() {
			if cfgDeletableCategories[item.Category] {
				targets = append(targets, item)
			}
		}
	} else {
		ci, ok := a.cfgList.SelectedItem().(cfgItem)
		if !ok || ci.isHeader {
			return a, nil
		}
		targets = append(targets, ci.item)
	}

	trashed := 0
	for _, item := range targets {
		entry, err := trashCfgItem(item)
		if err != nil {
			continue
		}
		// For hooks, also remove the entry from settings.json
		if item.Category == session.ConfigHook && item.RefBy != "" {
			backup, err := os.ReadFile(item.RefBy)
			if err == nil {
				if removeHookFromSettings(item.RefBy, item.Path, item.Group) == nil {
					entry.hookSettings = item.RefBy
					entry.hookBackup = backup
				}
			}
		}
		a.cfgTrash = append(a.cfgTrash, entry)
		trashed++
	}

	a.clearCfgSelection()
	a.refreshConfigExplorer()
	if trashed == 1 {
		a.copiedMsg = "Deleted " + targets[0].Name + " (u:undo)"
	} else {
		a.copiedMsg = fmt.Sprintf("Deleted %d items (u:undo)", trashed)
	}
	return a, nil
}

// trashCfgItem moves a config file to a temp location for undo.
func trashCfgItem(item session.ConfigItem) (cfgTrashEntry, error) {
	isDir := item.Category == session.ConfigSkill
	srcPath := item.Path
	if isDir {
		srcPath = filepath.Dir(item.Path) // skill directory
	}

	tmpDir, err := os.MkdirTemp("", "ccx-trash-")
	if err != nil {
		return cfgTrashEntry{}, err
	}
	tmpPath := filepath.Join(tmpDir, filepath.Base(srcPath))

	if err := os.Rename(srcPath, tmpPath); err != nil {
		os.RemoveAll(tmpDir)
		return cfgTrashEntry{}, err
	}

	return cfgTrashEntry{origPath: srcPath, tmpPath: tmpPath, isDir: isDir}, nil
}

// removeHookFromSettings removes a hook command referencing scriptPath
// from the given event type in settings.json. Cleans up empty matchers/events.
func removeHookFromSettings(settingsPath, scriptPath, eventType string) error {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	hooksRaw, ok := obj["hooks"]
	if !ok {
		return fmt.Errorf("no hooks key")
	}

	type hookCmd struct {
		Type    string `json:"type,omitempty"`
		Command string `json:"command"`
	}
	type matcherEntry struct {
		Matcher string    `json:"matcher"`
		Hooks   []hookCmd `json:"hooks"`
	}
	var hooks map[string][]matcherEntry
	if err := json.Unmarshal(hooksRaw, &hooks); err != nil {
		return err
	}

	home, _ := os.UserHomeDir()
	matchers := hooks[eventType]
	changed := false
	var kept []matcherEntry
	for _, m := range matchers {
		var keptHooks []hookCmd
		for _, h := range m.Hooks {
			resolved := session.ExtractScriptPath(h.Command, home)
			if resolved == scriptPath {
				changed = true
				continue // remove this hook
			}
			keptHooks = append(keptHooks, h)
		}
		if len(keptHooks) > 0 {
			m.Hooks = keptHooks
			kept = append(kept, m)
		}
	}
	if !changed {
		return fmt.Errorf("hook not found in settings")
	}

	if len(kept) == 0 {
		delete(hooks, eventType)
	} else {
		hooks[eventType] = kept
	}

	// Write back — preserve other top-level keys
	if len(hooks) == 0 {
		delete(obj, "hooks")
	} else {
		raw, _ := json.Marshal(hooks)
		obj["hooks"] = raw
	}
	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, 0o644)
}

func (a *App) undoCfgDelete() (tea.Model, tea.Cmd) {
	if len(a.cfgTrash) == 0 {
		a.copiedMsg = "Nothing to undo"
		return a, nil
	}

	// Pop last entry
	entry := a.cfgTrash[len(a.cfgTrash)-1]
	a.cfgTrash = a.cfgTrash[:len(a.cfgTrash)-1]

	// Restore: ensure parent dir exists, then move back
	os.MkdirAll(filepath.Dir(entry.origPath), 0o755)
	if err := os.Rename(entry.tmpPath, entry.origPath); err != nil {
		a.copiedMsg = "Undo failed: " + err.Error()
		return a, nil
	}
	// Clean up empty temp dir
	os.Remove(filepath.Dir(entry.tmpPath))

	// Restore settings.json if hook entry was removed
	if entry.hookSettings != "" && entry.hookBackup != nil {
		os.WriteFile(entry.hookSettings, entry.hookBackup, 0o644)
	}

	a.refreshConfigExplorer()
	a.copiedMsg = "Restored " + filepath.Base(entry.origPath)
	return a, nil
}

// --- Actions menu ---

func (a *App) handleCfgActionsMenu(key string) (tea.Model, tea.Cmd) {
	a.cfgActionsMenu = false
	a.copiedMsg = ""

	switch key {
	case "d":
		return a.deleteCfgItems()
	case "e":
		return a.editCfgItems()
	case "t":
		return a.launchConfigTest()
	case "u":
		return a.undoCfgDelete()
	}
	// Any other key cancels
	return a, nil
}

// editCfgItems opens selected config files (or current item) in $EDITOR.
func (a *App) editCfgItems() (tea.Model, tea.Cmd) {
	if a.cfgHasSelection() {
		// Multi-select: open all selected files in editor
		var paths []string
		for _, item := range a.selectedConfigItems() {
			paths = append(paths, item.Path)
		}
		if len(paths) == 0 {
			return a, nil
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		args := append([]string{}, paths...)
		c := exec.Command(editor, args...)
		return a, tea.ExecProcess(c, func(err error) tea.Msg {
			return editorDoneMsg{}
		})
	}
	// Single item
	ci, ok := a.cfgList.SelectedItem().(cfgItem)
	if !ok || ci.isHeader {
		return a, nil
	}
	return a.openInEditor(ci.item.Path)
}

// renderCfgActionsHintBox renders a compact bordered hint box for the config actions menu.
func (a *App) renderCfgActionsHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "

	var lines []string
	if a.cfgHasSelection() {
		header := fmt.Sprintf("%d selected", len(a.cfgSelectedSet))
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(header))
	}
	line := hl.Render("d") + d.Render(":delete") + sp + hl.Render("e") + d.Render(":edit")
	if a.cfgHasSelection() {
		line += sp + hl.Render("t") + d.Render(":test")
	}
	if len(a.cfgTrash) > 0 {
		line += sp + hl.Render("u") + d.Render(":undo")
	}
	lines = append(lines, line)
	lines = append(lines, d.Render("esc:cancel"))

	body := strings.Join(lines, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

func (a *App) renderCfgPageHintBox() string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sp := "  "

	line1 := hl.Render("m") + d.Render(":memory") + sp + hl.Render("p") + d.Render(":project") + sp + hl.Render("l") + d.Render(":local")
	line2 := hl.Render("s") + d.Render(":skills") + sp + hl.Render("a") + d.Render(":agents") + sp + hl.Render("c") + d.Render(":cmds")
	line3 := hl.Render("h") + d.Render(":hooks") + sp + hl.Render("i") + d.Render(":mcp") + sp + hl.Render("e") + d.Render(":enterprise")
	line4 := hl.Render("o") + d.Render(":all")

	body := strings.Join([]string{line1, line2, line3, line4, d.Render("esc:cancel")}, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

func (a *App) handleCfgPageMenu(key string) (tea.Model, tea.Cmd) {
	a.cfgPageMenu = false
	switch key {
	case "m":
		a.cfgFilterCat = cfgFilterMemory
	case "p":
		a.cfgFilterCat = int(session.ConfigProject)
	case "l":
		a.cfgFilterCat = int(session.ConfigLocal)
	case "s":
		a.cfgFilterCat = int(session.ConfigSkill)
	case "a":
		a.cfgFilterCat = int(session.ConfigAgent)
	case "c":
		a.cfgFilterCat = int(session.ConfigCommand)
	case "h":
		a.cfgFilterCat = int(session.ConfigHook)
	case "i":
		a.cfgFilterCat = int(session.ConfigMCP)
	case "e":
		a.cfgFilterCat = int(session.ConfigEnterprise)
	case "o":
		a.cfgFilterCat = cfgFilterAll
	default:
		return a, nil
	}
	a.rebuildCfgList()
	return a, nil
}

// --- Project picker (fuzzy search overlay) ---

type cfgProjectEntry struct {
	path string
	name string // shortened display name
}

func (a *App) openCfgProjectPicker() (tea.Model, tea.Cmd) {
	home, err := os.UserHomeDir()
	if err != nil {
		a.copiedMsg = "Cannot find home dir"
		return a, nil
	}
	claudeDir := filepath.Join(home, ".claude")
	projects := session.ListProjects(claudeDir)
	if len(projects) == 0 {
		a.copiedMsg = "No projects found"
		return a, nil
	}

	entries := []cfgProjectEntry{{path: "", name: "(none)"}}
	for _, p := range projects {
		entries = append(entries, cfgProjectEntry{path: p, name: session.ShortenPath(p, home)})
	}

	ti := textinput.New()
	ti.Prompt = "Project: "
	ti.Focus()

	a.cfgProjectPicker = true
	a.cfgProjectEntries = entries
	a.cfgProjectInput = ti
	a.cfgProjectCursor = 0
	return a, ti.Cursor.BlinkCmd()
}

// cfgProjectFiltered returns entries matching the current fuzzy query.
func (a *App) cfgProjectFiltered() []cfgProjectEntry {
	query := strings.ToLower(a.cfgProjectInput.Value())
	if query == "" {
		return a.cfgProjectEntries
	}
	var out []cfgProjectEntry
	for _, e := range a.cfgProjectEntries {
		if fuzzyMatch(strings.ToLower(e.name), query) || fuzzyMatch(strings.ToLower(e.path), query) {
			out = append(out, e)
		}
	}
	return out
}

// fuzzyMatch checks if all characters of pattern appear in s in order.
func fuzzyMatch(s, pattern string) bool {
	pi := 0
	for i := 0; i < len(s) && pi < len(pattern); i++ {
		if s[i] == pattern[pi] {
			pi++
		}
	}
	return pi == len(pattern)
}

func (a *App) handleCfgProjectPicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "enter":
		filtered := a.cfgProjectFiltered()
		if len(filtered) > 0 && a.cfgProjectCursor < len(filtered) {
			a.cfgProjectPicker = false
			return a.switchCfgProject(filtered[a.cfgProjectCursor].path)
		}
		a.cfgProjectPicker = false
		return a, nil
	case "esc":
		a.cfgProjectPicker = false
		return a, nil
	case "up", "ctrl+p":
		if a.cfgProjectCursor > 0 {
			a.cfgProjectCursor--
		}
		return a, nil
	case "down", "ctrl+n":
		filtered := a.cfgProjectFiltered()
		if a.cfgProjectCursor < len(filtered)-1 {
			a.cfgProjectCursor++
		}
		return a, nil
	}

	// Pass to text input, then reset cursor to 0 on text change
	oldVal := a.cfgProjectInput.Value()
	var cmd tea.Cmd
	a.cfgProjectInput, cmd = a.cfgProjectInput.Update(msg)
	if a.cfgProjectInput.Value() != oldVal {
		a.cfgProjectCursor = 0
	}
	return a, cmd
}

func (a *App) switchCfgProject(projectPath string) (tea.Model, tea.Cmd) {
	home, err := os.UserHomeDir()
	if err != nil {
		a.copiedMsg = "Cannot find home dir"
		return a, nil
	}
	claudeDir := filepath.Join(home, ".claude")

	tree, err := session.ScanConfig(claudeDir, projectPath)
	if err != nil {
		a.copiedMsg = "Config scan failed"
		return a, nil
	}

	a.cfgTree = tree
	a.cfgSelectedSet = make(map[string]bool)
	items := buildConfigItemsFiltered(tree, a.cfgFilterCat, a.cfgSearchTerm)
	contentH := ContentHeight(a.height)
	listW := a.cfgSplit.ListWidth(a.width, a.splitRatio)
	a.cfgList = newConfigList(items, listW, contentH)
	a.cfgSplit.CacheKey = ""
	a.updateConfigPreview()
	a.copiedMsg = "Project: " + tree.ProjectName
	return a, nil
}

// renderProjectPickerOverlay renders the project picker as a compact centered overlay.
func (a *App) renderProjectPickerOverlay(bg string) string {
	filtered := a.cfgProjectFiltered()

	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#D1D5DB"))
	selStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	cursorStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	boxW := min(a.width-8, 70)
	// Fixed slot count — never changes with filter results
	maxSlots := min(15, ContentHeight(a.height)-8)
	if maxSlots < 3 {
		maxSlots = 3
	}

	// Build lines
	var lines []string
	lines = append(lines, hl.Render("Switch Project"))
	lines = append(lines, a.cfgProjectInput.View())
	lines = append(lines, dimStyle.Render(strings.Repeat("─", boxW-2)))

	// Scroll window around cursor
	start := 0
	visible := min(len(filtered), maxSlots)
	if a.cfgProjectCursor >= start+maxSlots {
		start = a.cfgProjectCursor - maxSlots + 1
	}
	end := start + visible
	if end > len(filtered) {
		end = len(filtered)
		start = max(0, end-maxSlots)
		visible = end - start
	}

	for i := start; i < end; i++ {
		e := filtered[i]
		display := e.name
		if len(display) > boxW-6 {
			display = "…" + display[len(display)-boxW+7:]
		}
		if i == a.cfgProjectCursor {
			lines = append(lines, cursorStyle.Render("▸ ")+selStyle.Render(display))
		} else {
			lines = append(lines, "  "+nameStyle.Render(display))
		}
	}

	// Pad remaining slots with empty lines to keep fixed height
	for i := visible; i < maxSlots; i++ {
		lines = append(lines, "")
	}

	// Footer: match count
	footer := dimStyle.Render(fmt.Sprintf("  %d/%d", len(filtered), len(a.cfgProjectEntries)))
	lines = append(lines, footer)

	body := strings.Join(lines, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1).
		Width(boxW).
		Height(maxSlots + 4) // title + input + ruler + footer
	overlay := boxStyle.Render(body)

	// Use ANSI-safe overlay onto background
	bgLines := strings.Split(bg, "\n")
	contentH := ContentHeight(a.height)
	for len(bgLines) < contentH {
		bgLines = append(bgLines, "")
	}

	fgLines := strings.Split(overlay, "\n")
	fgH := len(fgLines)
	fgW := 0
	for _, l := range fgLines {
		if w := lipgloss.Width(l); w > fgW {
			fgW = w
		}
	}

	startY := max((contentH-fgH)/2, 0)
	startX := max((a.width-fgW)/2, 0)

	for i, fgLine := range fgLines {
		y := startY + i
		if y >= len(bgLines) {
			break
		}
		bgLines[y] = overlayLine(bgLines[y], fgLine, startX, a.width)
	}

	if len(bgLines) > contentH {
		bgLines = bgLines[:contentH]
	}
	return strings.Join(bgLines, "\n")
}
