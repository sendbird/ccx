package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/extract"
)

var pickerDiffStyles = extract.DiffStyles{
	Add:    func(s string) string { return lipgloss.NewStyle().Foreground(lipgloss.Color("#4ADE80")).Render(s) },
	Del:    func(s string) string { return lipgloss.NewStyle().Foreground(lipgloss.Color("#F87171")).Render(s) },
	Hunk:   func(s string) string { return lipgloss.NewStyle().Foreground(lipgloss.Color("#7DD3FC")).Render(s) },
	Header: func(s string) string { return lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render(s) },
}

// PickerResult is returned when the picker exits with a jump target.
type PickerResult struct {
	SessionID string
	EntryUUID string
}

type pickerModel struct {
	kind     string // "urls", "files", "images", "changes"
	allItems []PickerItem
	items    []PickerItem // filtered
	cursor   int
	selected map[int]bool // indices in allItems

	// Ref selection: when an item has multiple refs, Enter opens ref picker
	refPicking bool // true when choosing which ref to jump to
	refCursor  int

	// Preview focus: right-arrow/tab moves focus to preview for scrolling
	previewFocused bool

	searching   bool
	searchInput textinput.Model
	searchTerm  string

	preview viewport.Model
	width   int
	height  int

	result *PickerResult
	quit   bool
}

func newPickerModel(kind string, items []PickerItem) pickerModel {
	return pickerModel{
		kind:     kind,
		allItems: items,
		items:    items,
		selected: make(map[int]bool),
	}
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.preview = viewport.New(m.previewWidth(), m.height-3)
		m.updatePreview()
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quit = true
			return m, tea.Quit
		}
		if m.refPicking {
			return m.handleRefKey(msg)
		}
		if m.previewFocused {
			return m.handlePreviewKey(msg)
		}
		if m.searching {
			return m.handleSearchKey(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

// --- Preview focus mode ---

func (m pickerModel) handlePreviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc", "left", "h", "tab":
		m.previewFocused = false
		return m, nil
	case "q":
		m.previewFocused = false
		return m, nil
	case "up", "k":
		m.preview.LineUp(3)
		return m, nil
	case "down", "j":
		m.preview.LineDown(3)
		return m, nil
	case "ctrl+d":
		m.preview.HalfViewDown()
		return m, nil
	case "ctrl+u":
		m.preview.HalfViewUp()
		return m, nil
	case "g":
		m.preview.GotoTop()
		return m, nil
	case "G":
		m.preview.GotoBottom()
		return m, nil
	case "enter", "e":
		// Pass through to normal handler
		m.previewFocused = false
		return m.handleKey(msg)
	}
	return m, nil
}

// --- Ref selection mode ---

func (m pickerModel) handleRefKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	item := m.items[m.cursor]
	switch key {
	case "esc":
		m.refPicking = false
		m.updatePreview()
		return m, nil
	case "up", "k":
		if m.refCursor > 0 {
			m.refCursor--
			m.updatePreview()
		}
		return m, nil
	case "down", "j":
		if m.refCursor < len(item.Refs)-1 {
			m.refCursor++
			m.updatePreview()
		}
		return m, nil
	case "enter":
		if m.refCursor >= 0 && m.refCursor < len(item.Refs) {
			ref := item.Refs[m.refCursor]
			m.result = &PickerResult{SessionID: item.SessionID, EntryUUID: ref.EntryUUID}
			return m, tea.Quit
		}
		return m, nil
	}
	// Number keys 1-9 for quick ref selection
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		idx := int(key[0] - '1')
		if idx < len(item.Refs) {
			ref := item.Refs[idx]
			m.result = &PickerResult{SessionID: item.SessionID, EntryUUID: ref.EntryUUID}
			return m, tea.Quit
		}
	}
	return m, nil
}

// --- Search mode ---

func (m pickerModel) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searching = false
		return m, nil
	case "enter":
		m.searching = false
		m.searchTerm = m.searchInput.Value()
		m.filterItems()
		return m, nil
	default:
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		m.searchTerm = m.searchInput.Value()
		m.filterItems()
		return m, cmd
	}
}

// --- Normal mode ---

func (m pickerModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "q", "esc":
		if m.searchTerm != "" {
			m.searchTerm = ""
			m.items = m.allItems
			m.cursor = 0
			m.updatePreview()
			return m, nil
		}
		m.quit = true
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.updatePreview()
		}
		return m, nil

	case "down", "j":
		if m.cursor < len(m.items)-1 {
			m.cursor++
			m.updatePreview()
		}
		return m, nil

	case "right", "l", "tab":
		m.previewFocused = true
		return m, nil

	case "/":
		m.searching = true
		ti := textinput.New()
		ti.Prompt = "/"
		ti.Width = 40
		ti.SetValue(m.searchTerm)
		ti.Focus()
		m.searchInput = ti
		return m, nil

	case " ":
		if m.cursor >= 0 && m.cursor < len(m.items) {
			idx := m.realIndex(m.cursor)
			if m.selected[idx] {
				delete(m.selected, idx)
			} else {
				m.selected[idx] = true
			}
			if m.cursor < len(m.items)-1 {
				m.cursor++
				m.updatePreview()
			}
		}
		return m, nil

	case "a":
		for i := range m.items {
			m.selected[m.realIndex(i)] = true
		}
		return m, nil

	case "A":
		clear(m.selected)
		return m, nil

	case "enter":
		if len(m.selected) > 0 {
			// Multi-select: open all
			m.openItems(m.selectedURLs())
			return m, nil
		}
		if m.cursor < 0 || m.cursor >= len(m.items) {
			return m, nil
		}
		item := m.items[m.cursor]
		if len(item.Refs) == 1 {
			// Single ref: jump directly
			m.result = &PickerResult{SessionID: item.SessionID, EntryUUID: item.Refs[0].EntryUUID}
			return m, tea.Quit
		}
		// Multiple refs: enter ref selection mode
		m.refPicking = true
		m.refCursor = 0
		m.updatePreview()
		return m, nil

	case "o":
		m.openItems(m.selectedURLs())
		return m, nil

	case "e":
		targets := m.selectedURLs()
		if len(targets) > 0 {
			m.editItems(targets)
		}
		return m, nil

	case "y":
		targets := m.selectedURLs()
		if len(targets) > 0 {
			copyToClipboard(strings.Join(targets, "\n"))
		}
		return m, nil
	}
	return m, nil
}

// --- Preview ---

func (m *pickerModel) updatePreview() {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return
	}
	item := m.items[m.cursor]
	pw := m.previewWidth()

	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	accent := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38BDF8"))
	highlight := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))

	var sb strings.Builder

	// Header
	sb.WriteString(accent.Render(strings.ToUpper(item.Item.Category)))
	sb.WriteString("  ")
	sb.WriteString(highlight.Render(item.Item.Label))
	if len(item.Refs) > 1 {
		sb.WriteString(dim.Render(fmt.Sprintf("  (%d refs)", len(item.Refs))))
	}
	sb.WriteString("\n")
	if item.Item.URL != item.Item.Label {
		url := item.Item.URL
		if len(url) > pw-2 {
			url = url[:pw-5] + "..."
		}
		sb.WriteString(dim.Render(url))
		sb.WriteString("\n")
	}
	if m.kind == "changes" {
		// Render actual diffs for all refs
		hasDiff := false
		for _, ref := range item.Refs {
			if ref.ToolName != "" && ref.ToolInput != "" {
				diff := extract.FormatDiff(ref.ToolName, ref.ToolInput, pw, pickerDiffStyles)
				if diff != "" {
					sb.WriteString(diff)
					hasDiff = true
				}
			}
		}
		if !hasDiff {
			sb.WriteString(dim.Render("(no diff data)"))
		}
		sb.WriteString("\n")
		sb.WriteString(dim.Render("↵:jump to message  e:open in $EDITOR"))
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("\n")
	}

	// Refs with context
	if m.refPicking {
		sb.WriteString(accent.Render("Select reference to jump to:"))
		sb.WriteString("\n\n")
	}

	for i, ref := range item.Refs {
		// Ref header
		cursor := "  "
		numStyle := dim
		if m.refPicking && i == m.refCursor {
			cursor = accent.Render("> ")
			numStyle = accent
		}
		refHeader := fmt.Sprintf("%s%s %s  %s",
			cursor,
			numStyle.Render(fmt.Sprintf("[%d]", i+1)),
			highlight.Render(strings.ToUpper(ref.Role)),
			dim.Render(ref.Timestamp.Format("15:04:05")),
		)
		sb.WriteString(refHeader + "\n")

		// Context lines (indented)
		if ref.Preview != "" {
			for _, line := range strings.Split(ref.Preview, "\n") {
				if line == "" {
					continue
				}
				// Skip the role+timestamp header line from entryContext since we show it above
				if strings.HasPrefix(line, "USER ") || strings.HasPrefix(line, "ASSISTANT ") || strings.HasPrefix(line, "ENTRY ") {
					continue
				}
				if len(line) > pw-6 {
					line = line[:pw-9] + "..."
				}
				sb.WriteString("    " + dim.Render(line) + "\n")
			}
		}
		sb.WriteString("\n")
	}

	if m.refPicking {
		sb.WriteString(dim.Render("↵:jump  1-9:quick select  esc:cancel"))
	}

	m.preview.SetContent(sb.String())
	m.preview.GotoTop()
}

// --- View ---

func (m pickerModel) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	listW := m.listWidth()
	pw := m.previewWidth()
	contentH := m.height - 2

	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	cat := lipgloss.NewStyle().Foreground(lipgloss.Color("#6366F1")).Bold(true)

	visMax := contentH - 2
	if visMax < 3 {
		visMax = 3
	}
	start := 0
	if m.cursor >= start+visMax {
		start = m.cursor - visMax + 1
	}
	end := start + visMax
	if end > len(m.items) {
		end = len(m.items)
	}

	var listLines []string
	for i := start; i < end; i++ {
		item := m.items[i]
		ri := m.realIndex(i)
		check := "  "
		if m.selected[ri] {
			check = sel.Render("* ")
		}
		badge := cat.Render(padRight(strings.ToUpper(item.Item.Category), 5))
		label := item.Item.Label
		maxLabel := listW - 12
		if maxLabel < 10 {
			maxLabel = 10
		}
		if len(label) > maxLabel {
			label = label[:maxLabel-3] + "..."
		}
		// Show ref count for items with multiple references
		refBadge := ""
		if len(item.Refs) > 1 {
			refBadge = dim.Render(fmt.Sprintf(" ×%d", len(item.Refs)))
		}
		if i == m.cursor {
			listLines = append(listLines, sel.Render(">")+check+badge+" "+sel.Render(label)+refBadge)
		} else {
			listLines = append(listLines, " "+check+badge+" "+dim.Render(label)+refBadge)
		}
	}

	searchLine := ""
	if m.searching {
		searchLine = m.searchInput.View()
	} else if m.searchTerm != "" {
		searchLine = dim.Render("[" + m.searchTerm + "]")
	}

	scrollInfo := ""
	if len(m.items) > 0 {
		scrollInfo = dim.Render(fmt.Sprintf("[%d/%d]", m.cursor+1, len(m.items)))
		if len(m.selected) > 0 {
			scrollInfo += dim.Render(fmt.Sprintf(" %d selected", len(m.selected)))
		}
	} else {
		scrollInfo = dim.Render("no matches")
	}

	listContent := strings.Join(listLines, "\n")
	if searchLine != "" {
		listContent = searchLine + "\n" + listContent
	}
	listContent += "\n" + scrollInfo

	listBox := lipgloss.NewStyle().Width(listW).Height(contentH).Render(listContent)
	borderColor := lipgloss.Color("#374151")
	if m.previewFocused {
		borderColor = lipgloss.Color("#38BDF8")
	}
	previewBox := lipgloss.NewStyle().
		Width(pw).Height(contentH).
		BorderLeft(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(borderColor).
		PaddingLeft(1).
		Render(m.preview.View())

	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38BDF8")).
		Render(fmt.Sprintf(" %s (%d)", m.kind, len(m.allItems)))

	actions := "↵:jump"
	switch m.kind {
	case "urls":
		actions = "↵:jump  o:open  e:$EDITOR"
	case "files":
		actions = "↵:jump  e:$EDITOR"
	case "changes":
		actions = "↵:jump  e:$EDITOR"
	case "images":
		actions = "↵:jump  e:$EDITOR"
	}
	var footer string
	if m.searching {
		// Show filter hints when search is active
		hint := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8"))
		filterHints := ""
		switch m.kind {
		case "urls":
			filterHints = hint.Render("is:") + dim.Render("pr gh github jira slack other") + "  " + hint.Render("role:") + dim.Render("user asst")
		case "files":
			filterHints = hint.Render("is:") + dim.Render("read write edit glob grep tool") + "  " + hint.Render("role:") + dim.Render("user asst")
		case "changes":
			filterHints = hint.Render("is:") + dim.Render("change") + "  " + hint.Render("role:") + dim.Render("user asst")
		case "images":
			filterHints = hint.Render("is:") + dim.Render("image") + "  " + hint.Render("role:") + dim.Render("user asst")
		}
		footer = filterHints
	} else if m.previewFocused {
		footer = dim.Render("j/k:scroll  ^d/^u:page  g/G:top/bottom  ←/esc:back")
	} else {
		footer = dim.Render(actions + "  y:copy  sp:select  a:all  A:none  →:preview  /:search  esc:quit")
	}

	return title + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, listBox, previewBox) + "\n" + footer
}

// --- Helpers ---

func (m pickerModel) listWidth() int  { return m.width * 40 / 100 }
func (m pickerModel) previewWidth() int { return m.width - m.listWidth() - 2 }

func (m *pickerModel) filterItems() {
	term := strings.ToLower(m.searchTerm)
	if term == "" {
		m.items = m.allItems
		m.cursor = 0
		return
	}
	terms := strings.Fields(term)
	var filtered []PickerItem
	for _, item := range m.allItems {
		text := strings.ToLower(item.FilterValue())
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
	m.items = filtered
	if m.cursor >= len(m.items) {
		m.cursor = max(len(m.items)-1, 0)
	}
}

func (m pickerModel) realIndex(filteredIdx int) int {
	if filteredIdx < 0 || filteredIdx >= len(m.items) {
		return -1
	}
	target := m.items[filteredIdx]
	for i, item := range m.allItems {
		if item.Item.URL == target.Item.URL {
			return i
		}
	}
	return filteredIdx
}

func (m pickerModel) selectedURLs() []string {
	if len(m.selected) > 0 {
		var urls []string
		for i, item := range m.allItems {
			if m.selected[i] {
				urls = append(urls, item.Item.URL)
			}
		}
		return urls
	}
	if m.cursor >= 0 && m.cursor < len(m.items) {
		return []string{m.items[m.cursor].Item.URL}
	}
	return nil
}

func (m pickerModel) openItems(urls []string) {
	for _, u := range urls {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", u)
		case "linux":
			cmd = exec.Command("xdg-open", u)
		}
		if cmd != nil {
			cmd.Start()
		}
	}
}

func (m pickerModel) editItems(paths []string) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, paths...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}

func copyToClipboard(text string) {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	cmd.Run()
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}

// RunPicker launches the interactive picker and returns the result.
func RunPicker(kind string, items []PickerItem) (*PickerResult, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("no %s found in session", kind)
	}
	model := newPickerModel(kind, items)
	p := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return nil, err
	}
	m := finalModel.(pickerModel)
	return m.result, nil
}
